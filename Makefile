.PHONY: all help bootstrap bootstrap-force hooks-ensure tools sync dependencies verify-dependencies version version-set version-bump-major version-bump-minor version-bump-patch
.PHONY: lint fmt-check test build install build-all package clean fmt validate-schemas check-all precommit prepush pr-final license-audit
.PHONY: release-check release-prepare release-build
.PHONY: release-clean release-create-draft release-guard-tag-name release-guard-tag-version release-checksums release-verify-checksums release-download release-sign release-export-keys release-verify-keys release-verify-signatures release-notes release-upload-provenance release-upload release-upload-all release-publish
.PHONY: sync-embedded-identity verify-embedded-identity test-standalone-binary
.PHONY: release-tag-tests release-prepare-tag-message release-tag release-push-tag release-verify-tag release-verify-remote-tag
.PHONY: release-export-pin release-validate-pin release-insert-anchors release-verify-published-tag

# Binary and version information
BINARY_NAME := spanwit
BINARY_EXT :=
ifeq ($(OS),Windows_NT)
	BINARY_EXT := .exe
endif
VERSION := $(shell cat VERSION 2>/dev/null || echo "dev")
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.buildDate=$(BUILD_DATE)

# Go related variables
GOCMD := go
GOTEST := $(GOCMD) test
GOMOD := $(GOCMD) mod

# Tool installation (user-space bin dir; overridable with BINDIR=...)
#
# Defaults:
# - macOS/Linux: $HOME/.local/bin
# - Windows (Git Bash / MSYS / MINGW / Cygwin): %USERPROFILE%\\bin (or $HOME/bin)
UNAME_S_RAW := $(shell uname -s 2>/dev/null || echo unknown)
IS_WINDOWS_SHELL := $(filter MINGW% MSYS% CYGWIN%,$(UNAME_S_RAW))

ifeq ($(strip $(IS_WINDOWS_SHELL)),)
DEFAULT_BINDIR := $(if $(HOME),$(HOME)/.local/bin,./bin)
else
CYGPATH_BIN := $(shell command -v cygpath 2>/dev/null)
ifeq ($(strip $(USERPROFILE)),)
DEFAULT_BINDIR := $(if $(HOME),$(HOME)/bin,./bin)
else
DEFAULT_BINDIR := $(if $(CYGPATH_BIN),$(shell cygpath -u "$(USERPROFILE)")/bin,$(USERPROFILE)/bin)
endif
endif

BINDIR ?= $(DEFAULT_BINDIR)

# Embedded identity mirror (build artifact contract)
EMBEDDED_IDENTITY_SRC := .fulmen/app.yaml
EMBEDDED_IDENTITY_DST := internal/assets/appidentity/app.yaml

# Tool versions
GONEAT_VERSION ?= v0.6.0
SFETCH_BIN := $(shell command -v sfetch 2>/dev/null)
GONEAT_BIN = $(firstword $(wildcard $(BINDIR)/goneat$(BINARY_EXT)) $(shell command -v goneat 2>/dev/null))

# Platform detection
ifeq ($(OS),Windows_NT)
    PLATFORM := windows
else
    UNAME_S := $(shell uname -s)
    ifeq ($(UNAME_S),Linux)
        PLATFORM := linux
    endif
    ifeq ($(UNAME_S),Darwin)
        PLATFORM := mac
    endif
    ifneq (,$(findstring BSD,$(UNAME_S)))
        PLATFORM := bsd
    endif
endif

# Default target
all: fmt test

help:  ## Show this help message
	@printf "%b" '$(BINARY_NAME) - Available Make Targets\n\nRequired targets (Fulmen Makefile Standard):\n  help                  - Show this help message\n  bootstrap             - Install external tools (goneat) and dependencies\n  bootstrap-force       - Force reinstall external tools\n  tools                 - Verify external tools are available\n  sync                  - Sync assets from Crucible SSOT (no-op for microtool)\n  sync-embedded-identity - Sync embedded identity mirror (.fulmen → internal/assets)\n  verify-embedded-identity - Verify embedded identity mirror is in sync\n  dependencies          - Generate SBOM for supply-chain security\n  license-audit         - Audit dependency licenses\n  lint                  - Run lint/format/style checks\n  test                  - Run all tests\n  build                 - Build distributable artifacts\n  install               - Install binary to BINDIR (default: ~/.local/bin)\n  build-all             - Build multi-platform binaries\n  test-standalone-binary - Verify built binary works outside repo\n  clean                 - Remove build artifacts and caches\n  fmt                   - Format code\n  validate-schemas      - Meta-validate schemas and validate example configs\n  version               - Print current version\n  version-set           - Set version to specific value (VERSION=x.y.z)\n  version-bump-major    - Bump major version\n  version-bump-minor    - Bump minor version\n  version-bump-patch    - Bump patch version\n  release-check         - Run release checklist validation\n  release-prepare       - Prepare for release\n  release-build         - Build release artifacts\n  check-all             - Run all quality checks (fmt, lint, schema validation, test)\n  fmt-check             - Check formatting without modifying files\n  precommit             - Run pre-commit hooks\n  prepush               - Run pre-push hooks (final gate for tag/pushtag)\n  pr-final              - Final non-mutating PR gate (identity, format, lint, schema, test, license, build)\n'

bootstrap:  ## Install external tools via sfetch + goneat
	@echo "Installing external tools..."
	@if [ -z "$(SFETCH_BIN)" ]; then echo "❌ sfetch not found (required trust anchor)"; echo ""; echo "Install sfetch with:"; echo "  curl -sSfL https://github.com/3leaps/sfetch/releases/latest/download/install-sfetch.sh | bash"; echo ""; echo "Or install into a specific directory:"; echo "  curl -sSfL https://github.com/3leaps/sfetch/releases/latest/download/install-sfetch.sh | bash -s -- --dir \"$(BINDIR)\" --yes"; exit 1; else echo "✅ sfetch found: $$($(SFETCH_BIN) --version 2>&1 | head -n1)"; echo "→ sfetch self-verify (trust anchors):"; $(SFETCH_BIN) --self-verify; fi
	@mkdir -p "$(BINDIR)"; if [ "$(FORCE)" = "1" ] || [ "$(FORCE)" = "true" ]; then rm -f "$(BINDIR)/goneat$(BINARY_EXT)"; fi; if [ -x "$(BINDIR)/goneat$(BINARY_EXT)" ] && "$(BINDIR)/goneat$(BINARY_EXT)" --version 2>/dev/null | grep -q "$(GONEAT_VERSION)"; then echo "→ goneat already available at $(BINDIR)/goneat$(BINARY_EXT)"; else echo "→ Installing goneat $(GONEAT_VERSION) to $(BINDIR) via sfetch..."; $(SFETCH_BIN) --repo fulmenhq/goneat --tag $(GONEAT_VERSION) --dest-dir "$(BINDIR)"; fi
	@echo "→ Installing tools via goneat (scopes from .goneat/tools.yaml; non-fatal if a scope is unavailable on this OS)..."
	@for scope in bootstrap foundation lint security format; do \
		echo "  → scope: $$scope"; \
		$(GONEAT_BIN) doctor tools --scope $$scope --install --yes || echo "  ⚠️  scope $$scope: some tools unavailable on this platform (continuing)"; \
	done
	@echo "→ Download Go module dependencies..." && go mod download && go mod tidy
	@$(MAKE) hooks-ensure
	@echo "✅ Bootstrap completed. Ensure '$(BINDIR)' is on your PATH"

bootstrap-force:  ## Force reinstall external tools
	@$(MAKE) bootstrap FORCE=1

hooks-ensure:  ## Ensure git hooks are installed (idempotent)
	@if [ -d ".git" ] && [ -n "$(GONEAT_BIN)" ] && [ ! -x ".git/hooks/pre-commit" ]; then \
		echo "🔗 Installing git hooks with goneat..."; \
		$(GONEAT_BIN) hooks install 2>/dev/null || true; \
	fi

tools:  ## Verify external tools are available
	@echo "Verifying external tools..."
	@if [ -z "$(GONEAT_BIN)" ]; then echo "❌ goneat not found. Run 'make bootstrap' first."; exit 1; fi
	@echo "✅ goneat: $$($(GONEAT_BIN) --version 2>&1 | head -n1)"
	@echo "→ Checking tool scopes..."; $(GONEAT_BIN) doctor tools --scope bootstrap || echo "⚠️  Some bootstrap tools missing."; $(GONEAT_BIN) doctor tools --scope foundation || echo "⚠️  Some foundation tools missing. Run 'make bootstrap' to install."; $(GONEAT_BIN) doctor tools --scope security || echo "⚠️  Some security tools missing. Run 'make bootstrap' to install."; $(GONEAT_BIN) doctor tools --scope format || echo "⚠️  Some format tools missing. Run 'make bootstrap' to install."; $(GONEAT_BIN) doctor tools --scope lint || echo "⚠️  Some lint tools missing. Run 'make bootstrap' to install."
	@echo "✅ Tool verification completed"

sync:  ## Sync assets from Crucible SSOT (no-op for microtool template)
	@echo "⚠️  $(BINARY_NAME) microtool does not consume SSOT assets directly"
	@echo "✅ Sync target satisfied (no-op)"

sync-embedded-identity:  ## Copy .fulmen/app.yaml → internal/assets/appidentity/app.yaml
	@mkdir -p "$(dir $(EMBEDDED_IDENTITY_DST))"
	@cp "$(EMBEDDED_IDENTITY_SRC)" "$(EMBEDDED_IDENTITY_DST)"
	@echo "✅ Synced embedded identity mirror: $(EMBEDDED_IDENTITY_DST)"

verify-embedded-identity:  ## Fail if embedded identity mirror is out of sync
	@if [ ! -f "$(EMBEDDED_IDENTITY_SRC)" ]; then echo "❌ Missing $(EMBEDDED_IDENTITY_SRC)"; exit 1; fi
	@if [ ! -f "$(EMBEDDED_IDENTITY_DST)" ]; then echo "❌ Missing $(EMBEDDED_IDENTITY_DST) (run: make sync-embedded-identity)"; exit 1; fi
	@if ! cmp -s "$(EMBEDDED_IDENTITY_SRC)" "$(EMBEDDED_IDENTITY_DST)"; then \
		echo "❌ Embedded identity is out of sync."; \
		echo "   Run: make sync-embedded-identity (then commit the updated mirror)"; \
		diff -u "$(EMBEDDED_IDENTITY_SRC)" "$(EMBEDDED_IDENTITY_DST)" || true; \
		exit 1; \
	fi
	@echo "✅ Embedded identity mirror is in sync"

dependencies:  ## Generate SBOM for supply-chain security
	@if [ -z "$(GONEAT_BIN)" ]; then echo "❌ goneat not found. Run 'make bootstrap' first."; exit 1; fi
	@echo "Generating Software Bill of Materials (SBOM)..."; mkdir -p sbom; $(GONEAT_BIN) dependencies --sbom --sbom-output sbom/$(BINARY_NAME).cdx.json; echo "✅ SBOM generated at sbom/$(BINARY_NAME).cdx.json"

verify-dependencies:  ## Alias for dependencies (compatibility)
	@$(MAKE) dependencies

license-audit:  ## Audit dependency licenses (goneat; policy in .goneat/dependencies.yaml)
	@if [ -z "$(GONEAT_BIN)" ]; then echo "❌ goneat not found. Run 'make bootstrap' first."; exit 1; fi
	@echo "🧪 Auditing dependency licenses (goneat)..."
	@$(GONEAT_BIN) dependencies --licenses --fail-on high
	@echo "✅ License audit passed"

version:  ## Print current version
	@echo "$(VERSION)"

version-set:  ## Set version to specific value (usage: make version-set VERSION=x.y.z)
	@if [ -z "$(VERSION)" ]; then \
		echo "❌ VERSION not specified. Usage: make version-set VERSION=x.y.z"; \
		exit 1; \
	fi
	@echo "$(VERSION)" > VERSION
	@echo "✅ Version set to $(VERSION)"

version-bump-major:  ## Bump major version
	@if [ -z "$(GONEAT_BIN)" ]; then \
		echo "❌ goneat not found. Run 'make bootstrap' first."; \
		exit 1; \
	fi
	@echo "Bumping major version..."
	@$(GONEAT_BIN) version bump major
	@echo "✅ Version bumped to $$(cat VERSION)"

version-bump-minor:  ## Bump minor version
	@if [ -z "$(GONEAT_BIN)" ]; then \
		echo "❌ goneat not found. Run 'make bootstrap' first."; \
		exit 1; \
	fi
	@echo "Bumping minor version..."
	@$(GONEAT_BIN) version bump minor
	@echo "✅ Version bumped to $$(cat VERSION)"

version-bump-patch:  ## Bump patch version
	@if [ -z "$(GONEAT_BIN)" ]; then \
		echo "❌ goneat not found. Run 'make bootstrap' first."; \
		exit 1; \
	fi
	@echo "Bumping patch version..."
	@$(GONEAT_BIN) version bump patch
	@echo "✅ Version bumped to $$(cat VERSION)"

release-check:  ## Run release checklist validation
	@echo "Running release checklist..."
	@$(MAKE) check-all
	@echo "✅ Release check passed"

release-prepare:  ## Prepare for release (tests, checks)
	@echo "Preparing release..."
	@$(MAKE) check-all
	@echo "✅ Release preparation complete"

release-build: build-all  ## Build release artifacts (binaries + checksums)
	@echo "✅ Release build complete"

build: verify-embedded-identity  ## Build binary for current platform
	@echo "→ Building $(BINARY_NAME) v$(VERSION)..."
	@mkdir -p bin
	@CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o bin/$(BINARY_NAME)$(BINARY_EXT) ./cmd/$(BINARY_NAME)
	@echo "✓ Binary built: bin/$(BINARY_NAME)$(BINARY_EXT)"

install: build  ## Install binary to BINDIR (default: ~/.local/bin)
	@echo "→ Installing $(BINARY_NAME) to $(BINDIR)..."
	@mkdir -p "$(BINDIR)"
	@# Remove destination first (no-op if missing). On macOS, overwriting an
	@# existing binary in-place can fail or leave a stale mapped image ("Text
	@# file busy" / confused local bin). Unlink-then-copy is safe and portable.
	@rm -f "$(BINDIR)/$(BINARY_NAME)$(BINARY_EXT)"
	@cp bin/$(BINARY_NAME)$(BINARY_EXT) "$(BINDIR)/$(BINARY_NAME)$(BINARY_EXT)"
	@echo "✓ Installed: $(BINDIR)/$(BINARY_NAME)$(BINARY_EXT)"

test-standalone-binary: build  ## Verify built binary runs outside repo
	@echo "→ Standalone binary check (outside repo)..."
	@cp "bin/$(BINARY_NAME)$(BINARY_EXT)" "/tmp/$(BINARY_NAME)$(BINARY_EXT)"
	@"/tmp/$(BINARY_NAME)$(BINARY_EXT)" version >/dev/null
	@"/tmp/$(BINARY_NAME)$(BINARY_EXT)" --help >/dev/null
	@echo "✅ Standalone binary check passed"

build-all: verify-embedded-identity  ## Build multi-platform binaries (linux amd64/arm64, windows amd64/arm64, darwin arm64)
	@echo "→ Building for multiple platforms..."
	@mkdir -p bin
	@echo "Building Linux amd64..."
	@GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o bin/$(BINARY_NAME)-linux-amd64 ./cmd/$(BINARY_NAME)
	@echo "Building Linux arm64..."
	@GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o bin/$(BINARY_NAME)-linux-arm64 ./cmd/$(BINARY_NAME)
	@echo "Building Windows amd64..."
	@GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o bin/$(BINARY_NAME)-windows-amd64.exe ./cmd/$(BINARY_NAME)
	@echo "Building Windows arm64..."
	@GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o bin/$(BINARY_NAME)-windows-arm64.exe ./cmd/$(BINARY_NAME)
	@echo "Building Darwin arm64..."
	@GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -buildvcs=false -ldflags="$(LDFLAGS)" -o bin/$(BINARY_NAME)-darwin-arm64 ./cmd/$(BINARY_NAME)
	@echo "✓ Multi-platform binaries built in bin/"

package: build-all  ## Package release archives + checksum manifests to dist/release (no signing)
	@BINARY_NAME=$(BINARY_NAME) ./scripts/package-artifacts.sh
	@$(MAKE) release-checksums

test: verify-embedded-identity  ## Run all tests
	@echo "Running test suite..."
	$(GOTEST) ./... -v -cover

lint:  ## Run lint checks with goneat
	@if [ -z "$(GONEAT_BIN)" ]; then echo "❌ goneat not found. Run 'make bootstrap' first."; exit 1; fi
	@echo "Running Go vet..." && $(GOCMD) vet ./... && echo "Running goneat assess (lint)..." && $(GONEAT_BIN) assess --categories lint --package-mode && echo "✅ Lint checks passed"

# Shell formatting args. These MUST match .goneat/assess.yaml lint.shell.shfmt.args,
# or `make fmt` will write a form that `make lint` then reports as a difference.
# goneat formats go/yaml/json/markdown but not shell, so the write step for shell
# lives here while goneat keeps owning the verification.
SHFMT_ARGS := -i 4 -ci -sr

fmt:  ## Format code with goneat (+ shfmt for shell)
	@if [ -z "$(GONEAT_BIN)" ]; then echo "❌ goneat not found. Run 'make bootstrap' first."; exit 1; fi
	@echo "Formatting with goneat..." && $(GONEAT_BIN) format
	@if command -v shfmt >/dev/null 2>&1; then \
		echo "Formatting shell scripts with shfmt..."; \
		shfmt $(SHFMT_ARGS) -l -w $$(git ls-files '*.sh') ; \
	else \
		echo "⚠️  shfmt not found; shell scripts not formatted (run 'make bootstrap')"; \
	fi
	@echo "✅ Formatting completed"

validate-schemas:  ## Meta-validate JSON Schemas and validate example configs
	@if [ -z "$(GONEAT_BIN)" ]; then echo "❌ goneat not found. Run 'make bootstrap' first."; exit 1; fi
	@echo "Meta-validating spanwit schemas..."
	@GONEAT_OFFLINE_SCHEMA_VALIDATION=true $(GONEAT_BIN) schema validate-schema --schema-id json-schema-2020-12 --format json --workers 4 config/schema/*.schema.json
	@echo "Validating example configs..."
	@for f in docs/examples/configs/*.yaml; do \
		echo "→ $$f"; \
		$(GONEAT_BIN) schema validate-data --format json --schema-file config/schema/spanwit-config.v1.schema.json --data "$$f"; \
	done
	@echo "Validating built-in domain catalog..."
	@$(GONEAT_BIN) schema validate-data --format json --schema-file config/schema/spanwit-domain-catalog.v1.schema.json --data config/catalog/spanwit-domain-catalog.v1.yaml
	@echo "✅ Schema validation passed"

check-all: fmt lint validate-schemas test  ## Run all quality checks (fmt, lint, schema validation, test)
	@echo "✅ All quality checks passed"

precommit:  ## Run pre-commit hooks (format/lint/security + tests)
	@if [ -z "$(GONEAT_BIN)" ]; then echo "❌ goneat not found. Run 'make bootstrap' first."; exit 1; fi
	@$(MAKE) verify-embedded-identity
	@echo "Running pre-commit validation..." && $(GONEAT_BIN) assess --categories format,lint,security --fail-on critical --package-mode
	@$(MAKE) test
	@echo "✅ Pre-commit checks passed"

prepush: license-audit ## Run pre-push hooks
	@if [ -z "$(GONEAT_BIN)" ]; then echo "❌ goneat not found. Run 'make bootstrap' first."; exit 1; fi
	@echo "Running pre-push validation..." && $(GONEAT_BIN) assess --categories format,lint,security,dependencies,dates,tools,maturity,repo-status --fail-on high --package-mode && echo "✅ Pre-push checks passed"

fmt-check:  ## Check formatting without modifying files (non-mutating)
	@if [ -z "$(GONEAT_BIN)" ]; then echo "❌ goneat not found. Run 'make bootstrap' first."; exit 1; fi
	@echo "Checking format (no changes written)..." && $(GONEAT_BIN) format --check && echo "✅ Format check passed"

pr-final: verify-embedded-identity fmt-check lint validate-schemas test license-audit build-all  ## Final non-mutating PR gate (format/lint/schema/test + license + multi-platform build)
	@echo "✅ pr-final gate passed — branch is PR-ready"

# -----------------------------------------------------------------------------
# Release ceremony (manual, operator-run; see RELEASE_CHECKLIST.md)
#
# Signing keys are set on the operator machine, NEVER in CI:
#   SPANWIT_MINISIGN_KEY  - path to minisign secret key (required to sign)
#   SPANWIT_MINISIGN_PUB  - path to minisign public key (optional; auto-derived)
#   SPANWIT_PGP_KEY_ID    - gpg key selector for PGP signing (optional; the signed
#                           tag requires the exact signing-subkey form <40-hex>!)
#   SPANWIT_GPG_HOMEDIR   - isolated gpg homedir (required if PGP_KEY_ID is set)
#
# Flow: signed tag -> read-only CI builds packages as a workflow artifact ->
# operator re-verifies the tag, downloads the packages, checksums, creates the
# DRAFT (release-create-draft), signs, verifies, uploads provenance ->
# release-publish promotes draft -> public. Never auto-default RELEASE_TAG to v$(VERSION).
# -----------------------------------------------------------------------------

RELEASE_TAG ?= $(SPANWIT_RELEASE_TAG)
DIST_RELEASE ?= dist/release

release-clean:  ## Reset dist/release staging to avoid stale artifacts
	@rm -rf "$(DIST_RELEASE)" "$(DIST_RELEASE).anchor"
	@mkdir -p "$(DIST_RELEASE)"
	@echo "✅ Cleaned $(DIST_RELEASE)"

# Post-tag operations bind to the published, verified tag rather than to the
# checked-out VERSION, so a version bump on main cannot strand a release.
release-guard-tag-name:  ## Guard: RELEASE_TAG set and canonical vX.Y.Z (no VERSION check)
	@if [ -z "$(RELEASE_TAG)" ]; then \
		echo "❌ RELEASE_TAG not set. Set SPANWIT_RELEASE_TAG in your shell, or pass RELEASE_TAG=vX.Y.Z." >&2; \
		exit 1; \
	fi
	@printf '%s\n' "$(RELEASE_TAG)" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$$' || { \
		echo "❌ RELEASE_TAG must be canonical vX.Y.Z" >&2; exit 1; }

release-guard-tag-version:  ## Guard: RELEASE_TAG set (SPANWIT_RELEASE_TAG or arg), canonical vX.Y.Z, matches VERSION
	@if [ -z "$(RELEASE_TAG)" ]; then \
		echo "❌ RELEASE_TAG not set. Set SPANWIT_RELEASE_TAG in your shell, or pass RELEASE_TAG=v$(VERSION)." >&2; \
		exit 1; \
	fi
	@env -u RELEASE_TAG SPANWIT_RELEASE_TAG="$(RELEASE_TAG)" ./scripts/release-guard-tag-version.sh

# Signed version tag (maintainer machine only; see RELEASE_CHECKLIST.md and
# docs/decisions/ADR-0005-release-publication-gate.md). Uses SPANWIT_RELEASE_TAG,
# SPANWIT_TAG_MESSAGE_DIR, SPANWIT_TAGGER_NAME, SPANWIT_TAGGER_EMAIL,
# SPANWIT_GPG_SIGNING_FINGERPRINT, SPANWIT_PGP_KEY_ID (<subkey>!), SPANWIT_GPG_HOMEDIR.

release-prepare-tag-message:  ## Maintainer-only: prepare and review the external per-cut tag message
	@./scripts/release-tag-operator.sh prepare-message

release-tag:  ## Maintainer-only: create and verify a local signed version tag (no push)
	@./scripts/release-tag-operator.sh local-tag

release-push-tag:  ## Maintainer-only: publish and verify the signed version tag
	@./scripts/release-tag-operator.sh remote-push

release-verify-tag:  ## Verify the local tag using only the committed public pin
	@./scripts/release-verify-tag.sh

release-verify-published-tag: release-guard-tag-name  ## Verify the published tag object and signature against its committed pin (any main)
	@SPANWIT_RELEASE_TAG="$(RELEASE_TAG)" ./scripts/release-verify-published-tag.sh

release-verify-remote-tag:  ## Compare local and remote tag objects and GitHub verification
	@./scripts/release-verify-remote-tag.sh

release-export-pin:  ## Maintainer-only: export the approved public key when no pin exists
	@./scripts/release-export-pin.sh

release-validate-pin:  ## Maintainer-only: read-only validation of the existing public pin
	@./scripts/release-validate-pin.sh

release-insert-anchors: release-validate-pin  ## Maintainer-only: validate pin, then generate absent public anchors
	@./scripts/release-insert-anchors.sh

release-tag-tests:  ## Signed-tag tooling tests (throwaway keys only; needs gpg, git, python3, jq, make)
	@./scripts/release-guard-tag-version.test.sh
	@./scripts/release-restore-tag-ref.test.sh
	@./scripts/release-tag-controls.test.sh
	@./scripts/release-tag-operator.test.sh
	@./scripts/release-prepare-tag-message.test.sh
	@./scripts/verify-pinned-tag.test.sh
	@./scripts/release-verify-published-tag.test.sh
	@./scripts/release-workflow-permissions.test.sh
	@./scripts/workflow-pins.test.sh
	@./scripts/release-ci-artifact-draft.test.sh
	@./scripts/release-pin-precursors.test.sh
	@./scripts/sign-release-manifests.test.sh
	@echo "✅ Signed-tag tooling tests passed"

release-checksums:  ## Generate SHA256SUMS and SHA512SUMS in dist/release
	@./scripts/generate-checksums.sh "$(DIST_RELEASE)" "$(BINARY_NAME)"

release-verify-checksums:  ## Verify SHA256SUMS / SHA512SUMS match actual artifacts
	@./scripts/verify-checksums.sh "$(DIST_RELEASE)"

release-download: release-guard-tag-name release-verify-published-tag  ## Download CI-built packages for the verified tag (workflow artifact)
	@./scripts/release-fetch-ci-artifacts.sh "$(RELEASE_TAG)" "$(DIST_RELEASE)"

release-create-draft: release-guard-tag-name release-verify-published-tag release-verify-checksums  ## Create the draft GitHub release from verified local packages (maintainer only)
	@./scripts/release-create-draft.sh "$(RELEASE_TAG)" "$(DIST_RELEASE)"

release-sign: release-guard-tag-name release-verify-published-tag  ## Sign checksum manifests (minisign required; PGP optional)
	@./scripts/sign-release-manifests.sh "$(RELEASE_TAG)" "$(DIST_RELEASE)"

release-export-keys:  ## Export public signing keys into dist/release
	@./scripts/export-release-keys.sh "$(DIST_RELEASE)"

release-verify-keys:  ## Verify exported public keys are public-only (no secrets)
	@if [ -f "$(DIST_RELEASE)/$(BINARY_NAME)-minisign.pub" ]; then ./scripts/verify-minisign-public-key.sh "$(DIST_RELEASE)/$(BINARY_NAME)-minisign.pub"; else echo "ℹ️  No minisign public key found (skipping)"; fi
	@if [ -f "$(DIST_RELEASE)/$(BINARY_NAME)-release-signing-key.asc" ]; then ./scripts/verify-public-key.sh "$(DIST_RELEASE)/$(BINARY_NAME)-release-signing-key.asc"; else echo "ℹ️  No PGP public key found (skipping)"; fi

release-verify-signatures:  ## Verify signatures on checksum manifests
	@echo "🔍 Verifying signatures in $(DIST_RELEASE)..."
	@has_any=false; \
	if [ -f "$(DIST_RELEASE)/SHA256SUMS.minisig" ]; then \
		if [ ! -f "$(DIST_RELEASE)/$(BINARY_NAME)-minisign.pub" ]; then \
			echo "❌ minisign public key not found; run 'make release-export-keys' first" >&2; exit 1; \
		fi; \
		(cd "$(DIST_RELEASE)" && minisign -V -p $(BINARY_NAME)-minisign.pub -m SHA256SUMS); \
		if [ -f "$(DIST_RELEASE)/SHA512SUMS.minisig" ]; then (cd "$(DIST_RELEASE)" && minisign -V -p $(BINARY_NAME)-minisign.pub -m SHA512SUMS); fi; \
		echo "✅ Minisign signatures verified"; has_any=true; \
	fi; \
	if [ -f "$(DIST_RELEASE)/SHA256SUMS.asc" ]; then \
		GPG_HOME="$${SPANWIT_GPG_HOMEDIR:-}"; \
		if [ -n "$$GPG_HOME" ]; then \
			(cd "$(DIST_RELEASE)" && gpg --homedir "$$GPG_HOME" --verify SHA256SUMS.asc SHA256SUMS); \
			if [ -f "$(DIST_RELEASE)/SHA512SUMS.asc" ]; then (cd "$(DIST_RELEASE)" && gpg --homedir "$$GPG_HOME" --verify SHA512SUMS.asc SHA512SUMS); fi; \
		else \
			(cd "$(DIST_RELEASE)" && gpg --verify SHA256SUMS.asc SHA256SUMS); \
			if [ -f "$(DIST_RELEASE)/SHA512SUMS.asc" ]; then (cd "$(DIST_RELEASE)" && gpg --verify SHA512SUMS.asc SHA512SUMS); fi; \
		fi; \
		echo "✅ PGP signatures verified"; has_any=true; \
	fi; \
	if [ "$$has_any" = false ]; then echo "❌ No signatures found to verify" >&2; exit 1; fi

release-notes: release-guard-tag-version  ## Stage docs/releases/<tag>.md into dist/release
	@notes_src="docs/releases/$(RELEASE_TAG).md"; \
	notes_dst="$(DIST_RELEASE)/release-notes-$(RELEASE_TAG).md"; \
	if [ ! -f "$$notes_src" ]; then echo "❌ Missing $$notes_src" >&2; exit 1; fi; \
	cp "$$notes_src" "$$notes_dst"; \
	echo "✅ Copied $$notes_src → $$notes_dst"

release-upload-provenance: release-guard-tag-name release-verify-published-tag release-verify-checksums release-verify-keys  ## Upload manifests + sigs + keys + notes (no binaries)
	@./scripts/release-upload-provenance.sh "$(RELEASE_TAG)" "$(DIST_RELEASE)"

release-upload: release-upload-provenance  ## Upload provenance assets to the GitHub release
	@:

release-upload-all: release-guard-tag-name release-verify-published-tag release-verify-checksums release-verify-keys  ## Upload binaries + provenance (manual override; release-create-draft already attaches packages)
	@./scripts/release-upload.sh "$(RELEASE_TAG)" "$(DIST_RELEASE)"

release-publish: release-guard-tag-name release-verify-published-tag  ## Promote the draft release → published (final ceremony step)
	@if ! command -v gh > /dev/null 2>&1; then echo "❌ gh (GitHub CLI) not found in PATH" >&2; exit 1; fi
	@echo "→ Promoting $(RELEASE_TAG) from draft → published..."
	@gh release edit "$(RELEASE_TAG)" --draft=false
	@echo "✅ $(RELEASE_TAG) is now publicly visible"
	@echo "   View: https://github.com/3leaps/$(BINARY_NAME)/releases/tag/$(RELEASE_TAG)"

clean:  ## Clean build artifacts and reports
	@echo "Cleaning artifacts..."
	@rm -rf bin/ dist/ coverage.out coverage.html sbom/ vendor/
	@echo "✅ Clean completed"

.DEFAULT_GOAL := help

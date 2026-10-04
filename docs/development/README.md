---
title: "Developer Guides Index"
description: "Entry point for Spanwit developer how-to guides"
last_updated: "2026-05-29"
---

# Developer Guides Index

This directory collects focused guides for Spanwit development. Start with the
[Spanwit Overview](../spanwit-overview.md) for architecture and integration
patterns.

## Available Guides

- [Spanwit Overview](../spanwit-overview.md) - Architecture, commands, gofulmen integration, quality gates, and hooks
- [Configuration](../configuration.md) - Config layout and the signature model
- [CI](ci.md) - Continuous integration notes

## Quick Reference

### Quality & Hooks

#### Generated Assets (Read-Only)

`internal/assets/**` is treated as generated/read-only. For embedded app identity, edit `.fulmen/app.yaml` then run `make sync-embedded-identity`.

Spanwit uses **goneat** (installed via `sfetch`) for unified code quality and automated git hooks.

**Configuration**: `.goneat/hooks.yaml`

```yaml
hooks:
  pre-commit:
    - command: "assess"
      args:
        [
          "--categories",
          "format,lint,security",
          "--fail-on",
          "critical",
          "--package-mode",
        ]
  pre-push:
    - command: "assess"
      args:
        [
          "--categories",
          "format,lint,security,dependencies,dates,tools,maturity,repo-status",
          "--fail-on",
          "high",
          "--package-mode",
        ]
```

**Testing hooks locally**:

```bash
goneat assess --hook pre-commit
goneat assess --hook pre-push

# Convenience aliases (optional)
make precommit
make prepush
```

**Regenerating hooks**:

```bash
goneat hooks generate    # Generate from .goneat/hooks.yaml
goneat hooks install     # Install to .git/hooks
```

### CI/CD

**GitHub Actions**: `.github/workflows/ci.yml`

- Runs on every push
- Executes: bootstrap → fmt → lint → test → build

**Makefile targets**:

```bash
make bootstrap           # Install goneat and dependencies
make check-all           # Run all quality checks (fmt, lint, schema validation, test)
make pr-final            # Non-mutating PR gate
make build               # Build binary
make build-all           # Build multi-platform binaries
```

### Code Quality Tools

**Format**:

```bash
make fmt                 # Format Go, YAML, Markdown (goneat)
make fmt-check           # Check formatting without modifying
```

**Lint**:

```bash
make lint                # Run go vet + goneat lint (includes actionlint/yamlfmt for workflows)
```

**Test**:

```bash
make test                # Run all tests (coverage report)
```

**SBOM** (Software Bill of Materials):

```bash
make dependencies        # Generate SBOM with goneat
```

**Security scanning**:

```bash
goneat assess --categories security    # Security vulnerability scan
```

### Version Management

**Using goneat**:

```bash
make version-bump-patch  # Increment patch version
make version-bump-minor  # Increment minor version
make version-bump-major  # Increment major version
```

**Manual**:

```bash
make version-set VERSION=1.2.3    # Set specific version
```

## Standards References

- [Fulmen Forge Microtool Standard](https://github.com/fulmenhq/crucible/blob/main/docs/architecture/fulmen-forge-microtool-standard.md) - Microtool requirements
- [Fulmen Makefile Standard](https://github.com/fulmenhq/crucible/blob/main/docs/standards/makefile-standard.md) - Required Makefile targets
- [App Identity Module](https://github.com/fulmenhq/crucible/blob/main/docs/standards/library/modules/app-identity.md) - `.fulmen/app.yaml` specification
- [Pathfinder Extension](https://github.com/fulmenhq/crucible/blob/main/docs/standards/library/extensions/pathfinder.md) - Safe filesystem operations

## Contributing

Follow the quality gates and hooks system for all contributions. See [MAINTAINERS.md](../../MAINTAINERS.md) for governance structure and AI co-maintainer information.

**Pre-commit checklist**:

- [ ] Code formatted (`make fmt`)
- [ ] Linting passes (`make lint`)
- [ ] Tests pass (`make test`)
- [ ] Hooks validate (`make precommit`)

**Before opening / updating a PR**:

- [ ] `make pr-final` passes

---

As new development patterns emerge, add focused guides here to keep this index as the single entry point for developer references.

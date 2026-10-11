# Release Notes

This document tracks release notes for Spanwit.

> **Convention**: Keep only the latest 3 releases here (rolling summary). The
> authoritative per-version notes live in [`docs/releases/v<semver>.md`](docs/releases/)
> and are what `make release-notes` stages into the GitHub release.

## [0.2.1] - 2026-10-11

### Overview

A visibility and release-hardening release. `space` now explains three kinds of
disk use it previously left unexplained: VM data disks, interrupted grype
database downloads and temp-folder residue. None of them become prune candidates;
deletion is unchanged and remains dry-run by default.

### Highlights

- **VM data disks**: colima/lima disks are reported with logical and host-allocated size, and a propose-only recipe to reclaim space inside the VM and trim it back to the host.
- **grype residue**: leftover `grype-db-download<N>` directories are listed exactly, with the live database named to keep.
- **Temp residue summary**: per-family counts, sizes and ages for temp-folder entries, keyed by a fixed family table; entry names are never reported.
- **Homebrew and Scoop**: `brew install 3leaps/tap/spanwit` and `scoop install spanwit`.
- **Release hardening**: Go 1.26.9 minimum on a digest-pinned v0.5.8 runner; SHA-pinned Node 24 actions; artifacts fetched from a named workflow run attempt; publish checks every asset against the pinned minisign and PGP keys; signing requires both keys.

### Release provenance

- Release tags are GPG-signed and verify against [`docs/security/release-signing-keys.asc`](docs/security/release-signing-keys.asc) (fingerprints in [`keys/expected-fingerprints.txt`](keys/expected-fingerprints.txt)). Verify with `git tag -v v0.2.1` after importing that key.
- Checksum manifests are signed with minisign and PGP; the published assets are byte-identical to the signed set.

### Install

```bash
brew install 3leaps/tap/spanwit                      # macOS Apple Silicon, Linux
go install github.com/3leaps/spanwit/cmd/spanwit@v0.2.1
```

```powershell
scoop bucket add 3leaps https://github.com/3leaps/scoop-bucket
scoop install spanwit
```

See [CHANGELOG.md](CHANGELOG.md) for the full list.

## [0.2.0] - 2026-10-08

### Overview

A diagnostic and evidence release: Spanwit now explains where disk space went
before it suggests reclaiming any of it, and reports honestly what it could not
observe. Deletion remains dry-run by default.

### Highlights

- **`space`** — read-only disk diagnostic with guided recipes, capacity accounting, held-open capture and `--compare`.
- **`inventory`** — bounded read-only inventory with typed JSONL, explicit gaps, directory summaries and accounting.
- **Readable folder audit** — `inventory --directories --directory-min-size 5GiB`, with the v2 structured profile and schema.
- **Coverage attestation** — `--coverage-attest PATH` for file inventories and the folder audit.
- **`diagnose`**, guided **structural recipes** and **`--read-only`**; **`observe`** is propose-only but keeps local state, so `--read-only` rejects it.
- **Signed release tags** — tags verify against the public key committed in the repository; release builds are read-only.
- **Breaking (alpha)** — config `min_age`/`max_age` rename and flip; `spanwit-prune-plan/v1` gained required fields.

## 0.1.0

### Overview

First release of Spanwit — a context-aware disk-reclamation CLI. It recognizes
regenerable build output the way a developer would (a `target/` next to a
`Cargo.toml` is fair game; an identically named directory elsewhere may not be),
reclaims it safely, and is dry-run by default.

### Highlights

- **Signature-aware reclamation** — directories are reclaimable only when their context matches, not by name alone.
- **Dry-run by default** — `prune` plans without deleting; `--execute` is a deliberate, explicit flag.
- **Pipeline-friendly** — strict stdout purity and `prune --format json` (schema-validated output) for composition.
- **Config** — YAML config validated against a JSON Schema; resolution order `--config` > `SPANWIT_CONFIG_PATH` > `~/.config/spanwit/config.yaml`.
- **Standard microtool surface** — `scan`, `prune`, `validate`, `version`, `envinfo`, `doctor`.
- **Quality + release** — goneat gates (`make pr-final`, `make prepush`), GitHub Actions CI, and a tag-triggered release workflow.

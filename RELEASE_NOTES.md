# Release Notes

This document tracks release notes for Spanwit.

> **Convention**: Keep only the latest 3 releases here (rolling summary). The
> authoritative per-version notes live in [`docs/releases/v<semver>.md`](docs/releases/)
> and are what `make release-notes` stages into the GitHub release.

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

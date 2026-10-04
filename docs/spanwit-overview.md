---
title: "Spanwit Overview"
description: "Architecture and command overview for the Spanwit disk-reclamation CLI"
author: "infoarch"
date: "2026-05-29"
last_updated: "2026-10-02"
status: "active"
tags: ["overview", "spanwit", "cli", "go", "fulmen"]
---

# Spanwit Overview

## Purpose & Scope

Spanwit is a context-aware, signature-driven disk-reclamation CLI. It recognizes
regenerable build output the way a developer would — a `target/` next to a
`Cargo.toml` is fair game; an identically named directory elsewhere may not be —
and reclaims it safely. **Dry-run is the default**; deletion requires an explicit
`--execute`.

Spanwit is built on the Fulmen microtool pattern (gofulmen + Crucible standards):
narrow scope, excellent defaults, strict stdout purity, standardized exit codes.

## Commands

| Command     | Purpose                                                                                                                                                                 |
| ----------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `scan`      | Read-only, context-aware analysis; reports context-verified reclaimable dirs (name-only matches are summarized, not counted)                                            |
| `space`     | Read-only disk diagnostic: volume pressure, verified reclaimable, prune handoff, hotspots, guided recipes (argv + structural journeys), unverified mass (never deletes) |
| `observe`   | Observe registered volumes over time; hysteresis + growth when safe; **propose-only** guidance (no delete/elevation)                                                    |
| `inventory` | Neutral read-only regular-file enumeration with size/age filters, bounded top-K, typed JSONL/NUL projections, explicit gaps; `--directories` folder audit (v2 profile)  |
| `diagnose`  | Read-only file classification under diagnostic policies (installers, archives); found-only text plus typed JSONL, explicit coverage, never deletion authority           |
| `prune`     | Plan (and with `--execute`) delete reclaimable directories; `--format json` emits a schema-validated plan                                                               |
| `validate`  | Validate a config/data file against a JSON Schema                                                                                                                       |
| `version`   | Show version (`--extended` adds commit, build date, gofulmen version)                                                                                                   |
| `envinfo`   | Show app identity, runtime, config, and env-var information                                                                                                             |
| `doctor`    | Environment diagnostics (Go version, gofulmen access, config health)                                                                                                    |

### Usage

```bash
spanwit scan ~/dev                                  # safe, read-only
spanwit inventory ~/dev --min-size 1GiB --older-than 30d --top 100
spanwit inventory ~/dev --min-size 1GiB --format jsonl
spanwit --read-only inventory ~/dev --directories --directory-min-size 5GiB
spanwit diagnose ~/Downloads                     # found-only file classification
spanwit diagnose ~/Downloads --format jsonl      # typed records for review tooling
spanwit space ~/dev                                 # pressure + actionability layout
spanwit space ~/dev --capacity-only --format json  # fast allocation-plane capture
spanwit space ~/dev --class prunable --sort idle    # can free now only
spanwit space ~/dev --class diagnostic-only --domain development  # named caches
# Depth-bounded sizes are lower bounds (human text uses ≥…; JSON keeps size_incomplete).
spanwit prune --config ~/.config/spanwit/config.yaml          # dry-run plan
spanwit prune --config ~/.config/spanwit/config.yaml --execute  # delete
spanwit prune --format json | jq .                  # machine-readable plan
```

`space` includes a **prune handoff** with an embedded **exact_plan** (signature

- filter provenance for prunable-only paths). Revalidate with
  `prune --from-space-report <space.json>` (dry-run by default; `--execute` only
  after match). Bare `--allowlist` is dry-run-only convenience. Optional **guided
  recipes** (e.g. rustup) print argv-safe external commands — never auto-run.

Hotspot, unverified, and unknown observation use the bounded inventory walker
with depth-limited discovery/sizing and `--workers`, `--backend`,
`--stall-timeout`, and `--include-remote`. Automatically selected roots do not
inherit the operator-named remote-root exception. Remote detection is limited to
recognized filesystems/placeholders on macOS/Linux and is not exhaustive;
Windows and other platforms may walk network directories without
`--include-remote`. Per-root admission failures
and depth/remote/stall omissions remain visible in path-free per-plane coverage
warnings. Never-read candidates have no numeric row; observed partial sizes
remain lower bounds. Coverage survives filters and display truncation. The
verified engine and handoff remain independent of observation results.

`space --capacity-only` emits SpaceReport v2 without deep inventory, recipes,
filters, tuning metadata, or prune handoff. `internal/capacity` owns the shared
filesystem observation and platform collectors; `internal/space` only composes
the carrier. The filesystem plane and carrier pressure are exact projections of
the same observation. The optional analysis-root observation and all platform
collectors share one five-second context budget. Optional platform failures
remain plane-local coverage gaps, while failure of the mandatory filesystem
observation fails the command. Native statfs-style calls are context-checked
before and after but are best-effort at the wall-clock boundary because kernels
do not universally make them interruptible. Capacity planes are informational
and are not added together or converted into deletion candidates.

`space --capacity-only --held-open` adds an opt-in Darwin `lsof +L1` evidence
plane under a separate bounded deadline. Objects are deduplicated by
`(device,inode)` before the logical lower-bound total. Default reports contain
no held-open path component; full paths require `--disclose paths`. Holder
detail is capped, coverage gaps quantify omissions, and Spanwit never stops a
process or self-elevates.

`space --compare LEFT RIGHT` validates exactly two explicit SpaceReport v2
captures, with one optional stdin input. The loader applies byte, JSON-depth,
and per-array ceilings before schema and application-invariant validation.
Filesystem claims require matching platform and volume identity;
container/volume/snapshot claims require matching container identity. Exact
deltas require exact endpoints with matching source and basis. Lower-bound
subtraction remains indeterminate, and the command never invents a reconciled
or numeric unexplained total.

Capacity reports are not automatically share-safe: analysis roots and mounts
are present, catalog-constrained system roots may be present, and user-renamed
APFS volume names may carry personal or organizational text.

`inventory` is neutral evidence: file size and age never grant deletion
authority. It requires explicit roots, follows no directory symlinks, creates
no persistent/temp state by default, and reports permission, disappearance,
cancellation, and mount-boundary omissions explicitly. Streaming text and
JSONL use bounded backpressure; `--top N` retains only N entries while scanning
the complete declared scope. The parallel directory frontier is explicitly
capped; exhausting it emits fail-loud subtree gaps and a partial lifecycle.
JSONL record types are producer-owned
`spanwit.inventory.*.v1` under the strict
`spanwit.filesystem-inventory/v0` profile. Summary-only emission, normalized
explicit exclusions, stderr-only path-free progress, and exact per-root
terminal rows preserve distinct subject/selection/emission semantics. An
explicit `--coverage-attest PATH` projects the same reconciled run state into a
separate, canonical `coverage-attestation/v0` producer document; raw paths are
not copied into that block-export claim. Destructive inventory handoff is not
provided.

`inventory --directories` is a readable folder audit: allocated size by default,
an inclusive `--directory-min-size` floor, output depth and top-K over the
full-subject walk. `--format jsonl` emits the versioned
`spanwit.filesystem-inventory-aggregation/v2` profile, and `--coverage-attest`
attests traversal of the full subject. See [inventory](inventory.md).

## Configuration

Spanwit reads a YAML config validated against `config/schema/spanwit-config.v1.schema.json`.

**Resolution precedence:** `--config` flag → `SPANWIT_CONFIG_PATH` → default
`~/.config/spanwit/config.yaml`.

Environment variables use the `SPANWIT_` prefix (defined in `.fulmen/app.yaml`).
The structured `prune --format json` output is validated against
`config/schema/spanwit-prune-plan.v1.schema.json`. See
[configuration.md](configuration.md) for the config layout and signature model.

## gofulmen Integration

| Module                   | Use                                                      |
| ------------------------ | -------------------------------------------------------- |
| **gofulmen/appidentity** | App name, vendor, env-var prefix from `.fulmen/app.yaml` |
| **gofulmen/logging**     | Structured stderr logging (stdout stays pure)            |
| **gofulmen/signals**     | Graceful shutdown, double-tap Ctrl+C                     |
| **gofulmen/foundry**     | Standardized exit codes                                  |
| **gofulmen/pathfinder**  | Safe filesystem traversal and pattern matching           |
| **gofulmen/schema**      | JSON Schema validation                                   |

### External Dependencies

| Dependency                 | Purpose            |
| -------------------------- | ------------------ |
| **github.com/spf13/cobra** | CLI framework      |
| **go.uber.org/zap**        | Structured logging |
| **gopkg.in/yaml.v3**       | Config parsing     |

## Quality Gates

Spanwit uses [goneat](https://github.com/fulmenhq/goneat) for unified quality tooling.

```bash
make bootstrap     # Install goneat (via sfetch) + git hooks
make fmt           # Format (Go, YAML, Markdown)
make lint          # go vet + goneat assess (lint)
make test          # Run tests
make check-all     # fmt + lint + schema validation + test
make pr-final      # Non-mutating PR gate (identity, format, lint, schema, test, license, build)
make prepush       # Final gate for tag/pushtag
```

Git hooks (`.goneat/hooks.yaml`): pre-commit runs format/lint/security (fail-on
critical); pre-push runs the thorough assess (fail-on high).

## CI/CD

- `.github/workflows/ci.yml` — format, lint, test, build on every push.
- `.github/workflows/release.yml` — tag-triggered; packages multi-platform
  binaries with SHA256 checksums.

```bash
make build         # current platform → bin/spanwit
make build-all     # multi-platform binaries + checksums
make package       # release archives + checksums (dist/release)
```

## Project Structure

```
spanwit/
├── .fulmen/app.yaml            # App identity (binary, env prefix, config name)
├── .goneat/                    # goneat hooks + assess config
├── cmd/spanwit/main.go         # Entry point
├── internal/
│   ├── cmd/                    # Cobra commands and output adapters
│   ├── config/                 # Config types, loader, signatures
│   ├── contract/               # Schema validation wrapper
│   ├── coverageattestation/    # Coverage-attestation projection and publication
│   ├── inventory/              # Neutral bounded read-only enumeration core
│   ├── space/                  # Disk diagnostic and capacity reports
│   └── runtime/                # Logging setup
├── config/schema/              # JSON Schemas (config, catalog, plans, reports, inventory profiles) + embed
├── docs/                       # This overview, configuration, standards, decisions
├── Makefile
├── VERSION
└── README.md
```

## Standards Compliance

- [Fulmen Forge Microtool Standard](https://github.com/fulmenhq/crucible/blob/main/docs/architecture/fulmen-forge-microtool-standard.md)
- [App Identity Module](https://github.com/fulmenhq/crucible/blob/main/docs/standards/library/modules/app-identity.md)
- [Pathfinder Extension](https://github.com/fulmenhq/crucible/blob/main/docs/standards/library/extensions/pathfinder.md)
- [Fulmen Makefile Standard](https://github.com/fulmenhq/crucible/blob/main/docs/standards/makefile-standard.md)

## Maintainers

See [MAINTAINERS.md](../MAINTAINERS.md).

---

**Document Status**: Active
**Last Updated**: 2026-10-02

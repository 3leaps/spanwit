# Decisions

Architecture Decision Records (ADRs) for `spanwit`.

> Product and architecture decisions live here (`docs/decisions/`).
> Development-process decisions (build, hooks, tooling) live under
> [`../development/adr/`](../development/adr/). The two trees number
> independently.

## ADR Index

| ID   | Title                                                              | Status   | Date       |
| ---- | ------------------------------------------------------------------ | -------- | ---------- |
| 0001 | [Stdout Purity](./ADR-0001-stdout-purity.md)                       | Accepted | 2026-05-25 |
| 0002 | [Dry-Run by Default](./ADR-0002-dry-run-default.md)                | Accepted | 2026-05-29 |
| 0003 | [Core/Surface Separation](./ADR-0003-core-surface-separation.md)   | Accepted | 2026-07-03 |
| 0004 | [Diagnostic-Only Trust State](./ADR-0004-diagnostic-only-state.md) | Accepted | 2026-07-14 |
| 0005 | [Release Publication Gate](./ADR-0005-release-publication-gate.md) | Accepted | 2026-10-02 |

## Adopted patterns

### Stdout purity

`spanwit` reserves **stdout** for requested program output (results; JSON when
`--format json`) and uses **stderr** for all logging, diagnostics, errors,
warnings, progress, `--help`, and `--version`. See
[ADR-0001](./ADR-0001-stdout-purity.md) for the local codification and the
[CLI Output Contract](../standards/CLI-Output-Contract.md).

The pattern is established across 3leaps tools (e.g. docprims, seclusor) and
aligns with Fulmen logging guidance — see
[Crucible ADR-0003: Progressive Logging Profiles](https://github.com/fulmenhq/crucible/blob/main/docs/architecture/decisions/ADR-0003-progressive-logging-profiles.md)
(stderr for console sinks in the SIMPLE profile, appropriate for CLI tools).

### Dry-run by default

Mutating operations default to a non-destructive plan/preview; performing the
change is an explicit, auditable opt-in. The principle is defined to hold across
the CLI, the future daemon, and the future control-plane CRUD surface. See
[ADR-0002](./ADR-0002-dry-run-default.md).

### Core/surface separation

Spanwit is CLI-first, not CLI-only. Every command's behavior lives in a
surface-agnostic core (`Verb(ctx, opts) → Report`); CLI commands and future
network endpoints are thin adapters over it, with no business logic of their
own. Product capabilities keep capability parity across surfaces (same core,
options/safety semantics, and versioned report schema); transport concerns stay
in the adapters. A capability's report schema is simultaneously its CLI
`--format json` contract and its future API response body. New commands land on
this seam; existing commands migrate opportunistically; control-plane adapters
land capability by capability without parallel HTTP-only implementations. This
is what makes ADR-0002's cross-surface guarantee cheap to keep. See
[ADR-0003](./ADR-0003-core-surface-separation.md).

## References

- [Crucible: Fulmen Ecosystem Guide](https://github.com/fulmenhq/crucible/blob/main/docs/architecture/fulmen-ecosystem-guide.md)
- [12 Factor CLI Apps](https://medium.com/@jdxcode/12-factor-cli-apps-dd3c227a0e46)
- Related standards: [CLI Output Contract](../standards/CLI-Output-Contract.md), [Exit Codes](../standards/Exit-Codes.md)

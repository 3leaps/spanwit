# ADR-0004: Diagnostic-Only Trust State

> **Status**: Accepted
> **Date**: 2026-07-14
> **Authors**: Product architecture (supervised AI draft; architecture review)

## Context

The product trust vocabulary for discovery vs deletion already includes:

- **`prunable`** — match may be deleted under prune with an explicit execute flag
- **`withheld`** — match is reported but excluded from the execute set

The disk-diagnostic surface (`space`) must observe capacity that the product
**must not** delete until a future, safeguarded target type exists (global tool
caches, toolchain stacks, name-shaped unverified mass). Folding those rows into
`withheld` would blur filter/safety withholding with "intentionally never
executable on this surface."

## Decision

Extend the shared trust vocabulary with:

| State                 | Meaning                                                                                                       |
| --------------------- | ------------------------------------------------------------------------------------------------------------- |
| `prunable`            | Executable under prune (when not dry-run)                                                                     |
| `withheld`            | Discovered by the signature engine but not executable (filters / safe_to_prune)                               |
| **`diagnostic-only`** | Observed for capacity triage; **never** an execute target on any surface until an explicit product graduation |
| `unverified`          | Name-shaped reclaimable-looking path without context evidence (transparency only)                             |
| `unknown`             | Large path matching no known class (triage only)                                                              |

### Section placement (cross-surface contract)

The SpaceReport JSON Schema enforces state placement by section:

| Section                           | Allowed states         |
| --------------------------------- | ---------------------- |
| `verified_reclaimable.candidates` | `prunable`, `withheld` |
| `hotspots`                        | `diagnostic-only` only |
| `unverified.entries`              | `unverified` only      |
| `unknown`                         | `unknown` only         |

Graduation later means a cache receives a safeguarded engine target and moves into
`verified_reclaimable`; it is **not** a silent hotspot state flip.

## Consequences

- `space` reports hotspots as `diagnostic-only` with optional `rebuild_expectation`.
- Graduation to executable reclaim requires a future location/cache target type.
- Daemon/API consumers of the SpaceReport schema inherit the same vocabulary and
  section constraints (ADR-0003).

## References

- Safe-to-prune trust model (prunable / withheld)
- `space` read-only disk-diagnostic surface
- ADR-0002 dry-run default / multi-surface safety
- ADR-0003 core/surface separation

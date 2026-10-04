# Observe + propose

`spanwit observe` samples **registered volumes** by opaque filesystem identity
and emits **proposals only** — never delete, never elevate, never write user
config. This first slice is one-shot CLI observation; it does not run a daemon
or consume filesystem events.

## Commands

```bash
# Register a path (resolves stable volume_id)
spanwit observe register /System/Volumes/Data

# One sample cycle (also registers --register paths)
spanwit observe once --register "$HOME" --format json

# Health view (alias of once)
spanwit observe status --format text

# Default: cycle; auto-registers $HOME if state empty
spanwit observe --format text

# Throttle concurrency for this cycle only (not sticky)
spanwit observe once --foreground --format json
```

State lives under `~/.local/state/spanwit/observe/state.json` (override with
`--state-dir`). That file is **local telemetry**, but it is still a filesystem
write, so the global `--read-only` assertion rejects observe commands. Corrupt
or incompatible state loads as an in-memory empty incomparable baseline and surfaces
`corrupt_or_incompatible_state` in health `unavailable` (no stale growth/runway).
A state-dir **cycle lock** (`cycle.lock`) prevents two processes overlapping
samples against the same store.

## Safety

| Rule         | Behavior                                                                                             |
| ------------ | ---------------------------------------------------------------------------------------------------- |
| Identity     | Volume id, not mount path; alias change → coverage transition, no false merge                        |
| Growth       | Same volume + basis + **both complete** samples + positive elapsed only; else indeterminate          |
| Coverage gap | Error/partial/non-complete → reconcile boundary; first later complete is baseline only               |
| Loss         | Degrades coverage, schedules one reconcile; successful complete sample clears; no growth across gap  |
| Foreground   | **Cycle-scoped** concurrency throttle; does not persist; does not hide known pressure                |
| Mutation     | Writes bounded telemetry state; global `--read-only` rejects the invocation; proposals never execute |
| Proposals    | Pressure-only cycles emit **generic space notes** (no catalog recipe ids without evidence)           |
| Bounds       | Max 64 registered volumes; fixed worker pool; path-safe health errors                                |
| Privileged   | **Not** this command — separate operator one-shot inventory path                                     |
| Auto-soft    | **Not** this slice                                                                                   |

## Related

- Capacity: `spanwit space --capacity-only`
- Structural recipes: `docs/guides/cargo-target-relocation.md`
- Dry-run default: ADR-0002

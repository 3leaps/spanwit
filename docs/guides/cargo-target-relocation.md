# Guided journey: relocate Cargo `target/`

Spanwit’s structural recipe **`cargo-target-placement`** walks you through
moving Rust build output off a tight volume **without** prior env-var expertise.

This is **print-only**: spanwit never writes `.envrc`, never moves trees, and
never deletes. Purge steps re-use the existing dry-run `prune` path; you add
`--execute` only after a matching dry-run.

## When it appears

`spanwit space <root>` emits the journey when **all** of:

1. The `development` domain is authorized (default), and
2. Context-verified **whole** `target/` trees (`development.rust.cargo-target`)
   exist under the analysis root, and
3. Primary volume pressure is `warn`/`critical`, **or** those targets are already
   prunable (planning ahead).

Look for:

```text
[cargo-target-placement] Relocate Cargo build output (guided structural journey)
  (state=diagnostic-only, kind=structural, rebuild=high)
  step 1 [observe] …
```

JSON: `recipes[].kind == "structural"` with ordered `steps`.

## Journey shape

| Step                      | Intent                                                                                                                                                                       |
| ------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Observe**               | Evidence-backed project roots (parent of each verified `target/`)                                                                                                            |
| **Purge before relocate** | Dry-run via **Cargo-only** `recipes[].exact_plan` + `spanwit prune --from-exact-plan` — **not** report-wide `prune --from-space-report` / `prune_handoff`                    |
| **Choose destination**    | Precondition only in this slice: pick a roomier volume yourself; no concrete `CARGO_TARGET_DIR` export (analysis-vs-primary free space is not source→destination separation) |
| **Verify after**          | Rebuild once destination is set; re-run `space` (optionally compare saved JSON) — destination growth under active build is expected                                          |

## Operator ritual (copy)

```bash
# 1) Sense
spanwit space ~/dev --format json > /tmp/space-before.json

# 2) Review structural recipe steps (text or recipes[] in JSON)
spanwit space ~/dev

# 3) Cargo-only dry-run purge (NOT report-wide --from-space-report)
# Extract the journey's closed-set plan (paths match purge-before-relocate.Paths):
jq '.recipes[] | select(.id=="cargo-target-placement") | .exact_plan' \
  /tmp/space-before.json > /tmp/cargo-exact-plan.json
spanwit prune --from-exact-plan /tmp/cargo-exact-plan.json
# Only after dry-run matches the Cargo paths you reviewed:
# spanwit prune --from-exact-plan /tmp/cargo-exact-plan.json --execute

# 4) Destination: journey stops at choose-destination (no concrete export in
# this slice). Pick a roomier parent volume yourself; unique per-project subdirs.

# 5) Observe after
spanwit space ~/dev --format json > /tmp/space-after.json
# optional: spanwit space --compare /tmp/space-before.json /tmp/space-after.json
```

## Safety notes

- Recipes stay **`diagnostic-only`**. They never promote caches to prunable.
- Relocated `CARGO_TARGET_DIR` **outside** a `Cargo.toml` ancestor is **not**
  context-verified by the built-in cargo-target signature — treat as diagnostic
  until a provenance contract exists (see placement caveats on `space`).
- `--read-only` / `SPANWIT_READ_ONLY` still assert no mutating capability for the
  invocation; structural recipes do not change that.

## Related

- [Observe and propose](observe-propose.md)
- [Spanwit overview](../spanwit-overview.md)
- Dry-run default: ADR-0002

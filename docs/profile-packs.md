# Experimental profile packs

A profile pack groups named recipes into personas such as `developer` and
`media`. Each recipe uses a supported capability and an existing Spanwit
configuration policy. Editing membership or policy parameters requires no
scanner code. Profile packs never execute deletions automatically: validation,
preview, and daemon proposal evaluation are strictly non-destructive.

```sh
spanwit --read-only profile validate docs/examples/profiles/developer.json
spanwit --read-only profile preview docs/examples/profiles/developer.json --name developer
spanwit --read-only profile diff before.json after.json
spanwit profile activate docs/examples/profiles/developer.json --name developer --expected-revision none
spanwit --read-only profile status
spanwit profile deactivate --expected-revision <sha256:...>
```

Policy roots must be absolute or home-relative (`~/`); cwd-relative roots are
rejected. Preview lists the resolved enabled roots so CLI and future daemon
clients can review the actual scope.

The example selects old Grype staging with the existing signature and a 24-hour
age floor. Its cache location is for macOS; select the appropriate cache root
for your machine. Signature patterns are relative to the configured root, so
retain the `grype/db` components beneath that root. On macOS, when a recipe
roots at `~/Library/Caches` and runs under a `launchd` background user service,
exclude Apple TCC-protected directories (`com.apple.*`, `CloudKit`,
`FamilyCircle`, `GeoServices`, `PassKit`) and unrelated large caches (`go-build`,
`uv`, `Homebrew`) via `ignores` so background proposal evaluation neither triggers
TCC prompts nor walks unrelated cache trees. The empty media persona
intentionally enables no cleanup rules.

A recipe currently supports `directory-prune` only, with signature-backed
policy targets. Custom signatures can use existing evidence checks; their
`safe_to_prune` value is policy, not proof of regenerability. Catalog overlays
remain diagnostic metadata. No arbitrary command text, unknown capabilities,
pattern-only target, or unknown fields are accepted in this pack version.

`profile diff` shows both sides of recipe and persona changes, including dormant
recipes, and binds the review to the two document revisions. It does not approve
or activate either revision.

Profiles select recipe IDs in order. Remove an ID to disable that recipe for a
persona. Every recipe is validated, including recipes not selected by a persona;
invalid dormant policy cannot be silently activated later. Documents are JSON,
limited to 1 MiB and 32 nested containers; duplicate object keys are rejected.
The revision is SHA-256 over the exact input bytes. Whitespace changes therefore
produce a different revision; it is a content identity, not a signature or grant.

Preview uses the existing prune planner. It reports matches and withholding
reasons by recipe, flags incomplete results, and always reports
`execution_authorized: false`. Candidates can overlap between recipes; do not
sum their raw recipe sizes as expected reclaim. There is no executable exact-plan
handoff in preview. A `prunable` row means the planner's current conditions
matched, not that a profile has permission to delete it.

## Per-recipe budgets and volume objectives

Recipes may optionally declare a `budget` ceiling (`max_bytes`, optional
`eviction_order` `idle-oldest-first`, and `authority: "propose"`),
and profiles may optionally declare a `volume_objective` (`floor_bytes`,
`recovery_target_bytes`, optional `path`, and optional `max_sample_age`).

- **Per-recipe budget evaluation (`budget`)**:
  - Measures `observed_bytes`, `protected_bytes` (candidates withheld by recency/age
    or incomplete sizing), and `eligible_bytes` (`state: "prunable"` with complete
    sizing).
  - When `observed_bytes <= max_bytes` and the recipe scan is complete, `status`
    is `"within"` and `approval_state` is `"not_needed"`. Prunable candidates are
    annotated with `"budget_retained": true`.
  - When `observed_bytes > max_bytes`, `status` is `"exceeded"` and eligible
    candidates are selected in oldest-first activity order (`"budget_selected": true`)
    until `selected_bytes >= overage_bytes`. Newer eligible candidates within the
    ceiling remain `"budget_retained": true`.
  - Protected/recent entries are **never** selected for deletion to satisfy a
    budget. If protected entries alone exceed `max_bytes`, `protected_overage_bytes`
    reports the excess (`protected_bytes - max_bytes`); if no eligible candidates
    exist to reduce the overage, `approval_state` is `"blocked_by_protection"`.
  - If the recipe scan has warnings or incomplete candidate sizes, `status` is
    `"unknown"` and `approval_state` is `"unknown_observation"` (never `"within"`).
- **Per-profile volume objective evaluation (`volume_objective`)**:
  - Takes a fresh `statfs_primary` capacity sample on the target volume and
    evaluates `floor_bytes` (triggers evaluation when available bytes fall below
    the floor) and `recovery_target_bytes` (target free headroom, with
    `recovery_target_bytes >= floor_bytes` for hysteresis).
  - Preserves the volume's `diagnostic_level` (`ok` / `warn` / `critical`) as an
    independent field so custom volume objectives never mask or redefine system
    pressure diagnostics.
  - Deduplicates candidates across all recipes in profile order (exact path
    duplicates and ancestor/descendant containment) before computing
    `deduplicated_protected_bytes`, `deduplicated_eligible_bytes`, and
    `deduplicated_selected_bytes`. Superseded overlap candidates carry
    `dedupe_superseded_by` and increment `dropped_overlap_count`.
  - Reports `outcome` as `"met"`, `"evaluable"`, `"unmet_protected"` (with
    `uncovered_shortfall_bytes` when protected bytes prevent reaching the floor),
    or `"unknown"` (when capacity observation is stale, failed, or incomplete).

## Reviewed activation and daemon proposal evaluation

- **Stopped-daemon activation (`profile activate` / `profile deactivate` / `profile status`)**:
  - Activation and deactivation acquire the coordinator's exclusive writer lock
    (`coordinator.lock` inside `--state-dir`). If the daemon is running, offline
    activation fails closed (`writer_busy`) so the active policy cannot change
    behind a live coordinator.
  - `profile activate FILE --name PERSONA [--expected-revision REV]` compares
    `--expected-revision` (`sha256:<hex>` or `none`) against the authoritative
    `profile` journal stream head, stores the exact immutable input bytes under
    `packs/<sha256>.json`, appends one authoritative activation record binding
    the input revision, selected persona, resolved roots, and compiled policy
    digest (`compiled_policy_digest`), and writes the rebuildable convenience
    pointer (`active-profile.json`).
  - `profile deactivate [--expected-revision REV]` appends an authoritative
    revocation record while retaining all referenced pack bodies in `packs/`.
- **Daemon proposal evaluation (`daemon proposals` / `GET /v1/proposals`)**:
  - When a profile is active, the coordinator loads and verifies the immutable
    pack bytes and compiled policy binding against the journal head, evaluates
    the active persona using the same planner and budget/volume-objective
    evaluator as offline `profile preview`, and publishes the read-only result
    (`state: "evaluated"` or `"partial"`, `execution_authorized: false`).
  - If no profile is active, `/v1/proposals` reports `state: "not_evaluated"`.
    If the pack body is missing/corrupt or the compiled policy binding has
    drifted, `/v1/proposals` reports `state: "unavailable"` (never a false
    empty `"evaluated"` list).

Draft generation, consumer leases, and standing grants are not implemented in
this slice.

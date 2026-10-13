# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased]

### Added

- **Container engine headroom**: `space --containers` adds an opt-in
  `container_runtime` section: the engine (colima, Docker Desktop, OrbStack,
  podman, native Linux), its data-disk fill, storage by kind (images with
  unique and shared bytes, build cache, dangling images, foreign-platform
  images, superseded semver tags, unreferenced volumes), and relief steps
  ordered by cost (`rebuild`, `re_pull`, `data_loss_risk`). It reads the Docker
  Engine API on the local socket, read-only. `--probe-vm` measures the VM disk
  from inside with `colima ssh`; `--image-refs` lists image refs for local
  review. Refs, volume names, profiles and socket paths are otherwise never
  reported. The section is defined identically in SpaceReport v1 and v2.
- **`space --compare`** diffs `container_runtime` figures between two
  captures of the same identified engine.

### Changed

- The colima/lima VM disk recipe now starts with
  `spanwit space --capacity-only --containers --probe-vm` and no longer
  suggests `docker image prune -a` as an early step.
- CI and release builds run on `goneat-tools-runner-glibc` v0.5.9 pinned by
  index digest; bootstrap installs goneat v0.6.3.

### Fixed

- statfs block counts convert through a checked helper that refuses values
  outside int64, instead of wrapping to a wrong size.
- Command probes stop waiting on child processes that hold output open after
  their deadline, so a timed-out probe cannot stall `space`.

## [0.2.1] - 2026-10-11

### Added

- **VM disk diagnostics**: `space` recognizes colima/lima VM data disks as a
  diagnostic-only hotspot with a propose-only recipe (`docker system df`, image
  and build-cache prunes, then `fstrim` in the guest). The recipe reports the
  disks' total logical size and the bytes actually allocated on the host; VM and
  disk file names are never printed.
- **grype download residue**: the grype cache recipe lists only leftover
  `grype-db-download<N>` directories beside the live database, one exact
  `rm -rf --` suggestion each, and names the live database to keep.
- **Temp residue summary**: temp plane coverage gains a `residue` summary of the
  direct children of each temp root (entry count, hidden count, and per-family
  count, bytes, oldest and newest) keyed by a fixed family table. Entry names
  are never reported; anything outside the table counts as `other`.
- **Homebrew and Scoop**: `brew install 3leaps/tap/spanwit` (macOS Apple
  Silicon, Linux) and `scoop install spanwit` (Windows amd64, arm64).
- **`make release-preflight`**: pre-tag gate for a clean tree at `origin/main`,
  a public repository, changelog and notes, workflow pins, and tests from a
  clean clone.

### Changed

- Go 1.26.9 is the minimum toolchain, and CI and release builds run on
  `goneat-tools-runner-glibc` v0.5.8 pinned by index digest.
- GitHub Actions are pinned by commit SHA to their Node 24 releases, and runners
  to `ubuntu-24.04`; a check refuses unpinned, Node 20 and `-latest` references.
- Release artifacts are fetched from an explicitly named workflow run and
  attempt, which must be the latest successful attempt for the tag.
- `make release-publish` promotes a draft only after checking the tag, notes,
  asset set, manifests, minisign and PGP signatures against the pinned keys, and
  that every draft asset is byte-identical to the signed local copy.
- Manifest signing requires both the minisign and PGP keys.

### Fixed

- The recreated-directory witness tests no longer fail when Linux assigns the
  same creation time within one clock tick.
- Validation tests locate the project root from the source tree instead of the
  home directory.

## [0.2.0] - 2026-10-08

### Added

- **Signed release tags**: release tags are GPG-signed on the maintainer
  machine and verified against the public key committed in
  `docs/security/release-signing-keys.asc` and `keys/expected-fingerprints.txt`.
  The release workflow is read-only and builds the packages as a workflow
  artifact; the maintainer creates and publishes the release locally after
  re-verifying the tag. See ADR-0005.
- **Readable folder audit (`inventory --directories`)**: lists the folders that
  hold the most space, using allocated size by default with an inclusive
  `--directory-min-size` floor, output depth and top-K. Text output shows size,
  bound, coverage, floor decision and depth; unavailable sizes are never treated
  as zero. `--format jsonl` emits the versioned profile
  `spanwit.filesystem-inventory-aggregation/v2` with a published schema and an
  in-repo stream validator (`inventory.ValidateAuditStream`).
- **Directory-audit coverage attestation**: `--coverage-attest PATH` with
  `--directories` attests traversal of the full declared subject. Failed,
  canceled or incomplete audits withhold it with a stderr notice; the audit
  stream is unchanged by the request.
- **Typed aggregate-budget failure**: exhausting `--max-aggregate-directories`
  reports the limit and retained counts, prints flag-level guidance on stderr,
  and the help and docs explain how the directory bounds differ.
- **`spanwit inventory`**: bounded, read-only filesystem inventory with a typed
  JSONL profile, explicit coverage gaps, `--summary-only`, exclusions,
  `--directory-summary` aggregation and typed `--directory-accounting` claims.
- **Inventory coverage attestation**: opt-in `--coverage-attest PATH` writes a
  separate, validated `coverage-attestation/v0` document with opaque gap scopes.
- **`spanwit diagnose`**: file-level diagnostics under a dedicated policy plane.
- **Capacity accounting**: `space --capacity-only` reports, held-open capture
  and `space --compare`.
- **Use-domain catalog**: schema-backed catalog with an enable policy, crisis
  recipes and a curated macOS capacity catalog with explicit partial hotspots.
- **Destructive-path guard**: one shared guard with authorization-boundary
  provenance for every deletion path.
- **Reclaim operations**: a reclaim-ops harness, including safe reclaim of agent
  temp residue.

- **Inventory remote switch and stall backstop**: `inventory` skips
  network-filesystem and cloud-placeholder (File Provider) directories below a
  root by default, deciding from `lstat`/`statfs` before any open, and records
  them as non-affecting `remote` gaps (`remote_skip_count`); `--include-remote`
  walks them. Each directory open is bounded by `--stall-timeout` (default 60s):
  a non-responding open raises one path-free stderr alert (also under `--quiet`)
  and its subtree becomes a path-free, completeness-affecting `stalled` gap
  (`stalled_count`, exit 1) instead of hanging the walk. `--stall-timeout 0`
  waits indefinitely. `diagnose` has the same defaults, flags, and alert; a
  stalled directory makes its coverage incomplete.

- **Explicit completed-with-gaps result for `space`**: reports carry
  `completion` (`complete` | `partial` with `warning_count`), text ends with a
  `Result:` line, and stderr adds a one-line partial summary. Exit codes are
  unchanged (0 complete, 1 partial); exit status is not prune authorization.
- **Activity evidence in text prune plans**: `prune`/`scan` text rows show
  `last <date> (<age>)` or `activity unknown`, with a ranking header. Unknown or
  incomplete activity is never labeled idle; ranking is unchanged and the JSON
  plan keeps its activity fields.

- **Temp-plane coverage under pressure**: when primary write pressure is
  warn/critical, `space` reports `temp_plane_coverage` naming the OS temp roots
  the run did not walk (shared `/tmp` and the per-user temp directory), each
  with a bounded apparent size (`measured` / `partial` lower bound /
  `unavailable`) and a root-level follow-up `space` command. Roots are deduped
  by directory identity; planes overlapping the analysis root are omitted. The
  probe lists directories and reads metadata only, never follows symlinks or
  crosses devices, and returns at a 2 s deadline. Observation only: no child
  names are disclosed and no prune authority is granted. The crisis example
  profile gains an inert, age-gated temp-path template.

- **Observe + propose (first slice)**: `spanwit observe` samples registered
  volumes by opaque volume identity (not mount path alone), applies pressure
  hysteresis, computes growth/runway only from two complete comparable samples,
  and emits dry-run-safe proposals (generic notes until catalog evidence exists).
  Local versioned state under `--state-dir` fails closed on corruption; exclusive
  cycle lease; fixed sample worker pool; registration caps. Coverage gaps
  (error/partial) force reconcile without growth across the gap. `--foreground`
  is cycle-scoped. Health `mutation_contract` mirrors invocation assertion
  (`open`/`read_only`). Path-safe sample errors. Injectable seams for tests.

- **Guided structural recipes (first journey)**: `space` can emit
  multi-step `kind=structural` recipes with ordered steps (observe, purge
  hint, config snippets, verify). First journey: **Cargo `target/` relocation**
  (`cargo-target-placement`) — evidence-backed project roots, purge-before-move
  via existing prune handoff (no `--execute` suggested), per-project
  `CARGO_TARGET_DIR` copy-paste snippets, and after-cutover `space` verify.
  Print-only: never writes config, never moves trees. Space-report schema v1/v2
  gain optional `kind`/`steps`. Guide: `docs/guides/cargo-target-relocation.md`.

- **Invocation no-mutation assertion**: global `--read-only` and
  `SPANWIT_READ_ONLY` require the resolved invocation to have no mutating
  capability. Commands declare observational / planning / mutating capability;
  `prune --execute` elevates planning to mutating and is rejected under
  assertion before discovery. Flag/env conflict fails closed. Inventory headers
  and space/capacity reports expose `mutation_contract` (`open`|`read_only`).
  Defense in depth only — does not replace dry-run default, `--execute`, or
  path guards.

- **`space` inventory UX**: default completed text report uses
  actionability order (pressure → can free now → withheld → named caches →
  unverified → unknown → placement notes). Opt-in repeatable `--class` and
  `--domain` filters (OR within flag type, AND across types; `StringArray` so
  packed CSV is not silently accepted). `--class` uses ADR-0004 state values
  only (`prunable|withheld|diagnostic-only|unverified|unknown`); `--domain`
  is segment-aware taxonomy match on `signature` / provisional `catalog_id`
  (no path/label guessing). Early OnPartial triage is also projected so
  filtered views do not flash hidden rows. JSON adds optional `domain`,
  `ecosystem`, `catalog_id`, `recipe_ids`, and top-level `applied_filters`
  when filters are set. Handoff is built from the full filtered prunable set
  (independent of `--top`); class filters excluding prunable never embed
  executable candidates. Loader rejects tampered carriers that disagree with
  `applied_filters`.
- **Rust depth (idle rank, partial target, placement)**: verified candidates
  carry `last_activity_at` / `activity_basis` / `activity_incomplete` from the
  sizing walk (newest descendant file mtime; symlink children not followed).
  `space --sort idle` (default) and `prune --sort idle` rank complete cold
  activity first; incomplete ranks last. Advisory `rebuild_expectation` is
  derived from complete activity only. `space`/`prune --reclaim-scope incremental`
  admits Cargo `target/**/incremental` only (parent provenance + containment
  antichain). Reports and exact plans carry required `sort_mode` /
  `reclaim_scope` for fail-closed replay. Placement notes on warn/critical
  pressure recommend project-specific `CARGO_TARGET_DIR` without auto-relocation.
- **`space` prune handoff + guided recipes**: `space` emits `prune_handoff` with
  an embedded `exact_plan` (signature + per-candidate filter provenance).
  Revalidate via `prune --from-space-report <json>` (supports `--execute` only
  for paths that still match that plan). Bare `--allowlist` is dry-run-only
  against built-ins. Optional rustup recipes use argv-safe commands
  (`diagnostic-only`). Analysis order is pressure → hotspots → recipes → deep
  walks; text mode streams early triage; critical pressure caps deep max_depth
  and skips unknown. Symlink path components (non-shallow) rejected on exact
  plan and execute.
- **`spanwit space`**: read-only disk diagnostic (pressure, verified reclaimable,
  diagnostic-only cache/toolchain hotspots, name-shaped unverified mass, top
  unknown dirs). Core lives in `internal/space` (`Analyze` → `Report`);
  CLI is a thin adapter (ADR-0003). JSON schema
  `config/schema/spanwit-space-report.v1.schema.json` enforces section-specific
  trust states (future API body). No `--execute` path. Trust vocabulary extended
  with `diagnostic-only` / `unverified` / `unknown` (**ADR-0004 accepted**).
  Shared discovery extracted to `internal/engine` for scan/prune/space. Primary
  pressure is the write/home volume (optional analysis-root pressure when mounts
  differ); discovery and sizing are depth-bounded and context-cancellable with
  honest `size_incomplete` lower bounds; display `--top` no longer corrupts
  classification.
- Crisis recovery example config `docs/examples/configs/crisis-dev.yaml`: no age
  gate, modest `min_size`, signature-backed `development.rust.cargo-target` under
  `~/dev`. README and `docs/configuration.md` document the dry-run → execute
  recovery recipe and label other examples as maintenance vs crisis posture.

### Changed

- **Bound-aware `space` presentation**: incomplete / depth-bounded sizes render
  as lower bounds in human text (`≥…`), never as bare exact totals. Size ranking
  (verified `--sort size` and diagnostic hotspot/unverified/unknown lists) places
  complete measurements before incomplete lower bounds so raw bytes are not
  treated as comparable. When an opt-in `min_size` floor cannot be decided from a
  lower bound, the outcome is `indeterminate` (verified: withheld reason
  `indeterminate`; diagnostic notes/warnings use the same term) rather than
  matched or excluded-as-small. Machine JSON keeps the existing
  `size_incomplete` field as the incompleteness signal — no second truth source.
  Notes steer follow-up to focused complete inventory, not inferred reclaim.

- **Breaking (v0.x age window):** config age fields are rename+flip. **`min_age`**
  is the older-than floor (what `max_age` meant previously). **`max_age`** is now
  a younger-than ceiling (new). Eligible when `min_age ≤ age ≤ max_age` for each
  bound that is set; both unset ⇒ no age gate. Example configs migrate
  maintenance floors to `min_age`. Update any hand-authored configs that used the
  old `max_age` floor meaning.
- Config load no longer seed-merges silent `defaults.min_size` / age values.
  Omitted age and size fields stay unset and mean **no gate** (opt-in). Partial
  `defaults:` blocks no longer inherit a ghost older-than floor that hid fresh
  build output during crisis recovery.
- Age and size filter exclusions are no longer silent drops: discovered matches
  that fail the age window or `min_size` are reported as **withheld** candidates
  with `withheld_reason` of `age` or `min_size` (same trust model as
  `safe_to_prune`). Text/JSON include a truthful **effective filters** aggregate
  plus per-candidate resolved gates (`min_size` / `min_age` / `max_age`;
  defaults → path → target; `none` / `mixed` when appropriate) and a per-reason
  withheld summary. `--execute` still deletes only `prunable` rows.
- `prune` now enforces `safe_to_prune` on signature-backed matches (fail-closed):
  a signature is deletable only when `safe_to_prune: true`. Matches that are not
  safe are **withheld** — still reported in `prune` text and JSON output (with
  `state: "withheld"` and `withheld_reason: "safe_to_prune"`) but never deleted by
  `--execute`. Raw `pattern:` targets remain prunable as explicit user intent.
  (`scan` is unchanged — aligning it with the signature engine is planned for a
  future release.)
- The `prune --format json` summary now reports an explicit prunable/withheld
  split (`prunable_*` and `withheld_*` fields) alongside the discovered totals;
  each candidate carries a `state` field. (Schema `spanwit-prune-plan.v1`.)
- **Schema contract (breaking during alpha):** the new required summary fields and
  the required candidate `state` extend `spanwit-prune-plan/v1` in place. Because
  that schema is `additionalProperties: false`, output no longer validates against
  the v0.1.0 copy of the v1 schema. Pre-1.0 the `v1` schema id is treated as
  **mutable**, so validate against the schema shipped with the matching spanwit
  version rather than pinning the v0.1.0 schema.
- Requires the patched Go toolchain; gofulmen is updated to v0.3.6 and the
  `golang.org/x/mod` floor is raised to v0.40.0.

### Fixed

- Overlapping `paths[]` roots no longer double-count the same absolute candidate
  in prune/scan plans (e.g. `~/dev` plus `~/dev/acme`). Totals and candidate
  lists use first-match only; a warning reports how many duplicates were
  dropped so nested profiles stay honest for crisis operators.
- Temp-plane text output is sanitized for terminal display.

## 0.1.0

Initial release of Spanwit — a context-aware, signature-driven disk-reclamation
CLI. Dry-run by default; recognizes regenerable build output in context rather
than by pattern alone.

### Added

- `scan` — read-only analysis that reports reclaimable directories above a size threshold.
- `prune` — plan and (with explicit `--execute`) delete reclaimable directories. Dry-run is the default; `--format json` emits a schema-validated prune plan for pipelines.
- Signature-aware config: a directory is only reclaimable when its context matches (e.g. `target/` alongside a `Cargo.toml`), driven by a YAML config validated against a JSON Schema.
- Config resolution precedence: `--config` flag > `SPANWIT_CONFIG_PATH` > default `~/.config/spanwit/config.yaml`.
- Standard microtool commands: `version`, `envinfo`, `doctor`, plus an `example` command and `validate` (schema/data validation).
- Strict stdout purity so output composes in pipelines; all logs go to stderr.
- App identity via `.fulmen/app.yaml` (binary `spanwit`, env prefix `SPANWIT_`).
- goneat-based quality gates (`make pr-final`, `make prepush`), GitHub Actions CI, and a tag-triggered release workflow.

[Unreleased]: https://github.com/3leaps/spanwit/compare/v0.2.1...HEAD
[0.2.1]: https://github.com/3leaps/spanwit/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/3leaps/spanwit/releases/tag/v0.2.0

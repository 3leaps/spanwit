# Configuration

`spanwit` follows a standard, developer-friendly configuration layout.

## Invocation no-mutation assertion

Global defense-in-depth (does not replace dry-run default or `--execute`):

| Source | Name                | Effect                                                           |
| ------ | ------------------- | ---------------------------------------------------------------- |
| Flag   | `--read-only`       | Assert resolved invocation has no mutating capability            |
| Env    | `SPANWIT_READ_ONLY` | Same; truthy `1\|true\|yes\|y\|on`, falsy `0\|false\|no\|n\|off` |

Commands declare capability: **observational** (scan/space/inventory/…),
**planning** (`prune` dry-run), **mutating** (`prune --execute` / `-e`, and
`inventory --coverage-attest` which publishes a durable file). Under assertion,
a mutating resolution is rejected before discovery or publication. Explicit flag
and env that disagree fail closed. Inventory JSONL headers and `space` reports
include optional `mutation_contract` (`open` or `read_only`); new CLI runs always
emit it, while historical inventory streams without the field remain valid under
the same profile/header identity.

Shell completion (`completion bash|zsh|…`) remains available under assertion.

## Recommended Locations

### User Configuration (Recommended)

**Primary location:**

```
~/.config/spanwit/config.yaml
```

This is the default location the tool looks for.

### Alternative / Testing Configurations

You can keep multiple configs in the same directory:

```
~/.config/spanwit/
├── config.yaml          # Main / daily config
├── crisis-dev.yaml      # Capacity crisis (no age gate; cargo signatures)
├── dogfood.yaml         # Aggressive scanning for this machine
└── conservative.yaml    # Very safe settings
```

Use them explicitly with the `--config` flag:

```bash
spanwit prune --config ~/.config/spanwit/crisis-dev.yaml
```

## Example configs (shipped)

Under `docs/examples/configs/`:

| File                       | Posture     | Notes                                                                |
| -------------------------- | ----------- | -------------------------------------------------------------------- |
| `crisis-dev.yaml`          | **Crisis**  | No age gate; `development.rust.cargo-target` only; modest `min_size` |
| `typical-dev.yaml`         | Maintenance | `min_age: 90d` floor for routine reclaim                             |
| `aggressive-cleanup.yaml`  | Maintenance | Shorter floors, broader patterns                                     |
| `minimal.yaml`             | Minimal     | Empty defaults; path root only                                       |
| `example-dev-machine.yaml` | Maintenance | Multi-root example                                                   |
| `with-path-defaults.yaml`  | Maintenance | Path/target override demo                                            |
| `domain-catalog.yaml`      | Maintenance | `domains.enabled` policy + user catalog overlay demo                 |

Copy a profile into `~/.config/spanwit/` or point `--config` at the path in a
checkout.

## Crisis recovery (disk full)

When free space is critically low and you need **signature-backed** reclaim
without a maintenance older-than floor hiding fresh `target/` trees:

```bash
# 1) Read-only triage — pressure first; filter to what you can free now
spanwit space ~/dev --class prunable --sort idle

# Named caches / toolchains (diagnostic-only; not deleted by spanwit)
spanwit space ~/dev --class diagnostic-only --domain development

# Domain-narrowed verified reclaim (segment-aware signature match)
spanwit space ~/dev --domain development.rust --class prunable

# 1b) Temp planes — under warn/critical pressure the report names the OS temp
#     roots it did not walk (temp_plane_coverage) with a bounded size. Inspect
#     one explicitly; this is observation only:
spanwit space /private/tmp        # macOS shared temp (/tmp on Linux)
spanwit space "$TMPDIR"           # per-user temp

# 2) Save JSON (carries prune_handoff + applied_filters when filters used)
spanwit space ~/dev --class prunable --format json > /tmp/space-report.json

# 3) Dry-run revalidate from the report carrier
spanwit prune --from-space-report /tmp/space-report.json

# 4) Delete only after revalidate matches
spanwit prune --from-space-report /tmp/space-report.json --execute

# Alternative: config-driven plan (maintenance/crisis profiles)
spanwit prune --config docs/examples/configs/crisis-dev.yaml
spanwit prune --config docs/examples/configs/crisis-dev.yaml --execute
```

If you are not in a spanwit checkout, copy the example first:

```bash
mkdir -p ~/.config/spanwit
cp /path/to/spanwit/docs/examples/configs/crisis-dev.yaml ~/.config/spanwit/
spanwit prune --config ~/.config/spanwit/crisis-dev.yaml
```

**Temp planes.** `temp_plane_coverage` appears only under warn/critical
pressure. Each plane carries a canonical root, `size_status`
(`measured` | `partial` lower bound | `unavailable`), and a root-level
follow-up command. The size probe lists directories and reads metadata only,
never follows symlinks or crosses devices, and stops at a 2 s deadline so it
cannot stall triage. It never names what is inside a plane. Listing a plane
grants no prune authority: `space --config` imports signatures and targets but
never activates a config's `paths:`, and the shipped crisis profile enables no
temp path. Its temp entry is a commented template that keeps an age gate,
because other sessions may still be using recent temp trees.

**Safety notes:** dry-run is the default (ADR-0002). The crisis profile targets
only `development.rust.cargo-target` (context-verified, `safe_to_prune`). Rebuild
cost is real after `--execute`; prefer idle repos when possible.

### Deletion guardrails — what `--execute` refuses

Every deletion passes through one shared guard, whatever produced the candidate
(config discovery, `--from-space-report` replay, or a future capability). A
candidate is deleted only when it carries a **non-inferred authorization
boundary** and a known provenance:

- **config discovery** → the enabled path-profile root is the boundary;
- **`--from-space-report`** → the carried `exact_plan.analysis_root` is the
  boundary.

A bare `--allowlist`, a catalog class, or `dirname(candidate)` is **never**
authorization: the allowlist surface is dry-run only, and a plan whose
provenance is missing or unknown fails closed.

Deletion is refused at two layers:

- **Plan admission — hard error, nothing deletes.** A `--from-space-report`
  candidate outside the carried `analysis_root`, a report missing `analysis_root`,
  or a relative/unclean authority path is a structural boundary violation: the
  whole command fails before any deletion.
- **Execute guard — skips the candidate, warns, and returns non-zero.** Each
  admitted candidate is deleted only behind a token that re-checks it immediately
  before removal. A partial run never reports as full success.

The execute guard refuses a target that:

- is **not a strict descendant** of its authorization boundary (component-aware,
  so `/root/ab` is not treated as inside `/root/a`), escapes it via `..`, equals
  the boundary (config discovery and space-report replay are strict-descendant
  only — boundary equality is reserved for a future registered capability), sits
  on a **different volume**, or has a **nested mount on a different device**
  inside the tree being removed;
- is a **filesystem/volume root**, your **home directory** (matched by
  filesystem identity, not only by spelling), or a **non-directory**;
- has a **symlink component** anywhere in its path (leaf or ancestor), excluding
  only well-known OS volume aliases;
- **changed identity** between authorization and deletion — re-checked with
  `os.SameFile`, with symlink components re-validated, immediately before removal.

Signature **evidence** gathering is held to the same posture: required ancestor
and child evidence is inspected with `Lstat` (a symlinked marker never satisfies
evidence), and configured evidence names must be local relative paths (no
absolute path or `..` escape).

**Residual risk (honest):** `os.RemoveAll` resolves by path, so a race in the
window after the final identity re-check cannot be fully excluded without
descriptor-relative (no-follow) removal. The guard **minimizes and documents**
this window; deletion is **not** claimed to be race-free.

### Idle ranking, inventory filters, partial reclaim, placement

- **`--sort idle`**: ranks verified candidates by complete activity (oldest
  first). Activity is the newest observed _descendant file_ mtime during sizing
  (not the `target/` directory mtime alone). Depth-bounded or symlink-stopped
  walks mark `activity_incomplete` and rank **last** (never falsely cold).
  Ranking is **advisory only** — it does not authorize deletion.
- **`--sort size`**: bound-aware largest-first order (complete measurements
  before incomplete lower bounds; within each band, larger first). Prune
  default. `space` defaults to `idle`.
- **Bound-aware sizes**: depth-bounded or coverage-gapped observations set `size_incomplete` and are
  lower bounds only. Human `space` text prefixes incomplete sizes with `≥`;
  JSON keeps numeric `size_human` plus `size_incomplete` (no second truth). A
  `min_size` floor that cannot be decided from a lower bound is
  **`indeterminate`** (verified: withheld reason; diagnostic: include + note),
  never silently matched or excluded as small. Suggested follow-up is focused
  complete inventory, not inferred reclaim volume.
- **Observation walker**: `space --workers auto|N` (auto = 4) and
  `--backend auto|serial|parallel` control hotspot, unverified, and unknown
  observation. Discovery and sizing retain their depth bounds; critical pressure
  reduces discovery depth and skips unknown, but is not a total run deadline.
  Observation sizes sum regular-file apparent bytes; symlinks are not followed
  or counted and are disclosed as coverage gaps. Verified discovery, sizing, and
  prune handoff use their existing engine independently of these controls.
- **Remote and stalled observation**: automatically selected catalog/discovered
  roots stay subject to remote/cloud exclusion **where the classifier recognizes
  them**. Detection is available on **macOS/Linux only** and is not exhaustive;
  Windows and other platforms may walk network directories even without
  `--include-remote`. On supported platforms, `--include-remote` opts recognized roots in;
  an operator-named analysis root retains the explicit-root opt-in. Observation
  directory opens have a 60-second `--stall-timeout` (0 waits forever); this is
  not a deadline for other filesystem calls or the verified phase. Stall alerts
  carry no paths. These walker flags are rejected with `--capacity-only` and
  `--compare`.
- **Unmeasured is not zero**: a never-read observation candidate has no numeric
  report row. Typed depth, permission, admission, remote, and stall gaps are
  summarized by kind with a wholly-unmeasured count in one path-free warning per
  plane; the report is partial (exit 1). Coverage warnings survive class/domain
  filters and `--top`. Observed partial rows retain lower-bound numbers and stay
  visible outside the complete-row `--top` budget. A measured empty directory
  remains an exact zero. Overlapping automatic roots are deterministically
  reported as gaps instead of being counted twice; valid sibling roots continue
  when an automatic root becomes inaccessible.
- **`--class` / `--domain` (opt-in)**: inventory projection only. Repeatable
  flags; OR within one flag type, AND across types. Omission keeps the broad
  inventory. `--class` accepts ADR-0004 states (`prunable`, `withheld`,
  `diagnostic-only`, `unverified`, `unknown`) — not free-form aliases.
  `--domain` matches signature / provisional catalog ids with segment boundaries
  (`development.rust` matches `development.rust.cargo-target`, not
  `development.rustup`). No path or label heuristics. Filters apply to early
  triage and the completed report; pressure always prints. Handoff membership
  follows the full filtered prunable set (not `--top` truncation).
- **`--reclaim-scope incremental`**: instead of whole `target/`, admit only
  `target/**/incremental` caches under a signature-verified cargo parent.
  Mutually exclusive with whole-target for a tree; parent+child containment is
  collapsed. Every partial path still runs the full symlink/root/home execute
  guards.
- **`rebuild_expectation`**: advisory refill likelihood (`high`/`medium`/`low`)
  derived from complete activity; omitted when activity is incomplete/unknown.
- **Placement**: when pressure is warn/critical, `space` notes suggest
  project-specific `CARGO_TARGET_DIR` on a roomier volume. Advice only — never
  auto-relocates. Relocated targets without a `Cargo.toml` ancestor are **not**
  context-verified by the built-in signature.
- **Carrier policy**: SpaceReport and exact_plan require plan-level
  `sort_mode` and `reclaim_scope`. Optional `applied_filters` records class/
  domain projection when used. Handoff and exact candidates must agree on
  scope/parent and applied filters; `prune --from-space-report` inherits
  carrier sort unless `--sort` is set explicitly.

## Use domains (domain catalog)

Spanwit groups disk concerns into **use domains** — top-level product
categories, not filesystem roots. The built-in catalog ships one fully wired
domain, `development` (language build trees, toolchains, package/module caches,
tool databases), plus `media` and `ml` as documented **stubs** (model and
extension point only; no reclaim rules yet). The catalog is the SSOT for stable
`domain.ecosystem.name` ids, labels, rebuild expectation, per-platform
locations, and recipe/signature linkage; it is validated against
`config/schema/spanwit-domain-catalog.v1.schema.json`.

### `domains.enabled` — discover-broad / authorize-narrow

The optional `domains` block is **policy**, not view projection. It is distinct
from the `space --domain` view filter (which only projects visibility and never
authorizes anything).

```yaml
domains:
  enabled:
    - development
```

Writing `domains.enabled` is the **consent moment** for domain policy. Omission
and explicit configuration deliberately behave differently on the two surfaces —
and, critically, **not writing the block is not consent to restrict reclaim**:

| Surface                                                   | `domains.enabled` **omitted**                                                                                                                              | `domains.enabled` **explicit** (incl. `[]`)                                                                                                                                             |
| --------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Signature reclaim** — `prune`/exact handoff/`--execute` | **No domain reclaim gate.** Every declared signature (including custom non-`development` ones) stays eligible under existing rules (pre-feature behavior). | Gated to the configured set. A disabled-domain candidate is **withheld** with `withheld_reason: domain_disabled`, excluded from the handoff, and never deleted (even with `--execute`). |
| **Domain-driven recipes** (catalog)                       | Default to `{development}`.                                                                                                                                | Respect the configured set; `enabled: []` ⇒ no domain-driven recipes.                                                                                                                   |

So omission preserves current feel for the surface that **mutates** (prune),
while still defaulting recipes to the curated `development` set. The reclaim gate
is enforced by one mechanism (the plan engine), so when it applies it covers
**both** `space` (report + handoff) and direct `prune --config` identically.
Pattern-only targets carry no domain and are never domain-gated.

`space` always **reveals** known mass across all domains regardless of policy
(honest inventory). When a config supplies a `domains` block, the SpaceReport
echoes the resolved set as `enabled_domains`.

### User catalog overlays

You may extend the catalog under `domains.catalog` with **full-entry
replacement by id**:

```yaml
domains:
  enabled: [development]
  catalog:
    - id: development.node.pnpm-store
      label: pnpm content-addressable store
      class_ceiling: diagnostic-only
      rebuild_expectation: medium
      locations:
        default:
          rel_home: .local/share/pnpm/store
```

Overlays are **fail-closed**: an overlay may add diagnostic inventory but can
**never** set `class_ceiling: prunable`. Catalog membership is classification
metadata, not deletion authority — only a code-registered signature (with
evidence and guards) can ever produce a delete path. Dangling signature/recipe
links, duplicate ids, and trust-ceiling escalation are rejected. This validation
runs at config load, so `scan`, `prune`, and `space` all reject the same invalid
catalog.

Recipe emission follows the resolved entry's `recipes` links. Guided recipes
carry **tool-global** commands (e.g. `go clean -cache`, `rustup toolchain …`)
whose real target is the tool's own canonical location, so a recipe is emitted
only when the linking entry's location matches the **built-in canonical**
location. Dropping a recipe link suppresses the recipe; **repointing** a
recipe-bearing built-in entry also suppresses its recipe (the entry still shows
as a diagnostic hotspot at the new location) so the reported path and the
command's real target can never diverge. A same-location relabel keeps the recipe.

A full-entry overlay **may** replace a built-in `prunable` entry with a
`diagnostic-only`/`recipe` one — a fail-closed **downgrade/relabel**. Because
catalog class is metadata (not authority), this lowers the entry's
classification only; it does **not** disable the underlying signature capability
(the signature still executes unless the domain is disabled via
`domains.enabled`). Raising an entry back to `prunable` via overlay is rejected.

## Philosophy

- **File-based config must be schema-backed** (strict validation).
- CLI flags take precedence for ad-hoc use.
- We deliberately chose `~/.config/spanwit/` over `~/Library/Application Support/Spanwit` because this is a developer tool.

## Schema Contracts

Configuration files are validated against:

```
config/schema/spanwit-config.v1.schema.json
```

Structured prune output is validated against:

```
config/schema/spanwit-prune-plan.v1.schema.json
```

The domain catalog (built-in data and user overlays) is validated against:

```
config/schema/spanwit-domain-catalog.v1.schema.json
```

Other published contracts (space and capacity reports, inventory and
aggregation profiles, coverage attestations) also live in `config/schema/`;
see [inventory](inventory.md) for the inventory profiles. The normal quality
gate meta-validates every schema there and validates the example configs and
the built-in catalog:

```bash
make validate-schemas
```

Use JSON output when another tool needs to consume a prune plan:

```bash
spanwit prune --config ~/.config/spanwit/config.yaml --format json
```

## Rule Shape

- `paths` define roots that may be scanned. Overlapping roots (for example
  `~/dev` and `~/dev/acme`) are allowed; the same absolute candidate path is
  counted **once** (first path profile wins). A warning reports how many
  duplicates were dropped so plan totals stay honest.
- `targets` define reclaimable patterns under a root.
- `signature` targets classify candidates using a pattern plus evidence.
- `ignores` define paths to skip while walking.
- `min_size`, `min_age`, and `max_age` may be set globally, per path, or per target. More specific settings win.
- Age is an **opt-in window** on file age (`now − mtime`):
  - **`min_age`** — older-than floor (only reclaim if _at least_ this old). Example maintenance posture: `min_age: 90d`.
  - **`max_age`** — younger-than ceiling (only reclaim if _at most_ this old). Example crisis cleanup of recent churn: `max_age: 7d`.
  - Both unset ⇒ **no age gate**. The loader does not inject silent age defaults.
- Size is likewise **opt-in**: omit `min_size` for no minimum-size filter.
- When age or size excludes a discovered match, `prune` reports it as **withheld**
  (reasons `age` / `min_size`) rather than dropping it silently. The plan prints
  an **effective filters** banner (resolved values, or `mixed` when candidates
  differ) and each candidate carries its own resolved `min_size` / `min_age` /
  `max_age` after defaults → path → target precedence (`none` when that gate is unset).

## Signatures

Signatures are the safer form of matching. A raw pattern such as `**/target` can find many unrelated directories. A signature adds evidence checks so `spanwit` can tell the difference between a Rust Cargo `target/` directory and another tool that happens to use the same name.

Signature ids use a simple hierarchy:

```
domain.ecosystem.name
```

For example:

```yaml
targets:
  - signature: development.rust.cargo-target
```

The built-in `development.rust.cargo-target` signature looks for directories matching `**/target`, then requires Rust evidence such as an ancestor `Cargo.toml` and Cargo-like children such as `debug/`, `release/`, or `.rustc_info.json`.

Users can extend signatures in config:

```yaml
signatures:
  development:
    dbt:
      target:
        description: dbt compiled output directory
        candidate_patterns:
          - "**/target"
        required_ancestor_files:
          - dbt_project.yml
        any_child_paths:
          - manifest.json
          - run_results.json
        confidence: high
        safe_to_prune: true
```

### `safe_to_prune` semantics

`safe_to_prune` gates whether a **signature-backed** match is eligible for
deletion. It is **fail-closed**: a signature is executable only when
`safe_to_prune` is exactly `true`; absent or `false` means _not safe_.

A match that is found but not safe is **withheld**, not hidden: it still appears
in `prune` output (and in the prune-plan JSON with `state: "withheld"` and
`withheld_reason: "safe_to_prune"`), but `--execute` will never delete it.
Discovery and execution-eligibility are deliberately separate states.

> `scan` is a separate, legacy pattern walker that does not yet consult the
> signature engine, so it does not report `safe_to_prune` state. Aligning `scan`
> with the signature engine is planned for a future release.

Raw `pattern:` targets (a target with `pattern:` and no `signature:`) are direct
user-authored intent and remain prunable — the `safe_to_prune` gate applies only
to signatures, which carry an intrinsic, catalog-level safety assertion. The
built-in `development.rust.cargo-target` signature is marked `safe_to_prune: true`,
so it continues to be reclaimed by default.

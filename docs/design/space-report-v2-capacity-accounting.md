# SpaceReport v2 — `capacity_accounting` field graph

| Field                   | Value                                                                                                     |
| ----------------------- | --------------------------------------------------------------------------------------------------------- |
| Status                  | Active product contract                                                                                   |
| Schema homes            | `config/schema/spanwit-space-report.v2.schema.json`, `config/schema/spanwit-space-compare.v1.schema.json` |
| Shared-standard posture | Product-scoped until a second independent consumer exists                                                 |

This document defines the field graph encoded by the shipping JSON Schemas.
Record shapes are strict (`additionalProperties: false`) and carrier modes use
explicit conditionals.

---

## 1. Goals

1. Explain capacity using **distinct measurement planes**, each with honest
   status — never a single “explained bytes” sum across incompatible semantics.
2. Support crisis-time **seconds-class** captures (`space --capacity-only`).
3. Allow opt-in **held-open** observation without implying reclaimability.
4. Enable **compare of two saved captures** (files/stdin) with explicit
   compatibility and coverage-gap reporting.
5. Preserve **SpaceReport v1** `pressure` and **prune handoff / exact_plan**
   safety for loaders that already exist.

## 2. Non-goals

- Daemon state, background cache, or implicit capture database.
- Private CacheDelete / unified-log parsers as stable metrics.
- Snapshot **byte** attribution on Darwin when the public API does not provide it.
- Graduation of system-managed roots into prune handoff.
- Inventory portable-profile graduation (separate product slice).
- Mutating v1 `pressure` semantics in place.
- Silently up-converting SpaceReport v1 into capacity planes for compare.

---

## 3. Carrier rules (SpaceReport v2)

### 3.1 Identity

| Field     | v1                                                        | v2                                                        |
| --------- | --------------------------------------------------------- | --------------------------------------------------------- |
| `$schema` | `https://schemas.3leaps.dev/spanwit/space-report/v1.json` | `https://schemas.3leaps.dev/spanwit/space-report/v2.json` |
| `version` | `1`                                                       | `2`                                                       |

Loader dispatch: read a minimal envelope; require `$schema` / `version` **pair
agreement**; then validate against the matching embedded schema.

### 3.2 `capture_mode` (required on every v2 report)

```text
capture_mode: "full" | "capacity_only"
```

| Mode            | Required surface                                                                                                                                                                                                        | Forbidden surface                                                                                                                                                                 |
| --------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `full`          | Complete v1 inventory/policy shape: deep inventory sections, and the same tuning metadata family as v1 (`min_size`, `top`, `max_depth`, `sort_mode`, `reclaim_scope`, …) plus optional handoff/recipes/filters as today | —                                                                                                                                                                                 |
| `capacity_only` | `root`, `pressure`, optional `analysis_pressure`, capture identity via `capacity_accounting`, and `capacity_accounting`                                                                                                 | Deep inventory, recipes, handoff, applied filters, and their tuning metadata (`min_size`, `top`, `max_depth`, `sort_mode`, `reclaim_scope`, etc.) — schema **forbids** these keys |

One strict SpaceReport v2 schema with conditionals by `capture_mode`. **Do not**
mint a separate `space-report-capacity.v2` carrier.

### 3.3 v1 pressure coexistence

- Keep v1 `pressure` / `analysis_pressure` field meanings and required numeric
  presence. v2 does **not** redefine `avail_bytes`.
- `capacity_accounting` is required on every v2 report (including capacity-only).

### 3.4 Prune loader coexistence

- **v1** validation and replay unchanged.
- **Full v2** with a handoff: existing exact-plan and applied-filter invariants
  unchanged.
- **Full v2** with honestly empty/absent handoff: may preserve current empty-plan
  replay behavior.
- **Capacity-only v2 is not a prune carrier.** `prune --from-space-report` must
  **fail clearly**; it must not degrade into the nil-handoff empty-plan path.

### 3.5 Application invariants (Go; not all expressible in JSON Schema)

- Carrier `generated_at` consistent with `capacity_accounting.capture.captured_at`
  policy (document exact rule at schema mint; at minimum both required and
  timezone-normalized).
- Carrier `root` is the requested analysis scope. `capacity_accounting.target`
  is the capacity observation anchor and exactly matches carrier `pressure`
  path/mount/volume identity; it need not equal `root`.
- When filesystem plane declares `pressure_relation: "same_observation"`,
  path/mount/volume_id and total/used/available **exactly match** carrier
  `pressure` (byte equality).
- When `root` is on another volume, `analysis_pressure` carries that distinct
  root-filesystem observation. It is not substituted into the PR1 capacity
  target or filesystem plane.
- Full mode: handoff policy fields remain consistent with verified section
  contents (existing rules).

### 3.6 No top-level `held_open_requested`

Plane collection state is canonical. Whether held-open was requested is
expressed only as `planes.held_open.collection_status: "not_requested"` (or a
collected state). Do not keep a parallel top-level boolean that can disagree.

---

## 4. Shared primitives

### 4.1 Collection vs measurement status

**Split** plane **collection state** from numeric **measurement status**.

#### Plane `collection_status` (always present on each plane)

| Value           | Meaning                                                                                   |
| --------------- | ----------------------------------------------------------------------------------------- |
| `measured`      | Collection succeeded with usable plane data (may still be partial at claim level)         |
| `partial`       | Collection ran; material coverage limits apply                                            |
| `unsupported`   | Platform / public interface **cannot** provide this plane by design                       |
| `unavailable`   | Supported in principle; this run could not collect (permission, timeout, missing tool, …) |
| `not_requested` | Opt-in plane was not requested this run (**only** for opt-in planes such as held-open)    |

`not_requested` is **not** `unsupported`. “Cannot provide by design” ≠ “not asked.”

#### Claim `measurement_status` (on `byte_claim` and similar)

| Value         | Meaning                                        |
| ------------- | ---------------------------------------------- |
| `measured`    | Value obtained from a supported public source  |
| `partial`     | Subset measured; totals are bounds as declared |
| `unsupported` | Metric not available from public API by design |
| `unavailable` | Supported in principle; this run failed        |

**Rule:** a missing number is **never** serialized as `0`. Numeric/display fields
appear only for `measured` | `partial`.

### 4.2 `byte_claim`

```json
{
  "status": "measured",
  "bytes": 123,
  "human": "123 B",
  "basis": "statfs_bavail",
  "bound": "exact"
}
```

| Field    | Rule                                                                               |
| -------- | ---------------------------------------------------------------------------------- |
| `status` | required; claim measurement_status                                                 |
| `bytes`  | required for `measured`\|`partial`; **forbidden** for `unsupported`\|`unavailable` |
| `basis`  | required when `bytes` present; **stable token**, not a command string              |
| `bound`  | required when `bytes` present: `exact` \| `lower` \| `upper` \| `unknown`          |
| `human`  | optional presentation; **derived from `bytes`**, not authoritative                 |
| `detail` | optional operator-facing reason (curated text; never raw tool stderr)              |
| `source` | optional override; otherwise inherit enclosing plane `source`                      |

Schema conditionals must enforce the measured/partial vs unsupported/unavailable
field sets.

### 4.3 Plane coverage (canonical location)

**One** coverage location per plane — not a top-level rollup plus plane-local
copies that can disagree.

Each plane requires:

- `collection_status`
- `source` when collection was attempted (`measured` \| `partial` \| `unavailable`);
  may be omitted only for `unsupported` \| `not_requested` with documented default
- `coverage.gaps`: **always-present array** (possibly empty)

Numeric claims inherit the enclosing plane source/coverage unless they override.

#### Coverage gap object

| Field                                 | Rule                                                                            |
| ------------------------------------- | ------------------------------------------------------------------------------- |
| `code`                                | closed set (below)                                                              |
| `scope`                               | plane or subsystem id                                                           |
| `detail`                              | **curated** diagnostic text only — **never** raw `lsof`/tool stderr passthrough |
| `paths`                               | default **empty**; must not smuggle path disclosure past held-open policy       |
| `omitted_objects` / `omitted_holders` | integers when truncation applies                                                |

#### Gap `code` set (v2)

- `permission_denied`
- `command_missing`
- `command_failed`
- `parse_failed`
- `timeout`
- `truncated`

**Not** a gap code: `not_applicable` — use plane `collection_status: unsupported`
for platform boundaries.

**Semantics:** `command_missing` / permission / timeout → `unavailable` or
`partial` **with a gap**, never `unsupported`.

### 4.4 Capture identity (`capture`)

Required on `capacity_accounting`.

| Field              | Required | Notes                                                                               |
| ------------------ | -------- | ----------------------------------------------------------------------------------- |
| `captured_at`      | yes      | ISO-8601                                                                            |
| `tool`             | yes      | `{ "name": "spanwit", "version": "…", "commit"? }`                                  |
| `os`               | yes      | `{ "goos", "goarch", "platform_version"? }`                                         |
| `boot_id`          | no       | Darwin production captures: stable per-boot, non-host-identifying utmpx observation |
| `boot_time`        | no       | Darwin production captures: UTC birth time of the per-boot utmpx observation        |
| `session_boundary` | no       | dogfood labels: `before-running`, `before-quiesced`, `after-reboot`, `after-update` |

**Hostname / hostname_hash:** **omit entirely**. Unsalted hashes are
dictionary-reversible on a known fleet; salted hashes need secret state that
contradicts no-implicit-state. Correlation uses `container_id` / `volume_id` +
optional `session_boundary`.

### 4.5 Target identity (`target`)

| Field           | Required | Notes                                                                     |
| --------------- | -------- | ------------------------------------------------------------------------- |
| `path`          | yes      | sampled capacity / primary-pressure path; not necessarily carrier `root`  |
| `resolved_path` | no       | if symlink resolution performed                                           |
| `mount`         | no       |                                                                           |
| `fs_type`       | no       | e.g. `apfs`                                                               |
| `volume_id`     | no       | opaque same-volume key; exactly matches `pressure.volume_id` when present |
| `container_id`  | no       | APFS container / pool when known                                          |
| `platform`      | yes      | e.g. `darwin`, `linux`                                                    |

`target.volume_id` is the portable filesystem identity obtained with the
carrier pressure observation. A Darwin `planes.volumes.entries[].id` is an
APFS device identifier from diskutil. These are intentionally separate
identity namespaces; `is_target` relates the APFS entry to the target without
equating its ID to the statfs FSID.

---

## 5. `capacity_accounting` section

```text
capacity_accounting:
  capture: <capture>
  target: <target>
  planes:
    filesystem: <plane>
    container: <plane>
    volumes: <plane>
    snapshots: <plane>
    system_managed: <plane>
    held_open: <plane>
```

All six planes are **always present**. Shape is stable; status tells the story.

### 5.1 `planes.filesystem`

| Field                          | Notes                                                                                                                                                         |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `collection_status`            |                                                                                                                                                               |
| `source`                       | e.g. stable token `statfs`                                                                                                                                    |
| `pressure_relation`            | `"same_observation"` when this plane is the dual of carrier `pressure` (Go-enforced equality); omit or other documented values only if deliberately different |
| `total` / `used` / `available` | `byte_claim`                                                                                                                                                  |
| `coverage.gaps`                | always present                                                                                                                                                |

Duplicate the normalized `byte_claim` copy for capacity consumers (self-contained
capacity-only reports). Do **not** use a loose `aligns_with_pressure` boolean.

### 5.2 `planes.container`

Pool / APFS container ceiling when exposed.

| Field               | Notes                                                 |
| ------------------- | ----------------------------------------------------- |
| `collection_status` | Linux without pool → `unsupported`                    |
| `id`                | when measured                                         |
| `capacity` / `free` | `byte_claim`                                          |
| `source`            | e.g. `diskutil.apfs.list.plist` (stable token family) |
| `coverage.gaps`     |                                                       |

### 5.3 `planes.volumes`

Sibling / role volumes.

```text
entries: [
  {
    id, name?, role, roles[], mount?,
    capacity_in_use: <byte_claim>,
    is_target: boolean
  }
]
```

**Do not** sum all `capacity_in_use` values as “disk used.”

### 5.4 `planes.snapshots`

| Field     | Notes                                                                    |
| --------- | ------------------------------------------------------------------------ |
| `count`   | when measured                                                            |
| `entries` | optional capped list (id, name?, purgeable?, limits_container_shrink?)   |
| `bytes`   | claim with `unsupported` on Darwin public APIs today — **do not invent** |

### 5.5 `planes.system_managed`

```text
policy: "diagnostic_only_never_prune_handoff"   # const
entries: [
  {
    id: "apple_assets_v2",
    path: "/System/Library/AssetsV2",   # catalog-constrained known roots only
    trust_state: "diagnostic-only",
    allocated: <byte_claim>,
    remediation: "os_or_update_mechanism" | "unknown" | "none_supported"
  }
]
```

Absolute paths for **catalog-constrained known OS roots** are allowed. Any future
open-ended discovery of managed paths falls under held-open path policy (no path
by default).

### 5.6 `planes.held_open` (security-critical)

Always present. When the flag was not set:

```text
collection_status: "not_requested"
coverage: { "gaps": [] }
# no entries, holders, or path fields
```

When requested and collected:

| Field                | Notes                                                                              |
| -------------------- | ---------------------------------------------------------------------------------- |
| `collection_status`  | `measured` \| `partial` \| `unavailable`                                           |
| `source`             | stable token, e.g. `lsof_plus_L1`                                                  |
| `disclosure`         | `"none"` \| `"full"` — self-describing for shared files                            |
| `privilege`          | `"unprivileged"` \| `"elevated"` — spanwit **never self-escalates** to close a gap |
| `processes_examined` | integer when known                                                                 |
| `processes_denied`   | integer when known                                                                 |
| `unique_objects`     | after `(device,inode)` dedupe                                                      |
| `holder_processes`   | distinct pids                                                                      |
| `relationships`      | optional raw handle rows before dedupe                                             |
| `logical_bytes`      | `byte_claim` with `bound: lower` — **not** reclaimable / not APFS allocated        |
| `entries`            | capped; see disclosure                                                             |
| `groups`             | optional aggregation by executable basename / interface_guess                      |
| `coverage.gaps`      | always present                                                                     |

#### Disclosure policy (locked)

| Topic                  | Rule                                                                                                                                     |
| ---------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| Paths (default)        | **No path component at all** — not even basename (basenames leak project/client names; reports are shared via compare)                   |
| Default identity       | `(device, inode)` + `logical_bytes` + `path_present` + closed `root_class`                                                               |
| `root_class`           | `user_home` \| `system_temp` \| `target_tree` \| `system` \| `other`                                                                     |
| Full path              | Only with explicit `--disclose paths`; path may be `~`-substituted; plane sets `disclosure: "full"`                                      |
| Deleted paths          | Same policy as live paths                                                                                                                |
| argv / command_summary | **Not in v2 at any disclosure level.** Executable basename + optional `interface_guess` only. Future need = schema revision + new review |
| Entry cap              | ≤ **200** objects (default)                                                                                                              |
| Holders per object     | ≤ **16** (default)                                                                                                                       |
| Truncation             | gap `truncated` with `omitted_objects` / `omitted_holders` counts                                                                        |
| stderr / logs          | **Counts and statuses only** — never pids or paths (logs are pasted more casually than report files)                                     |
| Sandbox                | `partial` + quantified gaps; never silent empty “no holds”                                                                               |

#### Held-open object (default disclosure)

| Field                       | Present by default               |
| --------------------------- | -------------------------------- |
| `device`, `inode`           | yes                              |
| `path_present`              | yes                              |
| `root_class`                | yes                              |
| `logical_bytes`             | yes                              |
| `path`                      | **no** unless `disclosure: full` |
| `holders[].pid`             | yes (JSON report only)           |
| `holders[].executable`      | basename only                    |
| `holders[].interface_guess` | optional best-effort             |
| `holders[].command_summary` | **forbidden in v2**              |

**Product rules:**

1. Dedupe by `(device, inode)` before any total.
2. Logical bytes are not reclaimable and not APFS allocated.
3. Spanwit does not kill processes or close sessions.
4. Do not double-count objects when grouping.

---

## 6. Narrative (optional, non-authoritative)

```text
narrative:
  summary: string
  known: [string, ...]
  unavailable: [string, ...]
  unexplained_hints: [string, ...]
```

**Forbidden:** `explained_bytes`, reconciled total, or numeric “unexplained”
residual that subtracts planes.

When rendering narrative/detail to a terminal, sanitize control characters
(ANSI-injection prevention).

Capacity-only collection uses one shared five-second context budget for the
mandatory primary filesystem observation, an optional distinct analysis-root
observation, public platform queries, and managed-root measurement. Injected
samplers and command runners must honor cancellation. Native statfs-style
syscalls are checked before and after the call, but their wall-clock boundary is
best-effort because supported kernels do not universally expose an interruptible
filesystem-capacity syscall. An analysis-root sampling failure is retained as a
curated unavailable narrative item rather than silently discarded.

---

## 7. Compare result (separate product schema)

`$id`: `https://schemas.3leaps.dev/spanwit/space-compare/v1.json`

- Strict schema; product/API contract (not undocumented CLI JSON).
- **Inputs for initial compare: SpaceReport v2 only.** v1 remains for prune
  replay, not silent up-conversion into missing capacity planes.
- Treat saved reports as **untrusted input**: schema validation first; array
  caps; input size ceiling; control-char sanitization when printing.

### Compatibility (locked)

| Check                                                       | Effect                                                                |
| ----------------------------------------------------------- | --------------------------------------------------------------------- |
| `capture_mode` mismatch (full vs capacity_only)             | **Informational** — capacity planes may still compare                 |
| Reboot / boot-id change                                     | **Metadata**, not incompatibility                                     |
| Platform + target `volume_id`                               | Gate **filesystem** claim compares                                    |
| `container_id`                                              | Gate **container / volumes / snapshots** claim compares               |
| Missing identity                                            | Partial / indeterminate — **not** a guessed match                     |
| Held-open on one side only, truncation, privilege asymmetry | That **plane** partial/indeterminate — not whole-compare incompatible |

Compatibility is **per plane/claim**.

### Deltas

- Emit `delta_bytes` only when **both** claims are present, `bound: exact`, and
  share **compatible source + basis** semantics.
- Subtracting two **lower** bounds does **not** yield a valid lower-bound delta:
  retain observed endpoints; mark delta **indeterminate**.
- No `explained_bytes` / reconciled total / numeric unexplained residual.

Illustrative shape:

```text
{
  "$schema": "https://schemas.3leaps.dev/spanwit/space-compare/v1.json",
  "version": 1,
  "generated_at": "...",
  "left": { "path", "captured_at", "capture_mode" },
  "right": { "path", "captured_at", "capture_mode" },
  "compatibility": {
    "status": "compatible" | "partial" | "incompatible",
    "checks": [ { "name", "ok", "detail?" } ]
  },
  "plane_deltas": [
    {
      "plane": "filesystem",
      "field": "available",
      "left_bytes", "right_bytes",
      "delta_bytes": null | integer,
      "delta_status": "exact" | "indeterminate" | "incomparable",
      "detail": null
    }
  ],
  "coverage": {
    "left_plane_notes": [...],
    "right_plane_notes": [...],
    "asymmetric_planes": ["held_open"]
  },
  "narrative": { "known", "unavailable", "unexplained" }
}
```

---

## 8. Implementation placement

| Component                  | Role                                                                                                                                                 |
| -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| `internal/capacity`        | Surface-neutral options/results; portable + platform collectors; injectable Darwin runner/parser; **no** Cobra; **does not** import `internal/space` |
| `internal/space`           | Composes SpaceReport carrier (v1/v2)                                                                                                                 |
| CLI / future API / observe | Adapters only; same core                                                                                                                             |
| `scripts/…`                | Not the product contract                                                                                                                             |

Platform id: `darwin` (canonical).

---

## 9. Contract decisions

### Carrier and placement

| Topic                  | Decision                                                                                  |
| ---------------------- | ----------------------------------------------------------------------------------------- |
| Capacity-only carrier  | Omit deep inventory; schema forbids those keys; `capture_mode` is required                |
| Schema shape           | One SpaceReport v2 schema with mode conditionals                                          |
| Plane presence         | Always present; `not_requested` is a collection status; no top-level held-open boolean    |
| Filesystem observation | Duplicate normalized claims with `pressure_relation: "same_observation"` plus Go equality |
| Compare output         | Separate strict compare v1 schema; initial inputs are SpaceReport v2 only                 |
| Numeric claims         | Nested `byte_claim`; conditional required/forbidden fields; `bytes` is canonical          |
| Core placement         | Surface-neutral `internal/capacity` seam as defined above                                 |

Per-plane coverage, compare compatibility, delta rules, and the capacity-only
prune-loader hard failure are also normative.

### Disclosure and privilege

| Topic                | Decision                                                                                     |
| -------------------- | -------------------------------------------------------------------------------------------- |
| Default path posture | No path component; use `root_class` plus device/inode; full path requires `--disclose paths` |
| Process arguments    | `command_summary` and argv are absent from v2                                                |
| Host identity        | Hostname and hostname hashes are absent                                                      |
| Managed roots        | Absolute paths are allowed only for catalog-constrained system roots                         |
| Output bounds        | Caps 200 / 16; truncation counts; curated gap detail; gap paths default empty                |
| Privilege gaps       | Partial with typed gaps; only measured omission quantities are emitted; never self-escalates |
| Diagnostic streams   | Pids/paths remain in the JSON report only; stderr carries counts/status                      |

### Implementation acceptance

- Darwin plist fixtures exercise supported, partial, unavailable, and parse-failure cases.
- Capacity-only collection meets its documented wall-clock budget.
- Controlled captures exercise comparison across quiescence and reboot boundaries.
- Schema conditionals and application-only invariants have positive and negative tests.

---

## 10. Minimal illustrative capture (capacity-only, held-open not requested)

Illustrative only — not a golden fixture.

```json
{
  "$schema": "https://schemas.3leaps.dev/spanwit/space-report/v2.json",
  "version": 2,
  "generated_at": "2026-07-30T15:00:00Z",
  "capture_mode": "capacity_only",
  "root": "/Users/example",
  "pressure": {
    "path": "/Users/example",
    "mount": "/System/Volumes/Data",
    "volume_id": "disk3s5",
    "role": "primary_write",
    "total_bytes": 1000000000000,
    "total_human": "931.3 GiB",
    "used_bytes": 500000000000,
    "used_human": "465.7 GiB",
    "avail_bytes": 450000000000,
    "avail_human": "419.1 GiB",
    "used_percent": 50.0,
    "level": "ok"
  },
  "capacity_accounting": {
    "capture": {
      "captured_at": "2026-07-30T15:00:00Z",
      "tool": { "name": "spanwit", "version": "0.1.0" },
      "os": { "goos": "darwin", "goarch": "arm64", "platform_version": "26.6" },
      "session_boundary": "before-running"
    },
    "target": {
      "path": "/Users/example",
      "mount": "/System/Volumes/Data",
      "fs_type": "apfs",
      "volume_id": "disk3s5",
      "container_id": "disk3",
      "platform": "darwin"
    },
    "planes": {
      "filesystem": {
        "collection_status": "measured",
        "source": "statfs",
        "pressure_relation": "same_observation",
        "coverage": { "gaps": [] },
        "total": {
          "status": "measured",
          "bytes": 1000000000000,
          "human": "931.3 GiB",
          "basis": "statfs_blocks",
          "bound": "exact"
        },
        "used": {
          "status": "measured",
          "bytes": 500000000000,
          "human": "465.7 GiB",
          "basis": "statfs_used",
          "bound": "exact"
        },
        "available": {
          "status": "measured",
          "bytes": 450000000000,
          "human": "419.1 GiB",
          "basis": "statfs_bavail",
          "bound": "exact"
        }
      },
      "container": {
        "collection_status": "measured",
        "id": "disk3",
        "source": "diskutil.apfs.list.plist",
        "coverage": { "gaps": [] },
        "capacity": {
          "status": "measured",
          "bytes": 1000000000000,
          "human": "931.3 GiB",
          "basis": "apfs_container_size",
          "bound": "exact"
        },
        "free": {
          "status": "measured",
          "bytes": 480000000000,
          "human": "447.0 GiB",
          "basis": "apfs_container_free",
          "bound": "exact"
        }
      },
      "volumes": {
        "collection_status": "measured",
        "source": "diskutil.apfs.list.plist",
        "coverage": { "gaps": [] },
        "entries": [
          {
            "id": "disk3s5",
            "role": "Data",
            "roles": ["Data"],
            "is_target": true,
            "capacity_in_use": {
              "status": "measured",
              "bytes": 487000000000,
              "human": "453.5 GiB",
              "basis": "CapacityInUse",
              "bound": "exact"
            }
          }
        ]
      },
      "snapshots": {
        "collection_status": "partial",
        "source": "diskutil.apfs.listSnapshots.plist",
        "coverage": { "gaps": [] },
        "count": 1,
        "bytes": {
          "status": "unsupported",
          "detail": "darwin public APIs do not expose snapshot bytes"
        },
        "entries": [
          {
            "id": "example-uuid",
            "purgeable": false,
            "limits_container_shrink": true
          }
        ]
      },
      "system_managed": {
        "collection_status": "partial",
        "source": "filesystem.allocated_blocks",
        "policy": "diagnostic_only_never_prune_handoff",
        "coverage": { "gaps": [] },
        "entries": [
          {
            "id": "apple_assets_v2",
            "path": "/System/Library/AssetsV2",
            "trust_state": "diagnostic-only",
            "allocated": {
              "status": "measured",
              "bytes": 3300000000,
              "human": "3.1 GiB",
              "basis": "allocated_blocks",
              "bound": "lower"
            },
            "remediation": "os_or_update_mechanism"
          }
        ]
      },
      "held_open": {
        "collection_status": "not_requested",
        "coverage": { "gaps": [] }
      }
    },
    "narrative": {
      "summary": "Filesystem and APFS container measured; snapshot bytes unsupported; held-open not requested.",
      "known": ["statfs available 419.1 GiB", "APFS container free 447.0 GiB"],
      "unavailable": ["snapshot bytes unsupported on darwin public APIs"],
      "unexplained_hints": []
    }
  }
}
```

Deep inventory keys are intentionally **absent** under `capture_mode: capacity_only`.

---

## 11. Implementation requirements

- Embed both product schemas and validate them against positive and negative fixtures.
- Dispatch loaders by an agreeing `$schema` / `version` envelope.
- Reject capacity-only reports as prune carriers.
- Enforce same-observation pressure equality in Go.
- Validate held-open default disclosure with no path keys.
- Validate untrusted compare inputs before use and apply runtime size caps.

## 12. References

- ADR-0003 core/surface separation (report schema = CLI + future API body)
- ADR-0004 diagnostic-only trust state
- `config/schema/spanwit-space-report.v1.schema.json`

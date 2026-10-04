---
title: "Filesystem Inventory"
description: "Read-only neutral filesystem enumeration and coverage semantics"
author: "devlead"
date: "2026-07-29"
last_updated: "2026-10-02"
status: "active"
tags: ["inventory", "filesystem", "read-only", "coverage"]
---

# Filesystem Inventory

`spanwit inventory` enumerates regular files and their observed metadata. It
does not classify a file as reclaimable, create a prune plan, or expose an
execute path. Size and age are selection facts, never deletion authority.

## Examples

```bash
# Which folders consume at least 5 GiB? (readable table, allocated size)
spanwit --read-only inventory ~/dev --directories --directory-min-size 5GiB

# The same audit as a versioned, schema-validated stream.
spanwit --read-only inventory ~/dev --directories --directory-min-size 5GiB \
  --format jsonl

# Stream every matching file as it is observed.
spanwit inventory ~/dev --min-size 1GiB --older-than 30d

# Scan the entire declared scope, then emit the largest 100 matches.
spanwit inventory /System/Volumes/Data --min-size 5GiB \
  --one-filesystem --top 100

# Typed records for a streaming consumer.
spanwit inventory ~/dev --min-size 1GiB --format jsonl

# One bounded walk, ranked full-subject directory aggregates. File filters do
# not narrow these totals.
spanwit inventory ~/dev --directory-summary --format jsonl \
  --directory-depth 3 --directory-top 100

# Walk and reconcile without emitting per-file records. Exclusions are part of
# the declared subject; ./build is root-relative, while node_modules is a
# basename rule at every depth.
spanwit inventory ~/dev --summary-only \
  --exclude ./build --exclude node_modules --format jsonl

# Write a separate producer coverage claim. PATH must not already exist and
# must be outside every enumerated root.
spanwit inventory ~/dev --summary-only \
  --coverage-attest /Volumes/diagnostics/dev-inventory-coverage.json

# Local shell path projection. This deliberately gives up identity metadata.
spanwit inventory ~/dev --min-size 1GiB --print0 |
  xargs -0 some-read-only-command
```

At least one root is required. Roots are resolved before traversal. Exact
duplicates are deduplicated; overlapping roots are refused so the same entries
cannot be silently counted twice.

## Selection

- `--type file` is the only v1 entry type.
- `--min-size` is inclusive and has no hidden default.
- `--older-than` and `--newer-than` use one captured clock for the whole run.
  Together they form an inclusive age window.
- `--size-basis apparent|allocated` controls filtering and top-K ranking.
  Both sizes are emitted when the platform exposes allocated blocks cheaply.
- `--top N` is a bounded final ranking. It does not stop traversal early.
- `--summary-only` suppresses entry emission after applying the declared
  predicate; it does not narrow traversal or make coverage partial.
- Repeatable `--exclude` rules define content outside the subject before
  directory descent. A bare value matches a basename at any depth; `./name`
  or a value containing `/` is root-relative. Absolute paths, Windows volume
  paths, and parent traversal are refused. There are no default exclusions.
- `--directory-summary` selects a separate JSONL-only directory projection.
  It accounts for every traversed regular file in the declared subject,
  independent of `--min-size` and age filters, and is incompatible with
  `--summary-only`, `--print0`, file `--top`, and `--coverage-attest` in the
  first slice.
- `--directory-depth N` bounds emitted aggregate depth without narrowing the
  walk. `--directory-top N` bounds final aggregate output.

Apparent and allocated entry sums are not `bytes_freed`. Hard-linked paths may
name the same object more than once, sparse files can have far fewer allocated
than apparent bytes, and clones may share physical blocks.

## Traversal and coverage

`--backend auto` selects the bounded parallel backend with four workers.
`--workers 1` selects the serial backend. The worker count and
`--max-open-dirs` are separate bounds; neither grows with the number of files.
`--max-pending-dirs` caps the directory frontier (default 65,536). If that
frontier is exhausted, Spanwit records each skipped subtree as a
`directory-queue-limit` gap and returns partial coverage rather than growing
without limit or silently omitting work.

Both product backends use the same unsorted 256-entry directory batches and
bounded coordinator; serial fixes the worker count at one rather than using a
sorting whole-directory walker. These are the same implementations exercised
by the repository's benchmark harness. External `find` and sharded-find remain
benchmark witnesses: BSD `find` cannot supply the structural gap
fidelity and same-pass metadata required by the product contract. They may
graduate only
through the declared backend-capability interface without weakening coverage.

Directory symlinks are never followed. `--one-filesystem` records and skips a
different-device boundary; that is a declared scope exclusion rather than an
unexpected incomplete traversal. Permission denials, vanished entries,
unavailable required metadata, cancellation, and I/O failures are explicit
gaps and make lifecycle `partial` or `failed`.

### Remote and cloud-placeholder directories

A read-only walk is not side-effect-free everywhere. Opening a directory on a
network or object-store filesystem (`nfs`, `smbfs`, `afpfs`, `webdav`, FUSE)
can block on the network, and opening a cloud-placeholder (File Provider)
directory asks the provider to download its contents. By default such
directories found **below** a root are skipped before they are opened: the
decision uses `lstat` and, on a device change, one cached `statfs`. Each skip is
a `remote` gap that does not affect completeness (a policy exclusion, like a
mount boundary) and is counted in `remote_skip_count`. `--include-remote` walks
them. A root you name explicitly is always walked, with a one-line notice when
it is itself remote.

### Stalled directory opens

A directory open can block in the kernel indefinitely, and cancellation cannot
interrupt it. Each directory open is therefore bounded by `--stall-timeout`
(default `60s`). An open that has not responded after 10 seconds raises one
coalesced stderr alert, which is shown even with `--quiet` and carries counts
only, never a path. At the timeout the walker skips that subtree as a
`stalled` gap and continues. The gap affects completeness and names no path,
because the blocked directory may itself be sensitive. The run is then
`partial` (exit `1`) and `stalled_count` reports how many subtrees were
skipped. `--stall-timeout 0` waits indefinitely (alerts are still shown); a
negative value is a usage error.
Abandoning an open is confined to this read-only walk; the helper that
performed it closes any descriptor that arrives late.

Exit status is:

- `0` — complete for the declared scope and filters;
- `1` — usable results with partial coverage;
- `2` — runtime or output failure;
- `3` — invalid command usage.

## Output

Text is the default. Matches go to stdout; gap warnings go to stderr; a
terminal summary states visited, matched, emitted, gap, and boundary counts.
Traversal order is not promised.

Path-free progress snapshots go only to stderr and are emitted at most once per
second. They include elapsed time, entry/directory counts, matches, affecting
gaps, opaque root ids, completed-root count, and entry rate. `--quiet`
suppresses progress without changing stdout or a requested attestation.

`--print0` writes NUL-delimited absolute local paths to stdout and its summary
to stderr. Feeding this projection into a destructive command forfeits
Spanwit's object-identity revalidation and prune authorization.

`--format jsonl` emits:

- `spanwit.inventory.header.v1`;
- `spanwit.inventory.entry.v1`;
- `spanwit.inventory.gap.v1`;
- `spanwit.inventory.summary.v1`.

Every record carries one `run_id` and monotonic `seq`. The terminal summary is
emitted exactly once on normal writable-stream termination. A broken output
stream fails the run and cannot be reported as complete.

The stream is the producer-owned `spanwit.filesystem-inventory/v0` profile.
Its strict record catalog is
`config/schema/spanwit-filesystem-inventory.v0.schema.json`; record types remain
`spanwit.inventory.*.v1`, so record and profile versions are independent axes.
Header exclusions and emission mode, gap codes, and global/per-root terminal
accounting are golden-tested. Paths are `source_structure` and blocked from
export across a trust boundary by default.

Directory-summary mode instead emits profile
`spanwit.filesystem-inventory-aggregation/v0`, replacing file entry records
with `spanwit.inventory.directory.v1`. Its strict schema is
`config/schema/spanwit-filesystem-inventory-aggregation.v0.schema.json`.
The header records `accounting_scope: full_subject` separately from file
filters. Directory byte fields are per-path-entry descendant sums, not unique
physical usage, expected reclaim, or deletion authority. Affecting gaps mark
the containing aggregates and ancestors partial. Allocated-basis top-K fails
before directory emission if any eligible aggregate has unmeasured allocation;
unknown values are never ranked as zero.

Adding `--directory-accounting` (requires `--directory-summary --format jsonl`)
selects profile `spanwit.filesystem-inventory-aggregation/v1` instead. Its
strict schema is
`config/schema/spanwit-filesystem-inventory-aggregation.v1.schema.json`. Every
directory record then carries typed byte-plane claims:

- `apparent` and `allocated` are observed per-path-entry sums with explicit
  `status` (`measured` | `partial`), `basis`
  (`path_entry_logical_size_sum` | `path_entry_allocated_blocks_sum`), `bound`
  (`exact` | `lower`), and `bytes`. `exact` means exact for the named sum over
  the declared subject only. Affecting coverage gaps downgrade observed sums to
  `partial` with `bound: lower`. An allocated sum with unmeasured entries is
  `partial/lower` with `unmeasured_count`; if allocation is unavailable for
  every covered file the allocated claim is `unsupported` with no number and is
  never substituted from apparent size.
- `unique_physical`, `shared_cloned`, and `expected_reclaim` are always
  `unsupported` with a detail and no number: portable public file metadata
  cannot observe deduplicated physical consumption, extent sharing, or
  post-deletion recovery. Unknown values are omitted, never serialized as zero.

These claims are observational evidence only. No byte plane, bound, or fixture
result creates a prune handoff, deletion boundary, exact plan, or execution
authority. Existing v0 streams — including default `--directory-summary`
output — remain byte-for-byte unchanged.

Global and per-root summaries separately reconcile matched and emitted
apparent bytes, allocated bytes, and allocated-size-unmeasured counts.
`terminal_observed_at` is also the timestamp on the terminal JSONL envelope.
These byte and timing fields are run-local selection/output evidence: the
canonical coverage attestation projects only its four specified per-root count
claims, so it does not invent byte-volume or performance claim units.

`--top`, `--summary-only`, `--directory-summary`, and `--exclude` are deliberately distinct:

- top-K changes final entry selection after the complete walk;
- summary-only changes entry emission (`matched_count` remains, `emitted_count`
  is zero);
- exclusions change the subject before traversal and count as exclusions, not
  coverage gaps.
- directory-summary changes the projection while retaining full-subject
  accounting and uses its own depth/top controls.
- directory-accounting upgrades the projection to the v1 profile with typed
  byte-plane claims; it does not change what is walked or what is observed.

## Folder audit (`--directories`)

`--directories` walks the full declared subject once, retains one aggregate per
admitted directory, and only then applies output selection: `--directory-depth`,
the inclusive `--directory-min-size` floor, and `--directory-top`. It reports
consumption and coverage, never reclaimability. Parent and child totals overlap;
do not add them together.

- **Size basis.** Defaults to `allocated` (disk blocks actually used). Pass
  `--size-basis apparent` for logical sizes. The resolved basis is shown in the
  text header and in the stream's `directory_selection.size_basis`. On a
  platform that cannot report allocated sizes (non-unix builds), the default is
  refused with guidance rather than silently switched.
- **Floor.** `--directory-min-size` uses the shared size parser, so `5G`, `5GB`
  and `5GiB` all mean 5,368,709,120 bytes; write `5000000000B` for a decimal
  floor. The header echoes it as `5 GiB (5368709120 bytes)`. Omitting it applies
  no floor; `0` is an applied floor.
- **Floor decisions.** An exact total at or above the floor `meets` it; below,
  the row is excluded. A partial lower bound at or above the floor meets it;
  below, it is `indeterminate` (the unobserved part could push it over). A
  folder with no size on the selected basis is `indeterminate`, never zero.
  With no floor every row is `not_applied`.
- **Order.** `directory_audit_rank_v1`: exact values, then definite lower
  bounds, then indeterminate lower bounds, then unavailable sizes; larger first
  within a group; then root id and relative path.
- **Text bounds.** Text output needs a floor or a positive `--directory-top`.
  Because unavailable sizes cannot be excluded by a floor, an explicit
  `--size-basis allocated` on a platform without allocated sizes also needs
  `--directory-top`. When a particular filesystem reports no allocation, the
  footer counts those folders and suggests `--directory-top` or
  `--size-basis apparent`. JSONL is not restricted.
- **Refused flags.** File predicates (`--min-size`, `--older-than`,
  `--newer-than`, `--top`, `--type`), `--print0`, `--summary-only`,
  `--directory-summary` and `--directory-accounting`. `--coverage-attest`
  is accepted; see [Directory-audit attestation](#directory-audit-attestation).

Tuning, when needed: `--directory-depth N` limits emitted rows (every descendant
is still walked); `--workers` defaults to auto; see
[Directory bounds](#directory-bounds-are-independent) for
`--max-aggregate-directories`.

### Structured stream (profile v2)

`--directories --format jsonl` emits profile
`spanwit.filesystem-inventory-aggregation/v2`, validated by
[`config/schema/spanwit-filesystem-inventory-aggregation.v2.schema.json`](../config/schema/spanwit-filesystem-inventory-aggregation.v2.schema.json):

- `spanwit.inventory.header.v2` — roots, traversal policy, budget and
  `directory_selection` (`floor_applied`, `floor_bytes` iff applied,
  `size_basis`, `depth`, `top`);
- `spanwit.inventory.directory.v2` — identity, counts, typed accounting claims
  and `floor_decision` (`meets` | `indeterminate` | `not_applied`);
- `spanwit.inventory.gap.v1` — unchanged;
- `spanwit.inventory.summary.v2` — traversal evidence per root plus the audit
  result.

The summary distinguishes traversal from audit output:

| Field                                                                                       | Meaning                                                                                  |
| ------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------- |
| `selection_reconciled`                                                                      | Depth/floor/top selection was computed over the observed subject                         |
| `emission_completed`                                                                        | Every planned row was written                                                            |
| `directory_emitted_count`                                                                   | Rows actually written (D)                                                                |
| `directory_depth_eligible_count` (E)                                                        | Aggregates within the output depth                                                       |
| `directory_meets_count` / `_indeterminate_count` / `_excluded_count` / `_not_applied_count` | M / I / X / U, with E = M + I + X + U                                                    |
| `directory_selected_before_top_count` (S)                                                   | S = E − X                                                                                |
| `directory_planned_emission_count` (K)                                                      | S, or min(top, S)                                                                        |
| `directory_selection_truncated`                                                             | K < S, due to `--directory-top` only                                                     |
| `failure`                                                                                   | Typed and path-free; budget exhaustion carries `limit`, `retained`, `attempted_retained` |

Selection counts are present only when `selection_reconciled` is true; they are
omitted, never zeroed, otherwise. A failed walk, or a cancellation before
selection, is `selection_reconciled: false` — zero rows there is not zero
matches. A cancellation while rows are being written keeps the selection counts,
marks `canceled: true` and `emission_completed: false`; once every row is
written, a later cancellation does not undo the result. Per-root entries keep
their real traversal lifecycle throughout. Integers are int64; decode them
losslessly.

The schema checks each record. Stream-level rules — one header first, one
summary last, contiguous `seq`, one `run_id`, known root ids, unique directory
identities, the count equations, emitted counts matching records, per-root
counters summing to the summary totals, and each row's lifecycle, gap count and
claims agreeing — are checked by the in-repo stream validator
(`inventory.ValidateAuditStream`). A stream with no terminal summary is
incomplete.

`relative_path` is root-relative and slash-delimited: `.` for the root,
otherwise segments joined by `/`, with no leading `/` and no empty, `.` or `..`
segment. Any other byte, including a literal backslash, is part of a segment
name. Consumers must not treat a backslash as a separator or join the path with
another platform's path rules. Synthetic example streams (complete,
canceled before selection, budget failure) live in
[`internal/inventory/testdata/audit-v2/`](../internal/inventory/testdata/audit-v2/).

## Coverage attestation

`--coverage-attest PATH` opts into a separate JSON
`contract: coverage-attestation/v0` producer claim. The inventory stream
remains run-local truth; the attestation is projected from the same header,
affecting gaps, and reconciled per-root terminal state without a second walk or
entry replay.

The implementation resolves the vendored canonical `contract.json`, requires
its exact capability, loads the relative entry schema, and validates the full
document before creating output. The vendored Crucible files record their
canonical source commit and checksums. A requested destination:

- must be a real file path outside all enumerated roots;
- cannot be `-`, an existing path, or an implicit sidecar;
- is published mode `0600` from a same-directory, file-fsynced stage through a
  no-replace link; containing-directory sync is also used where the platform
  supports it;
- returns nonzero on resolution, validation, bounded-gap, or write failure.

Definitive publication pins the approved parent directory and performs stage
creation, linking, cleanup, and supported directory sync through that pinned
handle. Parent, admitted-root, stage, and final identities are checked around
publication and again after cleanup. A replacement observed before linking
produces no final publication. If an identity or durability failure is observed
after the no-replace link is visible, the command returns nonzero but does not
unlink that final pathname; the validated file can remain in the pinned
original directory. A private stage can likewise remain when its identity can
no longer be proven safe to remove. Operators must therefore inspect the
requested destination after any write-time failure rather than assuming that
nonzero means no artifact exists.

Each root receives confirmed/enumerated integer claims for visited directories,
visited entries, matched entries, and emitted entries. Affecting inventory gaps
retain their machine code and use per-document keyed opaque path-prefix tokens
that cannot be reproduced from the public run/root ids and a candidate-path
dictionary; raw root and file paths are never copied into the attestation.
`as_of` is exactly the inventory terminal observation time. The collector binds
publication to the canonical root identities captured at header time and fails
closed if a root disappears, is replaced, or is retargeted before publication.
A complete walk remains complete under top-K, summary-only, exclusions, and
declared one-filesystem boundary skips. A partial or failed walk emits a partial
attestation only when its terminal observations remain trustworthy. Not
requesting an attestation is normal and makes no coverage claim.

### Directory-audit attestation

With `--directories`, `--coverage-attest PATH` uses the same destination,
identity, durability and `--read-only` rules, with a projection for the v2
profile:

- Each root receives confirmed/enumerated `entries_visited` and
  `directories_visited` claims. The run receives `directories_selected_before_top`
  (confirmed/derived: a floor and depth computation over observed counts) and
  `directories_emitted` (confirmed/enumerated: rows accepted by the output).
  File match and emission units are not used.
- The subject is the full declared traversal. Floor, depth and top are output
  selection, never coverage gaps, so they do not lower `coverage_state`.
- Affecting traversal gaps make the attestation `partial`, with the same opaque
  path-prefix tokens. Unavailable allocation does not: a complete walk with
  unknown allocation attests complete traversal and makes no byte claim.
- The collector checks every record against the stream semantics as the output
  accepts it, without retaining directory rows, and refuses evidence that does
  not reconcile.
- A failed, canceled, unreconciled or incomplete audit, or one whose terminal
  summary was not written, withholds the attestation. No file is created; a
  `Warning: coverage attestation withheld: …` line on stderr says why, and the
  exit status is the audit's own.
- Request, publication, withholding and failure are reported only on stderr and
  in the exit status. The audit stream is identical with or without
  `--coverage-attest`. A publication failure after a valid audit exits nonzero
  and leaves the completed stream as written.

Coverage attestation is evidence only. It does not classify reclaimability,
create a prune plan, or authorize deletion.

## Write and memory posture

The default command creates no report, index, database, temporary file, cache,
or checkpoint. Streaming output applies backpressure. `--top N` retains only N
entries; otherwise memory is bounded by workers, the declared pending-directory
limit, per-directory read batches, and output coordination state rather than
filesystem entry count.

Directory-summary mode additionally retains one aggregate state per admitted
directory, capped by `--max-aggregate-directories` (default 250,000). Exhausting
that budget or overflowing a counter fails the run and emits no directory
records. Aggregation never spills to disk.

### Directory bounds are independent

| Control                       | What it bounds                                                                                 |
| ----------------------------- | ---------------------------------------------------------------------------------------------- |
| `--directory-depth`           | Emitted root-relative depth; every descendant is still walked and accounted                    |
| `--directory-top`             | Final directory selection, not traversal or retained state                                     |
| `--max-aggregate-directories` | Retained aggregate states; each admitted directory consumes one                                |
| `--max-pending-dirs`          | Pending traversal frontier; exhaustion is an affecting coverage gap, not the aggregate failure |
| `--max-open-dirs`             | Concurrently open directory descriptors                                                        |
| `--workers`                   | Execution workers; does not raise either directory-count budget                                |

The aggregate budget is a count of directory states, not a byte-memory cap;
raising it can increase memory use. A shallow `--directory-depth` does not help
a large tree fit, because each admitted directory is still retained.

When the budget is exhausted, the run exits 2 and stderr reports the first
rejected admission, with no path:

```text
Error: aggregate directory budget exhausted (limit=250000 retained=250000 attempted_retained=250001)
No directory totals were emitted. ...
```

`attempted_retained` is the state count that one rejected admission would have
produced. It is not the budget a rerun needs: the walk stops without counting the
remaining directories. Header, gap and failed-summary records may already be on
stdout; directory totals are not. Two ways to proceed:

```bash
# Narrow the subject.
spanwit --read-only inventory ~/dev/projects --directory-summary --format jsonl \
  --directory-depth 2

# Or raise the budget explicitly after considering memory. 600000 is an example
# chosen for one observed ~329k-directory tree, not a default or recommendation.
spanwit --read-only inventory ~/dev --directory-summary --format jsonl \
  --directory-depth 2 --max-aggregate-directories 600000
```

When attestation is requested, Spanwit additionally retains a bounded set of
coverage-affecting gaps. If that bound is exceeded, the inventory stream remains
honest but the requested attestation fails closed and no destination is
published.

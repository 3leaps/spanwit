# Spanwit

> **Smart span. Free disks.**

**Spanwit helps to keep your disks free for the important work you do.**

Smart, signature-aware disk reclamation that knows when a `target/` folder is
fair game and when it isn't. Dev-aware today, fleet-aware tomorrow. Dry-run by
default.

---

## What this is

Disks fill up. A Rust monorepo, a few JS workspaces, a Go module cache or
two, and suddenly you're scrolling `df -h` wondering what's safe to remove.
Spanwit reads your disk the way a developer would — recognizing that a
`target/` next to a `Cargo.toml` is fair game, but a `target/` somewhere else
may not be. Smart rules, dry-run by default, composable in a pipeline today
and governable across a fleet tomorrow.

**Etymology:** `span` (capacity, free space) + `wit` (Old English _witt_ —
intelligence, awareness). The benefit and the mechanism, in one word.

**Dry-run is enabled by default.** Nothing is deleted without an explicit
`--execute` flag. There is no interactive prompt; `--execute` is the explicit
opt-in, and safety and policy guardrails still apply.

## Philosophy

This tool follows the Fulmen microtool pattern: narrow scope, excellent defaults, extremely safe operation.

Primary use cases:

- Find and safely remove large Rust `target/` directories
- Clean stale `node_modules`, build artifacts, caches
- Reclaim space on dev machines without accidental data loss

## Usage

```bash
# Safe read-only analysis (default)
spanwit scan ~/dev

# Neutral file inventory (read-only; size/age never imply safe-to-delete)
spanwit --read-only inventory ~/dev --directories --directory-min-size 5GiB
spanwit inventory ~/dev --min-size 1GiB --older-than 30d --top 100
spanwit inventory ~/dev --min-size 1GiB --format jsonl
spanwit inventory ~/dev --summary-only --exclude node_modules
spanwit inventory ~/dev --summary-only \
  --coverage-attest /Volumes/diagnostics/dev-inventory-coverage.json

# File-level diagnostics (read-only; found-only classification)
spanwit diagnose ~/Downloads

# Why is the disk full? (read-only; never deletes)
spanwit space ~/dev
spanwit space ~/dev --format json | jq .

# Tune observation walks (verified discovery and prune handoff are independent)
spanwit space ~/dev --workers 4 --stall-timeout 60s

# Fast capacity planes only: filesystem, pool/volumes, snapshots, managed roots
spanwit space ~/dev --capacity-only
spanwit space ~/dev --capacity-only --format json > /tmp/capacity-report.json

# Observe registered volumes over time (propose-only; never deletes)
spanwit observe register "$HOME"
spanwit observe once --format json
spanwit observe status --foreground

# Plan a prune (dry-run); add --execute to actually delete
spanwit prune --config ~/.config/spanwit/config.yaml
spanwit prune --config ~/.config/spanwit/config.yaml --execute

# Machine-readable plan for pipelines
spanwit prune --format json | jq .
```

### Disk critically full?

**Capacity ritual** (sense → handoff → reclaim → optional recipes):

```bash
# 0. When named diagnostics do not explain the pressure, enumerate large files.
# This is neutral evidence, not deletion authority.
spanwit inventory /System/Volumes/Data --min-size 5GiB --older-than 30d \
  --one-filesystem --top 200

# Or capture allocation planes without a deep inventory walk.
spanwit space ~/dev --capacity-only --format json > /tmp/capacity-report.json

# 1. Triage — actionability layout; filter to what you can free now
spanwit space ~/dev --min-size 500M --class prunable --sort idle

# Named caches / toolchains only (diagnostic-only; never deleted by spanwit)
spanwit space ~/dev --class diagnostic-only --domain development

# 2. Save JSON (carries prune_handoff.exact_plan + applied_filters when set)
spanwit space ~/dev --min-size 500M --class prunable --format json > /tmp/space-report.json

# 3. Revalidate the exact plan (dry-run), then execute only after it matches
spanwit prune --from-space-report /tmp/space-report.json
spanwit prune --from-space-report /tmp/space-report.json --execute

# 4. Optional: guided recipes in space text output (e.g. rustup). Never auto-run.
```

Bare `--allowlist` is a dry-run convenience against built-in signatures only and
**refuses `--execute`** (no custom-signature/filter provenance). Prefer
`--from-space-report` for policy-preserving reclaim.

`inventory` requires at least one explicit root and creates no report, cache,
index, or temporary file by default. Without `--top`, matches stream in
unspecified traversal order. `--top N` scans the complete declared scope and
retains only a bounded ranking. `--format jsonl` emits typed
`spanwit.inventory.*.v1` records under the producer profile
`spanwit.filesystem-inventory/v0`; `--summary-only` suppresses entries without
weakening coverage, and repeatable `--exclude` rules define the declared
subject. `--coverage-attest PATH` optionally writes a separate, validated,
block-export producer coverage claim with no overwrite; it never enters the
inventory stream. `--print0` emits local paths only. None of these projections
authorize deletion.

`inventory --directories` is a readable folder audit: allocated size by default
and an inclusive `--directory-min-size` floor over the full-subject walk.
`--format jsonl` emits the versioned profile
`spanwit.filesystem-inventory-aggregation/v2`, and `--coverage-attest` attests
traversal of the full subject. See [docs/inventory.md](docs/inventory.md).

`space --capacity-only` uses one filesystem observation for both `pressure` and
the filesystem capacity plane, then attempts the optional analysis-root sample
and platform-specific read-only accounting under one shared five-second context
budget. Add `--held-open` for a separate, slower read-only Darwin pass over
unlinked files that remain open:

```bash
spanwit space --capacity-only --held-open \
  --session-boundary before-running --format json > before.json
spanwit space --compare before.json after.json
```

Held-open objects are deduplicated by device and inode. Their logical sizes are
lower-bound diagnostic evidence, never reclaimable or APFS allocated-byte
claims; Spanwit does not stop processes or elevate privileges. Paths are absent
by default, including basenames. `--disclose paths` is an explicit report-file
disclosure choice.

`space --compare LEFT RIGHT` reads exactly two explicit SpaceReport v2 captures
(one may be `-` for stdin), validates them as untrusted bounded input, and emits
only claim-compatible right-minus-left deltas. Capture-mode and boot changes are
informational; missing or mismatched volume/container identity stays
plane-local and indeterminate/incomparable. No report database, cache, daemon
state, reconciled total, or numeric “unexplained” residual is used.

Optional planes report
`measured`, `partial`, `unsupported`, or `unavailable`; missing values are not
reported as zero and planes are never summed into an invented reconciled total.
On macOS it uses public `diskutil -plist` interfaces and measures only
catalog-constrained system roots as diagnostic-only lower bounds. Held-open
collection remains `not_requested` unless its flag is set. Capacity-only reports
are deliberately rejected by `prune --from-space-report`.

Before sharing a capacity report, review its ordinary analysis root, mounts,
catalog-constrained system roots, and APFS volume names. A user-renamed volume
name can contain personal or organizational text even when held-open path
disclosure is `none`.

Filesystem sampling checks the shared context before and after each native
statfs-style call. Because supported kernels do not universally provide an
interruptible statfs syscall, that syscall boundary is best-effort rather than a
hard wall-clock cancellation guarantee; command runners and managed-root
enumeration are context-cancelable.

See [docs/inventory.md](docs/inventory.md) for filter, backend, coverage, output,
and privacy semantics.

Configuration resolves in order: `--config` flag, then `SPANWIT_CONFIG_PATH`,
then the default `~/.config/spanwit/config.yaml`. Example profiles (crisis vs
maintenance) and the full crisis recipe live in
[docs/configuration.md](docs/configuration.md).

## Install

```bash
go install github.com/3leaps/spanwit/cmd/spanwit@latest
```

Release binaries with signed SHA256/SHA512 manifests are attached to each
[GitHub release](https://github.com/3leaps/spanwit/releases).

## Building

```bash
make build      # builds bin/spanwit
make pr-final   # full non-mutating quality gate
```

## Where this is going

Phase 1 (now): a CLI you run after a grumpy `df -h`.
Phase 2: a low-overhead background agent with continuous capacity awareness (`spanwitd`).
Phase 3: a small HTTP control surface for governing reclamation policy across
a fleet of dev boxes, build agents, and CI runners ("Spanwit Fleet").

The signature engine — the thing that knows a `target/` next to `Cargo.toml`
is regenerable but a `target/` elsewhere may not be — is the same engine that
will generalize to ML workstations (model artifacts, dataset caches),
data-engineering boxes (stale checkpoints, materialized views), and CI/build
farms.

## Status

v0.2.0 — alpha. Dry-run by default. `space`, `inventory` and `diagnose` are
read-only (`--coverage-attest` additionally writes a separate attestation
file). `observe` keeps local state and is propose-only: it never deletes, but
it writes, so `--read-only` rejects it.

## Related

- Spanwit is a [3leaps](https://3leaps.net) product — alongside
  [Docprims](https://github.com/3leaps/docprims) and
  [Seclusor](https://github.com/3leaps/seclusor)
- Built on the Fulmen microtool chassis (Crucible + gofulmen)
- Uses gofulmen v0.3.6+

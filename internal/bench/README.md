# Inventory measurement harness

Development tooling for comparing filesystem inventory implementations. It
lives under `internal/` so it cannot become a product surface, and the release
build targets `./cmd/spanwit` only.

## What it is for

Producing evidence, not conclusions. The harness emits rows; choosing an
implementation is a separate judgment made against rows that exist, measured on
corpora that were actually built, under conditions each row states for itself.

## Running it

```bash
go build -o /tmp/harness ./internal/bench/cmd/harness

/tmp/harness sweep \
  --work-dir /tmp/bench-work \
  --corpora wide,deep,dev-tree,mixed-permissions \
  --scale 4000 \
  --workers 1,4,8 \
  --repetitions 5 \
  --device-class ssd \
  --out rows.jsonl
```

`--device-class` is required and has no default. The harness cannot read it
portably, and a condition that was not stated at measurement time cannot be
recovered afterwards — only re-run, on a machine that may no longer exist. Pass
`unknown` to state explicitly that it is not known; that is a claim about
coverage and is emittable. Passing nothing is an absent field and is not.

Rows go to stdout or `--out`. The coverage statement — comparators not
measured, shape elements omitted, writes observed — goes to stderr, so a reader
sees what was _not_ measured without having to notice an absence.

## What a row carries

Every row states its own conditions: filesystem type, kernel, device class,
cache state, CPU count, worker count, and corpus identifier, in the same record
as the number. `NewMeasurement` refuses to construct a row whose environment is
incomplete, so an unenvironmented result is not representable rather than
merely discouraged.

Two rows carrying the same `corpus_id` ran against the same shape. Two that do
not are not directly comparable, whatever else they share. `Set.CheckComparable`
enforces this before a set is presented as a comparison.

## Reading the numbers honestly

**`entry_count_semantics` is not decoration.** `find` prints one line per entry
it visits; `du` reports something else entirely and carries `not-reported`; a
filtered search prints only matches. Comparing an entry rate across two
different semantics is comparing two different measurements.

**`below_noise_floor` marks runs under 50ms**, where process startup and
scheduling dominate wall time. Those rows are real measurements of real runs,
and they cannot distinguish implementations. A comparison built from them is
measuring `fork` and the Go runtime's initialization.

**Apparent bytes are not reclaimable space.** Hard-linked entries are counted
once per directory entry, and sparse files report far more than they allocate.
The fixture corpora contain both, deliberately, and
`Manifest.UniqueApparentBytes` gives the inode-distinct figure for comparison.
Neither is a figure to label `bytes_freed`.

**Examined, matched, and emitted are three different numbers.** A size filter
separates the first two; a top-K limit separates the last two.

## The write promise

`WriteWitness` tests the guarantee that is ours to make: the application
creates no file, temp, cache, or database of its own. Changes under a path the
application owns are _attributable_ and fail the promise. Everything else that
moved during the run is counted as _ambient_ machine activity and reported as
context.

That split exists because the first real sweep charged this tool with a
browser's cache writes. Whole-system write counters move for reasons no
implementation controls — filesystem metadata writeback, another process, the
caller's own redirection of output onto the measured volume. Those are benchmark
evidence about a machine; the attributable set is the promise.

## Fixture corpora

Built by `internal/corpus`, with two properties that make them an oracle:

- Ground truth comes from the construction plan, never from a walk of the
  result. Counting a tree with a walker and then validating a walker against
  those counts would be circular.
- A shape element the platform cannot produce is recorded as an **omission**
  rather than skipped. A corpus that quietly built fewer adversarial shapes
  than requested would report the same success as one that built them all.

Capability detection performs each operation — symlink, hard link, sparse
write, permission denial — rather than branching on the OS name. tmpfs, APFS,
ext4, and a container running as root all differ, and the difference is not
predictable from `GOOS`.

## What this harness cannot tell you

- **Anything about a filesystem it has no row for.** The extrapolation boundary
  is exactly the set of rows; a claim reaching past them is extrapolation and
  has to say so.
- **Whether a cold-cache row was really cold.** Dropping caches needs privilege
  the harness does not assume. Cold rows carry `cache_purge_attempted` and
  `cache_purge_succeeded`; a cold label over a failed purge is visible rather
  than silent.
- **Device-boundary behavior**, unless a real mount boundary exists below the
  measured root. Creating one requires privilege, so the sweep records an
  omission rather than reporting an untested pass.
- **Peak open descriptors.** Not sampled yet; rows carry `-1`, which means not
  observed rather than zero.

# Experimental observation daemon

The foreground `spanwit daemon run` (or `go run ./cmd/spanwitd`) samples one
filesystem every five minutes and writes inline growth samples to a private
recordprims journal. By default it performs no directory walk. Optional explicit
roots enable bounded allocation observations; neither mode evaluates policy or
performs cleanup. Stop it with SIGINT or SIGTERM. No service is installed automatically.

```sh
spanwit daemon run --path "$HOME"
spanwit --read-only daemon capabilities
spanwit --read-only daemon status
spanwit --read-only daemon proposals
# After stopping the daemon, append one sample through the same coordinator:
spanwit daemon once --path "$HOME"
```

For opt-in root observations, supply canonical absolute paths:

```sh
spanwit daemon run --path "$HOME" --root "$HOME/Downloads" --root "$HOME/dev/temp"
```

At most 16 lexically non-overlapping roots are accepted, each at most 1024 bytes,
with at most 16 KiB of JSON-encoded paths in total. Arguments that change under
whitespace normalization or cannot be represented as UTF-8 are rejected. There
is no implicit cache discovery. Each observation walks regular files using the
existing inventory walker, with two workers, two open directories, a 4096-directory
pending frontier, up to 16 retained root continuity handles, a five-second directory-open stall timeout, and a two-minute
deadline for the whole batch. Roots run sequentially. Directory symlinks are not
followed; noncanonical/symlinked roots are unavailable. Remote descendants and
mount boundaries are skipped and prevent a comparable complete baseline.

Deep observations run initially, hourly after a complete baseline, after a
free-space drop of at least 5 GiB since that baseline or 2 GiB between successful
heartbeats, or on worsening pressure. Attempts have a 15-minute cooldown;
a transition into critical pressure can bypass it once. Failed or partial
attempts retain pending reasons for a cooldown-bounded retry. Cadence uses
monotonic elapsed time. No budget crossing is evaluated yet.

Status reports `awaiting_attribution` until the first result, then `complete`,
`partial`, or `unavailable` in `attribution_state`. It includes the latest committed `attribution` when available, with its
capture time, trigger reasons and originating capacity record. Its rows report
allocated regular-file **path-entry sums**, not unique physical allocation,
expected recovery, or bytes written. Deltas are emitted only between complete
observations with the same path, device, root inode, continuity generation and basis and increasing
capture times. Missing/partial observations carry no byte value. A root's delta
does not prove that it caused a volume's free-space loss. Root identity checks
are best-effort observation checks, not race-free mutation admission. A retained
directory handle prevents inode reuse from inheriting an earlier generation;
the walker itself remains path-based rather than an atomic filesystem snapshot.
Attribution payloads are capped at 32 KiB, and combined status is size-checked
before journal append and publication to fit the 64 KiB client response bound.

Attribution leaves cached reads available, but runs serially with capacity
sampling. The previous attribution remains visible between observations;
inspect its timestamp and coverage rather than assuming it matches the latest
capacity sample. Runtime baselines start empty on restart; the first observation
has no delta. Activity is currently `unknown`: no live build detector is wired.
The core can defer roots identified as active by a future activity adapter.
There is no trim or approval path.

`--state-dir` selects an absolute, user-owned directory with mode 0700.
Every component is opened relative to its verified parent descriptor without
following symlinks. Use canonical paths (for example `/private/tmp` on macOS,
not the `/tmp` symlink). The
socket is mode 0600. State ancestors must be owned by the user or root;
writable ancestors require root-owned sticky-directory protection. Socket mode
changes never follow symlinks. Each accepted connection must have a kernel-verified peer
UID equal to the daemon UID; the client verifies the server UID too. This is a
local-user boundary, not agent identity or approval. Only macOS and Linux are
supported. Run as an ordinary user.

The daemon and offline sampler hold the same process-exclusive coordinator
lock for their complete lifetime. A missing socket does not authorize a second
writer. The lock file is retained after shutdown. A pre-existing socket is
refused without deleting it; after an unclean exit, inspect its owner and
confirm the previous process has stopped before removing that stale socket.
An incomplete journal initialization fails closed and is not overwritten.

HTTP/1.1 supports only bodyless, queryless GETs to `/v1/capabilities`,
`/v1/status`, and `/v1/proposals`. Unknown versions/operations return 404 with
`unsupported_operation`; other methods return 405. Requests with bodies or
queries return 400. Responses carry `protocol: 1`. Connections are limited to
16, headers to the HTTP server's 4096-byte configured limit, and request/response
timeouts to five seconds (header timeout two seconds). Keep-alive is disabled.
Clients bound responses to 64 KiB and never replay failed requests offline.

Status reads an in-memory snapshot without taking the sampling lock. Startup
reports `awaiting_sample`; a successful journal append publishes `observing`.
Samples are durable before they become visible. Sampling failures retain the
previous sample as `sample_unavailable` and retry on the next heartbeat. Journal
failures retain `journal_unavailable` for readers and suspend further writes
until restart; no uncertain append is silently replayed. On restart, the cache starts
empty and a fresh sample is taken; existing journal records remain unchanged.
Proposal reads explicitly return `not_evaluated` and an empty list, not a claim
that the machine has no reclaimable files. No mutation socket methods exist.

The journal uses durable-record/v1 and recordprims-fs/v2. Sample payload version
0 is distinct from API protocol version 1. Existing observe JSON state is not
migrated. Root payloads use `root-attribution/v0` on a separate journal stream.
There is no snapshot-body retention, approval, grant, execution, or service
installation in this experimental slice. Journal retention is not implemented;
keep experiments bounded rather than installing an unattended service.
Native filesystem sampling is best-effort at the timeout boundary: a blocked
kernel call cannot be forcibly interrupted by a Go context.

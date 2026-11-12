# Experimental observation daemon

The foreground `spanwit daemon run` (or `go run ./cmd/spanwitd`) samples one
filesystem every five minutes and writes inline growth samples to a private
recordprims journal. It performs no directory walk, policy evaluation, or
cleanup. Stop it with SIGINT or SIGTERM. No service is installed automatically.

```sh
spanwit daemon run --path "$HOME"
spanwit --read-only daemon capabilities
spanwit --read-only daemon status
spanwit --read-only daemon proposals
# After stopping the daemon, append one sample through the same coordinator:
spanwit daemon once --path "$HOME"
```

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
migrated. There is no snapshot-body retention, approval, grant, execution,
installation, or deep-attribution implementation in this experimental slice.
Native filesystem sampling is best-effort at the timeout boundary: a blocked
kernel call cannot be forcibly interrupted by a Go context.

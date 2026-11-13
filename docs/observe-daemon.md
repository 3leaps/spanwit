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
has no delta. Immediately before a deep batch, a bounded `lsof` query looks for
visible `go`, `cargo`, and `rustc` processes with a current directory or open path
under a selected root. A positive match defers that root (`activity: active`,
`coverage: deferred`); retry follows the normal cooldown. The query has a
two-second deadline and 512 KiB output cap; inherited-pipe cleanup may add two
seconds. No matches, denied visibility, missing tools and failed/truncated
queries remain `unknown`, never proof of inactivity. Processes may start after
the query, and cross-worktree cache consumers without a visible path are missed.
This is a scheduling hint, not a complete build detector or cleanup guard.
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
There is no snapshot-body retention, approval, grant, or cleanup execution
in this experimental slice. Before opening a store and before
appending observations, a bounded metadata walk checks aggregate logical file
lengths (including indexes and temporary files) against a 16 MiB stop-admission
watermark. Enumeration is limited to 4096 entries and eight directory levels;
links, special files, public entries and different-device entries are refused.
Same-filesystem bind mounts are not separately detected by this check.

A running coordinator that reaches the watermark publishes
`journal_capacity_reached`, preserves the last durable cached observations, and
suspends sampling and deep walks. A pre-existing oversized store refuses startup.
Restart alone does not reduce its size. This is not a hard physical quota: an
admitted append and temporary index rewrites can cross the watermark; filesystem
allocation and other processes are not bounded by it. Uncertain append outcomes
still latch `journal_unavailable`.

Journal history is never deleted, truncated or automatically reset. Recordprims
retirement and stream continuation retain history and do not reclaim its bytes.
Automatic history reclamation remains unavailable; keep experiments bounded
rather than installing an unattended service.
Native filesystem sampling is best-effort at the timeout boundary: a blocked
kernel call cannot be forcibly interrupted by a Go context.

## Prepare and manage a user service

`daemon service preview` renders a launchd agent or systemd user unit as a
versioned `user-service-plan/v0` JSON document (maximum 128 KiB),
including the definition text and explicit manager command arguments. The
commands describe a first start after separately installing the definition;
they are not an idempotent lifecycle transaction. It does
not write a definition, invoke a service manager, or enable login persistence.
The executable path is an explicit input; preview does not verify the binary
or establish that it is installed or compatible. Use a stable installed
executable, not a checkout or temporary build.
Linux previews reject executable paths containing single quotes, double quotes,
or backslashes, which systemd does not support. These characters remain
supported in ordinary argument paths and in launchd executable paths.

```sh
spanwit daemon service preview --executable /absolute/installed/spanwit \
  --path /absolute/observed/filesystem --root /absolute/observation/root
```

The platform defaults to the current OS; `--platform darwin` and
`--platform linux` allow inspecting either format. Linux definition paths honor
`XDG_CONFIG_HOME` when set; it must be an absolute clean path. Otherwise they
use the home directory’s `.config` directory. Attribution roots remain
explicit and optional. The inherited `--state-dir` selects the private daemon
state directory. The capacity interval defaults to five minutes. Definitions
use argument arrays, disable automatic restart, and do not enable login
startup.

Explicit manual-start lifecycle commands emit a versioned
`user-service-status/v0` JSON report (maximum 128 KiB) separating definition
integrity, service-manager observation, and daemon socket/instance health:

```sh
spanwit daemon service install --executable /absolute/installed/spanwit \
  --path /absolute/observed/filesystem --root /absolute/observation/root
spanwit daemon service start
spanwit --read-only daemon service status
spanwit daemon service stop
spanwit daemon service uninstall
```

- `install` verifies that `--executable` is a symlink-free regular executable
  in a non-writable directory owned by the user or root, computes its SHA-256
  and runtime configuration digest, refuses foreign or externally modified unit
  files, and publishes `<state-dir>/service-receipt.json` and the unit file in
  two crash-consistent phases under a dedicated `<state-dir>/service-control.lock`
  (never contending with `coordinator.lock`). It does not start the service.
- `start` requires a verified installed definition and matching on-disk
  executable, invokes fixed platform manager verbs without shell interpretation
  or blind retries, and confirms that the running daemon’s `/v1/status`
  `instance` (`pid`, `config_sha256`, `executable_sha256`) matches both the
  service manager’s active PID and the ownership receipt.
- `status` is strictly observational (`--read-only` compatible): it never
  creates directories or locks, and reports `installed_stopped`, `healthy`,
  `drifted` (such as when a binary is replaced on disk while the old process is
  running), `conflict` (such as an unmanaged foreground process holding the
  socket), `interrupted`, or `unknown` when manager state cannot be confirmed.
- `stop` stops an owned service and requires independent confirmation that the
  service manager reports an inactive state, `control.sock` has been removed by
  the daemon, and any verified daemon PID has exited (`ESRCH`). It never deletes
  `control.sock` or signals an unverified PID; timeouts and unconfirmed stops
  report `outcome: "unconfirmed"` with `uncertain: true`.
- `uninstall` requires the service to be confirmed stopped and removes only a
  definition whose digest matches the ownership receipt, preserving
  `<state-dir>/journal` and `coordinator.lock`.

`make install BINDIR=/absolute/bin` deploys only the CLI executable. On macOS
and Linux it stages
a fresh executable beside the destination and replaces the destination only
after staging succeeds. A running process continues using its old executable;
binary deployment does not restart it. Other hosts retain their existing
unlink-and-copy installation path; the staged replacement tests are limited
to macOS and Linux. A service upgrade therefore requires
an explicit `stop`, binary deployment, `install` (to re-verify and bind the new
executable digest), `start`, and health check.

The Linux qualification uses [`systemd-analyze verify`](https://manpages.debian.org/bookworm/systemd/systemd-analyze.1.en.html)
on a private fixture definition. Syntax validation does not start a service.

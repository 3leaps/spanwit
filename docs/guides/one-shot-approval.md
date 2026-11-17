# One-shot approval of an exact prune plan

An approval lets someone other than the maintainer run one exact prune plan
once, after the maintainer has seen it and signed it with a hardware security
key. macOS and Linux only.

## What an approval binds

The maintainer signs a payload (`spanwit-approval/v0`, canonical JSON) that
binds:

- the digest of the exact plan bytes and of its candidate set, and the
  analysis root;
- the built-in signature catalog in force;
- `--min-age`, when given (execution must pass the same value);
- with `--bind-active-profile`, the active profile pack and its activation:
  deactivating and reactivating the same pack invalidates the approval;
- limits: at most the plan's candidate count and bytes;
- the store it was requested in, an approval id, and a window of at most 24h.

An approval is used once. Execution re-checks all of it immediately before
deleting; any difference refuses with `stale_plan_or_drift` and deletes nothing.

## Setup (once)

### A signing tool with security-key support

Approvals are verified with the system `/usr/bin/ssh-keygen`. Signing needs an
`ssh-keygen` that can use a security key:

| Platform | Signing `ssh-keygen`                                                                  |
| -------- | ------------------------------------------------------------------------------------- |
| Linux    | `/usr/bin/ssh-keygen` from an OpenSSH client built with security-key support (8.2+).  |
| macOS    | The system `ssh-keygen` cannot use security keys. Install Homebrew's OpenSSH (below). |
| Windows  | Approvals are unavailable.                                                            |

On macOS:

```sh
brew install openssh
```

This links `ssh`, `ssh-keygen` and `ssh-agent` into `/opt/homebrew/bin`. If
that directory comes before `/usr/bin` on your `PATH`, they replace the system
tools for everything you run. To avoid surprises, call it by full path and pass
it to spanwit with `--ssh-keygen /opt/homebrew/bin/ssh-keygen`. spanwit never
searches `PATH` for it, and verification always uses `/usr/bin/ssh-keygen`.
Because it reads the key's PIN, spanwit refuses a signing binary that another
user could replace: the file and every directory above it must be owned by you
or root and not writable by others (group write is accepted only for an
administrative group, such as `admin` on Homebrew's prefix).

`spanwit doctor` reports which `ssh-keygen` signs and verifies on this machine.
It does not touch the key, so security-key support is confirmed by the first
signature.

### The approval key

The approval key must be an OpenSSH security-key key (`sk-ssh-ed25519` or
`sk-ecdsa-sha2-nistp256`) that requires a touch. A key dedicated to spanwit
is easiest to tell apart from other SSH keys (on macOS, use
`/opt/homebrew/bin/ssh-keygen`):

```sh
ssh-keygen -t ed25519-sk -O application=ssh:spanwit -O verify-required \
  -C spanwit-approver -f ~/.ssh/id_ed25519_sk_spanwit
```

`-O verify-required` is optional: signing then asks for the security key's PIN
as well as a touch. spanwit requires the touch; it does not check for the PIN.

List its public key in `/etc/spanwit/allowed_signers` (or the file passed with
`--allowed-signers`):

```text
spanwit-approver sk-ssh-ed25519@openssh.com AAAA...
```

The file, and every directory above it, must not be writable by the user who
runs spanwit (owned by another user, no group or other write, not a symlink).
An entry with `no-touch-required`, `cert-authority`, or a key type that can
sign without the device makes the whole file untrusted.

`approval sign` runs in your terminal, where `ssh-keygen` asks for the key's
PIN or the key file's passphrase when either is set, and then for a touch.

### No security key?

Approvals are only needed to let someone or something else run a prune.
`spanwit prune --execute` run by you directly works without one.

A passphrase-protected software key is not accepted: its signature does not
show whether anyone was present, and a process running as the same user can
often use the key through `ssh-agent` without the passphrase. A security-key
signature carries a presence flag set by the device, which spanwit checks.

## Flow

The daemon must be stopped for every step that writes the journal
(`spanwit daemon service stop`; start it again afterwards). The journal lives
in `~/.local/state/spanwit/daemon` (`--state-dir`), which must be a private
directory on a local APFS or HFS+ (macOS) or ext4, XFS or Btrfs (Linux)
filesystem.

1. Anyone requests an approval for a plan:

   ```sh
   spanwit approval request --plan plan.json --out payload.json [--expires 1h] [--min-age 48h]
   ```

   This journals a request with no authority and writes the payload.

2. The maintainer, in their own terminal, reviews and signs:

   ```sh
   spanwit approval sign --plan plan.json --payload payload.json --key ~/.ssh/id_ed25519_sk_spanwit
   # macOS: add --ssh-keygen /opt/homebrew/bin/ssh-keygen
   ```

   `sign` reads both files once, checks the payload binds that plan, prints the
   candidates, limits, window and options, asks for the first 12 characters of
   the plan digest, and then has `ssh-keygen` sign those same bytes (touch the
   key). `approval show` performs the same checks without signing. Signing the
   payload file directly with `ssh-keygen -Y sign` skips this check and is not
   recommended.

3. Record the approval:

   ```sh
   spanwit approval issue --payload payload.json --sig payload.json.sig
   ```

4. Execute it:

   ```sh
   spanwit prune --from-exact-plan plan.json --execute --approval <id>
   ```

`--sort` is refused with `--approval`: the approved plan's `sort_mode` decides
the order. `spanwit approval status <id>` shows the state and, once used, each
candidate's outcome; `spanwit approval revoke <id>`
revokes a requested or issued approval. A revocation recorded before execution
starts wins; a running execution is not interrupted.

## Approved-only invocations

`--approved-only` (or `SPANWIT_APPROVED_ONLY=1`) refuses every mutation except
`prune --from-exact-plan <plan> --execute --approval <id>`. Observational
commands still run and report the `read_only` contract. It cannot be combined
with `--read-only`, which still refuses all mutation, approved or not. The
assertion only narrows what an invocation may attempt; the approval itself is
checked by the executor.

## Refusals

| Code                           | Meaning                                                                                                              |
| ------------------------------ | -------------------------------------------------------------------------------------------------------------------- |
| `stale_plan_or_drift`          | The plan, catalog, `--min-age`, profile activation or store differs from the approval.                               |
| `unauthorized_principal`       | No valid presence signature by an allowed key, or no such issued approval.                                           |
| `allowed_signers_untrusted`    | The allowed-signers file is missing, writable by this user, a symlink, or lists a key that can sign without a touch. |
| `grant_expired_or_revoked`     | The window has closed or the approval was revoked.                                                                   |
| `grant_already_consumed`       | The approval was already used.                                                                                       |
| `idempotency_payload_mismatch` | `--operation-id` names an operation that ran with a different request.                                               |
| `persistence_uncertain`        | The journal could not record a step; nothing further is deleted.                                                     |
| `limits_exceeded`              | More candidates or bytes are prunable than the approval allows; nothing is deleted and the approval is spent.        |

## Outcomes

Every execution, approved or not, records each candidate's outcome in the
journal: `applied`, `withheld`, `failed` (the guard refused; nothing removed),
`partial` (a removal error left part of the target), or `unattempted` (the run
stopped first). Before the first deletion the run stores the list of
candidates it will attempt, so after an interruption
`spanwit approval operation <operation-id>` reports every candidate, with
`unknown` for any whose outcome was not recorded; nothing is re-executed and
the original plan file is not needed. `approval status` and `approval
operation` read the journal through the same store lock as other journal
commands (the store may rebuild its indexes while reading), so they are
refused under `--read-only` and while the daemon runs; they never create a
missing store. `--operation-id <id>` with the original plan
returns the recorded result of an earlier approved run without deleting
anything.

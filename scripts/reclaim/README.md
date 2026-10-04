# Reclaim scripts (ops library)

Operator scripts for **app-state** disk reclaim that is adjacent to spanwit's
mission but not (yet) signature-engine product surface.

| Property     | Rule                                                                 |
| ------------ | -------------------------------------------------------------------- |
| Default      | **Dry-run** (plan) — print a typed record stream; no deletes         |
| Execute      | Explicit `--execute`, and only after every required preflight passes |
| Product gate | Never auto-mapped under `development.*`                              |
| Spanwit      | Optional later graduation (custom signature / `space` hotspot)       |

These paths are usually **conversation history, undo snapshots, or tool
binaries** — not regenerable build output. Treat them like prune plans: review,
then deliberate execute.

## The shared harness

All reclaim scripts source `reclaim-lib.sh` so we get **one I/O shape for N
scripts**, not N dialects. A tool provides only profile discovery, preflight
checks, and whitelisted hooks; the library owns everything safety-critical,
including the run lifecycle itself:

- **Modes** — `--plan` (default) | `--preflight` | `--execute`; text or
  `--format json` (JSON stays on stdout, human lines on stderr).
- **Run lifecycle** — `reclaim_main` owns ordering: header first (always, even
  when admission fails), admission, root binding, inventory, preflight, gate,
  phase dispatch, and a **terminal record on every exit path**. A profile cannot
  improvise its own ordering or forget to report a final state.
- **Preflight gate** — a per-profile registry of named checks, each verdicted on
  two axes: `outcome` (`pass|fail|unknown`) × `basis` (`confirmed|inferred`).
  A phase runs under `--execute` **only** when every required check is
  `outcome=pass` **and** `basis=confirmed`. Uncertainty fails closed. There is
  **no `--force` and no override flag**: a failed preflight cannot be bypassed.
- **Typed record stream** (`spanwit.reclaim-ops/v0`) — ordered `header`,
  `admission`, `inventory`, `preflight`, `phase`, `terminal` records that map
  their fields to existing spanwit vocabulary (trust class, withheld reasons)
  rather than inventing a parallel taxonomy. Declared scalar fields (`bytes`,
  `candidates`, `exit_code`, `required`, …) are emitted as JSON **numbers and
  booleans**, not strings; undeclared fields stay strings.
- **Lifecycle statuses** — phase and terminal records use
  `pending | running | complete | failed | blocked | skipped`. A run with any
  failed operation ends `failed` with a non-zero exit; nothing reports success
  it did not achieve.
- **Strict path admission** — a data root is admitted only if it is a real,
  owned, **symlink-free** directory strictly under `$HOME`; filesystem root,
  `$HOME` itself, mount/volume roots, and unverifiable owner/device evidence are
  refused. Mutation targets are re-checked for containment, symlinks, device,
  and `auth.json` disjointness. Destinations that do not exist yet are admitted
  **before** anything is created, so a refused path leaves nothing behind.
  Untrusted ids that would steer a write must pass a single-component name
  grammar. Credentials are never touched.
- **Export destinations** are treated as a trust boundary: a secret-bearing copy
  target must be under `$HOME`, symlink-free, **outside** the data root, created
  `0700` with `0600` files, and capacity-checked before any delete. Outputs are
  written under their exact final basename into a private staging directory,
  then published with an atomic **no-replace** primitive whose second operand is
  the exact pathname (`link`, or `ln <staged> <dir>` as a fallback — valid only
  because the staged basename _is_ the final name), and the result is verified
  by device + inode identity. Whatever appears at the destination _while_ the
  export runs — a file, a directory, or a symlink to one — causes publication to
  fail rather than being replaced or silently redirected into. Checking that a
  name is absent cannot reserve it, and two-operand `ln` with a directory
  destination links _inside_ it. A lost publication race blocks that session's
  delete and fails the run.
- **Exit classes** — `0` ok · `64` usage · `65` admission refused · `70` runtime
  error · `75` preflight blocked (resolve, then retry).

Every profile must pass `reclaim_conformance_report` (three-segment profile id,
all hooks registered, at least one preflight check) before the harness will run
it — that is the guard against script #2 growing its own dialect.

## Layout

```
scripts/reclaim/
  README.md                     # this file
  reclaim-lib.sh                # shared lifecycle, gate, record stream, and admission
  opencode-data.sh              # profile agent.opencode.data
  agent-tmp-residue.sh          # profile agent.tmp.residue
  reclaim.test.sh               # shared harness tests
  agent-tmp-residue.test.sh     # temp-residue profile and mutation-safety tests
```

## Tests

`./scripts/reclaim/reclaim.test.sh` runs three layers with no external
dependencies beyond `sqlite3`/`python3`: library unit tests, black-box CLI runs
against a `$HOME` fixture, and **stubbed mutation tests** that exercise the real
execute paths (quarantine rename, export-then-delete, wrong-store refusal,
containment, failure exit codes) against fixtures via a `PATH` shim — no live
OpenCode data is touched.

`./scripts/reclaim/agent-tmp-residue.test.sh` exercises shared-temp and
user-temp admission, per-candidate authorization, age and exclusion behavior,
complete-versus-inferred coverage, exact session-set membership binding,
mutation-time freshness/live-hold/size revalidation, typed record-schema
conformance, and real process exit statuses. Its live-hold cases must run
outside a restricted agent sandbox when that sandbox prevents `lsof`/`ps` from
observing the host; unavailable evidence fails closed rather than becoming “no
hold.”

## Catalog

| Script                 | Target                 | What it can free                                                                   | Notes                                                                                                                                                                                                                                                                                                                                                                                                        |
| ---------------------- | ---------------------- | ---------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `opencode-data.sh`     | OpenCode data dir      | Legacy JSON store, old sessions (+ VACUUM), optional bin/logs                      | Uses `opencode session delete`/`export` when available; never touches `auth.json`; legacy-json requires a migration fingerprint (a **populated** live SQLite store + a frozen JSON store); the sessions phase refuses to run unless the CLI's own store is the admitted root                                                                                                                                 |
| `agent-tmp-residue.sh` | Admitted OS temp plane | Stale allowlisted Go test binaries, named Go caches, and Claude temp session trees | Dry-run by default. File phases require age, allowlist, per-candidate authorization, and no active Go build. Session execute additionally binds the reviewed count plus membership digest and requires explicit incomplete-live-set acceptance; exclusions, freshness, concrete holds, containment, ownership, type, and device are revalidated at mutation time and cannot be bypassed. No generic temp GC. |

## Conventions (for new scripts)

1. **Source the harness and hand it the run.** `source "$_SCRIPT_DIR/reclaim-lib.sh"`,
   then `reclaim_set_profile`, `reclaim_register_check` for each check,
   `reclaim_register_hook` for `args` / `bind` / `inventory` / `phase`, and
   finally `reclaim_main "$@"`. The profile does **not** call `reclaim_admit_root`,
   order the run, emit the terminal record, or choose the exit code — the
   harness owns all of that. Do not re-implement mode/preflight/admission.
   Anything that can fail inside a profile should return non-zero and let the
   top-level shell call `reclaim_die`; calling it from a command substitution
   would capture the terminal record in a subshell instead of emitting it.
2. **One profile per tool**, named `<domain>.<ecosystem>.<name>` (e.g.
   `agent.opencode.data`) — three segments, aligned with the domain catalog.
3. **Register a preflight check for every way execute could be unsafe** (live
   process holding a lock, insufficient free space, missing migration evidence).
   A check that cannot confirm safety must return `unknown`/`inferred` so the
   gate blocks.
4. **Phases** with independent selection; `--execute` takes exactly one named
   phase (never `all`).
5. **Irreversible classes** (session/transcript deletion) require a distinct ack
   beyond `--execute`, and export-before-delete must fail closed. **Publish with
   an atomic no-replace primitive whose second operand is the exact pathname**,
   never a checked-then-`mv` and never two-operand `ln` against a bare
   destination: an absence check observes a name, it does not reserve it, so
   anything appearing in between would be replaced — and a directory (or symlink
   to one) at the destination makes `ln` link _inside_ it and return success.
   Verify the published path by identity afterwards.
6. **Bind the subject to the mutator.** If a phase mutates through another tool
   that resolves its own store/root, verify that root equals the admitted root —
   in preflight _and_ again at mutation time. A plan for one subject must never
   be able to mutate another.
7. **Quarantine is honest**: a same-device rename frees 0 bytes until the
   quarantine dir is later deleted — say so in the plan.
8. Document **graduation**: what would become a spanwit signature vs stay ops-only.

## Graduation path (spanwit)

| Pattern                                                               | Likely product home                      | Why                                        |
| --------------------------------------------------------------------- | ---------------------------------------- | ------------------------------------------ |
| Legacy dual-store leftovers (e.g. frozen JSON after SQLite migration) | Custom signature + `safe_to_prune: true` | Path+context, low false-positive if frozen |
| Session retention by age                                              | Unlikely as default signature            | Policy/export, not "regenerable artifact"  |
| Tool binary caches under XDG                                          | Diagnostic-only domain or signature      | Often redownloadable; still app-owned      |
| Hotspot report for `~/.local/share/*` agents                          | `space` recipe / unverified mass         | Guidance, not auto-delete                  |

Do **not** fold session transcripts into `development.*` reclaim without an
explicit retention product decision.

## Running

From a spanwit checkout (or copy the script + `reclaim-lib.sh` together):

```bash
# Always inventory + plan first (dry-run is the default)
./scripts/reclaim/opencode-data.sh --phase legacy-json

# Check the gate without mutating (exit 75 if anything blocks)
./scripts/reclaim/opencode-data.sh --preflight --phase vacuum

# Machine-readable stream
./scripts/reclaim/opencode-data.sh --format json > inventory.jsonl

# Inventory known temp-residue classes (read-only)
./scripts/reclaim/agent-tmp-residue.sh
./scripts/reclaim/agent-tmp-residue.sh --phase named-gocache
./scripts/reclaim/agent-tmp-residue.sh --phase go-test-binaries
./scripts/reclaim/agent-tmp-residue.sh --phase agent-scratch

# Execute only after review, from a NON-OpenCode shell (quit OpenCode first)
./scripts/reclaim/opencode-data.sh --phase legacy-json --execute
./scripts/reclaim/opencode-data.sh --phase sessions --older-than 180d \
    --export-dir ~/backups/oc-sessions --confirm-delete-sessions <N> --execute
./scripts/reclaim/opencode-data.sh --phase vacuum --execute

# After verifying OpenCode still works, delete the quarantine to reclaim space:
#   rm -rf ~/.local/share/opencode/.reclaim-quarantine/<stamp>
```

Prefer a **non-OpenCode shell** when mutating OpenCode's own data directory; the
preflight will refuse `--execute` while an `opencode` process is running.

## Known residuals

- **Process detection** matches the process name (`pgrep -x opencode`). A renamed
  binary or wrapper could evade it; that is a documented residual, and the answer
  is never a bypass flag. If `pgrep` is unavailable the check returns
  `unknown`/`inferred`, which blocks.
- **Plan mode reads a possibly live tree.** Inventory is read-only and may race a
  running app; sizes are a snapshot, not a lock.
- **Agent live-set completeness is not knowable from local probes.**
  `agent-scratch` automatically excludes fixture-proven self ids and checks
  cwd/argv/open-path holds, but an idle sibling session may expose none of
  those. Session execute therefore requires an exact reviewed membership
  digest plus explicit acceptance of the incomplete live set; every concrete
  safety signal remains non-bypassable. Codex/Grok persistent session stores
  are outside this profile.
- **OpenCode mutation paths are fixture-tested, not dogfooded.** Those execute
  paths run end-to-end in `reclaim.test.sh` against fixtures with a stubbed
  `opencode` CLI. They have not been exercised against a real OpenCode data
  directory, so do not describe them as field-proven. The temp-residue file
  phase has separate live-host evidence; the higher-risk session phase remains
  fixture-tested and must not be described as field-proven.

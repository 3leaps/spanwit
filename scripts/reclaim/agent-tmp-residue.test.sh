#!/usr/bin/env bash
# agent-tmp-residue.test.sh — tests for the temp-residue profile and the
# root-class extension to the shared guard.
#
# Two layers:
#   1. Unit tests sourcing reclaim-lib.sh directly, covering root-class
#      admission and per-candidate authorization against fixtures.
#   2. Black-box CLI tests against a sticky fixture root, covering the
#      acknowledgement binding, exclusion behaviour, and the refusal to execute
#      the session phase.
#
# The class of defect these exist for: a guard that admits what it should
# refuse, or that reports a refusal it did not actually make.
#
# Run: ./scripts/reclaim/agent-tmp-residue.test.sh   (exit 0 = all passed)

# shellcheck source-path=SCRIPTDIR
set -uo pipefail

# This suite deliberately drives failure paths — refused admissions, blocked
# gates, non-zero exits are the subject matter — so an ERR trap fires on
# expected behaviour and proves nothing. The defect that actually occurred was
# assertions silently not running: a subshell aborted on an unbound variable,
# its assertions were never counted, and the harness still printed failed=0 and
# exited 0. The instrument for that is a floor on how many assertions ran,
# checked at the end, plus an explicit status check on every block that runs
# assertions in a subshell.
UNEXPECTED=0
note_unexpected() {
    UNEXPECTED=$((UNEXPECTED + 1))
    printf 'FAIL block exited early: %s\n' "$1"
}

# Blocks that need an isolated shell (because they source a library) run in a
# subshell, and a subshell's counters die with it. Previously those blocks
# printed 17 assertion lines that the summary never counted, so the floor was
# calibrated against assertions outside them and could not notice them
# vanishing. run_block captures the output, replays it, and absorbs the
# subshell's own tallies — and fails if the block did not reach its end.
run_block() { # <name> <function>
    local name="$1" fn="$2" out rc=0
    out=$("$fn" 2>&1) || rc=$?
    printf '%s\n' "$out" | grep -vE '^__BLOCK_(DONE|TALLY) ' || true

    local tally
    tally=$(printf '%s\n' "$out" | sed -n 's/^__BLOCK_TALLY //p' | tail -1)
    if [[ -z "$tally" ]] || ! printf '%s\n' "$out" | grep -q '^__BLOCK_DONE'; then
        note_unexpected "$name did not reach its end (exit $rc)"
        return 0
    fi
    local p f k
    read -r p f k <<< "$tally"
    PASS=$((PASS + p))
    FAIL=$((FAIL + f))
    SKIP=$((SKIP + k))
    return 0
}

# Inside a block, assertions accumulate locally and are reported on the way out.
block_end() {
    printf '__BLOCK_TALLY %d %d %d\n' "$PASS" "$FAIL" "$SKIP"
    printf '__BLOCK_DONE\n'
}

TESTDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
LIB="$TESTDIR/reclaim-lib.sh"
TOOL="$TESTDIR/agent-tmp-residue.sh"

PASS=0
FAIL=0
SKIP=0
ok() {
    PASS=$((PASS + 1))
    printf 'ok   %s\n' "$1"
}
no() {
    FAIL=$((FAIL + 1))
    printf 'FAIL %s\n' "$1"
}
skip() {
    SKIP=$((SKIP + 1))
    printf 'skip %s (%s)\n' "$1" "$2"
}
assert_eq() {
    if [[ "$2" == "$3" ]]; then ok "$1"; else no "$1 (want=[$3] got=[$2])"; fi
}
assert_contains() {
    if [[ "$2" == *"$3"* ]]; then ok "$1"; else no "$1 (missing [$3])"; fi
}
assert_not_contains() {
    if [[ "$2" != *"$3"* ]]; then ok "$1"; else no "$1 (unexpected [$3])"; fi
}
# Assert the process's real exit status, not the exit code it printed. A script
# can emit a correct-looking terminal record and still exit 0; asserting the
# record alone would let that through, and every caller downstream — a shell
# `&&`, a Makefile, a CI step — reads the status, not the text.
assert_exit() { # <name> <want> <cmd...>
    local n="$1" want="$2"
    shift 2
    "$@" > /dev/null 2>&1
    local got=$?
    if [[ "$got" == "$want" ]]; then ok "$n"; else no "$n (want exit=$want got exit=$got)"; fi
}

# Canonicalised: on macOS mktemp lands under /var, which is itself a symlink to
# /private/var, and the guard rightly refuses a root reached through one.
# Assertions are counted against this floor at the end. The defect this guards
# is assertions silently not running, which no individual assertion can detect.
EXPECTED_MIN=145

FIXROOT="$(cd "$(mktemp -d)" && pwd -P)"

# The live-build guard consults pgrep, so a stray `go` process anywhere on the
# machine would otherwise block phases under test and make results depend on
# what the host happened to be doing. A shim makes the guard deterministic
# without disabling it; the case where it fires is tested explicitly below.
SHIMBIN="$FIXROOT/bin"
mkdir -p "$SHIMBIN"
cat > "$SHIMBIN/pgrep" << 'SHIM'
#!/usr/bin/env bash
# Reports no matching process unless the fixture asks otherwise.
if [[ -n "${SHIM_PGREP_MATCH:-}" ]]; then
    for arg in "$@"; do
        [[ "$arg" == "$SHIM_PGREP_MATCH" ]] && { echo 1; exit 0; }
    done
fi
exit 1
SHIM
chmod +x "$SHIMBIN/pgrep"
export PATH="$SHIMBIN:$PATH"
# Execute-path fixtures cannot use the plan-only seam, so they need a root the
# production rule approves. They must NOT use the ambient per-user session root:
# a supported tool may genuinely be using that namespace, and a suite that
# recursively timestamps and then removes it would destroy real sessions. A
# fixed project key does not make removing the parent safe.
#
# Instead a unique parent is created inside the real platform temp tree, which
# satisfies the structural rule while belonging to this process alone.
PLATFORM_TMP="$(cd "${TMPDIR:-/tmp}" && pwd -P)"
REAL_TMP="$PLATFORM_TMP/spanwit-agent-tmp-$$/T"
if [[ -e "$REAL_TMP" ]]; then
    printf 'FAIL fixture parent already exists, refusing to reuse it: %s\n' "$REAL_TMP"
    exit 1
fi
mkdir -p "$REAL_TMP"
REAL_SESSION_HOME="$REAL_TMP/claude-$(id -u)"

cleanup() {
    chmod -R u+rwx "$FIXROOT" 2> /dev/null || true
    rm -rf "$FIXROOT"
    # Only the unique parent this run created, never the ambient namespace.
    rm -rf "$PLATFORM_TMP/spanwit-agent-tmp-$$"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# 1. Root-class admission
# ---------------------------------------------------------------------------
block_root_class() {
    PASS=0
    FAIL=0
    SKIP=0
    # shellcheck source=./reclaim-lib.sh
    source "$LIB"

    # reclaim_die exits; run each admission in a subshell and read the code.
    admit() { # <class> <path>
        (
            RECLAIM_FORMAT=json
            reclaim_admit_root_class "$1" "$2" > /dev/null 2>&1
        )
        printf '%s\n' "$?"
    }

    sticky="$FIXROOT/sticky"
    mkdir -p "$sticky" && chmod 1777 "$sticky"
    plain="$FIXROOT/plain"
    mkdir -p "$plain" && chmod 0777 "$plain"
    private="$FIXROOT/private"
    mkdir -p "$private" && chmod 0700 "$private"
    loose="$FIXROOT/loose"
    mkdir -p "$loose" && chmod 0755 "$loose"

    assert_eq "shared-temp admits a sticky root" "$(admit shared-temp "$sticky")" "0"
    assert_eq "shared-temp refuses a non-sticky root" "$(admit shared-temp "$plain")" "65"
    assert_eq "user-temp admits a 0700 root" "$(admit user-temp "$private")" "0"
    assert_eq "user-temp refuses a group/other-readable root" "$(admit user-temp "$loose")" "65"
    assert_eq "unknown class is refused" "$(admit not-a-class "$sticky")" "65"

    # home-app-state must not regress: the original rule still refuses a root
    # outside $HOME, which is exactly what the temp plane is.
    assert_eq "home-app-state still refuses a root outside \$HOME" \
        "$(admit home-app-state "$sticky")" "65"
    assert_eq "home-app-state still refuses the real temp plane" \
        "$(admit home-app-state /private/tmp)" "65"
    block_end
}
run_block "root-class block" block_root_class

# ---------------------------------------------------------------------------
# 2. Per-candidate authorization
# ---------------------------------------------------------------------------
block_candidate_auth() {
    PASS=0
    FAIL=0
    SKIP=0
    # shellcheck source=./reclaim-lib.sh
    source "$LIB"

    root="$FIXROOT/cand"
    mkdir -p "$root" && chmod 1777 "$root"
    RECLAIM_FORMAT=json
    reclaim_admit_root_class shared-temp "$root" > /dev/null 2>&1

    mine="$RECLAIM_ROOT/mine.test"
    : > "$mine"
    subdir="$RECLAIM_ROOT/sub"
    mkdir -p "$subdir"
    link="$RECLAIM_ROOT/link.test"
    ln -s "$mine" "$link"

    auth() { reclaim_authorize_candidate "$1" "$2" > /dev/null 2>&1; }

    if auth "$mine" file; then ok "own regular file authorizes"; else no "own regular file authorizes"; fi
    if auth "$link" file; then no "symlink candidate refused"; else ok "symlink candidate refused"; fi
    if auth "$subdir" file; then no "type mismatch refused (dir as file)"; else ok "type mismatch refused (dir as file)"; fi
    if auth "$subdir" dir; then ok "directory authorizes as dir"; else no "directory authorizes as dir"; fi
    if auth "$RECLAIM_ROOT" dir; then no "the admitted root itself is refused"; else ok "the admitted root itself is refused"; fi
    if auth "$FIXROOT/outside.test" file; then no "path outside the root is refused"; else ok "path outside the root is refused"; fi
    if auth "$RECLAIM_ROOT/../escape" file; then no "dot-segment path is refused"; else ok "dot-segment path is refused"; fi
    if auth "$RECLAIM_ROOT/gone.test" file; then no "vanished candidate is refused"; else ok "vanished candidate is refused"; fi
    if auth "relative.test" file; then no "relative path is refused"; else ok "relative path is refused"; fi

    # Another user's file: the case shared-temp exists for. Only testable when
    # the host actually has one; a skip states that rather than passing quietly.
    other=""
    while IFS= read -r f; do
        [[ -n "$f" ]] && other="$f" && break
    done < <(find /private/tmp -maxdepth 1 -type f ! -user "$(id -u)" 2> /dev/null | head -1)

    if [[ -n "$other" ]]; then
        root2="$FIXROOT/other"
        mkdir -p "$root2" && chmod 1777 "$root2"
        # Authorize against the real temp plane, where the foreign file lives.
        RECLAIM_ROOT="/private/tmp"
        RECLAIM_ROOT_DEV=$(stat -f '%d' /private/tmp 2> /dev/null || stat -c '%d' /private/tmp 2> /dev/null)
        if reclaim_authorize_candidate "$other" file > /dev/null 2>&1; then
            no "another user's file is refused"
        else
            ok "another user's file is refused"
        fi
    else
        skip "another user's file is refused" "no foreign-owned file in /private/tmp on this host"
    fi
    block_end
}
run_block "candidate-authorization block" block_candidate_auth

# ---------------------------------------------------------------------------
# 3. CLI behaviour against a sticky fixture root
# ---------------------------------------------------------------------------
CLIROOT="$FIXROOT/cli"
mkdir -p "$CLIROOT" && chmod 1777 "$CLIROOT"

# Two stale test binaries and one fresh one; the fresh one must not be a
# candidate at the default age.
: > "$CLIROOT/alpha.test"
: > "$CLIROOT/beta-review-bin"
: > "$CLIROOT/fresh.test"
touch -t 202001010000 "$CLIROOT/alpha.test" "$CLIROOT/beta-review-bin"

# A session-shaped tree carrying a UUID component.
SESSION_ID="e20ed074-ecf6-423a-b71d-8ad86f448897"
mkdir -p "$CLIROOT/claude-501/$SESSION_ID/scratchpad"
: > "$CLIROOT/claude-501/$SESSION_ID/scratchpad/held.test"
touch -t 202001010000 "$CLIROOT/claude-501/$SESSION_ID/scratchpad/held.test"

# The suite may itself be running inside a live agent seat, whose session id is
# in the ambient environment. Clearing it makes the exclusion tests measure what
# they set, not what the host happens to export; the cases that exercise
# environment pickup set it explicitly.
run() { env -u CLAUDE_CODE_SESSION_ID TMP_RESIDUE_ROOT="$CLIROOT" "$TOOL" "$@" 2>&1; }
run_with_env_id() { TMP_RESIDUE_ROOT="$CLIROOT" CLAUDE_CODE_SESSION_ID="$1" "$TOOL" "${@:2}" 2>&1; }

out=$(run)
assert_contains "plan admits the sticky fixture root" "$out" "status=complete root=$CLIROOT"
assert_contains "plan finds the stale binaries" "$out" "phase=go-test-binaries candidates=2"
assert_contains "session plane appears in the overview" "$out" "phase=agent-scratch"

# The fresh file must be excluded by age, not merely absent from the count.
out=$(run --older-than 1d)
assert_contains "fresh candidate excluded by age" "$out" "phase=go-test-binaries candidates=2"

# Acknowledgement binding.
out=$(run --phase go-test-binaries --execute)
assert_contains "execute without ack refuses" "$out" "needs --confirm-delete-files 2"
assert_contains "execute without ack exits usage" "$out" "exit_code=64"

out=$(run --phase go-test-binaries --execute --confirm-delete-files 1)
assert_contains "ack that disagrees with the plan refuses" "$out" "!= candidate count 2"

# Environment self-exclusion: with the id in the environment, the session tree
# is excluded; the id must be reported so the filtering is auditable.
out=$(run_with_env_id "$SESSION_ID")
assert_contains "environment id is counted as an exclusion" "$out" "phase=exclusions count=1"

# A malformed environment id must not count as a known id.
out=$(run_with_env_id "not-a-uuid")
assert_contains "malformed environment id is not counted" "$out" "phase=exclusions count=0"
assert_contains "malformed id leaves auto self-id unknown" "$out" "check=auto-self-id-known required=0 outcome=unknown"

# Explicit ids union with environment ids.
out=$(run_with_env_id "$SESSION_ID" --exclude-session-id "11111111-2222-3333-4444-555555555555")
assert_contains "explicit and environment ids union" "$out" "phase=exclusions count=2"

# Exact-component matching: an id that is a prefix of the real one must not
# match it, or a substring rule would protect or expose the wrong tree.
out=$(run --exclude-session-id "e20ed074-ecf6-423a-b71d-000000000000")
assert_contains "a different id does not match by prefix" "$out" "phase=exclusions count=1"

# The real execute path, on the fixture only.
out=$(run --phase go-test-binaries --execute --confirm-delete-files 2)
assert_contains "execute deletes the acknowledged candidates" "$out" "deleted=2"
assert_contains "execute reports what it reclaimed" "$out" "status=complete"
if [[ -e "$CLIROOT/alpha.test" ]]; then no "stale candidate removed"; else ok "stale candidate removed"; fi
if [[ -e "$CLIROOT/fresh.test" ]]; then ok "fresh file survived"; else no "fresh file survived"; fi
if [[ -d "$CLIROOT/claude-501/$SESSION_ID" ]]; then
    ok "session tree untouched by the file phase"
else
    no "session tree untouched by the file phase"
fi
if [[ -d "$CLIROOT" ]]; then ok "the root itself survived"; else no "the root itself survived"; fi

# ---------------------------------------------------------------------------
# 3b. Session phase
# ---------------------------------------------------------------------------
# The session phase runs against the per-user agent root, not the whole plane,
# so the fixture mirrors that shape: <root>/claude-<uid>/<project>/<uuid>.
SROOT="$FIXROOT/sess"
mkdir -p "$SROOT" && chmod 1777 "$SROOT"
SESS_HOME="$SROOT/claude-$(id -u)"
mkdir -p "$SESS_HOME" && chmod 0700 "$SESS_HOME"

STALE_ID="aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
KEEP_ID="11111111-2222-3333-4444-555555555555"
mkdir -p "$SESS_HOME/-Users-someone-proj/$STALE_ID/scratchpad"
mkdir -p "$SESS_HOME/-Users-someone-proj/$KEEP_ID/scratchpad"
# A directory at session depth whose name is not a UUID must never be a
# candidate: depth alone is not the filter.
mkdir -p "$SESS_HOME/-Users-someone-proj/not-a-session"
: > "$SESS_HOME/-Users-someone-proj/$STALE_ID/scratchpad/work.txt"
: > "$SESS_HOME/-Users-someone-proj/$STALE_ID/scratchpad/.env.local"
: > "$SESS_HOME/-Users-someone-proj/$KEEP_ID/scratchpad/work.txt"
: > "$SESS_HOME/-Users-someone-proj/not-a-session/work.txt"
# Files first, then directories: creating a file updates its parent's mtime, so
# the directories have to be stamped last or they would still look fresh. The
# age rule is the newest mtime found anywhere inside, which is the whole point —
# a tree written into an hour ago is not abandoned however old its root looks.
touch -t 202001010000 \
    "$SESS_HOME/-Users-someone-proj/$STALE_ID/scratchpad/work.txt" \
    "$SESS_HOME/-Users-someone-proj/$STALE_ID/scratchpad/.env.local" \
    "$SESS_HOME/-Users-someone-proj/$KEEP_ID/scratchpad/work.txt" \
    "$SESS_HOME/-Users-someone-proj/not-a-session/work.txt"
find "$SESS_HOME" -type d -exec touch -t 202001010000 {} +

# The fixture parent is not one of the platform temp roots, so the session
# phase refuses it in production. Fixtures go through the declared test seam
# rather than through production authorization, and its use is visible in the
# record.
# Optional trailing args: some cases pass flags, others plan with none.
# shellcheck disable=SC2120
srun() {
    env -u CLAUDE_CODE_SESSION_ID TMP_RESIDUE_ROOT="$SROOT" \
        TMP_RESIDUE_TEST_TEMP_ROOT="$SROOT" "$TOOL" --phase agent-scratch "$@" 2>&1
}
srun_env() {
    TMP_RESIDUE_ROOT="$SROOT" TMP_RESIDUE_TEST_TEMP_ROOT="$SROOT" \
        CLAUDE_CODE_SESSION_ID="$1" "$TOOL" --phase agent-scratch "${@:2}" 2>&1
}
# Without the seam, an owned 0700 lookalike outside the temp plane is refused.
assert_exit "out-of-temp session root is refused" 65 \
    env -u CLAUDE_CODE_SESSION_ID TMP_RESIDUE_ROOT="$SROOT" "$TOOL" --phase agent-scratch
# The fixture seam widens the approved set for planning and must never be able
# to authorize a removal.
assert_exit "the test seam cannot execute" 65 \
    env -u CLAUDE_CODE_SESSION_ID TMP_RESIDUE_ROOT="$SROOT" \
    TMP_RESIDUE_TEST_TEMP_ROOT="$SROOT" "$TOOL" --phase agent-scratch --execute \
    --accept-incomplete-live-set --confirm-delete-sessions 2 --confirm-membership x
# A caller-controlled TMPDIR is not evidence about where temp is.
assert_exit "TMPDIR pointing outside the platform shape is refused" 65 \
    env -u CLAUDE_CODE_SESSION_ID TMPDIR="$FIXROOT" TMP_RESIDUE_ROOT="$FIXROOT" \
    "$TOOL" --phase agent-scratch

# shellcheck disable=SC2119
out=$(srun)
assert_contains "session phase admits the per-user agent root" "$out" "root=$SESS_HOME"
assert_contains "session phase finds both stale sessions" "$out" "candidates=2"
assert_contains "non-UUID directory is not a candidate" "$out" "candidates=2"
assert_contains "credential-shaped names are annotated" "$out" "caution_names=1"
assert_contains "plan marks its paths as source structure" "$out" "disclosure=source_structure"
assert_contains "plan lists the candidate paths for review" "$out" "[item] phase=agent-scratch path="
assert_contains "plan publishes a membership digest" "$out" "membership="
# Coverage accounting: a candidate count without its denominator is a
# projection presented as a scope.
assert_contains "plan states its coverage basis" "$out" "basis=confirmed"
assert_contains "plan reports what it considered" "$out" "volume_expected=2"
assert_contains "plan names its volume unit" "$out" "volume_unit=sessions"
assert_contains "plan states the next operation" "$out" "next_required_operation="

# An unreadable subtree must lower the basis rather than vanish from the count.
UNEVAL_ID="beefbeef-1111-2222-3333-444444444444"
mkdir -p "$SESS_HOME/-Users-someone-proj/$UNEVAL_ID/locked"
: > "$SESS_HOME/-Users-someone-proj/$UNEVAL_ID/locked/x"
find "$SESS_HOME/-Users-someone-proj/$UNEVAL_ID" -exec touch -t 202001010000 {} + 2> /dev/null
chmod 000 "$SESS_HOME/-Users-someone-proj/$UNEVAL_ID/locked"
uneval=$(srun)
assert_contains "an unevaluable tree lowers the basis" "$uneval" "basis=inferred"
assert_contains "an unevaluable tree is counted, not dropped" "$uneval" "volume_unevaluable=1"
chmod 700 "$SESS_HOME/-Users-someone-proj/$UNEVAL_ID/locked"
rm -rf "$SESS_HOME/-Users-someone-proj/$UNEVAL_ID"
assert_contains "live-set-complete is reported as unknown, never passed" "$out" "check=live-set-complete required=0 outcome=unknown basis=inferred"
assert_contains "risk acceptance is required and unmet" "$out" "check=live-set-risk-accepted required=1 outcome=fail"

# The environment id must remove that session from the candidate set.
out=$(srun_env "$KEEP_ID")
assert_contains "environment id removes its own session" "$out" "candidates=1"
assert_contains "exclusion record separates auto from explicit" "$out" "auto_count=1 auto_tokens=11111111 explicit_count=0"

# Acknowledgement interplay is exercised on the approved root below, since the
# plan-only seam refuses --execute before any of it is reached.

# With all three bindings, on the approved root, and with one session protected
# by the environment. The seam cannot authorize a removal, so this uses the real
# per-user temp root and a project key this suite created.
EHOME="$REAL_SESSION_HOME"
mkdir -p "$EHOME" && chmod 0700 "$EHOME"
E_KEEP="eeeeeeee-1111-2222-3333-444444444444"
E_GO="ffffffff-1111-2222-3333-444444444444"
for id in "$E_KEEP" "$E_GO"; do
    mkdir -p "$EHOME/-Users-suite-proj/$id/scratchpad"
    : > "$EHOME/-Users-suite-proj/$id/scratchpad/work.txt"
done
find "$EHOME" -exec touch -t 202001010000 {} + 2> /dev/null

erun_sess() {
    TMPDIR="$REAL_TMP" TMP_RESIDUE_ROOT="$REAL_TMP" CLAUDE_CODE_SESSION_ID="$1" \
        "$TOOL" --phase agent-scratch "${@:2}" 2>&1
}

kplan=$(erun_sess "$E_KEEP")
assert_contains "the environment id protects its own session" "$kplan" "candidates=1"
KDIGEST=$(printf '%s\n' "$kplan" | sed -n 's/.*membership=\([0-9a-f]*\).*/\1/p' | head -1)
out=$(erun_sess "$E_KEEP" --execute --accept-incomplete-live-set \
    --confirm-delete-sessions 1 --confirm-membership "$KDIGEST")
assert_contains "all three bindings execute" "$out" "deleted=1"
if [[ -d "$EHOME/-Users-suite-proj/$E_KEEP" ]]; then
    ok "the excluded session survived"
else
    no "the excluded session survived"
fi
if [[ -d "$EHOME/-Users-suite-proj/$E_GO" ]]; then
    no "the acknowledged session was removed"
else
    ok "the acknowledged session was removed"
fi
rm -rf "$EHOME/-Users-suite-proj"

# --- membership binding -----------------------------------------------------
# Runs against the real per-user temp root, because the plan-only seam cannot
# authorize a removal and these cases are about removal. The fixture is a
# project key this suite invents, so nothing pre-existing is in scope.
MHOME="$REAL_SESSION_HOME"
mkdir -p "$MHOME" && chmod 0700 "$MHOME"
M_A="aaaaaaaa-1111-2222-3333-444444444444"
M_B="bbbbbbbb-1111-2222-3333-444444444444"
M_C="cccccccc-1111-2222-3333-444444444444"
for id in "$M_A" "$M_B"; do
    mkdir -p "$MHOME/-Users-someone-proj/$id/scratchpad"
    : > "$MHOME/-Users-someone-proj/$id/scratchpad/work.txt"
done
find "$MHOME" -exec touch -t 202001010000 {} + 2> /dev/null

mrun() {
    env -u CLAUDE_CODE_SESSION_ID TMPDIR="$REAL_TMP" TMP_RESIDUE_ROOT="$REAL_TMP" \
        "$TOOL" --phase agent-scratch "$@" 2>&1
}

plan=$(mrun)
DIGEST=$(printf '%s\n' "$plan" | sed -n 's/.*membership=\([0-9a-f]*\).*/\1/p' | head -1)
if [[ -n "$DIGEST" ]]; then ok "a digest was published"; else no "a digest was published"; fi
assert_contains "the membership fixture has two candidates" "$plan" "candidates=2"

out=$(mrun --execute --accept-incomplete-live-set --confirm-delete-sessions 2)
assert_contains "count without membership is refused" "$out" "needs --confirm-membership"

out=$(mrun --execute --accept-incomplete-live-set --confirm-delete-sessions 2 --confirm-membership deadbeefdeadbeef)
assert_contains "a wrong digest is refused" "$out" "!= this run's set"

# Same-count substitution: swap one session for another so the count is
# unchanged and the set is not. Binding to a number alone permits this, and on
# a live host the set genuinely does move between one plan and the next.
mkdir -p "$MHOME/-Users-someone-proj/$M_C/scratchpad"
: > "$MHOME/-Users-someone-proj/$M_C/scratchpad/work.txt"
rm -rf "$MHOME/-Users-someone-proj/$M_B"
find "$MHOME" -exec touch -t 202001010000 {} + 2> /dev/null

after=$(mrun)
assert_contains "the swapped set still has the same count" "$after" "candidates=2"
out=$(mrun --execute --accept-incomplete-live-set --confirm-delete-sessions 2 --confirm-membership "$DIGEST")
assert_contains "a same-count membership swap is refused" "$out" "!= this run's set"
if [[ -d "$MHOME/-Users-someone-proj/$M_C" ]]; then
    ok "nothing was deleted on a membership mismatch"
else
    no "nothing was deleted on a membership mismatch"
fi

# The current digest does authorize the current set.
NOW_DIGEST=$(printf '%s\n' "$after" | sed -n 's/.*membership=\([0-9a-f]*\).*/\1/p' | head -1)
out=$(mrun --execute --accept-incomplete-live-set --confirm-delete-sessions 2 --confirm-membership "$NOW_DIGEST")
assert_contains "the matching digest authorizes" "$out" "deleted=2"

# Mutation-time eligibility, tested directly. Across two invocations a
# freshened candidate is caught at collection or by the digest; the
# revalidation exists for the window *inside* one run, between freezing the set
# and reaching a given path in the delete loop, which only a direct call can
# reach.
#
# Its own fixture, named explicitly: the previous version reached for an
# identifier defined in a different block, which under `set -u` aborted the
# subshell and removed these assertions from the run without failing it.
ELIG_ID="dddddddd-1111-2222-3333-444444444444"
ELIG_TARGET="$SESS_HOME/-Users-someone-proj/$ELIG_ID"
mkdir -p "$ELIG_TARGET/scratchpad"
: > "$ELIG_TARGET/scratchpad/work.txt"
find "$ELIG_TARGET" -exec touch -t 202001010000 {} + 2> /dev/null

elig_out=$(
    # Deliberately local to this subshell: the seam must not leak into the
    # rest of the suite.
    # shellcheck disable=SC2030,SC2031
    export TMP_RESIDUE_LIB_ONLY=1
    # shellcheck source=./agent-tmp-residue.sh
    source "$TOOL"
    RECLAIM_ROOT="$SESS_HOME"
    RECLAIM_ROOT_DEV=$(stat -f '%d' "$SESS_HOME" 2> /dev/null || stat -c '%d' "$SESS_HOME" 2> /dev/null)
    cutoff=$(($(date +%s) - 172800))

    if _candidate_still_eligible "$ELIG_TARGET" "$cutoff"; then
        printf 'stale-eligible=yes\n'
    else
        printf 'stale-eligible=no:%s\n' "$RECLAIM_ERR"
    fi

    touch "$ELIG_TARGET/scratchpad/work.txt"
    if _candidate_still_eligible "$ELIG_TARGET" "$cutoff"; then
        printf 'freshened=allowed\n'
    else
        printf 'freshened=refused:%s\n' "$RECLAIM_ERR"
    fi

    find "$ELIG_TARGET" -exec touch -t 202001010000 {} + 2> /dev/null
    EXCLUDE_IDS="$ELIG_ID"
    if _candidate_still_eligible "$ELIG_TARGET" "$cutoff"; then
        printf 'late-exclusion=allowed\n'
    else
        printf 'late-exclusion=refused:%s\n' "$RECLAIM_ERR"
    fi
)
assert_contains "a stale candidate is eligible" "$elig_out" "stale-eligible=yes"
assert_contains "a freshened candidate is refused at mutation time" "$elig_out" "freshened=refused"
assert_contains "a freshened candidate names why" "$elig_out" "written to since the plan"
assert_contains "an id excluded after planning is refused" "$elig_out" "late-exclusion=refused"
assert_contains "a late exclusion names why" "$elig_out" "excluded by session id"

# Live-hold probing must treat an unusable probe as unknown, not as "clear".
# A PATH with the ordinary utilities but neither lsof nor ps: the probe must
# answer "unknown", not "clear". Emptying PATH entirely would break mktemp and
# test nothing about the probe.
NOPROBE="$FIXROOT/noprobe"
mkdir -p "$NOPROBE"
for util in mktemp rm grep find stat date touch; do
    src=$(command -v "$util" 2> /dev/null) && ln -sf "$src" "$NOPROBE/$util"
done
hold_out=$(
    # Deliberately local to this subshell: the seam must not leak into the
    # rest of the suite.
    # shellcheck disable=SC2030,SC2031
    export TMP_RESIDUE_LIB_ONLY=1
    # shellcheck source=./agent-tmp-residue.sh
    source "$TOOL"
    PATH="$NOPROBE"
    # The tool is sourced with its own `set -e`, and a probe answering
    # "unknown" is a non-zero return, so the status has to be taken without
    # letting it terminate the block.
    set +e
    _probe_live_hold "$ELIG_TARGET"
    verdict=$?
    set -e
    printf 'verdict=%s\n' "$verdict"
)
assert_contains "an unprobeable candidate is unknown, not clear" "$hold_out" "verdict=2"

# Each probe covers a different dimension — lsof sees open files and working
# directories, ps sees argv — so one of them answering clear says nothing about
# the other. Every combination where a dimension is unavailable must be unknown.
probe_with() { # <name> <utils-to-provide...>
    local name="$1"
    shift
    local dir="$FIXROOT/probe-$name"
    mkdir -p "$dir"
    local u src
    for u in mktemp rm grep find stat date touch "$@"; do
        src=$(command -v "$u" 2> /dev/null) && ln -sf "$src" "$dir/$u"
    done
    (
        # Deliberately local to this subshell.
        # shellcheck disable=SC2030,SC2031
        export TMP_RESIDUE_LIB_ONLY=1
        # shellcheck source=./agent-tmp-residue.sh
        source "$TOOL"
        PATH="$dir"
        set +e
        _probe_live_hold "$ELIG_TARGET"
        printf 'verdict=%s\n' "$?"
    )
}
assert_contains "ps present but lsof missing is unknown" "$(probe_with psonly ps)" "verdict=2"
assert_contains "lsof present but ps missing is unknown" "$(probe_with lsofonly lsof)" "verdict=2"
assert_contains "both probes present can answer clear" "$(probe_with both lsof ps)" "verdict=1"

# Both blocks above run in subshells; if either died early its output would be
# short and its assertions would simply be missing from the count.
[[ "$elig_out" == *"late-exclusion="* ]] || note_unexpected "eligibility block did not reach its last case"
[[ "$hold_out" == *"verdict="* ]] || note_unexpected "live-hold block did not reach its last case"

rm -rf "$ELIG_TARGET"

# An unreadable subtree is unknown, and unknown is not stale.
UNREAD_ID="77777777-6666-5555-4444-333333333333"
mkdir -p "$SESS_HOME/-Users-someone-proj/$UNREAD_ID/locked"
: > "$SESS_HOME/-Users-someone-proj/$UNREAD_ID/locked/x.txt"
find "$SESS_HOME/-Users-someone-proj/$UNREAD_ID" -exec touch -t 202001010000 {} + 2> /dev/null
chmod 000 "$SESS_HOME/-Users-someone-proj/$UNREAD_ID/locked"
# shellcheck disable=SC2119
out=$(srun)
assert_not_contains "an unreadable subtree is not a candidate" "$out" "$UNREAD_ID"
chmod 700 "$SESS_HOME/-Users-someone-proj/$UNREAD_ID/locked"

# On an approved root with nothing acknowledged, the risk gate blocks. (With
# the plan-only seam the run is refused earlier, at admission, which is tested
# separately above.)
mkdir -p "$REAL_SESSION_HOME/-Users-blocked-proj/abcdef01-1111-2222-3333-444444444444"
find "$REAL_SESSION_HOME" -exec touch -t 202001010000 {} + 2> /dev/null
assert_exit "session execute without acks exits blocked" 75 \
    env -u CLAUDE_CODE_SESSION_ID TMPDIR="$REAL_TMP" TMP_RESIDUE_ROOT="$REAL_TMP" \
    "$TOOL" --phase agent-scratch --execute
rm -rf "$REAL_SESSION_HOME/-Users-blocked-proj"

# ---------------------------------------------------------------------------
# 3c. Emitted records conform to the published shape
# ---------------------------------------------------------------------------
# The script does not validate its own output. Doing so would make a schema
# tool a hard runtime dependency of a crisis-time reclaim script and would
# break the fail-open rule that telemetry must never obstruct the work it
# describes. goneat meta-validates the schema itself at build time; binding the
# *records* to it is this suite's job.
#
# When the validator is unavailable the check is skipped with a reason rather
# than passing quietly — an unrun check is not a clean one.
SCHEMA="$TESTDIR/../../config/schema/spanwit-reclaim-ops.v0.schema.json"
if ! command -v goneat > /dev/null 2>&1; then
    skip "emitted records validate against the schema" "goneat not on PATH"
elif [[ ! -f "$SCHEMA" ]]; then
    skip "emitted records validate against the schema" "schema not found at $SCHEMA"
else
    RECDIR="$FIXROOT/records"
    mkdir -p "$RECDIR"

    validate_line() { # <json-line> -> 0 valid
        printf '%s' "$1" > "$RECDIR/one.json"
        GONEAT_OFFLINE_SCHEMA_VALIDATION=true goneat schema validate-data \
            --format json --schema-file "$SCHEMA" --data "$RECDIR/one.json" 2>&1 |
            grep -q '"valid": true'
    }

    # Each invocation is captured separately, with its real exit status. A
    # concatenated file cannot show that a given run opened with exactly one
    # header, closed with exactly one terminal, and that the terminal's
    # exit_code equalled the status the process actually returned — and those
    # are the properties a consumer relies on to know a run is complete.
    run_seq=0
    conform_bad=0
    conform_kinds=""
    check_run() { # <label> <expected-exit> <cmd...>
        local label="$1" want="$2"
        shift 2
        run_seq=$((run_seq + 1))
        local out="$RECDIR/run-$run_seq.jsonl"
        "$@" > "$out" 2> /dev/null
        local real=$?

        if [[ "$real" != "$want" ]]; then
            no "conformance run '$label' exit status ($real, want $want)"
            return 0
        fi

        local first last count=0 line
        while IFS= read -r line; do
            [[ -n "$line" ]] || continue
            count=$((count + 1))
            [[ "$count" -eq 1 ]] && first="$line"
            last="$line"
            validate_line "$line" || {
                conform_bad=$((conform_bad + 1))
                printf '     invalid record: %s\n' "${line:0:150}"
            }
            conform_kinds="$conform_kinds $(printf '%s' "$line" | sed -n 's/.*"record":"\([a-z]*\)".*/\1/p')"
        done < "$out"

        if [[ "$count" -eq 0 ]]; then
            no "conformance run '$label' emitted records"
            return 0
        fi

        # Exactly one header, first; exactly one terminal, last.
        local headers terminals
        headers=$(grep -c '"record":"header"' "$out" || true)
        terminals=$(grep -c '"record":"terminal"' "$out" || true)
        assert_eq "run '$label' emits exactly one header" "$headers" "1"
        assert_eq "run '$label' emits exactly one terminal" "$terminals" "1"
        case "$first" in
            *'"record":"header"'*) ok "run '$label' opens with the header" ;;
            *) no "run '$label' opens with the header" ;;
        esac
        case "$last" in
            *'"record":"terminal"'*) ok "run '$label' closes with the terminal" ;;
            *) no "run '$label' closes with the terminal" ;;
        esac

        # The terminal's exit_code must equal the status the caller saw. A
        # record claiming 0 while the process returned 75 is the same
        # claim-not-bound-to-mechanism defect in the output format itself.
        local declared
        declared=$(printf '%s' "$last" | sed -n 's/.*"exit_code":\([0-9]*\).*/\1/p')
        assert_eq "run '$label' terminal exit_code matches the real status" "$declared" "$real"
    }

    check_run "file-phase plan" 0 \
        env -u CLAUDE_CODE_SESSION_ID TMP_RESIDUE_ROOT="$CLIROOT" "$TOOL" --format json
    check_run "session plan" 0 \
        env -u CLAUDE_CODE_SESSION_ID TMP_RESIDUE_ROOT="$SROOT" \
        TMP_RESIDUE_TEST_TEMP_ROOT="$SROOT" "$TOOL" --phase agent-scratch --format json
    check_run "blocked session execute" 75 \
        env -u CLAUDE_CODE_SESSION_ID TMPDIR="$REAL_TMP" TMP_RESIDUE_ROOT="$REAL_TMP" \
        "$TOOL" --phase agent-scratch --execute --format json
    check_run "usage refusal" 64 \
        env -u CLAUDE_CODE_SESSION_ID TMP_RESIDUE_ROOT="$CLIROOT" "$TOOL" --phase nonsense --format json

    assert_eq "every emitted record validates against the schema" "$conform_bad" "0"

    # A validation pass that only ever saw two kinds proves little about the
    # rest, so the kinds actually exercised are asserted by name.
    for kind in header admission inventory preflight exclusion item phase terminal; do
        case " $conform_kinds " in
            *" $kind "*) ok "record kind covered: $kind" ;;
            *) no "record kind covered: $kind (never emitted during validation)" ;;
        esac
    done

    # Negative fixtures: the schema must refuse records that contradict the
    # invariants it documents, or it is decoration.
    neg_reject() { # <name> <json>
        if validate_line "$2"; then
            no "schema refuses $1"
        else
            ok "schema refuses $1"
        fi
    }
    # Prove the conditional, not merely the field types: the neighboring
    # inferred records are valid, and changing only basis to confirmed makes
    # each one contradictory.
    if validate_line '{"contract":"spanwit.reclaim-ops/v0","record":"phase","phase":"x","status":"pending","basis":"inferred","method":"enumerated","volume_unit":"paths","volume_observed":0,"volume_expected":1,"volume_unevaluable":1,"volume_is_lower_bound":true}'; then
        ok "schema accepts inferred coverage with an unevaluable path"
    else
        no "schema accepts inferred coverage with an unevaluable path"
    fi
    neg_reject "confirmed coverage with an unevaluable path" \
        '{"contract":"spanwit.reclaim-ops/v0","record":"phase","phase":"x","status":"pending","basis":"confirmed","method":"enumerated","volume_unit":"paths","volume_observed":0,"volume_expected":1,"volume_unevaluable":1,"volume_is_lower_bound":true}'
    neg_reject "a partial removal bundle" \
        '{"contract":"spanwit.reclaim-ops/v0","record":"phase","phase":"x","status":"complete","delete_failed":1}'
    neg_reject "a successful record carrying an error class" \
        '{"contract":"spanwit.reclaim-ops/v0","record":"terminal","mode":"plan","status":"complete","exit_code":0,"error_class":"auth","error_kind":"admission"}'
    neg_reject "a header carrying removal fields" \
        '{"contract":"spanwit.reclaim-ops/v0","record":"header","profile":"a.b.c","mode":"plan","format":"json","root_requested":"/tmp","deleted":1,"exit_code":0}'
    if validate_line '{"contract":"spanwit.reclaim-ops/v0","record":"phase","phase":"x","status":"pending","basis":"inferred","method":"enumerated","volume_unit":"paths","volume_observed":1,"volume_expected":1,"volume_unevaluable":0,"volume_is_lower_bound":true}'; then
        ok "schema accepts an inferred lower-bound enumeration"
    else
        no "schema accepts an inferred lower-bound enumeration"
    fi
    neg_reject "an incomplete enumeration claiming confirmed" \
        '{"contract":"spanwit.reclaim-ops/v0","record":"phase","phase":"x","status":"pending","basis":"confirmed","method":"enumerated","volume_unit":"paths","volume_observed":1,"volume_expected":1,"volume_unevaluable":0,"volume_is_lower_bound":true}'
    # A mutation-time guard saving a candidate is the most useful "what
    # happened" path there is, and it must be representable: skipped is not
    # success, and its reason is machine evidence rather than decoration.
    if validate_line '{"contract":"spanwit.reclaim-ops/v0","record":"item","phase":"agent-scratch","path":"/tmp/x","status":"skipped","error_class":"integrity","error_kind":"no-longer-eligible","detail":"written to since the plan was taken"}'; then
        ok "schema accepts a skipped item carrying its reason"
    else
        no "schema accepts a skipped item carrying its reason"
    fi
    if validate_line '{"contract":"spanwit.reclaim-ops/v0","record":"item","phase":"agent-scratch","path":"/tmp/x","status":"failed","error_class":"auth","error_kind":"unlink-denied","detail":"rm exit 1"}'; then
        ok "schema accepts a failed item carrying its reason"
    else
        no "schema accepts a failed item carrying its reason"
    fi
    if validate_line '{"contract":"spanwit.reclaim-ops/v0","record":"item","phase":"agent-scratch","path":"/tmp/x","status":"pending","disclosure":"source_structure"}'; then
        ok "schema accepts a pending item"
    else
        no "schema accepts a pending item"
    fi

    neg_reject "an unknown field" \
        '{"contract":"spanwit.reclaim-ops/v0","record":"header","profile":"a.b.c","mode":"plan","format":"json","root_requested":"/tmp","invented":"x"}'
fi

# ---------------------------------------------------------------------------
# 3d. Adversarial runtime fixtures for the coverage defects
# ---------------------------------------------------------------------------
# These pin the two production bugs that motivated the enumeration and dedup
# work. Reproducing them by hand is review evidence; only a committed fixture is
# a regression gate.

# An unreadable parent hides whatever is below it. The candidate beneath it is
# never listed, so no per-path counter can notice the gap — the traversal's own
# failure is the only evidence there is.
UNREAD_TMP="$PLATFORM_TMP/spanwit-agent-unread-$$/T"
mkdir -p "$UNREAD_TMP/claude-$(id -u)/-proj-locked/aaaaaaaa-1111-2222-3333-444444444444/scratchpad"
mkdir -p "$UNREAD_TMP/claude-$(id -u)/-proj-open/bbbbbbbb-1111-2222-3333-444444444444/scratchpad"
: > "$UNREAD_TMP/claude-$(id -u)/-proj-open/bbbbbbbb-1111-2222-3333-444444444444/scratchpad/w.txt"
find "$UNREAD_TMP" -exec touch -t 202001010000 {} + 2> /dev/null
chmod 0700 "$UNREAD_TMP/claude-$(id -u)"
chmod 000 "$UNREAD_TMP/claude-$(id -u)/-proj-locked"

unread_out=$(env -u CLAUDE_CODE_SESSION_ID TMPDIR="$UNREAD_TMP" \
    TMP_RESIDUE_ROOT="$UNREAD_TMP" "$TOOL" --phase agent-scratch 2>&1)
assert_contains "an unreadable parent lowers the basis" "$unread_out" "basis=inferred"
assert_contains "an unreadable parent marks the volume a lower bound" "$unread_out" "volume_is_lower_bound=true"
assert_contains "an interrupted traversal is still an enumeration" "$unread_out" "method=enumerated"
chmod 700 "$UNREAD_TMP/claude-$(id -u)/-proj-locked" 2> /dev/null || true
rm -rf "$PLATFORM_TMP/spanwit-agent-unread-$$"

# Catalog patterns overlap by design. One directory matching two of them must be
# one candidate, or the acknowledgement binds to two rows for one path.
DEDUP_ROOT="$FIXROOT/dedup"
mkdir -p "$DEDUP_ROOT/both-gocache-go-cache" && chmod 1777 "$DEDUP_ROOT"
: > "$DEDUP_ROOT/both-gocache-go-cache/x"
find "$DEDUP_ROOT" -exec touch -t 202001010000 {} + 2> /dev/null
dedup_out=$(env -u CLAUDE_CODE_SESSION_ID TMP_RESIDUE_ROOT="$DEDUP_ROOT" \
    "$TOOL" --phase named-gocache 2>&1)
assert_contains "a name matching two patterns is one candidate" "$dedup_out" "candidates=1"
assert_contains "a name matching two patterns is counted once" "$dedup_out" "volume_observed=1"

# A size that cannot be read at mutation time must not become a reclaimed zero.
# Drive the phase function directly and fail reclaim_dir_bytes only when its
# immediate caller is _run_file_phase. Collection therefore admits one measured
# candidate; the later mutation-time probe alone becomes unknown. Making the
# tree unreadable before invocation would test collection failure instead and
# never reach removal.
SZ_ROOT="$FIXROOT/sizefail"
mkdir -p "$SZ_ROOT/gocache-a/inner" && chmod 1777 "$SZ_ROOT"
: > "$SZ_ROOT/gocache-a/inner/x"
find "$SZ_ROOT" -exec touch -t 202001010000 {} + 2> /dev/null
SZ_TARGET="$SZ_ROOT/gocache-a"
sz_out=$(
    # Deliberately local to this command-substitution fixture; it must not
    # suppress the real lifecycle anywhere else in the suite.
    # shellcheck disable=SC2030,SC2031
    export TMP_RESIDUE_LIB_ONLY=1
    # shellcheck source=./agent-tmp-residue.sh
    source "$TOOL"

    RECLAIM_ROOT="$SZ_ROOT"
    RECLAIM_ROOT_DEV=$(_reclaim_stat d "$SZ_ROOT")
    RECLAIM_MODE="execute"
    RECLAIM_FORMAT="json"
    RECLAIM_PHASE="named-gocache"
    CONFIRM_DELETE_FILES=1

    # Preserve the real implementation for collection. The wrapper fails only
    # for the direct mutation caller, giving this fixture one controlled fault
    # without adding a production authorization seam.
    eval "$(declare -f reclaim_dir_bytes | sed '1s/reclaim_dir_bytes/reclaim_dir_bytes_real/')"
    reclaim_dir_bytes() {
        if [[ "$1" == "$SZ_TARGET" && "${FUNCNAME[1]:-}" == "_run_file_phase" ]]; then
            return 1
        fi
        reclaim_dir_bytes_real "$@"
    }

    _run_file_phase named-gocache dir
)
assert_contains "mutation-size fixture admits one candidate" "$sz_out" '"candidates":1'
assert_contains "mutation-size fixture deletes that candidate" "$sz_out" '"deleted":1'
if [[ ! -e "$SZ_TARGET" ]]; then
    ok "mutation-size fixture removes the target"
else
    no "mutation-size fixture removes the target"
fi
assert_contains "mutation-size failure reports reclaimed zero" "$sz_out" '"bytes_reclaimed":0'
assert_contains "mutation-size failure reports one unmeasured removal" "$sz_out" '"bytes_unmeasured":1'

# ---------------------------------------------------------------------------
# 4. Process exit status on every path
# ---------------------------------------------------------------------------
EXITROOT="$FIXROOT/exits"
mkdir -p "$EXITROOT" && chmod 1777 "$EXITROOT"
: > "$EXITROOT/one.test"
: > "$EXITROOT/two.test"
touch -t 202001010000 "$EXITROOT/one.test" "$EXITROOT/two.test"

erun() { env -u CLAUDE_CODE_SESSION_ID TMP_RESIDUE_ROOT="$EXITROOT" "$TOOL" "$@"; }

assert_exit "help exits 0" 0 erun --help
assert_exit "plan exits 0" 0 erun
assert_exit "named plan exits 0" 0 erun --phase go-test-binaries
assert_exit "unknown phase exits usage" 64 erun --phase nonsense
assert_exit "bad age exits usage" 64 erun --older-than 7q
assert_exit "bad format exits usage" 64 erun --format bogus
assert_exit "unknown flag exits usage" 64 erun --not-a-flag
assert_exit "missing flag value exits usage" 64 erun --older-than
assert_exit "non-sticky root exits admission" 65 erun --root "$FIXROOT"
assert_exit "execute without ack exits usage" 64 erun --phase go-test-binaries --execute
assert_exit "execute with wrong count exits usage" 64 erun --phase go-test-binaries --execute --confirm-delete-files 1
assert_exit "seam-assisted execute is refused at admission" 65 \
    env -u CLAUDE_CODE_SESSION_ID TMP_RESIDUE_ROOT="$SROOT" \
    TMP_RESIDUE_TEST_TEMP_ROOT="$SROOT" "$TOOL" --phase agent-scratch --execute
assert_exit "execute with matching ack exits 0" 0 erun --phase go-test-binaries --execute --confirm-delete-files 2
assert_exit "a running build blocks the file phase" 75 \
    env -u CLAUDE_CODE_SESSION_ID SHIM_PGREP_MATCH=go TMP_RESIDUE_ROOT="$EXITROOT" \
    "$TOOL" --phase go-test-binaries --execute
# An empty candidate set is success, not failure: there was nothing to do and
# nothing went wrong. Pinned because it is the case that made the ack refusals
# look like they exited 0 when they had simply run out of work.
assert_exit "execute over an empty set exits 0" 0 erun --phase go-test-binaries --execute
assert_exit "plan over an empty set exits 0" 0 erun --phase go-test-binaries

# The tool must not leave its own scratch behind: it exists to remove such
# things. Counted across a full plan, in the directory it would land in.
leak_before=$(find "$PLATFORM_TMP" -maxdepth 1 -name 'tmp.*' 2> /dev/null | wc -l | tr -d ' ')
erun --phase agent-scratch > /dev/null 2>&1 || true
erun --phase go-test-binaries > /dev/null 2>&1 || true
leak_after=$(find "$PLATFORM_TMP" -maxdepth 1 -name 'tmp.*' 2> /dev/null | wc -l | tr -d ' ')
assert_eq "a plan leaves no reference files behind" "$leak_after" "$leak_before"

# Every block that runs assertions in a subshell reports its own count back, so
# a subshell that died before finishing cannot be mistaken for one that passed.
printf '\npassed=%d failed=%d skipped=%d unexpected=%d\n' "$PASS" "$FAIL" "$SKIP" "$UNEXPECTED"
if [[ "$EXPECTED_MIN" -gt 0 && $((PASS + FAIL + SKIP)) -lt "$EXPECTED_MIN" ]]; then
    printf 'FAIL only %d assertions ran; at least %d were expected (a block exited early)\n' \
        $((PASS + FAIL + SKIP)) "$EXPECTED_MIN"
    exit 1
fi
[[ "$FAIL" -eq 0 && "$UNEXPECTED" -eq 0 ]]

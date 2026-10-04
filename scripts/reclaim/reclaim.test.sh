#!/usr/bin/env bash
# reclaim.test.sh — dependency-free tests for the reclaim-ops harness.
#
# Three layers:
#   1. Unit tests that source reclaim-lib.sh directly and exercise the gate,
#      admission, destination admission, name grammar, typed emission, and
#      conformance logic — deterministic, independent of the host's processes.
#   2. Black-box CLI tests against a $HOME fixture for the integration paths
#      (admission exit codes, preflight verdicts, blocked execute, JSON stream).
#   3. Stubbed mutation tests: a PATH shim supplies `opencode` and `pgrep`, so
#      the real execute paths (quarantine rename, session export/delete, wrong
#      store refusal, containment) run end-to-end against fixtures without
#      touching real OpenCode data.
#
# Run: ./scripts/reclaim/reclaim.test.sh   (exit 0 = all passed)

# shellcheck source-path=SCRIPTDIR
set -uo pipefail

TESTDIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
LIB="$TESTDIR/reclaim-lib.sh"
TOOL="$TESTDIR/opencode-data.sh"

PASS=0
FAIL=0
ok() {
    PASS=$((PASS + 1))
    printf 'ok   %s\n' "$1"
}
no() {
    FAIL=$((FAIL + 1))
    printf 'FAIL %s\n' "$1"
}
assert_eq() { # <name> <got> <want>
    if [[ "$2" == "$3" ]]; then ok "$1"; else no "$1 (want=[$3] got=[$2])"; fi
}
assert_contains() { # <name> <haystack> <needle>
    if [[ "$2" == *"$3"* ]]; then ok "$1"; else no "$1 (missing [$3] in output)"; fi
}
assert_not_contains() { # <name> <haystack> <needle>
    if [[ "$2" != *"$3"* ]]; then ok "$1"; else no "$1 (unexpected [$3] in output)"; fi
}
assert_true() { # <name> <cmd...>
    local n="$1"
    shift
    if "$@" > /dev/null 2>&1; then ok "$n"; else no "$n"; fi
}
assert_false() { # <name> <cmd...>
    local n="$1"
    shift
    if "$@" > /dev/null 2>&1; then no "$n"; else ok "$n"; fi
}

# --- fixture --------------------------------------------------------------
FIXBASE="$(mktemp -d "$HOME/.reclaim-test.XXXXXX")"
FIXBASE="$(cd "$FIXBASE" && pwd -P)"
FIXROOT="$FIXBASE/opencode"
STUBBIN="$FIXBASE/stubbin"

mk_fixture() {
    rm -rf "$FIXROOT"
    mkdir -p "$FIXROOT/storage/message" "$FIXROOT/storage/part" "$FIXROOT/bin" "$FIXROOT/log"
    : > "$FIXROOT/auth.json"
    echo x > "$FIXROOT/storage/message/m1.json"
    echo x > "$FIXROOT/storage/part/p1.json"
    echo bin > "$FIXROOT/bin/tool"
    echo log > "$FIXROOT/log/a.log"
    mk_db
}

# A live SQLite store with two sessions: one old (delete candidate), one recent.
mk_db() {
    rm -f "$FIXROOT/opencode.db"
    local now old
    now=$(($(date +%s) * 1000))
    old=$((now - 400 * 86400 * 1000))
    sqlite3 "$FIXROOT/opencode.db" \
        "CREATE TABLE session (id TEXT PRIMARY KEY, time_updated INTEGER);
         INSERT INTO session VALUES ('ses_old1', $old);
         INSERT INTO session VALUES ('ses_new1', $now);"
}

freeze_fixture() { touch -t 202601010000 "$FIXROOT/storage/message/m1.json" "$FIXROOT/storage/part/p1.json"; }

# PATH shim: deterministic `pgrep` (nothing running) and a scriptable `opencode`.
mk_stubs() {
    mkdir -p "$STUBBIN"
    cat > "$STUBBIN/pgrep" << 'EOF'
#!/bin/sh
exit 1
EOF
    cat > "$STUBBIN/opencode" << 'EOF'
#!/bin/sh
case "$1" in
    debug)
        echo "data ${STUB_OC_DATA:-/nonexistent}"
        ;;
    export)
        [ "${STUB_OC_EXPORT_FAIL:-0}" = "1" ] && exit 1
        # race hook: signal that export has started, then linger so the test can
        # plant the final output path between admission and publication
        [ -n "${STUB_OC_EXPORT_MARKER:-}" ] && : > "$STUB_OC_EXPORT_MARKER"
        [ -n "${STUB_OC_EXPORT_DELAY:-}" ] && sleep "$STUB_OC_EXPORT_DELAY"
        printf '{"id":"%s"}\n' "$2"
        ;;
    session)
        [ -n "${STUB_OC_DELETE_LOG:-}" ] && echo "$3" >> "$STUB_OC_DELETE_LOG"
        [ "${STUB_OC_DELETE_FAIL:-0}" = "1" ] && exit 1
        exit 0
        ;;
    *) exit 1 ;;
esac
exit 0
EOF
    chmod +x "$STUBBIN/pgrep" "$STUBBIN/opencode"
}

cleanup() { rm -rf "$FIXBASE"; }
trap cleanup EXIT

mk_stubs

# =========================================================================
# Layer 1 — library unit tests
# =========================================================================
# shellcheck source=reclaim-lib.sh
source "$LIB"

# admission: rejects paths outside $HOME, $HOME itself, filesystem root
(reclaim_admit_root /tmp) > /dev/null 2>&1
assert_eq "admit rejects /tmp (outside \$HOME)" "$?" "$RECLAIM_EX_ADMISSION"
(reclaim_admit_root "$HOME") > /dev/null 2>&1
assert_eq "admit rejects \$HOME itself" "$?" "$RECLAIM_EX_ADMISSION"
(reclaim_admit_root /) > /dev/null 2>&1
assert_eq "admit rejects filesystem root" "$?" "$RECLAIM_EX_ADMISSION"
(reclaim_admit_root "$FIXBASE/does-not-exist") > /dev/null 2>&1
assert_eq "admit rejects missing dir" "$?" "$RECLAIM_EX_ADMISSION"

# admission: symlinked root / symlinked ancestor are refused (the admitted name
# and the mutated bytes must not be able to diverge)
mk_fixture
ln -s "$FIXROOT" "$FIXBASE/link-to-root"
(reclaim_admit_root "$FIXBASE/link-to-root") > /dev/null 2>&1
assert_eq "admit rejects a symlinked root" "$?" "$RECLAIM_EX_ADMISSION"
mkdir -p "$FIXBASE/realdir/child"
ln -s "$FIXBASE/realdir" "$FIXBASE/linkdir"
(reclaim_admit_root "$FIXBASE/linkdir/child") > /dev/null 2>&1
assert_eq "admit rejects a symlinked ancestor" "$?" "$RECLAIM_EX_ADMISSION"

assert_true "path_has_symlink: detects symlinked ancestor" reclaim_path_has_symlink "$FIXBASE/linkdir/child"
assert_false "path_has_symlink: clean path is clean" reclaim_path_has_symlink "$FIXROOT/storage"
assert_true "is_mount_root: / is a mount root" reclaim_is_mount_root /
assert_false "is_mount_root: fixture dir is not" reclaim_is_mount_root "$FIXROOT"

# name grammar (the guard on untrusted ids steering filesystem writes)
assert_true "safe name: plain token" reclaim_is_safe_name "ses_abc123"
assert_true "safe name: dot-directory allowed" reclaim_is_safe_name ".reclaim-quarantine"
assert_false "safe name: rejects separators" reclaim_is_safe_name "a/b"
assert_false "safe name: rejects parent segment" reclaim_is_safe_name ".."
assert_false "safe name: rejects traversal token" reclaim_is_safe_name "../../etc/passwd"
assert_false "safe name: rejects empty" reclaim_is_safe_name ""
assert_false "safe name: rejects whitespace" reclaim_is_safe_name "a b"

# admission: accepts a real owned dir under $HOME and sets state
mk_fixture
reclaim_admit_root "$FIXROOT"
assert_eq "admit sets RECLAIM_ROOT" "$RECLAIM_ROOT" "$FIXROOT"
assert_contains "admit sets auth path" "$RECLAIM_AUTH_PATH" "/auth.json"

# containment + auth disjointness (root now admitted)
(reclaim_assert_within "$RECLAIM_ROOT/storage") > /dev/null 2>&1
assert_eq "assert_within allows a child dir" "$?" "0"
(reclaim_assert_within "$RECLAIM_ROOT") > /dev/null 2>&1
assert_eq "assert_within refuses the root itself" "$?" "$RECLAIM_EX_ERROR"
(reclaim_assert_within "$RECLAIM_AUTH_PATH") > /dev/null 2>&1
assert_eq "assert_within refuses auth.json" "$?" "$RECLAIM_EX_ERROR"
(reclaim_assert_within /etc) > /dev/null 2>&1
assert_eq "assert_within refuses an escaping path" "$?" "$RECLAIM_EX_ERROR"
ln -s /etc "$FIXROOT/etclink"
(reclaim_assert_within "$FIXROOT/etclink") > /dev/null 2>&1
assert_eq "assert_within refuses a symlinked target" "$?" "$RECLAIM_EX_ERROR"

# destination admission: parent is admitted BEFORE anything is created, and the
# helper never dies (it returns non-zero so the caller owns the terminal record)
reclaim_admit_dest_within_root "$FIXROOT/.reclaim-quarantine/stamp1" > /dev/null 2>&1
assert_eq "admit_dest returns success for a contained destination" "$?" "0"
assert_eq "admit_dest reports the canonical path" "$RECLAIM_DEST" "$FIXROOT/.reclaim-quarantine/stamp1"
if [[ -d "$RECLAIM_DEST" ]]; then ok "admit_dest created the dir"; else no "admit_dest created the dir"; fi
perm=$(stat -f '%Lp' "$RECLAIM_DEST" 2> /dev/null || stat -c '%a' "$RECLAIM_DEST" 2> /dev/null)
assert_eq "admit_dest creates it 0700" "$perm" "700"

# exclusive creation: an existing destination is a collision, never a reuse
reclaim_admit_dest_within_root "$FIXROOT/.reclaim-quarantine/stamp1" > /dev/null 2>&1
assert_eq "admit_dest refuses an existing destination" "$?" "1"
assert_contains "admit_dest names the collision" "$RECLAIM_ERR" "already exists"

reclaim_admit_dest_within_root "$FIXBASE/outside/stamp" > /dev/null 2>&1
assert_eq "admit_dest refuses a destination outside the root" "$?" "1"
if [[ -e "$FIXBASE/outside" ]]; then
    no "admit_dest left nothing behind on refusal"
else
    ok "admit_dest left nothing behind on refusal"
fi
reclaim_admit_dest_within_root "$FIXROOT/../escape/stamp" > /dev/null 2>&1
assert_eq "admit_dest refuses a traversal destination" "$?" "1"

# export output admission: an existing output path is never truncated or removed
mkdir -p "$FIXBASE/exports-unit"
echo "PRIOR" > "$FIXBASE/exports-unit/ses_old1.json"
reclaim_admit_export_output "$FIXBASE/exports-unit" "ses_old1.json" > /dev/null 2>&1
assert_eq "admit_export_output refuses an existing output" "$?" "1"
ln -s /etc/hosts "$FIXBASE/exports-unit/ses_link.json"
reclaim_admit_export_output "$FIXBASE/exports-unit" "ses_link.json" > /dev/null 2>&1
assert_eq "admit_export_output refuses a symlinked output" "$?" "1"
reclaim_admit_export_output "$FIXBASE/exports-unit" "ses_fresh.json" > /dev/null 2>&1
assert_eq "admit_export_output accepts an absent output" "$?" "0"

# size measurement is evidence: a du failure must not read as a confirmed zero
mkdir -p "$FIXBASE/unreadable/sub"
chmod 000 "$FIXBASE/unreadable/sub"
reclaim_dir_bytes "$FIXBASE/unreadable" > /dev/null 2>&1
assert_eq "dir_bytes reports failure instead of a confirmed zero" "$?" "1"
chmod 755 "$FIXBASE/unreadable/sub"
assert_eq "dir_bytes returns 0 for a genuinely absent path" "$(reclaim_dir_bytes "$FIXBASE/nope")" "0"

# export destination policy: under $HOME, outside the data root, symlink-free
assert_true "export dest: sibling dir under \$HOME is acceptable" reclaim_export_dir_problem "$FIXBASE/exports"
assert_false "export dest: refuses inside the data root" reclaim_export_dir_problem "$FIXROOT/exports"
assert_false "export dest: refuses outside \$HOME" reclaim_export_dir_problem "/tmp/exports"
assert_false "export dest: refuses a symlinked path" reclaim_export_dir_problem "$FIXBASE/linkdir/exports"

# preflight gate: the core fail-closed invariant, with synthetic checks.
# The RECLAIM_* globals below are read back by the sourced harness through
# indirect calls shellcheck cannot trace, hence the scoped disables.
# shellcheck disable=SC2034
_ck_pass() {
    RECLAIM_CHECK_OUTCOME=pass
    RECLAIM_CHECK_BASIS=confirmed
}
# shellcheck disable=SC2034
_ck_fail() {
    RECLAIM_CHECK_OUTCOME=fail
    RECLAIM_CHECK_BASIS=confirmed
}
# shellcheck disable=SC2034
_ck_unknown() {
    RECLAIM_CHECK_OUTCOME=unknown
    RECLAIM_CHECK_BASIS=inferred
}
# shellcheck disable=SC2034
_ck_inferred_pass() {
    RECLAIM_CHECK_OUTCOME=pass
    RECLAIM_CHECK_BASIS=inferred
}
# shellcheck disable=SC2034
reset_registry() {
    RECLAIM_CHECK_NAMES=()
    RECLAIM_CHECK_FNS=()
    RECLAIM_CHECK_REQUIRED=()
    RECLAIM_CHECK_PHASES=()
}
run_gate() { # returns blocker count
    local b=0
    reclaim_run_preflight all > /dev/null 2>&1 || b=$?
    echo "$b"
}

reset_registry
reclaim_register_check a 1 '*' _ck_pass
assert_eq "gate: required pass+confirmed does not block" "$(run_gate)" "0"

reset_registry
reclaim_register_check a 1 '*' _ck_fail
assert_eq "gate: required fail blocks" "$(run_gate)" "1"

reset_registry
reclaim_register_check a 1 '*' _ck_unknown
assert_eq "gate: required unknown/inferred blocks (fail-closed)" "$(run_gate)" "1"

reset_registry
reclaim_register_check a 1 '*' _ck_inferred_pass
assert_eq "gate: required pass+inferred still blocks (needs confirmed)" "$(run_gate)" "1"

reset_registry
reclaim_register_check a 0 '*' _ck_fail
assert_eq "gate: non-required fail does not block" "$(run_gate)" "0"

reset_registry
reclaim_register_check a 1 '*' _ck_pass
reclaim_register_check b 1 '*' _ck_fail
reclaim_register_check c 1 '*' _ck_unknown
assert_eq "gate: counts every blocker" "$(run_gate)" "2"

# conformance: a profile missing hooks cannot run
reset_registry
# shellcheck disable=SC2034
RECLAIM_PROFILE_ID=""
# shellcheck disable=SC2034
RECLAIM_HOOK_ARGS=""
# shellcheck disable=SC2034
RECLAIM_HOOK_BIND=""
# shellcheck disable=SC2034
RECLAIM_HOOK_INVENTORY=""
# shellcheck disable=SC2034
RECLAIM_HOOK_PHASE=""
conf_out=$(reclaim_conformance_report)
conf_rc=$?
assert_eq "conformance: unconfigured profile is rejected" "$conf_rc" "1"
assert_contains "conformance: names the missing hook" "$conf_out" "missing args hook"
assert_contains "conformance: requires a 3-segment profile id" "$conf_out" "three dotted segments"
assert_contains "conformance: requires preflight checks" "$conf_out" "no preflight checks"

# emission: json is well-formed and scalars are typed, not stringified
RECLAIM_FORMAT=json
line="$(reclaim_emit sample k1 "value with \"quotes\" and	tab" k2 v2)"
typed="$(reclaim_emit sample bytes 4096 required 1 candidates 12 human 4.0KB)"
badint="$(reclaim_emit sample bytes "not-a-number")"
# shellcheck disable=SC2034
RECLAIM_FORMAT=text
printf '%s' "$line" | python3 -c 'import sys,json; json.loads(sys.stdin.read())' 2> /dev/null
assert_eq "emit: json record is valid JSON" "$?" "0"
assert_contains "emit: json carries contract" "$line" '"spanwit.reclaim-ops/v0"'
assert_contains "emit: bytes is an integer" "$typed" '"bytes":4096'
assert_contains "emit: required is a boolean" "$typed" '"required":true'
assert_contains "emit: counts are integers" "$typed" '"candidates":12'
assert_contains "emit: undeclared fields stay strings" "$typed" '"human":"4.0KB"'
assert_contains "emit: a non-numeric int field degrades to string" "$badint" '"bytes":"not-a-number"'

# =========================================================================
# Layer 2 — black-box CLI tests (no mutation)
# =========================================================================
run_tool() { "$TOOL" "$@" 2>&1; }
# stubbed run: deterministic pgrep + opencode bound to the fixture store
run_stub() { PATH="$STUBBIN:$PATH" STUB_OC_DATA="${STUB_OC_DATA:-$FIXROOT}" "$TOOL" "$@" 2>&1; }

# single-phase guard runs before admission — deterministic exit 64
run_tool --phase all --execute > /dev/null 2>&1
assert_eq "cli: --execute --phase all refused (usage)" "$?" "64"

# admission failure via CLI — exit 65
run_tool --data-dir /tmp > /dev/null 2>&1
assert_eq "cli: --data-dir /tmp refused (admission)" "$?" "65"

# an early failure is still a well-formed, projectable stream: header first,
# terminal last, never zero records
adm_json="$("$TOOL" --data-dir /tmp --format json 2> /dev/null)"
first_rec=$(printf '%s\n' "$adm_json" | head -n1)
last_rec=$(printf '%s\n' "$adm_json" | tail -n1)
assert_contains "cli: admission failure still emits header first" "$first_rec" '"record":"header"'
assert_contains "cli: admission failure emits a terminal record" "$last_rec" '"record":"terminal"'
assert_contains "cli: terminal carries a canonical error class" "$last_rec" '"error_class":"auth"'
assert_contains "cli: terminal carries the local error kind" "$last_rec" '"error_kind":"admission"'
assert_contains "cli: terminal carries the exit code as an integer" "$last_rec" '"exit_code":65'

# migration-evidence verdict is fixture-driven (independent of live processes)
mk_fixture
freeze_fixture
out="$(run_tool --data-dir "$FIXROOT" --phase legacy-json)"
assert_contains "cli: frozen store -> migration-evidence pass" "$out" "check=migration-evidence required=1 outcome=pass"
assert_contains "cli: populated live store -> live-store-populated pass" "$out" "check=live-store-populated required=1 outcome=pass"

mk_fixture # fresh mtimes (just created)
out="$(run_tool --data-dir "$FIXROOT" --phase legacy-json)"
assert_contains "cli: fresh store -> migration-evidence fail" "$out" "check=migration-evidence required=1 outcome=fail"

# a hollow live store blocks legacy-json even when the JSON tree is frozen
mk_fixture
freeze_fixture
sqlite3 "$FIXROOT/opencode.db" "DELETE FROM session;"
out="$(run_tool --data-dir "$FIXROOT" --phase legacy-json)"
assert_contains "cli: hollow live store blocks legacy-json" "$out" "check=live-store-populated required=1 outcome=fail"

# execute on a fresh store blocks (migration-evidence fails) and mutates nothing
mk_fixture
run_tool --data-dir "$FIXROOT" --phase legacy-json --execute > /dev/null 2>&1
rc=$?
assert_eq "cli: unsafe --execute blocks (exit 75)" "$rc" "75"
if [[ -e "$FIXROOT/.reclaim-quarantine" ]]; then
    no "cli: blocked --execute created no quarantine dir"
else
    ok "cli: blocked --execute created no quarantine dir"
fi
if [[ -d "$FIXROOT/storage/message" && -d "$FIXROOT/storage/part" ]]; then
    ok "cli: blocked --execute left legacy trees intact"
else
    no "cli: blocked --execute left legacy trees intact"
fi

# JSON stream over the fixture is entirely well-formed. Call the tool directly
# (not via run_tool, whose 2>&1 would merge stderr notes into the JSON stream):
# in --format json, stdout must be pure JSON and human notes stay on stderr.
mk_fixture
freeze_fixture
"$TOOL" --data-dir "$FIXROOT" --format json > "$FIXBASE/out.jsonl" 2> /dev/null
python3 - "$FIXBASE/out.jsonl" << 'PY'
import sys, json
bad = 0
for i, line in enumerate(open(sys.argv[1])):
    line = line.strip()
    if not line:
        continue
    try:
        json.loads(line)
    except Exception as e:
        bad += 1
        print("bad json line", i, e)
sys.exit(1 if bad else 0)
PY
assert_eq "cli: --format json emits only well-formed JSON" "$?" "0"

# inventory bytes are integers in the stream, not strings
inv_line=$(grep '"record":"inventory"' "$FIXBASE/out.jsonl" | head -n1)
assert_contains "cli: inventory bytes are typed integers" "$inv_line" '"bytes":'
assert_not_contains "cli: inventory bytes are not quoted" "$inv_line" '"bytes":"'

# =========================================================================
# Layer 3 — stubbed mutation tests (real execute paths, fixture data)
# =========================================================================

# wrong store: the CLI resolves a different root than the one we admitted, so
# session execute must refuse rather than delete from the live store
mk_fixture
STUB_OC_DATA="$FIXBASE/realdir" run_stub --data-dir "$FIXROOT" --phase sessions \
    --confirm-delete-sessions 1 --execute > "$FIXBASE/wrong.out" 2>&1
rc=$?
assert_eq "stub: wrong-store sessions --execute is blocked (75)" "$rc" "75"
assert_contains "stub: wrong-store names the mismatch" "$(cat "$FIXBASE/wrong.out")" "cli-store-binding required=1 outcome=fail"
assert_eq "stub: wrong-store deleted nothing" \
    "$(sqlite3 "$FIXROOT/opencode.db" 'SELECT COUNT(*) FROM session;')" "2"

# export destination inside the data root is refused by preflight
mk_fixture
run_stub --data-dir "$FIXROOT" --phase sessions --export-dir "$FIXROOT/exports" \
    --confirm-delete-sessions 1 --execute > "$FIXBASE/expin.out" 2>&1
assert_eq "stub: export dir inside the data root blocks execute" "$?" "75"
assert_contains "stub: export-destination check names the problem" \
    "$(cat "$FIXBASE/expin.out")" "check=export-destination required=1 outcome=fail"

# count ack must equal the candidate count
mk_fixture
run_stub --data-dir "$FIXROOT" --phase sessions --confirm-delete-sessions 7 --execute > "$FIXBASE/ack.out" 2>&1
assert_eq "stub: mismatched --confirm-delete-sessions refused (64)" "$?" "64"
assert_eq "stub: mismatched ack deleted nothing" \
    "$(sqlite3 "$FIXROOT/opencode.db" 'SELECT COUNT(*) FROM session;')" "2"

# missing ack entirely
mk_fixture
run_stub --data-dir "$FIXROOT" --phase sessions --execute > /dev/null 2>&1
assert_eq "stub: missing --confirm-delete-sessions refused (64)" "$?" "64"

# happy path: export succeeds, delete runs, files are 0600 under a 0700 dir
mk_fixture
run_stub --data-dir "$FIXROOT" --phase sessions --export-dir "$FIXBASE/exports" \
    --confirm-delete-sessions 1 --execute > "$FIXBASE/sess.out" 2>&1
rc=$?
assert_eq "stub: sessions --execute completes (0)" "$rc" "0"
assert_contains "stub: sessions reports one delete" "$(cat "$FIXBASE/sess.out")" "status=complete deleted=1"
if [[ -f "$FIXBASE/exports/ses_old1.json" ]]; then
    ok "stub: session was exported before delete"
else
    no "stub: session was exported before delete"
fi
perm=$(stat -f '%Lp' "$FIXBASE/exports/ses_old1.json" 2> /dev/null || stat -c '%a' "$FIXBASE/exports/ses_old1.json" 2> /dev/null)
assert_eq "stub: export file is 0600" "$perm" "600"
perm=$(stat -f '%Lp' "$FIXBASE/exports" 2> /dev/null || stat -c '%a' "$FIXBASE/exports" 2> /dev/null)
assert_eq "stub: export dir is 0700" "$perm" "700"

# export failure blocks that session's delete and fails the run
mk_fixture
rm -rf "$FIXBASE/exports"
STUB_OC_EXPORT_FAIL=1 run_stub --data-dir "$FIXROOT" --phase sessions \
    --export-dir "$FIXBASE/exports" --confirm-delete-sessions 1 --execute > "$FIXBASE/expfail.out" 2>&1
rc=$?
assert_eq "stub: export failure fails the run (70)" "$rc" "70"
assert_contains "stub: export failure blocks the delete" "$(cat "$FIXBASE/expfail.out")" "deleted=0 delete_failed=0 export_blocked=1"
if [[ -e "$FIXBASE/exports/ses_old1.json" ]]; then
    no "stub: failed export left no partial file"
else
    ok "stub: failed export left no partial file"
fi

# delete failure propagates to a non-zero exit (not a silent success)
mk_fixture
STUB_OC_DELETE_FAIL=1 run_stub --data-dir "$FIXROOT" --phase sessions \
    --confirm-delete-sessions 1 --execute > "$FIXBASE/delfail.out" 2>&1
rc=$?
assert_eq "stub: delete failure exits 70" "$rc" "70"
assert_contains "stub: delete failure is reported as failed" "$(cat "$FIXBASE/delfail.out")" "status=failed deleted=0 delete_failed=1"

# a session id that is not a plain token stops the run before any mutation
mk_fixture
sqlite3 "$FIXROOT/opencode.db" "UPDATE session SET id='../../escape' WHERE id='ses_old1';"
run_stub --data-dir "$FIXROOT" --phase sessions --export-dir "$FIXBASE/exports2" \
    --confirm-delete-sessions 1 --execute > "$FIXBASE/badid.out" 2>&1
rc=$?
assert_eq "stub: unsafe session id refuses the run (70)" "$rc" "70"
assert_contains "stub: unsafe session id is named" "$(cat "$FIXBASE/badid.out")" "not a safe token"
if [[ -e "$FIXBASE/exports2/../../escape.json" || -e "$FIXBASE/escape.json" ]]; then
    no "stub: unsafe session id wrote nothing outside the export dir"
else
    ok "stub: unsafe session id wrote nothing outside the export dir"
fi

# legacy-json happy path: same-device rename into a 0700 quarantine, frees 0
mk_fixture
freeze_fixture
run_stub --data-dir "$FIXROOT" --phase legacy-json --execute > "$FIXBASE/quar.out" 2>&1
rc=$?
assert_eq "stub: legacy-json --execute completes (0)" "$rc" "0"
assert_contains "stub: quarantine is honest about freeing nothing" "$(cat "$FIXBASE/quar.out")" "0 bytes until you delete"
if [[ -d "$FIXROOT/storage/message" || -d "$FIXROOT/storage/part" ]]; then
    no "stub: legacy trees were moved out of storage/"
else
    ok "stub: legacy trees were moved out of storage/"
fi
qdir=$(find "$FIXROOT/.reclaim-quarantine" -mindepth 1 -maxdepth 1 -type d 2> /dev/null | head -n1)
if [[ -f "$qdir/message/m1.json" && -f "$qdir/part/p1.json" ]]; then
    ok "stub: quarantined content is intact"
else
    no "stub: quarantined content is intact"
fi
perm=$(stat -f '%Lp' "$qdir" 2> /dev/null || stat -c '%a' "$qdir" 2> /dev/null)
assert_eq "stub: quarantine dir is 0700" "$perm" "700"
if [[ -f "$FIXROOT/auth.json" ]]; then
    ok "stub: auth.json untouched by quarantine"
else
    no "stub: auth.json untouched by quarantine"
fi

# a quarantine destination outside the admitted root is refused, mutates nothing
mk_fixture
freeze_fixture
run_stub --data-dir "$FIXROOT" --phase legacy-json --quarantine-dir "$FIXBASE/outside-q" \
    --execute > "$FIXBASE/qout.out" 2>&1
rc=$?
assert_eq "stub: out-of-root quarantine dir refused (70)" "$rc" "70"
if [[ -d "$FIXROOT/storage/message" && ! -e "$FIXBASE/outside-q" ]]; then
    ok "stub: refused quarantine left the tree and the target alone"
else
    no "stub: refused quarantine left the tree and the target alone"
fi

# bin / logs containment + happy paths
mk_fixture
run_stub --data-dir "$FIXROOT" --phase bin --execute > /dev/null 2>&1
rc=$?
assert_eq "stub: bin --execute completes (0)" "$rc" "0"
if [[ -e "$FIXROOT/bin" ]]; then
    no "stub: bin dir removed"
else
    ok "stub: bin dir removed"
fi

mk_fixture
run_stub --data-dir "$FIXROOT" --phase logs --execute > /dev/null 2>&1
rc=$?
assert_eq "stub: logs --execute completes (0)" "$rc" "0"
if [[ -e "$FIXROOT/log/a.log" ]]; then
    no "stub: log files cleared"
else
    ok "stub: log files cleared"
fi
if [[ -d "$FIXROOT/log" ]]; then
    ok "stub: log dir itself retained"
else
    no "stub: log dir itself retained"
fi

# snapshots never accepts --execute
mk_fixture
mkdir -p "$FIXROOT/snapshot/proj"
run_stub --data-dir "$FIXROOT" --phase snapshots --execute > /dev/null 2>&1
assert_eq "stub: snapshots refuses --execute (64)" "$?" "64"
if [[ -d "$FIXROOT/snapshot/proj" ]]; then
    ok "stub: snapshots left undo history intact"
else
    no "stub: snapshots left undo history intact"
fi

# terminal record is present and typed on a successful stubbed execute
mk_fixture
freeze_fixture
PATH="$STUBBIN:$PATH" STUB_OC_DATA="$FIXROOT" "$TOOL" --data-dir "$FIXROOT" \
    --phase legacy-json --execute --format json > "$FIXBASE/exec.jsonl" 2> /dev/null
term=$(grep '"record":"terminal"' "$FIXBASE/exec.jsonl" | tail -n1)
assert_contains "stub: execute terminal reports complete" "$term" '"status":"complete"'
assert_contains "stub: execute terminal exit_code is 0" "$term" '"exit_code":0'
phase_rec=$(grep '"record":"phase"' "$FIXBASE/exec.jsonl" | tail -n1)
assert_contains "stub: phase record uses a lifecycle status" "$phase_rec" '"status":"complete"'

# =========================================================================
# Layer 4 — adversarial boundaries: lifecycle, collisions, evidence honesty
# =========================================================================

# Every failure mode must produce a well-formed, projectable stream: header
# first, EXACTLY one terminal last, and a matching non-zero exit.
count_records() { grep -c "\"record\":\"$2\"" "$1" 2> /dev/null || echo 0; }
# line count that reports 0 for an absent file (an absent log means "never happened")
count_lines() {
    [[ -f "$1" ]] || {
        echo 0
        return
    }
    wc -l < "$1" | tr -d ' '
}
assert_stream_wellformed() { # <name> <jsonl-file> <want-exit> <got-exit>
    local n="$1" f="$2" want="$3" got="$4"
    local first last terms
    first=$(head -n1 "$f" 2> /dev/null)
    last=$(grep '"record"' "$f" 2> /dev/null | tail -n1)
    terms=$(count_records "$f" terminal)
    if [[ "$first" != *'"record":"header"'* ]]; then
        no "$n: header first"
    else ok "$n: header first"; fi
    assert_eq "$n: exactly one terminal record" "$terms" "1"
    if [[ "$last" != *'"record":"terminal"'* ]]; then
        no "$n: terminal is last"
    else ok "$n: terminal is last"; fi
    assert_eq "$n: exit code" "$got" "$want"
}

# --- lifecycle: malformed invocation ---------------------------------------
"$TOOL" --format json --phase > "$FIXBASE/noval.jsonl" 2> /dev/null
rc=$?
assert_stream_wellformed "missing --phase value" "$FIXBASE/noval.jsonl" 64 "$rc"
assert_contains "missing --phase value is a usage error" "$(cat "$FIXBASE/noval.jsonl")" '"error_kind":"usage"'

"$TOOL" --format json --data-dir > "$FIXBASE/noval2.jsonl" 2> /dev/null
rc=$?
assert_stream_wellformed "missing --data-dir value" "$FIXBASE/noval2.jsonl" 64 "$rc"

"$TOOL" --format json --bogus-flag > "$FIXBASE/badflag.jsonl" 2> /dev/null
rc=$?
assert_stream_wellformed "unknown flag" "$FIXBASE/badflag.jsonl" 64 "$rc"

"$TOOL" --format json --data-dir "$FIXROOT" --older-than 7q > "$FIXBASE/badage.jsonl" 2> /dev/null
rc=$?
assert_stream_wellformed "invalid --older-than" "$FIXBASE/badage.jsonl" 64 "$rc"

# --- lifecycle: a nonconformant profile still reports through the stream ---
cat > "$FIXBASE/nonconformant.sh" << EOF
#!/usr/bin/env bash
set -euo pipefail
source "$LIB"
reclaim_set_profile "bad"
reclaim_main "\$@"
EOF
chmod +x "$FIXBASE/nonconformant.sh"
"$FIXBASE/nonconformant.sh" --format json > "$FIXBASE/nonconf.jsonl" 2> /dev/null
rc=$?
assert_stream_wellformed "nonconformant profile" "$FIXBASE/nonconf.jsonl" 70 "$rc"
assert_contains "nonconformant profile names the defect" "$(cat "$FIXBASE/nonconf.jsonl")" "not conformant"

# --- lifecycle: refused destinations and mutation failures -----------------
mk_fixture
freeze_fixture
PATH="$STUBBIN:$PATH" STUB_OC_DATA="$FIXROOT" "$TOOL" --data-dir "$FIXROOT" --phase legacy-json \
    --quarantine-dir "$FIXBASE/outside-q" --execute --format json > "$FIXBASE/qrej.jsonl" 2> /dev/null
rc=$?
assert_stream_wellformed "refused quarantine destination" "$FIXBASE/qrej.jsonl" 70 "$rc"
if [[ -d "$FIXROOT/storage/message" && ! -e "$FIXBASE/outside-q" ]]; then
    ok "refused quarantine: tree and target untouched"
else
    no "refused quarantine: tree and target untouched"
fi

# a forced mv failure (read-only parent) must be reported, not swallowed
mk_fixture
freeze_fixture
chmod 500 "$FIXROOT/storage"
PATH="$STUBBIN:$PATH" STUB_OC_DATA="$FIXROOT" "$TOOL" --data-dir "$FIXROOT" --phase legacy-json \
    --execute --format json > "$FIXBASE/mvfail.jsonl" 2> /dev/null
rc=$?
chmod 700 "$FIXROOT/storage"
assert_stream_wellformed "quarantine mv failure" "$FIXBASE/mvfail.jsonl" 70 "$rc"
if [[ -d "$FIXROOT/storage/message" ]]; then
    ok "failed mv left the source tree in place"
else
    no "failed mv left the source tree in place"
fi

# --- export collision safety ----------------------------------------------
# A pre-existing output must never be truncated, overwritten, or removed, and
# the session it belongs to must not be deleted.
mk_fixture
mkdir -p "$FIXBASE/exp-collide"
echo "PRIOR BACKUP" > "$FIXBASE/exp-collide/ses_old1.json"
run_stub --data-dir "$FIXROOT" --phase sessions --export-dir "$FIXBASE/exp-collide" \
    --confirm-delete-sessions 1 --execute > "$FIXBASE/collide.out" 2>&1
rc=$?
assert_eq "export collision refuses the run (70)" "$rc" "70"
assert_eq "export collision preserved the prior file" "$(cat "$FIXBASE/exp-collide/ses_old1.json")" "PRIOR BACKUP"
assert_eq "export collision deleted no sessions" \
    "$(sqlite3 "$FIXROOT/opencode.db" 'SELECT COUNT(*) FROM session;')" "2"

# A symlinked output path must not be followed (no writing through it, no rm)
mk_fixture
mkdir -p "$FIXBASE/exp-link"
echo "OUTSIDE TARGET" > "$FIXBASE/link-target.json"
ln -s "$FIXBASE/link-target.json" "$FIXBASE/exp-link/ses_old1.json"
run_stub --data-dir "$FIXROOT" --phase sessions --export-dir "$FIXBASE/exp-link" \
    --confirm-delete-sessions 1 --execute > "$FIXBASE/link.out" 2>&1
rc=$?
assert_eq "symlinked export output refuses the run (70)" "$rc" "70"
assert_eq "symlinked export output left its target intact" "$(cat "$FIXBASE/link-target.json")" "OUTSIDE TARGET"
if [[ -L "$FIXBASE/exp-link/ses_old1.json" ]]; then
    ok "symlinked export output was not removed"
else
    no "symlinked export output was not removed"
fi

# a failed export leaves no temp residue behind
mk_fixture
rm -rf "$FIXBASE/exp-tmp"
STUB_OC_EXPORT_FAIL=1 run_stub --data-dir "$FIXROOT" --phase sessions \
    --export-dir "$FIXBASE/exp-tmp" --confirm-delete-sessions 1 --execute > /dev/null 2>&1
leftovers=$(find "$FIXBASE/exp-tmp" -name '*.tmp' 2> /dev/null | wc -l | tr -d ' ')
assert_eq "failed export left no temp files" "$leftovers" "0"

# The publication race: the final output appears AFTER admission observed it
# absent, while `opencode export` is still running. Publication must fail closed
# (no-replace), preserve the competing file, and block that session's delete.
mk_fixture
mkdir -p "$FIXBASE/exp-race"
rm -f "$FIXBASE/race-marker" "$FIXBASE/race-deletes" "$FIXBASE/race-rc"
(
    PATH="$STUBBIN:$PATH" STUB_OC_DATA="$FIXROOT" \
        STUB_OC_EXPORT_MARKER="$FIXBASE/race-marker" STUB_OC_EXPORT_DELAY=3 \
        STUB_OC_DELETE_LOG="$FIXBASE/race-deletes" \
        "$TOOL" --data-dir "$FIXROOT" --phase sessions --export-dir "$FIXBASE/exp-race" \
        --confirm-delete-sessions 1 --execute > "$FIXBASE/race.out" 2>&1
    echo "$?" > "$FIXBASE/race-rc"
) &
race_pid=$!
# wait for the export to actually start, then plant the final target
for _ in $(seq 1 200); do
    [[ -f "$FIXBASE/race-marker" ]] && break
    sleep 0.05
done
if [[ -f "$FIXBASE/race-marker" ]]; then
    ok "race: export started (marker observed)"
else
    no "race: export started (marker observed)"
fi
echo "PRIOR BACKUP" > "$FIXBASE/exp-race/ses_old1.json"
wait "$race_pid" 2> /dev/null
rc=$(cat "$FIXBASE/race-rc" 2> /dev/null)

assert_eq "race: planted output was NOT overwritten" \
    "$(cat "$FIXBASE/exp-race/ses_old1.json")" "PRIOR BACKUP"
assert_eq "race: no session was deleted" "$(count_lines "$FIXBASE/race-deletes")" "0"
assert_eq "race: run fails (70)" "$rc" "70"
assert_contains "race: reported as export_blocked" "$(cat "$FIXBASE/race.out")" "export_blocked=1"
leftovers=$(find "$FIXBASE/exp-race" -name '*.tmp' 2> /dev/null | wc -l | tr -d ' ')
assert_eq "race: no temp residue" "$leftovers" "0"

# The same window, but the planted object is a DIRECTORY or a SYMLINK TO ONE:
# a two-operand `ln` would treat it as a destination directory, link inside it,
# and report success. Publication must refuse and block the delete.
race_dir_probe() { # <name> <plant-cmd...>
    local label="$1"
    shift
    mk_fixture
    rm -rf "$FIXBASE/exp-$label" "$FIXBASE/redirect-$label"
    mkdir -p "$FIXBASE/exp-$label" "$FIXBASE/redirect-$label"
    rm -f "$FIXBASE/marker-$label" "$FIXBASE/deletes-$label" "$FIXBASE/rc-$label"
    (
        PATH="$STUBBIN:$PATH" STUB_OC_DATA="$FIXROOT" \
            STUB_OC_EXPORT_MARKER="$FIXBASE/marker-$label" STUB_OC_EXPORT_DELAY=3 \
            STUB_OC_DELETE_LOG="$FIXBASE/deletes-$label" \
            "$TOOL" --data-dir "$FIXROOT" --phase sessions --export-dir "$FIXBASE/exp-$label" \
            --confirm-delete-sessions 1 --execute > "$FIXBASE/out-$label" 2>&1
        echo "$?" > "$FIXBASE/rc-$label"
    ) &
    local pid=$!
    local _
    for _ in $(seq 1 200); do
        [[ -f "$FIXBASE/marker-$label" ]] && break
        sleep 0.05
    done
    "$@" # plant the racing object at the final output name
    wait "$pid" 2> /dev/null
}

# (1) symlink to a directory planted mid-export
race_dir_probe symdir ln -s "$FIXBASE/redirect-symdir" "$FIXBASE/exp-symdir/ses_old1.json"
rc=$(cat "$FIXBASE/rc-symdir" 2> /dev/null)
assert_eq "race/symlinked-dir: run fails (70)" "$rc" "70"
if [[ -L "$FIXBASE/exp-symdir/ses_old1.json" ]]; then
    ok "race/symlinked-dir: planted symlink left intact"
else
    no "race/symlinked-dir: planted symlink left intact"
fi
assert_eq "race/symlinked-dir: nothing was written through the redirect" \
    "$(find "$FIXBASE/redirect-symdir" -type f 2> /dev/null | wc -l | tr -d ' ')" "0"
assert_eq "race/symlinked-dir: no session was deleted" "$(count_lines "$FIXBASE/deletes-symdir")" "0"
assert_contains "race/symlinked-dir: reported as export_blocked" "$(cat "$FIXBASE/out-symdir")" "export_blocked=1"
assert_eq "race/symlinked-dir: no staging residue" \
    "$(find "$FIXBASE/exp-symdir" -name '*.json' -not -name 'ses_old1.json' 2> /dev/null | wc -l | tr -d ' ')" "0"

# (2) a real directory planted mid-export
race_dir_probe realdir mkdir -p "$FIXBASE/exp-realdir/ses_old1.json"
rc=$(cat "$FIXBASE/rc-realdir" 2> /dev/null)
assert_eq "race/real-dir: run fails (70)" "$rc" "70"
if [[ -d "$FIXBASE/exp-realdir/ses_old1.json" ]]; then
    ok "race/real-dir: planted directory left intact"
else
    no "race/real-dir: planted directory left intact"
fi
assert_eq "race/real-dir: nothing was written inside the planted directory" \
    "$(find "$FIXBASE/exp-realdir/ses_old1.json" -type f 2> /dev/null | wc -l | tr -d ' ')" "0"
assert_eq "race/real-dir: no session was deleted" "$(count_lines "$FIXBASE/deletes-realdir")" "0"
assert_contains "race/real-dir: reported as export_blocked" "$(cat "$FIXBASE/out-realdir")" "export_blocked=1"

# both publish primitives must refuse a directory / symlinked-directory target
# and both must succeed on a clean one (the `ln` fallback is only valid because
# the staged basename equals the final name)
publish_probe() { # <primitive>
    local prim="$1" base="$FIXBASE/pub-$1"
    rm -rf "$base"
    mkdir -p "$base/stage" "$base/dest" "$base/elsewhere"
    echo payload > "$base/stage/out.json"
    RECLAIM_PUBLISH_PRIMITIVE="$prim" reclaim_publish_no_replace "$base/stage/out.json" "$base/dest" "out.json" > /dev/null 2>&1
    assert_eq "publish/$prim: clean destination succeeds" "$?" "0"
    assert_eq "publish/$prim: published the staged content" "$(cat "$base/dest/out.json")" "payload"

    rm -rf "$base/dest2" && mkdir -p "$base/dest2"
    ln -s "$base/elsewhere" "$base/dest2/out.json"
    RECLAIM_PUBLISH_PRIMITIVE="$prim" reclaim_publish_no_replace "$base/stage/out.json" "$base/dest2" "out.json" > /dev/null 2>&1
    assert_eq "publish/$prim: refuses a symlinked-directory target" "$?" "1"
    assert_eq "publish/$prim: wrote nothing through the symlink" \
        "$(find "$base/elsewhere" -type f 2> /dev/null | wc -l | tr -d ' ')" "0"

    rm -rf "$base/dest3" && mkdir -p "$base/dest3/out.json"
    RECLAIM_PUBLISH_PRIMITIVE="$prim" reclaim_publish_no_replace "$base/stage/out.json" "$base/dest3" "out.json" > /dev/null 2>&1
    assert_eq "publish/$prim: refuses a real directory target" "$?" "1"
    assert_eq "publish/$prim: wrote nothing inside the directory" \
        "$(find "$base/dest3/out.json" -type f 2> /dev/null | wc -l | tr -d ' ')" "0"

    rm -rf "$base/dest4" && mkdir -p "$base/dest4"
    echo PRIOR > "$base/dest4/out.json"
    RECLAIM_PUBLISH_PRIMITIVE="$prim" reclaim_publish_no_replace "$base/stage/out.json" "$base/dest4" "out.json" > /dev/null 2>&1
    assert_eq "publish/$prim: refuses an existing regular file" "$?" "1"
    assert_eq "publish/$prim: left the existing file intact" "$(cat "$base/dest4/out.json")" "PRIOR"
}
publish_probe link
publish_probe ln

# and the same publication path still succeeds when nothing races it
mk_fixture
rm -rf "$FIXBASE/exp-norace"
rm -f "$FIXBASE/norace-deletes"
PATH="$STUBBIN:$PATH" STUB_OC_DATA="$FIXROOT" STUB_OC_DELETE_LOG="$FIXBASE/norace-deletes" \
    "$TOOL" --data-dir "$FIXROOT" --phase sessions --export-dir "$FIXBASE/exp-norace" \
    --confirm-delete-sessions 1 --execute > /dev/null 2>&1
rc=$?
assert_eq "no-race publish still completes (0)" "$rc" "0"
assert_eq "no-race publish exported the session" "$(cat "$FIXBASE/exp-norace/ses_old1.json" 2> /dev/null)" '{"id":"ses_old1"}'
assert_eq "no-race publish deleted the session" "$(count_lines "$FIXBASE/norace-deletes")" "1"
leftovers=$(find "$FIXBASE/exp-norace" -name '*.tmp' 2> /dev/null | wc -l | tr -d ' ')
assert_eq "no-race publish left no temp residue" "$leftovers" "0"

# an export dir that is a symlink is never adopted, even at admission time
mk_fixture
mkdir -p "$FIXBASE/real-elsewhere"
rm -f "$FIXBASE/exp-symlinked"
ln -s "$FIXBASE/real-elsewhere" "$FIXBASE/exp-symlinked"
reclaim_admit_export_dir "$FIXBASE/exp-symlinked" > /dev/null 2>&1
assert_eq "admit_export_dir refuses a symlinked directory" "$?" "1"
run_stub --data-dir "$FIXROOT" --phase sessions --export-dir "$FIXBASE/exp-symlinked" \
    --confirm-delete-sessions 1 --execute > "$FIXBASE/expsym.out" 2>&1
assert_eq "symlinked export dir blocks execute" "$?" "75"

# --- evidence honesty ------------------------------------------------------
# An untraversable legacy store must be `unknown`, never a confirmed idle pass.
mk_fixture
freeze_fixture
chmod 000 "$FIXROOT/storage/message"
out="$(run_stub --data-dir "$FIXROOT" --phase legacy-json)"
chmod 755 "$FIXROOT/storage/message"
assert_contains "unreadable legacy store -> migration-evidence unknown" "$out" \
    "check=migration-evidence required=1 outcome=unknown basis=inferred"

mk_fixture
freeze_fixture
chmod 000 "$FIXROOT/storage/message"
run_stub --data-dir "$FIXROOT" --phase legacy-json --execute > /dev/null 2>&1
rc=$?
chmod 755 "$FIXROOT/storage/message"
assert_eq "unverifiable evidence blocks execute (75)" "$rc" "75"
if [[ -e "$FIXROOT/.reclaim-quarantine" ]]; then
    no "unverifiable evidence mutated nothing"
else
    ok "unverifiable evidence mutated nothing"
fi

# A SQLite read failure must not read as "no candidates" and finish successfully
mk_fixture
echo "this is not a database" > "$FIXROOT/opencode.db"
run_stub --data-dir "$FIXROOT" --phase sessions --confirm-delete-sessions 1 --execute > "$FIXBASE/corrupt.out" 2>&1
rc=$?
assert_eq "unreadable store fails the sessions phase (70)" "$rc" "70"
assert_not_contains "unreadable store did not report success" "$(cat "$FIXBASE/corrupt.out")" "status=complete deleted="

# log removal failures are counted, not ignored
mk_fixture
chmod 500 "$FIXROOT/log"
run_stub --data-dir "$FIXROOT" --phase logs --execute > "$FIXBASE/logfail.out" 2>&1
rc=$?
chmod 700 "$FIXROOT/log"
assert_eq "log removal failure exits 70" "$rc" "70"
assert_contains "log removal failure is reported" "$(cat "$FIXBASE/logfail.out")" "status=failed"

# --- lifecycle vocabulary --------------------------------------------------
mk_fixture
freeze_fixture
"$TOOL" --data-dir "$FIXROOT" --phase legacy-json --format json > "$FIXBASE/vocab.jsonl" 2> /dev/null
pf=$(grep '"check":"migration-evidence"' "$FIXBASE/vocab.jsonl" | head -n1)
assert_contains "passing preflight uses a lifecycle status" "$pf" '"status":"complete"'
assert_not_contains "passing preflight does not emit ok" "$pf" '"status":"ok"'
assert_not_contains "passing preflight carries no error class" "$pf" '"error_class"'

mk_fixture # fresh mtimes: migration-evidence fails
"$TOOL" --data-dir "$FIXROOT" --phase legacy-json --format json > "$FIXBASE/vocab2.jsonl" 2> /dev/null
pf=$(grep '"check":"migration-evidence"' "$FIXBASE/vocab2.jsonl" | head -n1)
assert_contains "blocked preflight is status=blocked" "$pf" '"status":"blocked"'
assert_contains "blocked-on-evidence carries a canonical class" "$pf" '"error_class":"integrity"'
assert_contains "blocked-on-evidence carries the local kind" "$pf" '"error_kind":"check-failed"'

mk_fixture
"$TOOL" --data-dir "$FIXROOT" --phase legacy-json --execute --format json > "$FIXBASE/vocab3.jsonl" 2> /dev/null
rc=$?
term=$(grep '"record":"terminal"' "$FIXBASE/vocab3.jsonl" | tail -n1)
assert_eq "gate-blocked execute exits 75" "$rc" "75"
assert_contains "gate-blocked terminal is status=blocked" "$term" '"status":"blocked"'
assert_contains "gate-blocked terminal class is cancelled" "$term" '"error_class":"cancelled"'

mk_fixture
freeze_fixture
PATH="$STUBBIN:$PATH" STUB_OC_DATA="$FIXROOT" "$TOOL" --data-dir "$FIXROOT" --phase legacy-json \
    --execute --format json > "$FIXBASE/vocab4.jsonl" 2> /dev/null
term=$(grep '"record":"terminal"' "$FIXBASE/vocab4.jsonl" | tail -n1)
assert_contains "successful terminal is complete" "$term" '"status":"complete"'
assert_not_contains "successful terminal invents no error class" "$term" '"error_class"'
assert_not_contains "successful terminal invents no error kind" "$term" '"error_kind"'

# --- summary --------------------------------------------------------------
echo
echo "passed=$PASS failed=$FAIL"
[[ "$FAIL" -eq 0 ]]

#!/usr/bin/env bash
# agent-tmp-residue.sh — reclaim build and harness residue from the OS temp plane.
#
# Two risk classes, deliberately gated apart.
#
# Build output left in temp by test and review tooling is abandoned by
# construction: regenerable, named after the work rather than the tool, and held
# by nothing once its build finished. Those phases need age, admission, an
# allowlist and a count acknowledgement.
#
# Per-agent session trees are different. Deleting one takes a live agent's
# working directory with it, and the set of live sessions is not establishable
# here. That phase runs against the narrower per-user agent root and requires
# both an exact-membership acknowledgement and an explicit acceptance of the
# unknowable live set, on top of every other guard. Merged under one
# acknowledgement the cheap phase would drag the expensive one along behind it,
# so they never share a gate.
#
# Root class is `shared-temp`: the sticky, world-writable temp plane is
# root-owned by design, so ownership of the directory proves nothing about what
# is inside it. Authorization is per candidate, immediately before each unlink.
#
# Dry-run by default. See reclaim-lib.sh for the shared lifecycle.
#
# CONTRACT FOR CALLERS
#
# This is an operational surface people and agents depend on, so what it
# promises is worth stating plainly. It may one day move into the binary; these
# are the properties that should survive that move.
#
#   Modes        --plan (default) inspects and emits; --preflight runs the gate
#                and stops; --execute is the only mode that removes anything.
#   Streams      Machine records go to stdout in --format json. Human lines and
#                progress go to stderr. stdout stays parseable.
#   Exit codes   0 success (including "nothing to do"), 64 usage, 65 admission
#                refused, 70 runtime failure, 75 a required check blocked.
#                Callers should branch on the status, not on the text.
#   Records      Every run emits a header first and exactly one terminal record
#                last, on every exit path, so a consumer can always determine
#                the outcome from the stream alone.
#   Safety       Nothing is removed without a phase-appropriate acknowledgement
#                that matches this run's own plan. An acknowledgement never
#                bypasses admission, age, exclusion, per-candidate
#                authorization, or a required check — it only answers the
#                question the tool cannot answer for itself.
#
# A guiding rule throughout: unknown blocks. Where evidence is unavailable —
# an unreadable subtree, a probe that could not run, a platform that will not
# say who owns a file — the candidate is skipped rather than assumed safe.

# shellcheck source-path=SCRIPTDIR
set -euo pipefail

_HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=./reclaim-lib.sh
source "$_HERE/reclaim-lib.sh"

reclaim_set_profile "agent.tmp.residue"

# The class is declared here, not inferred from the path: a directory that
# happens to look like temp is not evidence about the boundary it sits on.
reclaim_set_root_class "shared-temp"

# --- options ---------------------------------------------------------------
#
# Defaults and flag state. Values an operator supplies are passed to the guard
# unchanged so it can refuse them and say why; only values this script chooses
# for itself are pre-resolved.
#
# The default root is resolved here rather than handed to the guard as "/tmp",
# because on macOS /tmp is a symlink to /private/tmp and the shared guard
# rightly refuses a root reached through a symlink. Resolving our own default is
# not the same as resolving an operator's: a path the operator typed is passed
# through untouched so the guard can refuse it and say why. We only pre-resolve
# the value we chose ourselves, on a platform alias we know the shape of.
_default_temp_root() {
    if [[ -L /tmp ]]; then
        (cd /tmp 2> /dev/null && pwd -P) || printf '/tmp\n'
    else
        printf '/tmp\n'
    fi
}

ROOT_REQUEST="${TMP_RESIDUE_ROOT:-$(_default_temp_root)}"
OLDER_THAN="2d"
CONFIRM_DELETE_FILES=""
CONFIRM_DELETE_SESSIONS=""
CONFIRM_MEMBERSHIP=""
ACCEPT_INCOMPLETE_LIVE_SET=0
CATALOG_FILE=""
EXCLUDE_IDS=""

# The session phase runs against a narrower root than the file phases.
# /private/tmp is sticky and world-writable; the per-user agent directory below
# it is 0700 and owned by us. Admitting the narrower one is strictly less
# authority for the same job, so the session phase takes it and the file phases
# — whose candidates genuinely sit at the top of the plane — do not.
SESSION_ROOT_NAME="claude-$(id -u)"

# The session phase narrows the root, and narrowing must not become a way to
# point the destructive boundary somewhere new. `user-temp` proves a directory
# is private and ours; it does not prove it is the OS temp plane. Without this
# binding, any owned 0700 `.../claude-$UID` anywhere on the disk would qualify.
#
# TMP_RESIDUE_TEST_TEMP_ROOT is a test seam, not production authorization: it
# widens the approved set for fixtures, and its use is emitted in the record so
# a run that relied on it cannot be mistaken for one that did not.
_approved_temp_roots() {
    local r
    r=$(_default_temp_root)
    [[ -n "$r" ]] && printf '%s\n' "$r"

    # $TMPDIR is caller-controlled, so its value is not evidence. It is
    # accepted only when it canonicalises to the per-user temp shape the
    # platform actually creates; anything else pointed at by the variable —
    # including a repository checkout — is not a temp plane because an
    # environment variable said so.
    if [[ -n "${TMPDIR:-}" ]]; then
        r=$(cd "$TMPDIR" 2> /dev/null && pwd -P) || r=""
        if [[ -n "$r" ]] && _is_platform_user_temp "$r"; then
            printf '%s\n' "$r"
        fi
    fi

    # The fixture seam is plan-only and is refused outright for mutation; see
    # _assert_seam_not_executable. Emitting that it was used records the
    # widening but does not authorize it.
    if [[ -n "${TMP_RESIDUE_TEST_TEMP_ROOT:-}" ]]; then
        r=$(cd "$TMP_RESIDUE_TEST_TEMP_ROOT" 2> /dev/null && pwd -P) || r=""
        [[ -n "$r" ]] && printf '%s\n' "$r"
    fi
}

# The shapes a platform creates for private per-user temp. Structural, so a
# directory qualifies by where it is rather than by what an operator exported.
_is_platform_user_temp() {
    case "$1" in
        /private/var/folders/*/T | /var/folders/*/T) return 0 ;; # macOS
        /run/user/[0-9]*) return 0 ;;                            # systemd
        *) return 1 ;;
    esac
}

# A fixture seam must never participate in a run that can delete anything.
_assert_seam_not_executable() {
    if [[ -n "${TMP_RESIDUE_TEST_TEMP_ROOT:-}" && "$RECLAIM_MODE" == "execute" ]]; then
        reclaim_die "$RECLAIM_EX_ADMISSION" \
            "TMP_RESIDUE_TEST_TEMP_ROOT is a plan-only fixture seam and cannot be used with --execute" \
            "unset it and name a real platform temp root"
    fi
}

_assert_approved_temp_parent() {
    local parent="$1" canon approved
    canon=$(cd "$parent" 2> /dev/null && pwd -P) || canon=""
    [[ -n "$canon" ]] ||
        reclaim_die "$RECLAIM_EX_ADMISSION" "session root parent does not exist: $parent"
    while IFS= read -r approved; do
        [[ -n "$approved" ]] || continue
        [[ "$canon" == "$approved" ]] && return 0
    done < <(_approved_temp_roots)
    reclaim_die "$RECLAIM_EX_ADMISSION" \
        "session root parent is not an approved OS temp root: $canon" \
        "the session phase only runs under the platform temp plane"
}

usage() {
    cat << 'EOF'
agent-tmp-residue.sh — reclaim build/harness residue from the OS temp plane

USAGE
  agent-tmp-residue.sh [--phase PHASE] [--plan|--preflight|--execute] [options]

PHASES
  all                 plan-only inventory across every style (default)
  go-test-binaries    compiled test/review binaries left in temp
  named-gocache       build cache directories named into temp
  agent-scratch       per-agent session trees; runs against the per-user agent
                      directory, not the whole temp plane

OPTIONS
  --root PATH                 temp root (default /tmp; must be sticky)
  --older-than AGE            Nd|Nw|Nm|Ny, default 2d. For directories the age
                              is the newest mtime found inside, not the
                              directory's own.
  --exclude-session-id ID     never touch paths containing this id component
                              (repeatable; unioned with ids read from the
                              environment, which cannot be overridden)
  --confirm-delete-files N    irreversible-class acknowledgement for file
                              phases; must equal the executable candidate count
                              from this same run
  --confirm-delete-sessions N irreversible-class acknowledgement for the session
                              phase; must equal this run's candidate count
  --confirm-membership DIGEST bind to the exact reviewed candidate set; a set of
                              the same size is not the same set
  --accept-incomplete-live-set
                              state that you accept deleting session trees while
                              the set of live sessions cannot be established.
                              Required for the session phase, and it is not a
                              force: every other guard still applies
  --catalog FILE              additional name patterns, one per line, '#' comments
  --format text|json          default text
  -h, --help                  this help

SAFETY
  Dry-run by default. Each candidate is authorized on its own evidence
  immediately before removal: owned by you, expected type, same device, never a
  symlink, never the root. Files belonging to another user are skipped and
  counted, never retried.
EOF
}

_arg_value() {
    # _arg_value <flag> <index> <argv...>
    local flag="$1" idx="$2"
    shift 2
    local all=("$@")
    [[ "$idx" -lt "${#all[@]}" ]] || return 1
    local v="${all[$idx]}"
    [[ -n "$v" && "$v" != --* ]] || return 1
    printf '%s\n' "$v"
}

_age_to_seconds() {
    local a="$1" n unit
    n="${a%[dwmy]}"
    unit="${a: -1}"
    case "$unit" in
        d) printf '%s\n' $((n * 86400)) ;;
        w) printf '%s\n' $((n * 604800)) ;;
        m) printf '%s\n' $((n * 2592000)) ;;
        y) printf '%s\n' $((n * 31536000)) ;;
        *) return 1 ;;
    esac
}

human_size() {
    local b="${1:-0}"
    if [[ "$b" -ge 1073741824 ]]; then
        printf '%d.%01dG\n' $((b / 1073741824)) $(((b % 1073741824) * 10 / 1073741824))
    elif [[ "$b" -ge 1048576 ]]; then
        printf '%dM\n' $((b / 1048576))
    else
        printf '%dK\n' $((b / 1024))
    fi
}

# --- name catalog ----------------------------------------------------------
#
# Generic grammar only. Site-specific names stay out of this repo: a committed
# allowlist is a world-readable surface, and one team's project names have no
# business on it. Operators extend the catalog from a file.
_default_patterns_go_test_binaries() {
    cat << 'EOF'
*.test
*.test.exe
*-review-bin
*-review-bin.exe
EOF
}

_default_patterns_named_gocache() {
    cat << 'EOF'
*gocache*
*go-cache*
*go-build-cache*
EOF
}

_catalog_patterns() {
    # _catalog_patterns <phase>
    case "$1" in
        go-test-binaries) _default_patterns_go_test_binaries ;;
        named-gocache) _default_patterns_named_gocache ;;
    esac
    if [[ -n "$CATALOG_FILE" && -f "$CATALOG_FILE" ]]; then
        # Operator-supplied additions; comments and blanks ignored.
        sed -e 's/#.*//' -e '/^[[:space:]]*$/d' "$CATALOG_FILE"
    fi
}

# --- session id exclusion --------------------------------------------------
#
# Read from our own environment so a seat cannot forget to protect itself, and
# unioned with anything the operator names. This can only ever shrink the
# candidate set; it is never evidence that the set is safe. Knowing our own id
# says nothing about sibling seats, which is why the session phase does not
# execute in this build.
_env_session_ids() {
    # Only keys whose id-to-path correlation is fixture-proven. A variable
    # existing is not a mapping.
    local v="${CLAUDE_CODE_SESSION_ID:-}"
    [[ -n "$v" ]] && printf '%s\n' "$v"
    return 0
}

_id_is_well_formed() {
    # UUID grammar. A malformed value stays unknown: it must not be able to
    # widen or narrow anything by accident.
    [[ "$1" =~ ^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$ ]]
}

_auto_ids() {
    local id
    while IFS= read -r id; do
        [[ -n "$id" ]] || continue
        _id_is_well_formed "$id" && printf '%s\n' "$id"
    done < <(_env_session_ids)
}

_explicit_ids() {
    local id
    while IFS= read -r id; do
        [[ -n "$id" ]] || continue
        _id_is_well_formed "$id" && printf '%s\n' "$id"
    done < <(printf '%s\n' "${EXCLUDE_IDS//,/$'\n'}")
}

# The union, deduplicated: an id supplied both ways is one protection, not two.
_excluded_ids() {
    {
        _auto_ids
        _explicit_ids
    } | LC_ALL=C sort -u
}

_path_is_excluded() {
    # Exact path-component match, never substring: one id can be a prefix of
    # another, and a substring rule would silently protect or expose the wrong
    # tree.
    local path="$1" id
    while IFS= read -r id; do
        [[ -n "$id" ]] || continue
        case "/$path/" in
            */"$id"/*) return 0 ;;
        esac
    done < <(_excluded_ids)
    return 1
}

# --- age -------------------------------------------------------------------
#
# Whether a candidate is abandoned. For a file its own timestamp answers it; for
# a tree it does not, and the difference is the whole point of this section.
#
# For a directory the newest mtime *inside* it decides, not the directory's own:
# a tree can be written into for hours without its root mtime moving, and using
# the root would call an active tree stale. Stops at the first entry newer than
# the cutoff, so one recent file is enough and the walk stays cheap.
# A reference file stamped to the cutoff, for `find -newer`.
#
# `-newermt @epoch` is GNU-only: BSD find answers "Can't parse date/time" and
# exits non-zero. `-newer <file>` is in both. The conversion from epoch to the
# touch stamp differs too, so both spellings are tried and a failure to build
# the reference is reported rather than assumed away.
_cutoff_ref_file=""

# A temp-reclaim tool that leaves its own scratch behind is not one to trust.
_tmp_residue_cleanup() {
    if [[ -n "$_cutoff_ref_file" ]]; then
        rm -f "$_cutoff_ref_file"
        _cutoff_ref_file=""
    fi
    return 0
}

# The harness owns the EXIT trap — it installs its own terminal-record backstop
# in reclaim_main — so setting one here at load time is simply replaced. This
# composes instead: run our cleanup first, then hand control to whatever was
# already registered, with the original exit status restored so the backstop
# still reports the right code.
_TMP_RESIDUE_PREV_EXIT_TRAP=""
# Assigned inside the trap body at exit; declared here so the value is visible
# to readers and to static analysis, neither of which can see into a trap.
__tr_rc=0
_install_lifecycle_cleanup() {
    local prev
    prev=$(trap -p EXIT)
    prev=${prev#trap -- \'}
    prev=${prev%\' EXIT}
    _TMP_RESIDUE_PREV_EXIT_TRAP="$prev"
    trap '__tr_rc=$?
        _tmp_residue_cleanup
        if [[ -n "$_TMP_RESIDUE_PREV_EXIT_TRAP" ]]; then
            (exit "$__tr_rc")
            eval "$_TMP_RESIDUE_PREV_EXIT_TRAP"
        fi
        exit "$__tr_rc"' EXIT
}

_make_cutoff_ref() {
    local cutoff="$1" stamp
    stamp=$(date -r "$cutoff" +%Y%m%d%H%M.%S 2> /dev/null) ||
        stamp=$(date -d "@$cutoff" +%Y%m%d%H%M.%S 2> /dev/null) || return 1
    _cutoff_ref_file=$(mktemp) || return 1
    touch -t "$stamp" "$_cutoff_ref_file" 2> /dev/null || return 1
    return 0
}

# Returns 0 older, 1 newer, 2 unknown.
#
# A traversal that could not read part of the tree cannot see a recent file
# inside it, so discarding the failure would convert an unreadable subtree into
# a stale one — exactly the unknown-treated-as-safe error the guard forbids.
# Both the exit status and anything on stderr are treated as unknown.
#
# This is also why the previous `|| true` was a defect rather than a shortcut:
# it swallowed a portability failure and silently fell back to the target's own
# mtime, which is the very thing the subtree rule exists to replace.
_subtree_is_older_than() {
    local target="$1" cutoff="$2"

    if [[ -z "$_cutoff_ref_file" || ! -e "$_cutoff_ref_file" ]]; then
        _make_cutoff_ref "$cutoff" || return 2
    fi

    local errfile newest rc=0
    errfile=$(mktemp) || return 2
    newest=$(find "$target" -newer "$_cutoff_ref_file" -print -quit 2> "$errfile") || rc=$?

    local had_err=0
    if [[ -s "$errfile" ]]; then
        had_err=1
    fi
    rm -f "$errfile"

    if [[ -n "$newest" ]]; then
        return 1
    fi
    if [[ "$rc" -ne 0 || "$had_err" -eq 1 ]]; then
        return 2
    fi
    local m
    m=$(_reclaim_stat m "$target")
    [[ -n "$m" ]] || return 2
    [[ "$m" -le "$cutoff" ]]
}

_file_is_older_than() {
    local target="$1" cutoff="$2" m
    m=$(_reclaim_stat m "$target")
    [[ -n "$m" ]] || return 2
    [[ "$m" -le "$cutoff" ]]
}

# --- checks ----------------------------------------------------------------
#
# Preflight checks registered with the harness. Each sets an outcome
# (pass/fail/unknown) and a basis (confirmed/inferred); a check marked required
# lets a phase mutate only when it is both pass and confirmed, so uncertainty
# blocks without any phase having to remember to ask.
#
# The distinction that matters here is between a check that reports a fact
# about the world and one that reports a fact about the invocation. The first
# kind can be unknowable; the second is always answerable. Keeping them
# separate is what lets an operator proceed past something unknowable without
# the record ever claiming it was established.
_ck_no_active_go_build() {
    if ! command -v pgrep > /dev/null 2>&1; then
        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL="pgrep unavailable; cannot confirm no build is running"
        RECLAIM_CHECK_RESOLVE="install pgrep, or verify manually that no build is in flight"
        return
    fi
    # A live build can be writing the very artefacts we are about to remove.
    # Go tolerates a vanished cache by rebuilding, but deleting underneath a
    # running compile produces failures that look like source errors.
    local proc
    for proc in go compile link; do
        if pgrep -x "$proc" > /dev/null 2>&1; then
            RECLAIM_CHECK_OUTCOME="fail"
            RECLAIM_CHECK_BASIS="confirmed"
            RECLAIM_CHECK_DETAIL="a '$proc' process is running"
            RECLAIM_CHECK_RESOLVE="wait for the build to finish, then retry"
            return
        fi
    done
    RECLAIM_CHECK_OUTCOME="pass"
    RECLAIM_CHECK_BASIS="confirmed"
    RECLAIM_CHECK_DETAIL="no go/compile/link process detected"
}

# Two facts, not one. Supplying an arbitrary explicit id says nothing about
# whether this seat protected itself, and folding them together let one stand in
# for the other.
_ck_auto_self_id_known() {
    local n=0 id
    while IFS= read -r id; do
        [[ -n "$id" ]] && n=$((n + 1))
    done < <(_auto_ids)

    if [[ "$n" -gt 0 ]]; then
        RECLAIM_CHECK_OUTCOME="pass"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="$n session id(s) read from this invocation's environment"
    else
        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL="no session id in this invocation's environment; the caller may not be an agent seat"
        RECLAIM_CHECK_RESOLVE="run from the seat you wish to protect, or pass --exclude-session-id"
    fi
}

_ck_explicit_exclusions_present() {
    local n=0 id
    while IFS= read -r id; do
        [[ -n "$id" ]] && n=$((n + 1))
    done < <(_explicit_ids)

    if [[ "$n" -gt 0 ]]; then
        RECLAIM_CHECK_OUTCOME="pass"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="$n session id(s) supplied on the command line"
    else
        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL="no session ids supplied on the command line"
        RECLAIM_CHECK_RESOLVE="pass --exclude-session-id for any other seat you care about"
    fi
}

# Two checks, deliberately separate, because they assert different things and
# only one of them can ever be true.
#
# The first states a fact about the world that no probe here can establish. It
# is informational and it is always inferred: reporting it as confirmed because
# an operator waved it through would be a claim outrunning its evidence, which
# is the defect class this whole profile is built to avoid. It stays visible in
# every run so the limitation is never quietly absent.
#
# The second states a fact about the operator: that a human was told the live
# set is unknowable and accepted the consequence anyway. That *is* confirmable —
# the flag was passed or it was not — and it is what the gate keys on. The
# acknowledgement does not make the set complete; it records who decided to
# proceed without it.
_ck_live_set_complete() {
    # The fact did not pass; it could not be established. Reporting "pass"
    # alongside a detail saying completeness is unknowable overstates what was
    # observed, however clearly the basis is qualified.
    RECLAIM_CHECK_OUTCOME="unknown"
    RECLAIM_CHECK_BASIS="inferred"
    RECLAIM_CHECK_DETAIL="the set of live agent sessions cannot be established on this host"
    RECLAIM_CHECK_RESOLVE="not resolvable by probing; proceed only with --accept-incomplete-live-set"
}

_ck_live_set_risk_accepted() {
    if [[ "$ACCEPT_INCOMPLETE_LIVE_SET" == "1" ]]; then
        RECLAIM_CHECK_OUTCOME="pass"
        RECLAIM_CHECK_BASIS="confirmed"
        # What is confirmed is that the invocation carried the flag. It does
        # not establish that a human read the plan, who they were, or that they
        # understood it — so the detail claims only what the evidence supports.
        RECLAIM_CHECK_DETAIL="invocation carried the incomplete-live-set acceptance flag"
    else
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="the incomplete-live-set risk has not been accepted"
        RECLAIM_CHECK_RESOLVE="pass --accept-incomplete-live-set once you have reviewed the plan"
    fi
}

# --- coverage accounting ----------------------------------------------------
#
# A candidate count on its own is a projection presented as a scope. These
# collectors therefore report three numbers, following coverage-attestation
# semantics: how many paths were considered, how many could actually be
# evaluated, and how many could not. A path whose age or ownership could not be
# established is excluded from the candidate set — correctly, since unknown
# blocks — but silently dropping it would let the output claim complete
# knowledge of a tree it only partly read.
#
# basis is confirmed only when every considered path was evaluable. Otherwise
# the set is real but the coverage is inferred, and the record says so.
#
# Collectors emit a trailer line rather than returning values, because they run
# inside a process substitution and only their stdout reaches the caller.
_STAT_PREFIX="__COVERAGE "

# Age classification, captured without letting a non-zero return terminate the
# caller. A bare call followed by `case "$?"` looks equivalent and is not: under
# `set -e` the call itself ends the shell before the case is reached. That has
# now bitten this file three times, so the guarded form lives in one place and
# the raw predicate is never called directly from a phase or collector.
_age_class() { # <target> <cutoff> -> prints older|newer|unknown
    local rc=0
    _subtree_is_older_than "$1" "$2" || rc=$?
    case "$rc" in
        0) printf 'older\n' ;;
        1) printf 'newer\n' ;;
        *) printf 'unknown\n' ;;
    esac
}

_file_age_class() { # <target> <cutoff> -> prints older|newer|unknown
    local rc=0
    _file_is_older_than "$1" "$2" || rc=$?
    case "$rc" in
        0) printf 'older\n' ;;
        1) printf 'newer\n' ;;
        *) printf 'unknown\n' ;;
    esac
}

# Enumeration is itself evidence, and it can fail. A directory the traversal
# could not read hides whatever is beneath it — including well-formed
# candidates — and that absence never reaches a per-path counter, because the
# path was never listed. Suppressing the traversal's own status therefore lets
# an incomplete enumeration report a complete-looking denominator.
#
# So the enumerator reports whether it finished, and when it did not the
# population is a lower bound rather than a total, and the basis is inferred.
# The failure is reported on stdout, not through a variable. _enumerate runs
# inside a process substitution, and an assignment there dies with the subshell
# — which is exactly how an incomplete traversal previously reported itself as
# complete. stdout is the only channel that crosses the boundary, so the
# sentinel travels with the data it qualifies.
_ENUM_FAIL_LINE="__ENUMERATION_INCOMPLETE"
_enumerate() { # <find-args...>
    local errfile rc=0
    errfile=$(mktemp) || {
        printf '%s\n' "$_ENUM_FAIL_LINE"
        return 0
    }
    find "$@" 2> "$errfile" || rc=$?
    if [[ "$rc" -ne 0 || -s "$errfile" ]]; then
        printf '%s\n' "$_ENUM_FAIL_LINE"
    fi
    rm -f "$errfile"
    return 0
}

# coverage-attestation makes the method part of the claim: how a population was
# arrived at determines what its numbers can support.
#
# These populations are always enumerated — members are listed directly by
# traversal — and an interrupted traversal is an *incomplete enumeration*, not a
# different acquisition method. `declared` means the population was supplied as a
# declaration by something else, which is never the case here. Completeness is
# carried by basis and the lower-bound marker; degrading the method to signal it
# would misuse the vocabulary and lose the fact that we did enumerate.
_coverage_method() {
    printf 'enumerated\n'
}

# confirmed requires BOTH that every considered path was evaluable AND that the
# enumeration which produced them completed. Either failing makes the coverage a
# partial view, and a partial view reported as confirmed is the defect this
# record shape exists to prevent.
_coverage_basis() { # <unevaluable> <enumeration-complete 0|1>
    if [[ "$1" -eq 0 && "$2" -eq 1 ]]; then
        printf 'confirmed\n'
    else
        printf 'inferred\n'
    fi
}

# True the first time a path is offered, false thereafter. Catalog patterns
# overlap by design, so matches are pooled and deduplicated before counting:
# otherwise one directory is reported twice and an acknowledgement binds to two
# rows for one path.
_first_sighting() { # <seen-file> <path>
    if grep -qxF "$2" "$1" 2> /dev/null; then
        return 1
    fi
    printf '%s\n' "$2" >> "$1"
    return 0
}

# --- inventory -------------------------------------------------------------
#
# Candidate selection, shared by planning and removal so the two can never
# disagree about what is in scope. Every filter here is a reason a path is
# *excluded*; nothing widens the set. The order is deliberate — cheap
# structural tests first, then exclusion, then the age walk, which is the only
# expensive one.
#
# The inventory hook reports every phase in one pass, including phases this
# invocation will not run, so an operator sees the whole picture rather than
# only the slice they asked about.
_collect_candidates() {
    # _collect_candidates <phase> — prints "type<TAB>bytes<TAB>path" per line.
    local phase="$1" cutoff pat considered=0 unevaluable=0
    cutoff=$(($(date +%s) - $(_age_to_seconds "$OLDER_THAN")))
    local enum_ok=1

    # Catalog patterns overlap by design — a name can match several — so the
    # matches are pooled and deduplicated by path before anything is counted.
    # Counting per pattern would report one directory twice, and the removal
    # acknowledgement would then bind to two rows for one path.
    local seen_file
    seen_file=$(mktemp) || {
        printf '%s0 0 0\n' "$_STAT_PREFIX"
        return 0
    }

    case "$phase" in
        go-test-binaries)
            while IFS= read -r pat; do
                [[ -n "$pat" ]] || continue
                local f
                while IFS= read -r f; do
                    [[ -n "$f" ]] || continue
                    if [[ "$f" == "$_ENUM_FAIL_LINE" ]]; then
                        enum_ok=0
                        continue
                    fi
                    [[ -L "$f" ]] && continue
                    _first_sighting "$seen_file" "$f" || continue
                    considered=$((considered + 1))
                    _path_is_excluded "$f" && continue
                    case "$(_file_age_class "$f" "$cutoff")" in
                        older) : ;;
                        newer) continue ;;
                        *)
                            unevaluable=$((unevaluable + 1))
                            continue
                            ;;
                    esac
                    local b
                    b=$(_reclaim_bytes_of_file "$f") || {
                        unevaluable=$((unevaluable + 1))
                        continue
                    }
                    printf 'file\t%s\t%s\n' "$b" "$f"
                done < <(_enumerate "$RECLAIM_ROOT" -maxdepth 1 -type f -name "$pat")
            done < <(_catalog_patterns "$phase")
            ;;
        named-gocache)
            while IFS= read -r pat; do
                [[ -n "$pat" ]] || continue
                local d
                while IFS= read -r d; do
                    [[ -n "$d" ]] || continue
                    if [[ "$d" == "$_ENUM_FAIL_LINE" ]]; then
                        enum_ok=0
                        continue
                    fi
                    [[ -L "$d" ]] && continue
                    _first_sighting "$seen_file" "$d" || continue
                    considered=$((considered + 1))
                    _path_is_excluded "$d" && continue
                    case "$(_age_class "$d" "$cutoff")" in
                        older) : ;;
                        newer) continue ;;
                        *)
                            unevaluable=$((unevaluable + 1))
                            continue
                            ;;
                    esac
                    local b
                    b=$(reclaim_dir_bytes "$d" 2> /dev/null) || b="unknown"
                    printf 'dir\t%s\t%s\n' "$b" "$d"
                done < <(_enumerate "$RECLAIM_ROOT" -maxdepth 1 -type d -name "$pat")
            done < <(_catalog_patterns "$phase")
            ;;
    esac
    rm -f "$seen_file"
    printf '%s%s %s %s\n' "$_STAT_PREFIX" "$considered" "$unevaluable" "$enum_ok"
}

_reclaim_bytes_of_file() {
    local out
    out=$(stat -f '%z' "$1" 2> /dev/null || stat -c '%s' "$1" 2> /dev/null)
    [[ "$out" =~ ^[0-9]+$ ]] || return 1
    printf '%s\n' "$out"
}

hook_inventory() {
    local phase bytes n line sz considered unevaluable enum_ok basis method bytes_unknown
    for phase in go-test-binaries named-gocache; do
        bytes=0
        n=0
        considered=0
        unevaluable=0
        enum_ok=1
        bytes_unknown=0
        while IFS= read -r line; do
            [[ -n "$line" ]] || continue
            if [[ "$line" == "$_STAT_PREFIX"* ]]; then
                read -r considered unevaluable enum_ok <<< "${line#"$_STAT_PREFIX"}"
                continue
            fi
            n=$((n + 1))
            sz=$(printf '%s' "$line" | cut -f2)
            if [[ "$sz" == "unknown" ]]; then
                bytes_unknown=$((bytes_unknown + 1))
            else
                bytes=$((bytes + sz))
            fi
        done < <(_collect_candidates "$phase")
        basis=$(_coverage_basis "$unevaluable" "$enum_ok")
        method=$(_coverage_method)
        reclaim_emit "inventory" phase "$phase" candidates "$n" bytes "$bytes" \
            human "$(human_size "$bytes")" bytes_unmeasured "$bytes_unknown" \
            older_than "$OLDER_THAN" \
            basis "$basis" method "$method" volume_unit "paths" \
            volume_observed "$((considered - unevaluable))" \
            volume_expected "$considered" volume_unevaluable "$unevaluable" \
            volume_is_lower_bound "$([[ "$enum_ok" -eq 1 ]] && printf 'false' || printf 'true')"
    done

    # The session plane is reported so an operator sees the whole picture, and
    # is explicitly marked as not executable here rather than quietly absent.
    local scratch=0 scratch_unmeasured=0
    if [[ -d "$RECLAIM_ROOT" ]]; then
        local d
        while IFS= read -r d; do
            [[ -n "$d" ]] || continue
            local sb
            if sb=$(reclaim_dir_bytes "$d" 2> /dev/null); then
                scratch=$((scratch + sb))
            else
                scratch_unmeasured=$((scratch_unmeasured + 1))
            fi
        done < <(find "$RECLAIM_ROOT" -maxdepth 1 -type d -name "claude-*" -user "$(id -u)" 2> /dev/null)
    fi
    reclaim_emit "inventory" phase "agent-scratch" bytes "$scratch" \
        human "$(human_size "$scratch")" bytes_unmeasured "$scratch_unmeasured" \
        detail "select --phase agent-scratch to plan against the per-user agent root"

    local ids=0 id
    while IFS= read -r id; do
        [[ -n "$id" ]] && ids=$((ids + 1))
    done < <(_excluded_ids)
    reclaim_emit "inventory" phase "exclusions" count "$ids" \
        detail "session ids excluded from every phase (environment + flags)"
    return 0
}

# --- phases ----------------------------------------------------------------
#
# Each phase follows the same shape: collect, report, and — only under
# --execute — acknowledge, then remove one candidate at a time, re-establishing
# that candidate's eligibility immediately before acting on it.
#
# Removal is deliberately a loop of individually authorized acts rather than a
# bulk operation. A single recursive removal over a collected list would act on
# evidence gathered minutes earlier; doing it per candidate keeps the gap
# between deciding and acting as small as the work allows, and lets a single
# refused path be skipped and counted instead of failing the run.
#
# Counts are kept distinct throughout: candidates, deleted, skipped (refused at
# mutation time) and denied (the filesystem refused) mean different things and
# are never summed into a single number.
_run_file_phase() {
    # _run_file_phase <phase> <expected-type>
    local phase="$1" want="$2"
    local -a paths=() types=()
    local line n=0 bytes=0 bytes_unknown=0

    local considered=0 unevaluable=0 enum_ok=1
    while IFS= read -r line; do
        [[ -n "$line" ]] || continue
        if [[ "$line" == "$_STAT_PREFIX"* ]]; then
            read -r considered unevaluable enum_ok <<< "${line#"$_STAT_PREFIX"}"
            continue
        fi
        types+=("$(printf '%s' "$line" | cut -f1)")
        local sz
        sz=$(printf '%s' "$line" | cut -f2)
        if [[ "$sz" == "unknown" ]]; then
            bytes_unknown=$((bytes_unknown + 1))
        else
            bytes=$((bytes + sz))
        fi
        paths+=("$(printf '%s' "$line" | cut -f3-)")
        n=$((n + 1))
    done < <(_collect_candidates "$phase")
    local basis method lower_bound
    basis=$(_coverage_basis "$unevaluable" "$enum_ok")
    method=$(_coverage_method)
    lower_bound=$([[ "$enum_ok" -eq 1 ]] && printf 'false' || printf 'true')

    if [[ "$RECLAIM_MODE" != "execute" ]]; then
        reclaim_emit "phase" phase "$phase" status pending candidates "$n" \
            bytes "$bytes" human "$(human_size "$bytes")" bytes_unmeasured "$bytes_unknown" \
            older_than "$OLDER_THAN" \
            basis "$basis" method "$method" volume_unit "paths" volume_observed "$((considered - unevaluable))" \
            volume_expected "$considered" volume_unevaluable "$unevaluable" \
            volume_is_lower_bound "$lower_bound" \
            next_required_operation "re-run with --execute --confirm-delete-files $n" \
            note "irreversible; --execute needs --confirm-delete-files $n"
        return 0
    fi

    if [[ "$n" -eq 0 ]]; then
        reclaim_emit "phase" phase "$phase" status skipped detail "no candidates"
        return 0
    fi

    # The acknowledgement binds to this run's candidate set. A count agreed
    # against an earlier plan is not evidence about this one: the tree moves.
    [[ -n "$CONFIRM_DELETE_FILES" ]] ||
        reclaim_die "$RECLAIM_EX_USAGE" "$phase --execute needs --confirm-delete-files $n (irreversible)"
    [[ "$CONFIRM_DELETE_FILES" == "$n" ]] ||
        reclaim_die "$RECLAIM_EX_USAGE" \
            "--confirm-delete-files $CONFIRM_DELETE_FILES != candidate count $n (re-run the plan)"

    local deleted=0 freed=0 denied=0 skipped=0 i
    local sk_not_owned=0 sk_type_changed=0 sk_containment=0 sk_vanished=0 sk_unknown=0
    local freed_unmeasured=0
    for i in "${!paths[@]}"; do
        local target="${paths[$i]}"

        # Authorize on this candidate's own evidence, now, not at plan time.
        if ! reclaim_authorize_candidate "$target" "$want"; then
            reclaim_emit "item" phase "$phase" path "$target" status skipped \
                error_class "auth" error_kind "candidate-refused" detail "$RECLAIM_ERR"
            skipped=$((skipped + 1))
            case "$RECLAIM_ERR_KIND" in
                vanished) sk_vanished=$((sk_vanished + 1)) ;;
                type-changed | symlink) sk_type_changed=$((sk_type_changed + 1)) ;;
                containment | device | device-unknown) sk_containment=$((sk_containment + 1)) ;;
                ownership | ownership-unknown) sk_not_owned=$((sk_not_owned + 1)) ;;
                *) sk_unknown=$((sk_unknown + 1)) ;;
            esac
            continue
        fi

        # bytes_reclaimed is computed from this probe, so the unmeasured
        # counter reported alongside it must come from this probe too.
        # Reporting a collection-time gap next to a mutation-time total lets
        # either direction lie: a measured zero with nothing marked unmeasured,
        # or a marked gap beside a total that was in fact fully measured.
        local sz=0 sz_known=1
        if [[ "$want" == "file" ]]; then
            sz=$(_reclaim_bytes_of_file "$target" 2> /dev/null) || sz_known=0
        else
            sz=$(reclaim_dir_bytes "$target" 2> /dev/null) || sz_known=0
        fi
        if [[ "$sz_known" -eq 0 ]]; then
            freed_unmeasured=$((freed_unmeasured + 1))
            sz=0
        fi

        local rc=0
        if [[ "$want" == "file" ]]; then
            rm -f "$target" 2> /dev/null || rc=$?
        else
            rm -rf "$target" 2> /dev/null || rc=$?
        fi

        if [[ "$rc" -ne 0 ]]; then
            # In a sticky directory a refusal means the name is not ours to
            # remove. It is never a success and it is never retried.
            reclaim_emit "item" phase "$phase" path "$target" status failed \
                error_class "auth" error_kind "unlink-denied" detail "rm exit $rc"
            denied=$((denied + 1))
            reclaim_record_failure 1
            continue
        fi

        deleted=$((deleted + 1))
        freed=$((freed + sz))
    done

    # Counts reconcile: candidates = deleted + delete_failed + skipped, and the
    # skip total is broken out by reason rather than described in prose, so a
    # consumer can tell an ownership refusal from a vanished path without
    # parsing English.
    reclaim_emit "phase" phase "$phase" status complete \
        candidates "$n" deleted "$deleted" delete_failed "$denied" \
        skipped "$skipped" skipped_not_owned "$sk_not_owned" \
        skipped_type_changed "$sk_type_changed" skipped_containment "$sk_containment" \
        skipped_vanished "$sk_vanished" skipped_unknown "$sk_unknown" \
        bytes_reclaimed "$freed" human "$(human_size "$freed")" \
        bytes_unmeasured "$freed_unmeasured" \
        basis "$basis" method "$method" volume_unit "paths" volume_observed "$((considered - unevaluable))" \
        volume_expected "$considered" volume_unevaluable "$unevaluable" \
        volume_is_lower_bound "$lower_bound" \
        next_required_operation "none"
    return 0
}

# Session candidates are the UUID-named directories two levels below the
# per-user agent root: <project-key>/<session-uuid>. The UUID grammar is the
# filter, not the depth alone — the root also holds directories that are not
# project keys at all, and a depth rule by itself would sweep them in.
_collect_sessions() {
    local cutoff
    cutoff=$(($(date +%s) - $(_age_to_seconds "$OLDER_THAN")))

    local d base considered=0 unevaluable=0 enum_ok=1
    while IFS= read -r d; do
        [[ -n "$d" ]] || continue
        if [[ "$d" == "$_ENUM_FAIL_LINE" ]]; then
            enum_ok=0
            continue
        fi
        [[ -L "$d" ]] && continue
        base="${d##*/}"
        _id_is_well_formed "$base" || continue
        considered=$((considered + 1))
        _path_is_excluded "$d" && continue

        case "$(_age_class "$d" "$cutoff")" in
            older) : ;;
            newer) continue ;;
            *)
                # Age could not be established. Not a candidate, and not a
                # silent omission either: it is a hole in what this run knows.
                unevaluable=$((unevaluable + 1))
                continue
                ;;
        esac

        # The shared library reports a size failure rather than a confirmed
        # zero, and that distinction must survive here: a tree whose size could
        # not be read is not a tree of no size. Eligibility is unaffected —
        # size is not an admission criterion — but the byte total becomes a
        # lower bound and the record says so.
        local b
        b=$(reclaim_dir_bytes "$d" 2> /dev/null) || b="unknown"
        printf 'dir\t%s\t%s\n' "$b" "$d"
    done < <(_enumerate "$RECLAIM_ROOT" -mindepth 2 -maxdepth 2 -type d)
    printf '%s%s %s %s\n' "$_STAT_PREFIX" "$considered" "$unevaluable" "$enum_ok"
}

# A stable digest of the exact candidate set. Sorted so it does not depend on
# traversal order, and over the paths themselves so a same-count substitution
# changes it. Count equality is not membership equality: two read-only plans
# seconds apart on a live host moved from 67 candidates to 68, which is what
# binding to a number rather than to a set actually permits.
_membership_digest() {
    local sorted
    sorted=$(printf '%s\n' "$@" | LC_ALL=C sort)
    if command -v shasum > /dev/null 2>&1; then
        printf '%s' "$sorted" | shasum -a 256 | cut -c1-16
    elif command -v sha256sum > /dev/null 2>&1; then
        printf '%s' "$sorted" | sha256sum | cut -c1-16
    else
        # No hash available: emit a value that cannot match any acknowledgement,
        # so the phase refuses rather than binding to something weaker.
        printf 'unavailable'
    fi
}

# Concrete liveness evidence, gathered once for the whole root rather than per
# candidate. This is a backstop and it is known to be incomplete — a seat idle
# between tool calls holds nothing — but incompleteness is a reason to keep it
# non-bypassable, not a reason to omit it. Accepting the unknowable live set
# must never be permission to ignore a hold we can actually see.
# Returns 0 held, 1 not held (proven), 2 unknown.
#
# Absence of evidence is not evidence of absence. A missing lsof, a probe that
# errored, or a tool we could not run means we do not know whether anything is
# using this tree — and "unknown" must skip, exactly as it does for age and
# ownership. Suppressing probe failure would turn every machine without lsof
# into one where nothing is ever held.
#
# Probed per candidate at the moment of removal rather than once for the whole
# root, because a snapshot taken before a long delete loop is stale by the time
# the loop reaches its later entries — which are precisely the ones a newly
# started seat is most likely to be using.
_probe_live_hold() {
    local target="$1"
    local lsof_verdict=2 ps_verdict=2

    if command -v lsof > /dev/null 2>&1; then
        local errfile out rc=0
        errfile=$(mktemp) || return 2
        out=$(lsof -Fn +D "$target" 2> "$errfile") || rc=$?
        local had_err=0
        if [[ -s "$errfile" ]]; then
            had_err=1
        fi
        rm -f "$errfile"
        if [[ -n "$out" ]]; then
            return 0 # an open file under the tree; nothing else matters
        fi
        # lsof exits 1 with no output when nothing matched, which is a real
        # negative. Any other status, or noise on stderr, is not.
        if [[ "$rc" -le 1 && "$had_err" -eq 0 ]]; then
            lsof_verdict=1
        fi
    fi

    if command -v ps > /dev/null 2>&1; then
        local args rc2=0
        args=$(ps -Ao args= 2> /dev/null) || rc2=$?
        if [[ "$rc2" -eq 0 ]]; then
            if printf '%s\n' "$args" | grep -qF "$target" 2> /dev/null; then
                return 0
            fi
            ps_verdict=1
        fi
    fi

    # Clear only when *every* dimension was available and answered clear. The
    # probes are complementary, not redundant: lsof covers open files and
    # working directories, ps covers argv. A clean argv scan says nothing about
    # whether a file is open, so ORing them lets one dimension vouch for a
    # dimension nobody measured — which is how "no evidence" becomes "no hold".
    if [[ "$lsof_verdict" -eq 1 && "$ps_verdict" -eq 1 ]]; then
        return 1
    fi
    return 2
}

# Re-establish every eligibility fact immediately before removal.
#
# Collection happened earlier and the tree is live. Ownership and type are
# rechecked by reclaim_authorize_candidate; this covers the rest: still in the
# frozen plan, still excluded-or-not, still stale, still unheld. A session that
# became active after collection must survive, and neither acknowledgement may
# override that — the acknowledgements cover the set we could not observe, not
# the evidence we can.
_ELIG_KIND=""
_candidate_still_eligible() {
    local target="$1" cutoff="$2"
    RECLAIM_ERR=""
    # The machine answer. Classifying by matching the human message would break
    # the moment the wording improved, and it already misfiled two cases.
    _ELIG_KIND=""

    if _path_is_excluded "$target"; then
        RECLAIM_ERR="excluded by session id at mutation time"
        _ELIG_KIND="excluded"
        return 1
    fi
    _probe_live_hold "$target"
    case "$?" in
        0)
            RECLAIM_ERR="a process holds a path under this session"
            _ELIG_KIND="live-hold"
            return 1
            ;;
        1) : ;;
        *)
            RECLAIM_ERR="could not establish whether anything is using this session"
            _ELIG_KIND="probe-unknown"
            return 1
            ;;
    esac
    case "$(_age_class "$target" "$cutoff")" in
        older) : ;;
        newer)
            RECLAIM_ERR="written to since the plan was taken"
            _ELIG_KIND="freshened"
            return 1
            ;;
        *)
            RECLAIM_ERR="age could not be established (unreadable subtree)"
            _ELIG_KIND="age-unknown"
            return 1
            ;;
    esac
    return 0
}

# Credential-shaped names, annotated and counted, never withheld.
#
# Withholding a .env from an age-qualified abandoned session would leave the
# credential on disk indefinitely, which is a worse security outcome than
# removing it with the rest of the tree. The operator is told it is there; the
# decision stays theirs. No file is ever opened to decide this — the name is the
# only evidence used.
_caution_count() {
    local target="$1" n=0 f
    while IFS= read -r f; do
        [[ -n "$f" ]] && n=$((n + 1))
    done < <(find "$target" -type f \( \
        -name '.env' -o -name '.env.*' -o -name '*.pem' -o -name 'id_rsa' \
        -o -name '*.p12' -o -name '*credentials*' -o -name '*token*' -o -name '.netrc' \
        \) 2> /dev/null)
    printf '%s\n' "$n"
}

phase_agent_scratch() {
    local -a paths=()
    local line n=0 bytes=0 bytes_unknown=0 caution=0 cutoff
    cutoff=$(($(date +%s) - $(_age_to_seconds "$OLDER_THAN")))

    # Built here, in the parent. Collection runs inside a process substitution,
    # and a filename assigned in that subshell is invisible to the cleanup that
    # has to remove it.
    _make_cutoff_ref "$cutoff" ||
        reclaim_die "$RECLAIM_EX_ERROR" "cannot establish an age reference" \
            "check that the temporary directory is writable"

    local considered=0 unevaluable=0 enum_ok=1
    while IFS= read -r line; do
        [[ -n "$line" ]] || continue
        if [[ "$line" == "$_STAT_PREFIX"* ]]; then
            read -r considered unevaluable enum_ok <<< "${line#"$_STAT_PREFIX"}"
            continue
        fi
        local sz
        sz=$(printf '%s' "$line" | cut -f2)
        if [[ "$sz" == "unknown" ]]; then
            bytes_unknown=$((bytes_unknown + 1))
        else
            bytes=$((bytes + sz))
        fi
        paths+=("$(printf '%s' "$line" | cut -f3-)")
        n=$((n + 1))
    done < <(_collect_sessions)
    local basis method lower_bound
    basis=$(_coverage_basis "$unevaluable" "$enum_ok")
    method=$(_coverage_method)
    lower_bound=$([[ "$enum_ok" -eq 1 ]] && printf 'false' || printf 'true')

    local p
    for p in ${paths[@]+"${paths[@]}"}; do
        caution=$((caution + $(_caution_count "$p")))
    done

    local digest="empty"
    [[ "$n" -gt 0 ]] && digest=$(_membership_digest ${paths[@]+"${paths[@]}"})

    # Exclusion reporting, split by source. One record saying
    # "environment+flags" cannot tell an auditor whether the seat protected
    # itself or whether an operator typed an id, and those are different facts.
    local auto_tokens="" auto_n=0 exp_tokens="" exp_n=0 id
    while IFS= read -r id; do
        [[ -n "$id" ]] || continue
        auto_tokens+="${auto_tokens:+,}${id:0:8}"
        auto_n=$((auto_n + 1))
    done < <(_auto_ids)
    while IFS= read -r id; do
        [[ -n "$id" ]] || continue
        exp_tokens+="${exp_tokens:+,}${id:0:8}"
        exp_n=$((exp_n + 1))
    done < <(_explicit_ids)

    reclaim_emit "exclusion" phase "agent-scratch" \
        auto_count "$auto_n" auto_tokens "${auto_tokens:-none}" \
        explicit_count "$exp_n" explicit_tokens "${exp_tokens:-none}" \
        detail "session ids protected from this phase"

    if [[ -n "${TMP_RESIDUE_TEST_TEMP_ROOT:-}" ]]; then
        reclaim_emit "exclusion" phase "agent-scratch" \
            detail "TEST SEAM ACTIVE: approved temp roots were widened for fixtures"
    fi

    if [[ "$RECLAIM_MODE" != "execute" ]]; then
        # The operator cannot review a set they were never shown. Paths are
        # local-only output; they encode home layout and project names, which is
        # why the record marks them as source structure rather than as something
        # to paste into a ticket.
        for p in ${paths[@]+"${paths[@]}"}; do
            reclaim_emit "item" phase "agent-scratch" path "$p" status pending \
                disclosure "source_structure"
        done
        reclaim_emit "phase" phase "agent-scratch" status pending candidates "$n" \
            bytes "$bytes" human "$(human_size "$bytes")" bytes_unmeasured "$bytes_unknown" \
            older_than "$OLDER_THAN" \
            caution_names "$caution" membership "$digest" disclosure "source_structure" \
            basis "$basis" method "$method" volume_unit "sessions" volume_observed "$((considered - unevaluable))" \
            volume_expected "$considered" volume_unevaluable "$unevaluable" \
            volume_is_lower_bound "$lower_bound" \
            next_required_operation "re-run with --execute --confirm-delete-sessions $n --confirm-membership $digest --accept-incomplete-live-set" \
            note "irreversible; --execute needs --confirm-delete-sessions $n --confirm-membership $digest and --accept-incomplete-live-set"
        return 0
    fi

    if [[ "$n" -eq 0 ]]; then
        reclaim_emit "phase" phase "agent-scratch" status skipped detail "no candidates"
        return 0
    fi

    [[ -n "$CONFIRM_DELETE_SESSIONS" ]] ||
        reclaim_die "$RECLAIM_EX_USAGE" \
            "agent-scratch --execute needs --confirm-delete-sessions $n (irreversible)"
    [[ "$CONFIRM_DELETE_SESSIONS" == "$n" ]] ||
        reclaim_die "$RECLAIM_EX_USAGE" \
            "--confirm-delete-sessions $CONFIRM_DELETE_SESSIONS != candidate count $n (re-run the plan)"

    # Membership, not just cardinality. A set of the same size is not the same
    # set, and on a live host the difference appears within seconds.
    [[ -n "$CONFIRM_MEMBERSHIP" ]] ||
        reclaim_die "$RECLAIM_EX_USAGE" \
            "agent-scratch --execute needs --confirm-membership $digest (binds to the reviewed set, not its size)"
    [[ "$CONFIRM_MEMBERSHIP" == "$digest" ]] ||
        reclaim_die "$RECLAIM_EX_USAGE" \
            "--confirm-membership $CONFIRM_MEMBERSHIP != this run's set $digest (the candidate set changed; re-run the plan)"

    # The set is frozen here. Everything below acts only on these paths, and
    # each is re-probed for holds as it is reached.
    local deleted=0 freed=0 denied=0 skipped=0 i
    local sk_excluded=0 sk_live_hold=0 sk_freshened=0 sk_unknown=0
    local sk_not_owned=0 sk_type_changed=0 sk_containment=0 sk_vanished=0
    local freed_unmeasured=0
    for i in "${!paths[@]}"; do
        local target="${paths[$i]}"

        if ! _candidate_still_eligible "$target" "$cutoff"; then
            reclaim_emit "item" phase "agent-scratch" path "$target" status skipped \
                error_class "integrity" error_kind "no-longer-eligible" detail "$RECLAIM_ERR"
            skipped=$((skipped + 1))
            # Why a session survived is the most useful thing in this record. A
            # consumer deciding whether to retry needs to tell "someone is using
            # it" from "we could not tell" from "it was protected", and those
            # are different answers.
            case "$_ELIG_KIND" in
                excluded) sk_excluded=$((sk_excluded + 1)) ;;
                live-hold) sk_live_hold=$((sk_live_hold + 1)) ;;
                freshened) sk_freshened=$((sk_freshened + 1)) ;;
                *) sk_unknown=$((sk_unknown + 1)) ;;
            esac
            continue
        fi
        if ! reclaim_authorize_candidate "$target" dir; then
            reclaim_emit "item" phase "agent-scratch" path "$target" status skipped \
                error_class "auth" error_kind "candidate-refused" detail "$RECLAIM_ERR"
            skipped=$((skipped + 1))
            case "$RECLAIM_ERR_KIND" in
                vanished) sk_vanished=$((sk_vanished + 1)) ;;
                type-changed | symlink) sk_type_changed=$((sk_type_changed + 1)) ;;
                containment | device | device-unknown) sk_containment=$((sk_containment + 1)) ;;
                ownership | ownership-unknown) sk_not_owned=$((sk_not_owned + 1)) ;;
                *) sk_unknown=$((sk_unknown + 1)) ;;
            esac
            continue
        fi

        local sz=0 sz_known=1
        sz=$(reclaim_dir_bytes "$target" 2> /dev/null) || sz_known=0
        if [[ "$sz_known" -eq 0 ]]; then
            freed_unmeasured=$((freed_unmeasured + 1))
            sz=0
        fi

        local rc=0
        rm -rf "$target" 2> /dev/null || rc=$?
        if [[ "$rc" -ne 0 ]]; then
            reclaim_emit "item" phase "agent-scratch" path "$target" status failed \
                error_class "auth" error_kind "unlink-denied" detail "rm exit $rc"
            denied=$((denied + 1))
            reclaim_record_failure 1
            continue
        fi

        deleted=$((deleted + 1))
        freed=$((freed + sz))
    done
    reclaim_emit "phase" phase "agent-scratch" status complete \
        candidates "$n" deleted "$deleted" delete_failed "$denied" \
        skipped "$skipped" skipped_excluded "$sk_excluded" skipped_live_hold "$sk_live_hold" \
        skipped_freshened "$sk_freshened" skipped_unknown "$sk_unknown" \
        skipped_not_owned "$sk_not_owned" skipped_type_changed "$sk_type_changed" \
        skipped_containment "$sk_containment" skipped_vanished "$sk_vanished" \
        bytes_reclaimed "$freed" human "$(human_size "$freed")" \
        bytes_unmeasured "$freed_unmeasured" \
        caution_names "$caution" membership "$digest" disclosure "source_structure" \
        basis "$basis" method "$method" volume_unit "sessions" volume_observed "$((considered - unevaluable))" \
        volume_expected "$considered" volume_unevaluable "$unevaluable" \
        volume_is_lower_bound "$lower_bound" \
        next_required_operation "none"
    return 0
}

hook_phase() {
    case "$RECLAIM_PHASE" in
        go-test-binaries) _run_file_phase go-test-binaries file ;;
        named-gocache) _run_file_phase named-gocache dir ;;
        agent-scratch) phase_agent_scratch ;;
        all | "")
            reclaim_emit "phase" phase "all" status skipped \
                detail "plan-only overview; name one phase to act"
            ;;
        *) reclaim_die "$RECLAIM_EX_USAGE" "unknown phase: $RECLAIM_PHASE" ;;
    esac
    return 0
}

# --- wiring ----------------------------------------------------------------
#
# The harness owns the run: it parses shared flags, emits the header, admits the
# root, runs the inventory and the gate, dispatches to one phase, and emits the
# terminal record. A profile contributes only these hooks and its registered
# checks, which is what keeps every reclaim script behaving identically.
#
# hook_args runs before admission, so it is the only place a phase-dependent
# root or class can be chosen; a phase must not be able to widen its own
# boundary once the lifecycle has admitted one.
hook_args() {
    local rest=("$@")
    local i=0 v
    while [[ $i -lt ${#rest[@]} ]]; do
        local flag="${rest[$i]}"
        case "$flag" in
            --accept-incomplete-live-set)
                ACCEPT_INCOMPLETE_LIVE_SET=1
                i=$((i + 1))
                ;;
            --root | --older-than | --exclude-session-id | --confirm-delete-files | \
                --confirm-delete-sessions | --confirm-membership | --catalog)
                if ! v=$(_arg_value "$flag" "$((i + 1))" ${rest[@]+"${rest[@]}"}); then
                    reclaim_die "$RECLAIM_EX_USAGE" "$flag needs a value"
                fi
                case "$flag" in
                    --root) ROOT_REQUEST="$v" ;;
                    --older-than) OLDER_THAN="$v" ;;
                    --exclude-session-id) EXCLUDE_IDS="${EXCLUDE_IDS:+$EXCLUDE_IDS,}$v" ;;
                    --confirm-delete-files) CONFIRM_DELETE_FILES="$v" ;;
                    --confirm-delete-sessions) CONFIRM_DELETE_SESSIONS="$v" ;;
                    --confirm-membership) CONFIRM_MEMBERSHIP="$v" ;;
                    --catalog) CATALOG_FILE="$v" ;;
                esac
                i=$((i + 2))
                ;;
            -h | --help)
                usage
                exit "$RECLAIM_EX_OK"
                ;;
            *) reclaim_die "$RECLAIM_EX_USAGE" "unknown arg: $flag (try --help)" ;;
        esac
    done

    _age_to_seconds "$OLDER_THAN" > /dev/null ||
        reclaim_die "$RECLAIM_EX_USAGE" "invalid --older-than '$OLDER_THAN' (use Nd|Nw|Nm|Ny)"
    if [[ -n "$CONFIRM_DELETE_FILES" && ! "$CONFIRM_DELETE_FILES" =~ ^[0-9]+$ ]]; then
        reclaim_die "$RECLAIM_EX_USAGE" "--confirm-delete-files needs a count"
    fi
    if [[ -n "$CONFIRM_DELETE_SESSIONS" && ! "$CONFIRM_DELETE_SESSIONS" =~ ^[0-9]+$ ]]; then
        reclaim_die "$RECLAIM_EX_USAGE" "--confirm-delete-sessions needs a count"
    fi

    # The session phase narrows both the root and its class. Done here because
    # the lifecycle admits the root before any phase code runs, and because a
    # phase must not be able to widen its own boundary later.
    if [[ "$RECLAIM_PHASE" == "agent-scratch" ]]; then
        _assert_seam_not_executable
        _assert_approved_temp_parent "$ROOT_REQUEST"
        ROOT_REQUEST="$ROOT_REQUEST/$SESSION_ROOT_NAME"
        reclaim_set_root_class "user-temp"
    fi
    if [[ -n "$CATALOG_FILE" && ! -f "$CATALOG_FILE" ]]; then
        reclaim_die "$RECLAIM_EX_USAGE" "--catalog file not found: $CATALOG_FILE"
    fi

    RECLAIM_ROOT_REQUEST="$ROOT_REQUEST"
    return 0
}

# Every later path derives from the canonical admitted root, never from the
# operator's request string.
hook_bind() {
    # After the harness has installed its own EXIT trap, so ours composes with
    # it rather than being replaced by it.
    _install_lifecycle_cleanup
    return 0
}

reclaim_register_check no-active-go-build 1 "go-test-binaries,named-gocache" _ck_no_active_go_build
reclaim_register_check auto-self-id-known 0 "*" _ck_auto_self_id_known
reclaim_register_check explicit-exclusions-present 0 "*" _ck_explicit_exclusions_present
reclaim_register_check live-set-complete 0 "agent-scratch" _ck_live_set_complete
reclaim_register_check live-set-risk-accepted 1 "agent-scratch" _ck_live_set_risk_accepted

reclaim_register_hook args hook_args
reclaim_register_hook bind hook_bind
reclaim_register_hook inventory hook_inventory
reclaim_register_hook phase hook_phase
reclaim_register_hook usage usage

# Test seam: sourcing with this set loads the functions without running a
# lifecycle, so eligibility logic can be exercised directly. It grants no
# capability — it only declines to start.
if [[ -z "${TMP_RESIDUE_LIB_ONLY:-}" ]]; then
    reclaim_main "$@"
fi

#!/usr/bin/env bash
# reclaim-lib.sh — sourced harness for spanwit reclaim-ops scripts.
#
# This is NOT a standalone script; a reclaim tool sources it, registers a
# profile + named preflight checks + whitelisted hooks, and calls reclaim_main.
# The library owns everything that must be identical across every reclaim script
# so we get one I/O shape for N scripts, not N dialects:
#
#   - mode/format handling (--plan default | --preflight | --execute; text|json)
#   - exactly-one-phase enforcement for mutation
#   - a named preflight registry with a fail-closed gate (outcome × basis)
#   - the whole run lifecycle: header-first, ordered records, a terminal record
#     on every exit path, and failure propagation to the exit code
#   - typed scalar emission (a declared field dictionary, not all-strings)
#   - standard exit classes
#   - strict path admission + containment + credential disjointness, including
#     destinations that do not exist yet (admit before mkdir)
#
# Contract: reclaim-ops I/O contract. The gate
# rule is deliberately fail-closed: --execute runs a phase only when every
# required check is outcome=pass AND basis=confirmed. Uncertainty blocks.
#
# Lifecycle status vocabulary (ADR-0005): pending | running | complete | failed |
# blocked | skipped. Profiles must not invent statuses outside that set.

# --- exit classes (public API; consumed by sourcing scripts) --------------
# shellcheck disable=SC2034  # used by scripts that source this library
readonly RECLAIM_EX_OK=0
readonly RECLAIM_EX_USAGE=64     # bad invocation
readonly RECLAIM_EX_ADMISSION=65 # a path failed admission
readonly RECLAIM_EX_ERROR=70     # runtime error during a phase
# shellcheck disable=SC2034  # used by scripts that source this library
readonly RECLAIM_EX_BLOCKED=75 # preflight blocked; resolve then retry

# --- shared state ---------------------------------------------------------
RECLAIM_CONTRACT="spanwit.reclaim-ops/v0"
RECLAIM_MODE="plan"      # plan | preflight | execute
RECLAIM_FORMAT="text"    # text | json
RECLAIM_PHASE=""         # set by --phase; "" means default-none
RECLAIM_PROFILE_ID=""    # set by the tool via reclaim_set_profile
RECLAIM_ROOT_REQUEST=""  # the root the operator asked for (pre-admission)
RECLAIM_ROOT=""          # admitted, canonical data root
RECLAIM_ROOT_DEV=""      # device id of the admitted root
RECLAIM_AUTH_PATH=""     # never-touch credential path under the root
RECLAIM_REST=()          # args reclaim_parse_common did not consume
RECLAIM_HEADER_EMITTED=0 # header is always the first record
RECLAIM_TERMINAL_EMITTED=0
RECLAIM_FAILURES=0     # profile-reported operation failures
RECLAIM_ERR=""         # last failure message from a non-dying helper
RECLAIM_ERR_KIND=""    # stable machine reason for that failure, never prose
RECLAIM_CANON=""       # canonical path set by reclaim_within_root
RECLAIM_DEST=""        # canonical destination set by reclaim_admit_dest_*
RECLAIM_EXPORT_DEST="" # canonical export dir set by reclaim_admit_export_dir
RECLAIM_ERROR_CLASS="" # canonical class override for the next terminal record

# profile hooks (set via reclaim_register_hook)
RECLAIM_HOOK_ARGS=""
RECLAIM_HOOK_BIND=""
RECLAIM_HOOK_INVENTORY=""
RECLAIM_HOOK_PHASE=""
RECLAIM_HOOK_USAGE=""

# preflight registry (parallel arrays; bash 3.2-compatible for macOS)
RECLAIM_CHECK_NAMES=()
RECLAIM_CHECK_FNS=()
RECLAIM_CHECK_REQUIRED=()
RECLAIM_CHECK_PHASES=() # comma list of phases the check applies to, or "*"

# a check callback sets these before returning
RECLAIM_CHECK_OUTCOME="" # pass | fail | unknown
RECLAIM_CHECK_BASIS=""   # confirmed | inferred
RECLAIM_CHECK_DETAIL=""  # human one-liner
RECLAIM_CHECK_RESOLVE="" # how to clear a fail/unknown

# --- logging + emission ---------------------------------------------------
reclaim_log() { printf '%s\n' "$*" >&2; }

# JSON string escaper for the small, controlled value set we emit.
_reclaim_json_str() {
    local s="$1"
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    s="${s//$'\t'/\\t}"
    s="${s//$'\n'/\\n}"
    printf '"%s"' "$s"
}

# --- field dictionary -----------------------------------------------------
# Scalars must be typed in the JSON stream so a consumer can map them to
# spanwit integer fields without re-parsing strings. Any field not declared
# here is a string; a declared field whose value does not match its type is
# emitted as a string rather than producing invalid/misleading JSON.
_reclaim_field_type() {
    case "$1" in
        bytes | bytes_before | bytes_after | bytes_reclaimed | bytes_free | bytes_needed | \
            candidates | deleted | delete_failed | export_blocked | blockers | exit_code | \
            count | freed_now_bytes | wal_bytes | idle_days | \
            volume_observed | volume_expected | volume_unevaluable | \
            skipped | skipped_excluded | skipped_live_hold | skipped_unknown | \
            skipped_not_owned | skipped_vanished | skipped_freshened | \
            skipped_type_changed | skipped_containment | \
            bytes_unmeasured | caution_names | auto_count | explicit_count)
            printf 'int'
            ;;
        required | confirmed | volume_is_lower_bound)
            printf 'bool'
            ;;
        *) printf 'string' ;;
    esac
}

_reclaim_json_val() {
    local key="$1" val="$2" t
    t=$(_reclaim_field_type "$key")
    case "$t" in
        int)
            if [[ "$val" =~ ^-?[0-9]+$ ]]; then
                printf '%s' "$val"
                return
            fi
            ;;
        bool)
            case "$val" in
                1 | true)
                    printf 'true'
                    return
                    ;;
                0 | false)
                    printf 'false'
                    return
                    ;;
            esac
            ;;
    esac
    _reclaim_json_str "$val"
}

# reclaim_emit <record-type> [key value]...
# text mode: a human line to stderr. json mode: one object per line to stdout.
# stdout stays pure (json only) so the stream is machine-consumable.
reclaim_emit() {
    local rtype="$1"
    shift
    if [[ "$RECLAIM_FORMAT" == "json" ]]; then
        local out
        out="{\"contract\":$(_reclaim_json_str "$RECLAIM_CONTRACT"),"
        out+="\"record\":$(_reclaim_json_str "$rtype")"
        while [[ $# -ge 2 ]]; do
            out+=",$(_reclaim_json_str "$1"):$(_reclaim_json_val "$1" "$2")"
            shift 2
        done
        out+="}"
        printf '%s\n' "$out"
    else
        local line="[$rtype]"
        while [[ $# -ge 2 ]]; do
            line+=" $1=$2"
            shift 2
        done
        printf '%s\n' "$line" >&2
    fi
}

# Error vocabulary. `error_class` is the ADR-0005 canonical set
# (transient|throttled|auth|integrity|cancelled|unknown) so a consumer can
# project it; `error_kind` carries our local precision alongside it. Successful
# records carry neither — there is no "none" class.
_reclaim_error_kind() {
    case "$1" in
        "$RECLAIM_EX_USAGE") printf 'usage' ;;
        "$RECLAIM_EX_ADMISSION") printf 'admission' ;;
        "$RECLAIM_EX_BLOCKED") printf 'blocked' ;;
        *) printf 'runtime' ;;
    esac
}

_reclaim_error_class() {
    case "$1" in
        "$RECLAIM_EX_ADMISSION") printf 'auth' ;;    # refused at a policy boundary
        "$RECLAIM_EX_BLOCKED") printf 'cancelled' ;; # not attempted, deliberately
        *) printf 'unknown' ;;                       # no canonical class fits
    esac
}

# Emit the terminal record exactly once. Every exit path goes through here so a
# consumer can always project a final operation state from the stream alone.
reclaim_terminal() {
    # reclaim_terminal <status> <exit-code> [key value]...
    local status="$1" code="$2"
    shift 2
    [[ "$RECLAIM_TERMINAL_EMITTED" == "1" ]] && return 0
    RECLAIM_TERMINAL_EMITTED=1
    if [[ "$code" == "$RECLAIM_EX_OK" ]]; then
        reclaim_emit "terminal" \
            mode "$RECLAIM_MODE" \
            phase "${RECLAIM_PHASE:-none}" \
            status "$status" \
            exit_code "$code" \
            "$@"
    else
        reclaim_emit "terminal" \
            mode "$RECLAIM_MODE" \
            phase "${RECLAIM_PHASE:-none}" \
            status "$status" \
            exit_code "$code" \
            error_class "${RECLAIM_ERROR_CLASS:-$(_reclaim_error_class "$code")}" \
            error_kind "$(_reclaim_error_kind "$code")" \
            "$@"
    fi
    return 0
}

# reclaim_die <exit-code> <message> [next-required-operation] [canonical-class]
# Always emits a header (if not yet emitted) and a terminal record, so an early
# failure is still a well-formed, projectable stream rather than zero records.
#
# Never call this from inside a command substitution: the terminal record would
# be captured by the subshell instead of reaching the stream, and the exit would
# not propagate. Helpers that can fail return non-zero and set RECLAIM_ERR; the
# caller in the top-level shell decides to die.
reclaim_die() {
    local code="$1" msg="$2" next="${3:-}" class="${4:-}"
    _reclaim_emit_header_once
    local status="failed"
    [[ "$code" == "$RECLAIM_EX_BLOCKED" ]] && status="blocked"
    RECLAIM_ERROR_CLASS="${class:-$(_reclaim_error_class "$code")}"
    reclaim_terminal "$status" "$code" detail "$msg" next_required_operation "$next"
    printf 'error: %s\n' "$msg" >&2
    exit "$code"
}

# Backstop: any exit that did not route through reclaim_die/reclaim_terminal —
# an unhandled `set -e` failure, an unexpected signal — still produces exactly
# one terminal record. Command substitutions do not inherit this trap, so it
# cannot fire from a subshell.
_reclaim_exit_backstop() {
    local code="$1"
    [[ "$RECLAIM_TERMINAL_EMITTED" == "1" ]] && return 0
    _reclaim_emit_header_once
    if [[ "$code" == "0" ]]; then
        reclaim_terminal complete 0
    else
        reclaim_terminal failed "$code" \
            detail "run ended without a reported outcome" \
            next_required_operation "treat this run as incomplete; re-check state before retrying"
    fi
    return 0
}

# Profiles call this when an individual operation inside a phase fails; the
# harness maps a non-zero count to terminal status=failed and exit 70.
reclaim_record_failure() { RECLAIM_FAILURES=$((RECLAIM_FAILURES + ${1:-1})); }

# --- portable stat helpers (BSD/macOS and GNU) ----------------------------
_reclaim_stat() {
    # _reclaim_stat <fmt-char> <path>; fmt-char in {u owner, d device, m mtime,
    # i inode}. Uses lstat semantics (-f/-c do not follow the final symlink).
    local fc="$1" p="$2"
    case "$fc" in
        u) stat -f '%u' "$p" 2> /dev/null || stat -c '%u' "$p" 2> /dev/null ;;
        d) stat -f '%d' "$p" 2> /dev/null || stat -c '%d' "$p" 2> /dev/null ;;
        m) stat -f '%m' "$p" 2> /dev/null || stat -c '%Y' "$p" 2> /dev/null ;;
        i) stat -f '%i' "$p" 2> /dev/null || stat -c '%i' "$p" 2> /dev/null ;;
    esac
}

_reclaim_canon_dir() { (cd "$1" 2> /dev/null && pwd -P); }

# Permission bits of <path> as exactly four octal digits (special,user,group,
# other) — e.g. "1777" for the sticky temp plane, "0750" for a home directory.
# lstat semantics; empty when the platform will not answer, which callers must
# treat as unknown rather than as permissive.
_reclaim_mode() {
    local raw
    raw=$(stat -f '%p' "$1" 2> /dev/null || stat -c '%f' "$1" 2> /dev/null)
    [[ -n "$raw" ]] || return 1
    # BSD %p is octal with the file-type prefix; GNU %f is hex with the same.
    # Normalising through the low twelve bits gives one answer on both.
    local octal
    if [[ "$raw" =~ ^[0-7]+$ ]] && [[ ${#raw} -ge 4 ]]; then
        octal="${raw: -4}"
    else
        octal=$(printf '%04o' $(((0x$raw) & 07777)) 2> /dev/null) || return 1
    fi
    [[ "$octal" =~ ^[0-7]{4}$ ]] || return 1
    printf '%s\n' "$octal"
}

# available bytes on the volume holding <path>; empty + non-zero when df cannot
# answer, so a caller can report "unknown" instead of inventing a number
reclaim_free_bytes() {
    local out rc=0
    out=$(df -k "$1" 2> /dev/null) || rc=$?
    [[ "$rc" -eq 0 ]] || return 1
    out=$(printf '%s\n' "$out" | awk 'NR==2 {print $4 * 1024}')
    [[ "$out" =~ ^[0-9]+$ ]] || return 1
    printf '%s\n' "$out"
}

# Recursive byte size (du -sk -> bytes). 0 for an absent path is a real answer;
# a du failure (unreadable subtree, vanished path) is NOT — it returns empty and
# non-zero so the caller reports unknown rather than a confirmed zero.
reclaim_dir_bytes() {
    [[ -e "$1" ]] || {
        echo 0
        return 0
    }
    local out rc=0 kib
    out=$(du -sk "$1" 2> /dev/null) || rc=$?
    [[ "$rc" -eq 0 ]] || return 1
    kib=$(printf '%s\n' "$out" | awk 'END {print $1}')
    [[ "$kib" =~ ^[0-9]+$ ]] || return 1
    echo $((kib * 1024))
}

# reclaim_dir_bytes_or_unknown <path> -> echoes bytes, or "" when unknown.
# Convenience for report paths that must not fabricate a number.
reclaim_bytes_or_empty() {
    local b
    b=$(reclaim_dir_bytes "$1") || b=""
    printf '%s\n' "$b"
}

# --- path predicates (shared trust boundary) ------------------------------
# True when <path> or any ancestor component is a symlink. A symlinked ancestor
# means the name we admitted and the bytes we would mutate can diverge, so the
# admission decision would not bind the mutation.
reclaim_path_has_symlink() {
    local p="$1"
    [[ "$p" == /* ]] || return 0 # non-absolute: cannot reason; treat as unsafe
    while [[ -n "$p" && "$p" != "/" ]]; do
        [[ -L "$p" ]] && return 0
        p="${p%/*}"
    done
    return 1
}

# True when <canonical-dir> is a volume/mount root (its device differs from its
# parent's, or it is "/"). Reclaiming a mount root is never in scope.
reclaim_is_mount_root() {
    local d="$1"
    [[ "$d" == "/" ]] && return 0
    local parent="${d%/*}"
    [[ -z "$parent" ]] && return 0
    local dev pdev
    dev=$(_reclaim_stat d "$d")
    pdev=$(_reclaim_stat d "$parent")
    # missing evidence is uncertainty: fail closed by declaring it a mount root
    [[ -z "$dev" || -z "$pdev" ]] && return 0
    [[ "$dev" != "$pdev" ]]
}

# A single path component we are willing to create or write. Rejects separators,
# dot segments, whitespace, and control characters — the grammar an untrusted id
# (e.g. a row from someone else's database) must satisfy before it can steer a
# filesystem write. A leading dot is allowed because legitimate destinations are
# dot-directories (e.g. .reclaim-quarantine); "." and ".." are not.
reclaim_is_safe_name() {
    local n="$1"
    [[ -n "$n" ]] || return 1
    [[ "$n" == "." || "$n" == ".." ]] && return 1
    [[ "$n" =~ ^[A-Za-z0-9._-]{1,128}$ ]] || return 1
    return 0
}

reclaim_assert_safe_name() {
    reclaim_is_safe_name "$1" ||
        reclaim_die "$RECLAIM_EX_ERROR" "unsafe name component: '$1'" \
            "reject the source id; do not derive a path from it"
}

# --- data-root admission --------------------------------------------------
# Admit the tree a reclaim script is allowed to touch. Fail-closed: only a real,
# owned, non-symlinked directory strictly under $HOME is admitted. Root, $HOME
# itself, mount/volume roots, and symlinked ancestors are refused. Sets
# RECLAIM_ROOT + RECLAIM_ROOT_DEV.
# --- root classes ---------------------------------------------------------
#
# A root is admitted under a declared *class*, because the authorization
# boundary is not the same shape everywhere.
#
#   home-app-state  application state under $HOME. The directory itself is
#                   owned by us, so ownership of the root implies ownership of
#                   what is inside it. This is the original rule, unchanged.
#
#   user-temp       private per-user temp (macOS /var/folders/…/T). Owned by
#                   us and mode 0700, so it carries app-state-like risk while
#                   living outside $HOME.
#
#   shared-temp     the sticky world-writable OS temp plane (/private/tmp).
#                   The directory is root-owned by design, so root ownership
#                   proves nothing about its contents — authorization is
#                   *per candidate*, not per directory. Admission here grants a
#                   containment ceiling and nothing else: it is never authority
#                   to remove the root or to remove anything not separately
#                   proven to be ours.
#
# The class is declared by the profile. It is never inferred from the path,
# because a path string that happens to look like temp is not evidence about
# the boundary it sits on.
# Defaults to the original rule so every existing profile keeps its behaviour
# without saying anything; a profile that needs a different boundary declares it
# with reclaim_set_root_class before the lifecycle admits anything.
RECLAIM_ROOT_CLASS="home-app-state"

_reclaim_validate_root_class() {
    case "$1" in
        home-app-state | user-temp | shared-temp) return 0 ;;
        *) return 1 ;;
    esac
}

# reclaim_set_root_class <class> — declared by the profile at registration time.
reclaim_set_root_class() {
    _reclaim_validate_root_class "$1" ||
        reclaim_die "$RECLAIM_EX_ADMISSION" "unknown root class: $1"
    RECLAIM_ROOT_CLASS="$1"
}

# reclaim_admit_root_class <class> <path>
reclaim_admit_root_class() {
    reclaim_set_root_class "$1"
    _reclaim_admit_root_impl "$2"
}

# Admits under whatever class the profile declared, defaulting to
# home-app-state. The lifecycle calls this; profiles do not.
reclaim_admit_root() {
    _reclaim_admit_root_impl "$1"
}

_reclaim_admit_root_impl() {
    local given="$1"
    given="${given/#\~/$HOME}"
    [[ -n "$given" ]] || reclaim_die "$RECLAIM_EX_ADMISSION" "empty data root"
    [[ -d "$given" ]] || reclaim_die "$RECLAIM_EX_ADMISSION" "data root not a directory: $given"

    if reclaim_path_has_symlink "$given"; then
        reclaim_die "$RECLAIM_EX_ADMISSION" "data root path traverses a symlink: $given" \
            "pass the real path (no symlinked components)"
    fi

    local canon
    canon=$(_reclaim_canon_dir "$given")
    [[ -n "$canon" ]] || reclaim_die "$RECLAIM_EX_ADMISSION" "cannot canonicalize: $given"

    # reject dangerous roots
    [[ "$canon" == "/" ]] && reclaim_die "$RECLAIM_EX_ADMISSION" "refusing filesystem root"
    [[ "$canon" == "$HOME" ]] && reclaim_die "$RECLAIM_EX_ADMISSION" "refusing \$HOME itself"
    if reclaim_is_mount_root "$canon"; then
        reclaim_die "$RECLAIM_EX_ADMISSION" "refusing a volume/mount root (or unverifiable device): $canon"
    fi

    local owner mode
    owner=$(_reclaim_stat u "$canon")
    [[ -n "$owner" ]] || reclaim_die "$RECLAIM_EX_ADMISSION" "cannot determine owner of $canon"

    case "$RECLAIM_ROOT_CLASS" in
        home-app-state)
            # Unchanged: strictly under $HOME, owned by us. Ownership of the
            # root is what licenses everything below it for this class.
            case "$canon/" in
                "$HOME"/*) : ;;
                *) reclaim_die "$RECLAIM_EX_ADMISSION" "data root must be under \$HOME: $canon" ;;
            esac
            [[ "$owner" == "$(id -u)" ]] || reclaim_die "$RECLAIM_EX_ADMISSION" "data root not owned by uid $(id -u): $canon (owner uid $owner)"
            ;;
        user-temp)
            # Private per-user temp: outside $HOME, but owned by us and closed
            # to everyone else, so it carries app-state-like risk.
            [[ "$owner" == "$(id -u)" ]] || reclaim_die "$RECLAIM_EX_ADMISSION" "user-temp root not owned by uid $(id -u): $canon (owner uid $owner)"
            mode=$(_reclaim_mode "$canon")
            [[ -n "$mode" ]] || reclaim_die "$RECLAIM_EX_ADMISSION" "cannot determine mode of $canon"
            # No group or other bits at all: nobody else may read, enter, or
            # write. Anything looser is not a private temp.
            [[ "${mode:1:3}" =~ ^[0-7]00$ ]] || reclaim_die "$RECLAIM_EX_ADMISSION" \
                "user-temp root must be private (0700 or stricter): $canon has mode $mode"
            ;;
        shared-temp)
            # The sticky world-writable temp plane. Root ownership is expected
            # and proves nothing, so it is not required — but the sticky bit is,
            # because without it any user could remove any other user's names
            # and the per-candidate ownership rule would be unenforceable by the
            # kernel underneath us.
            mode=$(_reclaim_mode "$canon")
            [[ -n "$mode" ]] || reclaim_die "$RECLAIM_EX_ADMISSION" "cannot determine mode of $canon"
            (((0${mode:0:1} & 1) == 1)) || reclaim_die "$RECLAIM_EX_ADMISSION" \
                "shared-temp root must carry the sticky bit: $canon has mode $mode"
            ;;
    esac

    local dev
    dev=$(_reclaim_stat d "$canon")
    [[ -n "$dev" ]] || reclaim_die "$RECLAIM_EX_ADMISSION" "cannot determine device of $canon"

    RECLAIM_ROOT="$canon"
    RECLAIM_ROOT_DEV="$dev"
    RECLAIM_AUTH_PATH="$canon/auth.json"
    return 0
}

# Authorize one mutation candidate on its own evidence, immediately before it
# is touched.
#
# For home-app-state the root's ownership licenses its contents, so this is
# belt-and-braces. For shared-temp it is *the* authorization: the sticky
# world-writable plane is root-owned by design and holds other users' names, so
# nothing about the directory says anything about a file inside it.
#
# Every answer is derived from a single lstat, taken as late as possible, and
# missing evidence blocks. It is checked here rather than at plan time because
# a world-writable directory can change adversarially between the two — this is
# the check/use boundary, and it is narrowed rather than eliminated: a residual
# window remains between this lstat and the unlink that follows it, which is
# documented rather than papered over.
#
# Sets RECLAIM_ERR and returns non-zero on refusal. Never dies: callers own
# their accounting.
#
# reclaim_authorize_candidate <path> <expected-type: file|dir>
reclaim_authorize_candidate() {
    local target="$1" want="$2"
    RECLAIM_ERR=""
    # A caller classifying refusals by matching the message text would break the
    # moment the wording improved. The kind is the machine answer; the message
    # is for a human reading the same record.
    RECLAIM_ERR_KIND=""

    if [[ -z "$RECLAIM_ROOT" ]]; then
        RECLAIM_ERR="root not admitted"
        RECLAIM_ERR_KIND="containment"
        return 1
    fi
    if [[ "$target" != /* ]]; then
        RECLAIM_ERR="candidate is not an absolute path: $target"
        RECLAIM_ERR_KIND="containment"
        return 1
    fi
    # Never the root itself, and never anything outside it. Lexical, because the
    # target may be about to stop existing.
    if [[ "$target" == "$RECLAIM_ROOT" ]]; then
        RECLAIM_ERR="refusing the admitted root itself: $target"
        RECLAIM_ERR_KIND="containment"
        return 1
    fi
    case "$target/" in
        "$RECLAIM_ROOT"/*) : ;;
        *)
            RECLAIM_ERR="candidate escapes the admitted root: $target"
            RECLAIM_ERR_KIND="containment"
            return 1
            ;;
    esac
    case "$target" in
        */../* | */.. | */./* | */.)
            RECLAIM_ERR="candidate contains a dot segment: $target"
            RECLAIM_ERR_KIND="containment"
            return 1
            ;;
    esac

    # A symlink is never a candidate. Removing one is cheap and looks harmless,
    # but in a world-writable directory it is exactly how an attacker gets us to
    # act on a path we did not choose — and the size we attributed to it during
    # planning was the target's, not the link's.
    if [[ -L "$target" ]]; then
        RECLAIM_ERR="candidate is a symlink: $target"
        RECLAIM_ERR_KIND="symlink"
        return 1
    fi

    if [[ ! -e "$target" ]]; then
        RECLAIM_ERR="candidate vanished before authorization: $target"
        RECLAIM_ERR_KIND="vanished"
        return 1
    fi
    case "$want" in
        file)
            if [[ ! -f "$target" ]]; then
                RECLAIM_ERR="candidate is not a regular file: $target"
                RECLAIM_ERR_KIND="type-changed"
                return 1
            fi
            ;;
        dir)
            if [[ ! -d "$target" ]]; then
                RECLAIM_ERR="candidate is not a directory: $target"
                RECLAIM_ERR_KIND="type-changed"
                return 1
            fi
            ;;
        *)
            RECLAIM_ERR="unknown expected type: $want"
            RECLAIM_ERR_KIND="usage"
            return 1
            ;;
    esac

    # Ownership, per candidate. Unknown blocks.
    local owner
    owner=$(_reclaim_stat u "$target")
    if [[ -z "$owner" ]]; then
        RECLAIM_ERR="cannot determine owner of $target"
        RECLAIM_ERR_KIND="ownership-unknown"
        return 1
    fi
    if [[ "$owner" != "$(id -u)" ]]; then
        RECLAIM_ERR="candidate belongs to uid $owner, not $(id -u): $target"
        RECLAIM_ERR_KIND="ownership"
        return 1
    fi

    # Same device as the admitted root: a different device below a temp plane is
    # a mount we were never scoped to.
    local dev
    dev=$(_reclaim_stat d "$target")
    if [[ -z "$dev" ]]; then
        RECLAIM_ERR="cannot determine device of $target"
        RECLAIM_ERR_KIND="device-unknown"
        return 1
    fi
    if [[ -n "$RECLAIM_ROOT_DEV" && "$dev" != "$RECLAIM_ROOT_DEV" ]]; then
        RECLAIM_ERR="candidate is on device $dev, root is on $RECLAIM_ROOT_DEV: $target"
        RECLAIM_ERR_KIND="device"
        return 1
    fi

    return 0
}

# Assert <target> is a real path strictly within RECLAIM_ROOT, same device, not
# symlinked, and not the credential file. For mutation targets.
#
# Containment is expressed as non-dying predicates (they set RECLAIM_ERR and
# return non-zero) with asserting wrappers on top. Predicates are what callers
# use when they must own the terminal record; the wrappers are for the
# top-level shell.

# Lexical containment for an already-canonical path that need not exist:
# strictly inside the root, never the root itself, disjoint from auth.json.
reclaim_within_root_prospective() {
    local canon="$1"
    RECLAIM_ERR=""
    if [[ -z "$RECLAIM_ROOT" ]]; then
        RECLAIM_ERR="root not admitted"
        return 1
    fi
    case "$canon/" in
        "$RECLAIM_ROOT"/*) : ;;
        *)
            RECLAIM_ERR="path escapes data root: $canon"
            return 1
            ;;
    esac
    if [[ "$canon" == "$RECLAIM_ROOT" ]]; then
        RECLAIM_ERR="refusing to mutate the data root itself"
        return 1
    fi
    if [[ "$canon" == "$RECLAIM_AUTH_PATH" ]]; then
        RECLAIM_ERR="refusing to touch auth.json"
        return 1
    fi
    case "$RECLAIM_AUTH_PATH" in
        "$canon"/*)
            RECLAIM_ERR="path would contain auth.json: $canon"
            return 1
            ;;
    esac
    return 0
}

# Full containment for an EXISTING path: symlink-free, canonicalizable,
# lexically contained, and on the root's device (missing evidence blocks).
# Sets RECLAIM_CANON on success.
reclaim_within_root() {
    local target="$1"
    RECLAIM_ERR=""
    RECLAIM_CANON=""
    if [[ -z "$RECLAIM_ROOT" ]]; then
        RECLAIM_ERR="root not admitted"
        return 1
    fi
    if [[ ! -e "$target" ]]; then
        RECLAIM_ERR="mutation target missing: $target"
        return 1
    fi
    if reclaim_path_has_symlink "$target"; then
        RECLAIM_ERR="mutation target traverses a symlink: $target"
        return 1
    fi

    local canon
    if [[ -d "$target" ]]; then
        canon=$(_reclaim_canon_dir "$target")
    else
        local d b
        d=$(_reclaim_canon_dir "$(dirname "$target")")
        b=$(basename "$target")
        [[ -n "$d" ]] && canon="$d/$b"
    fi
    if [[ -z "$canon" ]]; then
        RECLAIM_ERR="cannot canonicalize target: $target"
        return 1
    fi

    reclaim_within_root_prospective "$canon" || return 1

    # device evidence is required, not optional: uncertainty blocks
    local dev
    dev=$(_reclaim_stat d "$canon")
    if [[ -z "$dev" ]]; then
        RECLAIM_ERR="cannot determine device of target: $canon"
        return 1
    fi
    if [[ "$dev" != "$RECLAIM_ROOT_DEV" ]]; then
        RECLAIM_ERR="target on a different device than root: $canon"
        return 1
    fi
    RECLAIM_CANON="$canon"
    return 0
}

# Asserting wrapper for the top-level shell (never call from a subshell).
reclaim_assert_within() {
    reclaim_within_root "$1" || reclaim_die "$RECLAIM_EX_ERROR" "$RECLAIM_ERR"
    return 0
}

# Admit a destination that does not exist yet and create it 0700, exclusively.
# The parent is admitted BEFORE any mkdir, so a rejected destination never
# leaves directories behind (and a symlinked/escaping parent never gets written
# through). The final component is created with a bare `mkdir`, so an existing
# path — a collision, or something planted — fails instead of being adopted.
#
# Sets RECLAIM_DEST on success; sets RECLAIM_ERR and returns non-zero on
# failure. It never dies, so it is safe to call outside a command substitution
# and let the caller own the terminal record.
reclaim_admit_dest_within_root() {
    local dest="$1"
    RECLAIM_DEST=""
    RECLAIM_ERR=""
    dest="${dest/#\~/$HOME}"
    if [[ -z "$dest" ]]; then
        RECLAIM_ERR="empty destination"
        return 1
    fi
    if [[ "$dest" != /* ]]; then
        RECLAIM_ERR="destination must be absolute: $dest"
        return 1
    fi
    dest="${dest%/}"

    if reclaim_path_has_symlink "$dest"; then
        RECLAIM_ERR="destination path traverses a symlink: $dest"
        return 1
    fi

    local parent base
    parent="${dest%/*}"
    base="${dest##*/}"
    if [[ -z "$parent" ]]; then
        RECLAIM_ERR="refusing a destination at filesystem root"
        return 1
    fi
    if ! reclaim_is_safe_name "$base"; then
        RECLAIM_ERR="unsafe destination name: $base"
        return 1
    fi
    if [[ -e "$dest" || -L "$dest" ]]; then
        RECLAIM_ERR="destination already exists: $dest"
        return 1
    fi

    # walk up to the deepest existing ancestor and admit that
    local probe="$parent" missing=()
    while [[ ! -d "$probe" && -n "$probe" && "$probe" != "/" ]]; do
        missing=("${probe##*/}" "${missing[@]+"${missing[@]}"}")
        probe="${probe%/*}"
    done
    if [[ ! -d "$probe" ]]; then
        RECLAIM_ERR="no existing ancestor for destination: $dest"
        return 1
    fi
    local m
    for m in ${missing[@]+"${missing[@]}"}; do
        if ! reclaim_is_safe_name "$m"; then
            RECLAIM_ERR="unsafe destination path component: $m"
            return 1
        fi
    done

    # The deepest existing ancestor may legitimately BE the data root (a
    # first-run quarantine dir). The root is refused as a mutation target, not
    # as a container, so accept it here and bind the destination itself below.
    local canon_probe
    canon_probe=$(_reclaim_canon_dir "$probe")
    if [[ -z "$canon_probe" ]]; then
        RECLAIM_ERR="cannot canonicalize destination ancestor: $probe"
        return 1
    fi
    if [[ "$canon_probe" != "$RECLAIM_ROOT" ]]; then
        reclaim_within_root "$canon_probe" || return 1
    fi

    local canon_dest="$canon_probe"
    for m in ${missing[@]+"${missing[@]}"}; do canon_dest="$canon_dest/$m"; done
    canon_dest="$canon_dest/$base"

    # containment is decided before anything is created
    reclaim_within_root_prospective "$canon_dest" || return 1

    local created=""
    if [[ ${#missing[@]} -gt 0 ]]; then
        local build="$canon_probe"
        for m in "${missing[@]}"; do
            build="$build/$m"
            if [[ ! -d "$build" ]]; then
                if ! (umask 077 && mkdir "$build" 2> /dev/null); then
                    RECLAIM_ERR="cannot create destination ancestor: $build"
                    [[ -n "$created" ]] && rmdir "$created" 2> /dev/null
                    return 1
                fi
                created="${created:-$build}"
            fi
        done
    fi
    # exclusive: an existing final component is a collision, not a reuse
    if ! (umask 077 && mkdir "$canon_dest" 2> /dev/null); then
        RECLAIM_ERR="cannot create destination (exists or unwritable): $canon_dest"
        [[ -n "$created" ]] && rm -rf "$created" 2> /dev/null
        return 1
    fi
    chmod 700 "$canon_dest" 2> /dev/null || true
    RECLAIM_DEST="$canon_dest"
    return 0
}

# An export destination is a trust-boundary COPY target for secret-bearing data.
# It must be under $HOME, symlink-free, a safe single component under an
# existing parent, and outside the admitted data root (we do not write plaintext
# exports into the tree we are mutating).
#
# reclaim_export_dir_problem echoes a problem description and returns 1 when the
# destination is unacceptable; it creates nothing, so preflight can verdict an
# export destination in plan mode without side effects. On success it echoes the
# canonical path and returns 0.
reclaim_export_dir_problem() {
    local dir="$1"
    dir="${dir/#\~/$HOME}"
    if [[ -z "$dir" ]]; then
        printf 'empty export dir\n'
        return 1
    fi
    if [[ "$dir" != /* ]]; then
        printf 'export dir must be absolute: %s\n' "$dir"
        return 1
    fi
    dir="${dir%/}"
    if reclaim_path_has_symlink "$dir"; then
        printf 'export dir path traverses a symlink: %s\n' "$dir"
        return 1
    fi
    local base="${dir##*/}" parent="${dir%/*}"
    if ! reclaim_is_safe_name "$base"; then
        printf 'unsafe export dir name: %s\n' "$base"
        return 1
    fi
    if [[ ! -d "$parent" ]]; then
        printf 'export dir parent does not exist: %s\n' "$parent"
        return 1
    fi
    local canon_parent
    canon_parent=$(_reclaim_canon_dir "$parent")
    if [[ -z "$canon_parent" ]]; then
        printf 'cannot canonicalize export parent: %s\n' "$parent"
        return 1
    fi
    case "$canon_parent/" in
        "$HOME"/*) : ;;
        *)
            printf "export dir must be under \$HOME: %s\n" "$dir"
            return 1
            ;;
    esac
    local canon_dir="$canon_parent/$base"
    if [[ -n "$RECLAIM_ROOT" ]]; then
        case "$canon_dir/" in
            "$RECLAIM_ROOT"/*)
                printf 'export dir must not be inside the data root: %s\n' "$canon_dir"
                return 1
                ;;
        esac
    fi
    printf '%s\n' "$canon_dir"
    return 0
}

# Admit + create the export destination 0700 (mutation path). Sets
# RECLAIM_EXPORT_DEST on success; sets RECLAIM_ERR and returns non-zero
# otherwise. Never dies — the caller owns the terminal record.
reclaim_admit_export_dir() {
    local out
    RECLAIM_EXPORT_DEST=""
    if ! out=$(reclaim_export_dir_problem "$1"); then
        RECLAIM_ERR="$out"
        return 1
    fi
    # Never `mkdir -p` here: it would happily follow a symlinked directory that
    # appeared after the check above. The parent is already required to exist,
    # so either the final component is absent (create it exclusively) or it
    # exists and must be re-admitted as a real, owned directory.
    if [[ -L "$out" ]]; then
        RECLAIM_ERR="export dir is a symlink: $out"
        return 1
    elif [[ -d "$out" ]]; then
        if reclaim_path_has_symlink "$out"; then
            RECLAIM_ERR="export dir path traverses a symlink: $out"
            return 1
        fi
        local owner
        owner=$(_reclaim_stat u "$out")
        if [[ -z "$owner" || "$owner" != "$(id -u)" ]]; then
            RECLAIM_ERR="existing export dir is not owned by uid $(id -u): $out"
            return 1
        fi
    elif [[ -e "$out" ]]; then
        RECLAIM_ERR="export dir path exists and is not a directory: $out"
        return 1
    else
        # bare mkdir: fails closed if the path was raced into existence
        if ! (umask 077 && mkdir "$out" 2> /dev/null); then
            RECLAIM_ERR="cannot create export dir (raced or unwritable): $out"
            return 1
        fi
    fi
    chmod 700 "$out" 2> /dev/null || true
    if [[ ! -w "$out" ]]; then
        RECLAIM_ERR="export dir not writable: $out"
        return 1
    fi
    RECLAIM_EXPORT_DEST="$out"
    return 0
}

# Publish a staged file to its final name with an atomic NO-REPLACE primitive.
#
# The second operand must be the exact new pathname. Two-operand `ln` is NOT
# that: when the destination is a directory — or a symlink to one, which is
# exactly what a racing process can plant — POSIX `ln` treats it as a
# destination *directory* and links inside it, returning success while
# publishing somewhere else entirely. `link` calls link(2) with no such
# reinterpretation, so a directory (or symlinked directory) at the destination
# fails EEXIST like any other existing object.
#
# Fallback when the `link` utility is unavailable: `ln <staged> <dir>`, valid
# only because the staged basename IS the final name — the kernel-created child
# path is then the exact destination, and any existing object there fails.
# Either way the result is verified by identity (device + inode), so a primitive
# that publishes anywhere other than the intended path is treated as a failure.
#
# RECLAIM_PUBLISH_PRIMITIVE=link|ln forces one path (tests exercise both).
reclaim_publish_no_replace() {
    local staged="$1" dir="$2" name="$3"
    RECLAIM_ERR=""
    local out="$dir/$name"
    if [[ "${staged##*/}" != "$name" ]]; then
        RECLAIM_ERR="staged basename must equal the published name ($staged -> $name)"
        return 1
    fi

    local primitive="${RECLAIM_PUBLISH_PRIMITIVE:-}"
    if [[ -z "$primitive" ]]; then
        if command -v link > /dev/null 2>&1; then primitive="link"; else primitive="ln"; fi
    fi

    case "$primitive" in
        link)
            if ! link "$staged" "$out" 2> /dev/null; then
                RECLAIM_ERR="publication refused: destination exists or cannot be linked: $out"
                return 1
            fi
            ;;
        ln)
            if ! ln "$staged" "$dir" 2> /dev/null; then
                RECLAIM_ERR="publication refused: destination exists or cannot be linked: $out"
                return 1
            fi
            ;;
        *)
            RECLAIM_ERR="unknown publish primitive: $primitive"
            return 1
            ;;
    esac

    # Identity check: the published path must be the very inode we staged, and
    # must not have become a symlink or directory along the way.
    if [[ -L "$out" || -d "$out" || ! -f "$out" ]]; then
        RECLAIM_ERR="publication landed on an unexpected object: $out"
        return 1
    fi
    local sdev sino odev oino
    sdev=$(_reclaim_stat d "$staged")
    sino=$(_reclaim_stat i "$staged")
    odev=$(_reclaim_stat d "$out")
    oino=$(_reclaim_stat i "$out")
    if [[ -z "$sino" || -z "$oino" || "$sdev" != "$odev" || "$sino" != "$oino" ]]; then
        RECLAIM_ERR="publication could not be verified at the intended path: $out"
        return 1
    fi
    return 0
}

# Admit a single export output path: it must not already exist in any form
# (regular file, symlink, directory), so a rerun or a planted path can never be
# truncated or removed by this run. Sets RECLAIM_DEST to the admitted path.
reclaim_admit_export_output() {
    local dir="$1" name="$2"
    RECLAIM_DEST=""
    RECLAIM_ERR=""
    if ! reclaim_is_safe_name "$name"; then
        RECLAIM_ERR="unsafe export output name: $name"
        return 1
    fi
    local out="$dir/$name"
    if [[ -e "$out" || -L "$out" ]]; then
        RECLAIM_ERR="export output already exists (refusing to overwrite): $out"
        return 1
    fi
    RECLAIM_DEST="$out"
    return 0
}

# --- profile + preflight registration -------------------------------------
reclaim_set_profile() { RECLAIM_PROFILE_ID="$1"; }

# reclaim_register_hook <args|bind|inventory|phase|usage> <fn>
reclaim_register_hook() {
    case "$1" in
        args) RECLAIM_HOOK_ARGS="$2" ;;
        bind) RECLAIM_HOOK_BIND="$2" ;;
        inventory) RECLAIM_HOOK_INVENTORY="$2" ;;
        phase) RECLAIM_HOOK_PHASE="$2" ;;
        usage) RECLAIM_HOOK_USAGE="$2" ;;
        *) reclaim_die "$RECLAIM_EX_ERROR" "unknown hook '$1'" ;;
    esac
}

# reclaim_register_check <name> <required 0|1> <phases csv or *> <fn>
reclaim_register_check() {
    RECLAIM_CHECK_NAMES+=("$1")
    RECLAIM_CHECK_REQUIRED+=("$2")
    RECLAIM_CHECK_PHASES+=("$3")
    RECLAIM_CHECK_FNS+=("$4")
}

# Conformance: every profile must satisfy this before it can run. This is the
# guard against script #2 quietly re-implementing its own dialect.
# Returns 0 when conformant; otherwise prints reasons and returns 1.
reclaim_conformance_report() {
    local bad=0
    if [[ ! "$RECLAIM_PROFILE_ID" =~ ^[a-z0-9]+(\.[a-z0-9-]+){2}$ ]]; then
        printf 'profile id must be three dotted segments, got: %s\n' "${RECLAIM_PROFILE_ID:-<unset>}"
        bad=1
    fi
    local h
    for h in ARGS BIND INVENTORY PHASE; do
        local fn
        eval "fn=\$RECLAIM_HOOK_$h"
        if [[ -z "$fn" ]]; then
            printf 'missing %s hook\n' "$(printf '%s' "$h" | tr '[:upper:]' '[:lower:]')"
            bad=1
        elif ! declare -F "$fn" > /dev/null 2>&1; then
            printf 'hook %s -> %s is not a function\n' "$h" "$fn"
            bad=1
        fi
    done
    if [[ ${#RECLAIM_CHECK_NAMES[@]} -eq 0 ]]; then
        printf 'no preflight checks registered\n'
        bad=1
    fi
    return "$bad"
}

_reclaim_check_applies() {
    # _reclaim_check_applies <phases-csv> <current-phase>
    local phases="$1" cur="$2"
    [[ "$phases" == "*" ]] && return 0
    [[ "$cur" == "all" ]] && return 0 # plan-only overview shows every check
    case ",$phases," in
        *",$cur,"*) return 0 ;;
        *) return 1 ;;
    esac
}

# Run every applicable preflight check, emit one record each, and gate.
# Returns 0 if clear to mutate; non-zero if any required check blocks.
reclaim_run_preflight() {
    local cur="${1:-$RECLAIM_PHASE}"
    local blockers=0 i
    [[ ${#RECLAIM_CHECK_NAMES[@]} -eq 0 ]] && return 0
    for i in "${!RECLAIM_CHECK_NAMES[@]}"; do
        local name="${RECLAIM_CHECK_NAMES[$i]}"
        local required="${RECLAIM_CHECK_REQUIRED[$i]}"
        local phases="${RECLAIM_CHECK_PHASES[$i]}"
        local fn="${RECLAIM_CHECK_FNS[$i]}"
        _reclaim_check_applies "$phases" "$cur" || continue

        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL=""
        RECLAIM_CHECK_RESOLVE=""
        "$fn"

        # a check blocks when it is required and not (pass AND confirmed)
        local blocks=0
        if [[ "$required" == "1" ]]; then
            if [[ "$RECLAIM_CHECK_OUTCOME" != "pass" || "$RECLAIM_CHECK_BASIS" != "confirmed" ]]; then
                blocks=1
            fi
        fi

        # canonical lifecycle status; a blocked check also carries the canonical
        # error class plus our local kind, per the companion contract
        if [[ "$blocks" == "1" ]]; then
            local eclass ekind
            if [[ "$RECLAIM_CHECK_OUTCOME" == "fail" ]]; then
                eclass="integrity" # evidence positively contradicts safety
                ekind="check-failed"
            else
                eclass="unknown" # safety could not be verified
                ekind="check-unverified"
            fi
            reclaim_emit "preflight" \
                check "$name" \
                required "$required" \
                outcome "$RECLAIM_CHECK_OUTCOME" \
                basis "$RECLAIM_CHECK_BASIS" \
                status "blocked" \
                error_class "$eclass" \
                error_kind "$ekind" \
                detail "${RECLAIM_CHECK_DETAIL:-}" \
                resolve "${RECLAIM_CHECK_RESOLVE:-}"
        else
            reclaim_emit "preflight" \
                check "$name" \
                required "$required" \
                outcome "$RECLAIM_CHECK_OUTCOME" \
                basis "$RECLAIM_CHECK_BASIS" \
                status "complete" \
                detail "${RECLAIM_CHECK_DETAIL:-}" \
                resolve "${RECLAIM_CHECK_RESOLVE:-}"
        fi

        [[ "$blocks" == "1" ]] && blockers=$((blockers + 1))
    done
    return "$blockers"
}

# --- common arg parse + phase guard ---------------------------------------
# Consumes mode/format/phase; leaves tool-specific args in RECLAIM_REST.
reclaim_parse_common() {
    RECLAIM_REST=()
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --plan | --dry-run)
                RECLAIM_MODE="plan"
                shift
                ;;
            --preflight)
                RECLAIM_MODE="preflight"
                shift
                ;;
            --execute)
                RECLAIM_MODE="execute"
                shift
                ;;
            --format)
                # explicit check, not ${2:?}: a missing value must exit through
                # the lifecycle (header + terminal, exit 64), not raw shell
                [[ $# -ge 2 ]] || reclaim_die "$RECLAIM_EX_USAGE" "--format needs a value (text|json)"
                RECLAIM_FORMAT="$2"
                shift 2
                ;;
            --phase)
                [[ $# -ge 2 ]] || reclaim_die "$RECLAIM_EX_USAGE" "--phase needs a name"
                RECLAIM_PHASE="$2"
                shift 2
                ;;
            *)
                RECLAIM_REST+=("$1")
                shift
                ;;
        esac
    done
    case "$RECLAIM_FORMAT" in
        text | json) : ;;
        *)
            RECLAIM_FORMAT="text"
            reclaim_die "$RECLAIM_EX_USAGE" "invalid --format (text|json)"
            ;;
    esac
    return 0
}

# Enforce exactly-one-named-phase for mutation. "all" is plan-only.
reclaim_require_single_phase() {
    if [[ "$RECLAIM_MODE" == "execute" ]]; then
        [[ -n "$RECLAIM_PHASE" ]] || reclaim_die "$RECLAIM_EX_USAGE" "--execute requires exactly one --phase (refusing to guess)"
        [[ "$RECLAIM_PHASE" == "all" ]] && reclaim_die "$RECLAIM_EX_USAGE" "--execute refuses --phase all; name one phase"
    fi
    return 0 # explicit: never let a trailing false test trip set -e in the caller
}

_reclaim_emit_header_once() {
    [[ "$RECLAIM_HEADER_EMITTED" == "1" ]] && return 0
    RECLAIM_HEADER_EMITTED=1
    reclaim_emit "header" \
        profile "${RECLAIM_PROFILE_ID:-unknown}" \
        root_requested "${RECLAIM_ROOT_REQUEST:-unset}" \
        mode "$RECLAIM_MODE" \
        phase "${RECLAIM_PHASE:-none}" \
        format "$RECLAIM_FORMAT"
    return 0
}

# --- the run lifecycle (owned by the harness, not by profiles) ------------
# A profile's args hook sets RECLAIM_ROOT_REQUEST; everything else — ordering,
# header-first, admission, preflight gating, terminal emission, and exit-code
# mapping — happens here so every script behaves identically.
reclaim_main() {
    # Backstop first: from here on, every exit path emits exactly one terminal
    # record, including failures that never reach reclaim_die.
    trap '_reclaim_exit_backstop $?' EXIT

    # Parse before conformance so mode/format are known and the conformance
    # failure itself is reported through the stream in the requested format.
    reclaim_parse_common "$@"

    local report
    if ! report=$(reclaim_conformance_report); then
        reclaim_die "$RECLAIM_EX_ERROR" "profile is not conformant: $(printf '%s' "$report" | tr '\n' ';')" \
            "fix the profile registration (hooks, profile id, preflight checks)" \
            "integrity"
    fi

    "$RECLAIM_HOOK_ARGS" ${RECLAIM_REST[@]+"${RECLAIM_REST[@]}"}

    _reclaim_emit_header_once
    reclaim_require_single_phase

    reclaim_admit_root "$RECLAIM_ROOT_REQUEST"
    reclaim_emit "admission" status complete root "$RECLAIM_ROOT" auth_path "$RECLAIM_AUTH_PATH"
    "$RECLAIM_HOOK_BIND"

    "$RECLAIM_HOOK_INVENTORY"

    local phase="${RECLAIM_PHASE:-all}"

    # Preflight: gating for execute, advisory otherwise. Capture the blocker
    # count without tripping set -e on a non-zero (blocking) return.
    local blockers=0
    reclaim_run_preflight "$phase" || blockers=$?

    if [[ "$RECLAIM_MODE" == "preflight" ]]; then
        if [[ "$blockers" -eq 0 ]]; then
            reclaim_terminal complete "$RECLAIM_EX_OK" blockers 0
            exit "$RECLAIM_EX_OK"
        fi
        reclaim_terminal blocked "$RECLAIM_EX_BLOCKED" blockers "$blockers" \
            next_required_operation "resolve blocked checks, then re-run --preflight"
        exit "$RECLAIM_EX_BLOCKED"
    fi

    if [[ "$RECLAIM_MODE" == "execute" && "$blockers" -gt 0 ]]; then
        reclaim_terminal blocked "$RECLAIM_EX_BLOCKED" blockers "$blockers" \
            detail "$blockers required preflight check(s) blocked" \
            next_required_operation "resolve blocked checks, then re-run --execute"
        printf 'error: %s required preflight check(s) blocked; resolve and retry\n' "$blockers" >&2
        exit "$RECLAIM_EX_BLOCKED"
    fi

    "$RECLAIM_HOOK_PHASE" "$phase"

    if [[ "$RECLAIM_FAILURES" -gt 0 ]]; then
        reclaim_terminal failed "$RECLAIM_EX_ERROR" count "$RECLAIM_FAILURES" \
            detail "$RECLAIM_FAILURES operation(s) failed" \
            next_required_operation "review the phase records and re-run the failed operations"
        exit "$RECLAIM_EX_ERROR"
    fi
    reclaim_terminal complete "$RECLAIM_EX_OK"
    exit "$RECLAIM_EX_OK"
}

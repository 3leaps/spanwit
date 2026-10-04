#!/usr/bin/env bash
# opencode-data.sh — reclaim ~/.local/share/opencode (ops script, not spanwit product reclaim).
#
# Profile: agent.opencode.data. Sources reclaim-lib.sh for the shared harness
# (run lifecycle, modes, preflight gate, record stream, path admission). This
# tool provides only profile discovery + whitelisted hooks and phase callbacks.
#
# Default is dry-run (plan). Nothing is deleted without --execute, and --execute
# runs only after every required preflight check passes (outcome=pass, basis=
# confirmed). Never touches auth.json.
#
#   ./scripts/reclaim/opencode-data.sh                       # inventory + plan
#   ./scripts/reclaim/opencode-data.sh --preflight --phase vacuum
#   ./scripts/reclaim/opencode-data.sh --phase legacy-json --execute
#   ./scripts/reclaim/opencode-data.sh --phase sessions --older-than 180d \
#       --export-dir ~/backups/oc --confirm-delete-sessions 482 --execute
#
# Prefer a non-OpenCode shell for --execute.
#
# Every phase callback, preflight check, and hook below is invoked indirectly by
# the harness (registry + hook dispatch), which shellcheck cannot trace.
# shellcheck disable=SC2329

# shellcheck source-path=SCRIPTDIR
set -euo pipefail

_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=reclaim-lib.sh
source "$_SCRIPT_DIR/reclaim-lib.sh"

# --- defaults -------------------------------------------------------------
DATA_DIR="${OPENCODE_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/opencode}"
OLDER_THAN="180d"
EXPORT_DIR=""
KEEP_SESSION=""
QUARANTINE_ROOT=""
CONFIRM_DELETE_SESSIONS="" # irreversible-class ack for the sessions phase
FREEZE_DAYS=30             # legacy store must be idle at least this long

reclaim_set_profile "agent.opencode.data"

usage() {
    cat << 'EOF' >&2
opencode-data.sh — inventory and reclaim ~/.local/share/opencode

Modes (from reclaim-lib): --plan (default) | --preflight | --execute
Format: --format text (default) | json

Phases (--phase NAME; exactly one required for --execute):
  inventory    sizes + session age buckets (always runs first)
  legacy-json  quarantine storage/message + storage/part (post-SQLite leftover)
  sessions     delete sessions older than --older-than via opencode CLI
  vacuum       sqlite3 VACUUM on opencode.db (quit OpenCode first)
  snapshots    list snapshot git dirs (undo history); never bulk-deletes
  bin          remove bundled tool binaries under data/bin (redownloadable)
  logs         remove log/*.log
  all          inventory + plan for every phase (plan-only; no --execute)

Options:
  --data-dir PATH                default: $XDG_DATA_HOME/opencode
  --older-than AGE               sessions only (Nd|Nw|Nm|Ny), default 180d
  --export-dir PATH              export each session before delete; export
                                 failure BLOCKS that session's delete. Must be
                                 under $HOME and outside the data dir; created
                                 0700 with 0600 files.
  --keep-session ID              never delete this id (repeatable / comma list)
  --quarantine-dir PATH          legacy-json destination (default: data-dir/.reclaim-quarantine)
  --confirm-delete-sessions N    required ack for sessions --execute; N must
                                 equal the candidate count
  -h, --help                     this help

Safety:
  - Session/transcript delete is irreversible and needs the separate
    --confirm-delete-sessions ack in addition to --execute.
  - The sessions phase drives the opencode CLI, which resolves its own store.
    Execute is refused unless that store is the same directory this run
    admitted, so a plan for one store can never delete from another.
  - Quarantine is a same-device rename: it frees 0 bytes until you delete the
    quarantine dir after verifying OpenCode still works.
  - There is no --force and no override flag: a failed preflight cannot be
    bypassed.
EOF
}

_h() { human_size "$1"; }

human_size() {
    local bytes="${1:-0}"
    awk -v b="$bytes" 'BEGIN{
        split("B KB MB GB TB", u, " ");
        i=1; while (b>=1024 && i<5) { b/=1024; i++ }
        printf "%.1f%s\n", b, u[i]
    }'
}

# Canonical data root the opencode CLI itself resolves (it does not honour
# OPENCODE_DATA_DIR; the CLI is authoritative about its own store). Echoes the
# path, or nothing when the CLI cannot be asked.
_opencode_cli_store() {
    command -v opencode > /dev/null 2>&1 || return 1
    local line dir
    line=$(opencode debug paths 2> /dev/null | awk '$1=="data" {print $2; exit}') || return 1
    [[ -n "$line" ]] || return 1
    dir=$(cd "$line" 2> /dev/null && pwd -P) || return 1
    [[ -n "$dir" ]] || return 1
    printf '%s\n' "$dir"
}

# =========================================================================
# Preflight checks. Each sets the RECLAIM_CHECK_* globals that reclaim-lib.sh
# reads back via an indirect call in reclaim_run_preflight; shellcheck cannot
# trace that data flow, hence the scoped SC2034 disables.
# =========================================================================

# shellcheck disable=SC2034
_ck_opencode_not_running() {
    # Match the process NAME exactly (-x), not the full command line: the real
    # process comm is "opencode", and -x will not self-match this script (whose
    # comm is bash/opencode-data.sh). A renamed helper binary could evade this;
    # documented as the residual for this profile.
    if ! command -v pgrep > /dev/null 2>&1; then
        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL="pgrep unavailable; cannot confirm OpenCode is stopped"
        RECLAIM_CHECK_RESOLVE="verify manually that OpenCode is quit, or install pgrep"
        return
    fi
    if pgrep -x opencode > /dev/null 2>&1; then
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="an opencode process is running"
        RECLAIM_CHECK_RESOLVE="quit OpenCode and run this from a plain shell"
        return
    fi
    RECLAIM_CHECK_OUTCOME="pass"
    RECLAIM_CHECK_BASIS="confirmed"
    RECLAIM_CHECK_DETAIL="no 'opencode' process detected"
}

# shellcheck disable=SC2034
_ck_wal_quiescent() {
    local wal="$DATA_DIR/opencode.db-wal"
    if [[ ! -f "$wal" ]]; then
        RECLAIM_CHECK_OUTCOME="pass"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="no WAL present"
        return
    fi
    local s1 m1 s2 m2
    s1=$(reclaim_dir_bytes "$wal") || s1=""
    m1=$(_reclaim_stat m "$wal")
    sleep 1
    s2=$(reclaim_dir_bytes "$wal") || s2=""
    m2=$(_reclaim_stat m "$wal")
    if [[ -z "$s1" || -z "$s2" ]]; then
        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL="cannot measure the WAL to sample activity"
        RECLAIM_CHECK_RESOLVE="verify OpenCode is quit"
        return
    fi
    if [[ -z "$m1" || -z "$m2" ]]; then
        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL="cannot stat WAL to sample activity"
        RECLAIM_CHECK_RESOLVE="verify OpenCode is quit"
        return
    fi
    if [[ "$s1" == "$s2" && "$m1" == "$m2" ]]; then
        RECLAIM_CHECK_OUTCOME="pass"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="WAL stable across sample (size=$s1)"
    else
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="WAL changed during sample; DB is being written"
        RECLAIM_CHECK_RESOLVE="quit OpenCode; the database is live"
    fi
}

# shellcheck disable=SC2034
_ck_free_space_for_vacuum() {
    local db="$DATA_DIR/opencode.db"
    if [[ ! -f "$db" ]]; then
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="opencode.db missing"
        return
    fi
    local dbsz free
    dbsz=$(reclaim_dir_bytes "$db") || dbsz=""
    free=$(reclaim_free_bytes "$db") || free=""
    if [[ -z "$free" || -z "$dbsz" ]]; then
        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL="cannot determine free space or database size"
        RECLAIM_CHECK_RESOLVE="check df and the database size on the data volume before VACUUM"
        return
    fi
    if [[ "$free" -gt "$dbsz" ]]; then
        RECLAIM_CHECK_OUTCOME="pass"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="free $(_h "$free") > db $(_h "$dbsz")"
    else
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="free $(_h "$free") <= db $(_h "$dbsz"); VACUUM needs ~db-size temp"
        RECLAIM_CHECK_RESOLVE="free space on the data volume first"
    fi
}

# The live store must be live in substance, not merely present: a hollow
# opencode.db next to a frozen JSON tree would otherwise let us quarantine the
# only real copy of that history.
# shellcheck disable=SC2034
_ck_live_store_populated() {
    local db="$DATA_DIR/opencode.db"
    if [[ ! -f "$db" ]]; then
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="no live opencode.db"
        RECLAIM_CHECK_RESOLVE="do not quarantine the legacy store without a live store"
        return
    fi
    if ! command -v sqlite3 > /dev/null 2>&1; then
        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL="sqlite3 unavailable; cannot probe the live store"
        RECLAIM_CHECK_RESOLVE="install sqlite3 so the live store can be verified"
        return
    fi
    local n
    if ! n=$(sqlite3 "$db" "SELECT COUNT(*) FROM session;" 2> /dev/null); then
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="opencode.db has no readable session table"
        RECLAIM_CHECK_RESOLVE="confirm the SQLite store is the live one before quarantining JSON"
        return
    fi
    if [[ ! "$n" =~ ^[0-9]+$ || "$n" -eq 0 ]]; then
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="live store holds no sessions (count=$n)"
        RECLAIM_CHECK_RESOLVE="the SQLite store looks hollow; do not quarantine the JSON store"
        return
    fi
    RECLAIM_CHECK_OUTCOME="pass"
    RECLAIM_CHECK_BASIS="confirmed"
    RECLAIM_CHECK_DETAIL="live store holds $n session(s)"
}

# shellcheck disable=SC2034
_ck_migration_evidence() {
    # The legacy JSON store is a safe leftover only when the legacy trees have
    # been idle (frozen) for FREEZE_DAYS. We probe for any file modified within
    # the window (-mtime -N is POSIX; portable across BSD/GNU find) rather than
    # computing a max mtime, which keeps this set -e / pipefail safe (no fragile
    # sort|head or GNU-only -printf fallback). Live-store substance is a
    # separate required check.
    local dirs=()
    [[ -d "$DATA_DIR/storage/message" ]] && dirs+=("$DATA_DIR/storage/message")
    [[ -d "$DATA_DIR/storage/part" ]] && dirs+=("$DATA_DIR/storage/part")
    if [[ ${#dirs[@]} -eq 0 ]]; then
        RECLAIM_CHECK_OUTCOME="pass"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="legacy trees absent/empty"
        return
    fi

    # Traversal status matters: an unreadable subtree yields no output, and
    # "no output" must not be read as "idle". Empty result + failed traversal is
    # unverifiable (blocks); any output at all means a recent write was found.
    local recent frc
    recent=$(find "${dirs[@]}" -type f -mtime -"$FREEZE_DAYS" -print 2> /dev/null | head -n1)
    frc="${PIPESTATUS[0]}"
    if [[ -z "$recent" && "$frc" -ne 0 ]]; then
        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL="cannot traverse the legacy store to establish idleness (find exit $frc)"
        RECLAIM_CHECK_RESOLVE="make the legacy store readable, then re-run; do not quarantine on unverified evidence"
        return
    fi
    if [[ -z "$recent" ]]; then
        RECLAIM_CHECK_OUTCOME="pass"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="legacy store idle >= ${FREEZE_DAYS}d"
    else
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="legacy store written within ${FREEZE_DAYS}d; not clearly a leftover"
        RECLAIM_CHECK_RESOLVE="confirm the migration completed; there is no override"
    fi
}

# The sessions phase mutates through the opencode CLI, which resolves its own
# store and ignores OPENCODE_DATA_DIR. Unless that store is exactly the root we
# admitted, the plan describes one subject and the delete would hit another.
# shellcheck disable=SC2034
_ck_cli_store_binding() {
    if ! command -v opencode > /dev/null 2>&1; then
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="opencode CLI not on PATH; sessions delete needs it"
        RECLAIM_CHECK_RESOLVE="install/expose the opencode CLI"
        return
    fi
    local cli
    if ! cli=$(_opencode_cli_store); then
        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL="cannot resolve the CLI's own data store"
        RECLAIM_CHECK_RESOLVE="check 'opencode debug paths'; the store must be verifiable"
        return
    fi
    if [[ "$cli" != "$RECLAIM_ROOT" ]]; then
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="CLI store $cli != admitted root $RECLAIM_ROOT"
        RECLAIM_CHECK_RESOLVE="run against the CLI's own store; the CLI ignores --data-dir"
        return
    fi
    RECLAIM_CHECK_OUTCOME="pass"
    RECLAIM_CHECK_BASIS="confirmed"
    RECLAIM_CHECK_DETAIL="CLI store matches the admitted root"
}

# Exported transcripts are secret-bearing copies leaving the data root, so the
# destination is verdicted before any delete: shape, location, and capacity.
# shellcheck disable=SC2034
_ck_export_destination() {
    if [[ -z "$EXPORT_DIR" ]]; then
        RECLAIM_CHECK_OUTCOME="pass"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="no --export-dir: sessions are deleted without a copy"
        return
    fi
    local out
    if ! out=$(reclaim_export_dir_problem "$EXPORT_DIR"); then
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="$out"
        RECLAIM_CHECK_RESOLVE="choose an export dir under \$HOME and outside the data root"
        return
    fi
    local db="$DATA_DIR/opencode.db" need free probe
    need=$(reclaim_dir_bytes "$db") || need=""
    probe="$out"
    [[ -d "$probe" ]] || probe="${out%/*}"
    free=$(reclaim_free_bytes "$probe") || free=""
    if [[ -z "$free" || -z "$need" ]]; then
        RECLAIM_CHECK_OUTCOME="unknown"
        RECLAIM_CHECK_BASIS="inferred"
        RECLAIM_CHECK_DETAIL="cannot determine free space on the export volume or store size"
        RECLAIM_CHECK_RESOLVE="check df on the export volume"
        return
    fi
    if [[ "$free" -le "$need" ]]; then
        RECLAIM_CHECK_OUTCOME="fail"
        RECLAIM_CHECK_BASIS="confirmed"
        RECLAIM_CHECK_DETAIL="export volume free $(_h "$free") <= store $(_h "$need")"
        RECLAIM_CHECK_RESOLVE="free space on the export volume, or pick another destination"
        return
    fi
    RECLAIM_CHECK_OUTCOME="pass"
    RECLAIM_CHECK_BASIS="confirmed"
    RECLAIM_CHECK_DETAIL="export dest $out (free $(_h "$free"))"
}

# =========================================================================
# Phases
# =========================================================================

hook_inventory() {
    local p
    for p in "" storage/part storage/message storage/session_diff opencode.db snapshot bin log; do
        local path="$DATA_DIR${p:+/$p}"
        local b
        # A du failure is not a zero: report it as unknown rather than claiming
        # a confirmed size we could not measure.
        if b=$(reclaim_dir_bytes "$path"); then
            reclaim_emit "inventory" path "$path" bytes "$b" human "$(_h "$b")" basis confirmed
        else
            reclaim_emit "inventory" path "$path" basis unknown \
                detail "size could not be measured (unreadable or vanished)"
        fi
    done

    local db="$DATA_DIR/opencode.db"
    if [[ -f "$db" ]] && command -v sqlite3 > /dev/null 2>&1; then
        local age_sec cutoff old_n rc=0
        age_sec=$(_age_to_seconds "$OLDER_THAN")
        cutoff=$((($(date +%s) - age_sec) * 1000))
        # `x=$(cmd) || rc=$?`: a bare assignment would trip set -e before the
        # status could be inspected, turning a read failure into a raw exit
        old_n=$(sqlite3 "$db" "SELECT COUNT(*) FROM session WHERE time_updated < $cutoff;" 2> /dev/null) || rc=$?
        if [[ "$rc" -eq 0 && "$old_n" =~ ^[0-9]+$ ]]; then
            reclaim_emit "inventory-sessions" older_than "$OLDER_THAN" candidates "$old_n" basis confirmed
        else
            reclaim_emit "inventory-sessions" older_than "$OLDER_THAN" basis unknown \
                detail "session table not readable"
        fi
    else
        reclaim_log "note: sqlite3 or opencode.db missing — session stats skipped"
    fi
    return 0
}

# Pure conversion; the format is validated at parse time so this never dies
# (it is called from command substitutions).
_age_to_seconds() {
    local age="$1"
    [[ "$age" =~ ^([0-9]+)([dwmy])$ ]] || {
        echo 0
        return 1
    }
    local n="${BASH_REMATCH[1]}" u="${BASH_REMATCH[2]}"
    case "$u" in
        d) echo $((n * 86400)) ;;
        w) echo $((n * 604800)) ;;
        m) echo $((n * 2592000)) ;;
        y) echo $((n * 31536000)) ;;
    esac
    return 0
}

phase_legacy_json() {
    local msg="$DATA_DIR/storage/message" part="$DATA_DIR/storage/part"
    local q="${QUARANTINE_ROOT:-$DATA_DIR/.reclaim-quarantine}"
    local mb pb
    mb=$(reclaim_dir_bytes "$msg") || mb="unknown"
    pb=$(reclaim_dir_bytes "$part") || pb="unknown"

    if [[ ! -d "$msg" && ! -d "$part" ]]; then
        reclaim_emit "phase" phase legacy-json status skipped detail "legacy trees absent"
        return 0
    fi

    if [[ "$RECLAIM_MODE" != "execute" ]]; then
        reclaim_emit "phase" phase legacy-json status pending \
            message "$(_h "$mb")" part "$(_h "$pb")" \
            action "same-device rename to quarantine" \
            freed_now_bytes 0 \
            frees_now "0 bytes (freed only when quarantine is later deleted)" \
            quarantine "$q"
        return 0
    fi

    # Validate every source BEFORE creating any destination, so a refused move
    # never leaves an empty quarantine dir behind.
    local sources=()
    [[ -d "$msg" ]] && sources+=("$msg")
    [[ -d "$part" ]] && sources+=("$part")
    local src
    for src in "${sources[@]}"; do
        reclaim_assert_within "$src"
    done

    # Admit + create the destination exclusively: an existing timestamp dir is a
    # collision, not something to reuse.
    local stamp dest
    stamp=$(date +%Y%m%dT%H%M%S)
    if ! reclaim_admit_dest_within_root "$q/$stamp"; then
        reclaim_die "$RECLAIM_EX_ERROR" "$RECLAIM_ERR" \
            "choose a quarantine destination inside the data root that does not already exist"
    fi
    dest="$RECLAIM_DEST"

    # Each child destination must be absent inside the freshly created dir, and
    # every mv failure is reported — never swallowed by set -e.
    for src in "${sources[@]}"; do
        local leaf="${src##*/}" target
        target="$dest/$leaf"
        if [[ -e "$target" || -L "$target" ]]; then
            reclaim_die "$RECLAIM_EX_ERROR" "quarantine target already exists: $target"
        fi
        if ! mv "$src" "$target"; then
            reclaim_record_failure
            reclaim_emit "phase" phase legacy-json status failed \
                detail "could not move $src to $target" quarantine "$dest"
            return 0
        fi
    done

    reclaim_emit "phase" phase legacy-json status complete quarantine "$dest" \
        freed_now_bytes 0 \
        frees_now "0 bytes until you delete: $dest"
}

# Remove the private staging dir, but only ever our own staged files — never a
# recursive delete of a path something else could have populated.
_cleanup_stage() {
    local stage="$1"
    [[ -n "$stage" && -d "$stage" && ! -L "$stage" ]] || return 0
    local f
    for f in "$stage"/*.json; do
        [[ -f "$f" && ! -L "$f" ]] && rm -f "$f"
    done
    rmdir "$stage" 2> /dev/null || reclaim_log "note: staging dir not empty, left in place: $stage"
    return 0
}

_session_is_kept() {
    [[ -z "$KEEP_SESSION" ]] && return 1
    case ",$KEEP_SESSION," in *",$1,"*) return 0 ;; *) return 1 ;; esac
}

phase_sessions() {
    local db="$DATA_DIR/opencode.db"
    [[ -f "$db" ]] || reclaim_die "$RECLAIM_EX_ERROR" "missing $db"
    command -v sqlite3 > /dev/null 2>&1 || reclaim_die "$RECLAIM_EX_ERROR" "sqlite3 required"

    local age_sec cutoff
    age_sec=$(_age_to_seconds "$OLDER_THAN")
    cutoff=$((($(date +%s) - age_sec) * 1000))

    # Capture the query result AND its status: a failed read must not look like
    # "no candidates" and let the phase finish successfully.
    local rows rc=0
    rows=$(sqlite3 "$db" "SELECT id FROM session WHERE time_updated < $cutoff ORDER BY time_updated ASC;" 2> /dev/null) || rc=$?
    if [[ "$rc" -ne 0 ]]; then
        reclaim_die "$RECLAIM_EX_ERROR" "could not read session candidates from $db (sqlite3 exit $rc)" \
            "repair or verify the store before retrying" "integrity"
    fi

    local ids=() id
    while IFS= read -r id; do
        [[ -z "$id" ]] && continue
        if _session_is_kept "$id"; then continue; fi
        ids+=("$id")
    done <<< "$rows"

    local n="${#ids[@]}"
    if [[ "$RECLAIM_MODE" != "execute" ]]; then
        reclaim_emit "phase" phase sessions status pending older_than "$OLDER_THAN" candidates "$n" \
            note "irreversible; --execute needs --confirm-delete-sessions $n"
        return 0
    fi

    [[ "$n" -eq 0 ]] && {
        reclaim_emit "phase" phase sessions status skipped detail "no candidates"
        return 0
    }

    # Irreversible-class ack, distinct from --execute, must match the count.
    [[ -n "$CONFIRM_DELETE_SESSIONS" ]] ||
        reclaim_die "$RECLAIM_EX_USAGE" "sessions --execute needs --confirm-delete-sessions $n (irreversible)"
    [[ "$CONFIRM_DELETE_SESSIONS" == "$n" ]] ||
        reclaim_die "$RECLAIM_EX_USAGE" "--confirm-delete-sessions $CONFIRM_DELETE_SESSIONS != candidate count $n (re-check the plan)"

    # Re-verify the CLI store at mutation time, not only at preflight: this is
    # the binding between the subject we inventoried and the subject the CLI
    # will actually delete from.
    local cli
    cli=$(_opencode_cli_store) ||
        reclaim_die "$RECLAIM_EX_ERROR" "cannot resolve the opencode CLI store" "verify 'opencode debug paths'"
    [[ "$cli" == "$RECLAIM_ROOT" ]] ||
        reclaim_die "$RECLAIM_EX_ERROR" "refusing to delete: CLI store $cli != admitted root $RECLAIM_ROOT" \
            "run against the CLI's own store"

    # Ids steer filesystem writes (export files); validate the whole set before
    # any mutation, so an id that is not a plain token stops the run instead of
    # escaping the export directory.
    for id in "${ids[@]}"; do
        reclaim_is_safe_name "$id" ||
            reclaim_die "$RECLAIM_EX_ERROR" "refusing: session id is not a safe token: '$id'" \
                "inspect the store; ids must be a single plain path component"
    done

    local export_dest=""
    if [[ -n "$EXPORT_DIR" ]]; then
        if ! reclaim_admit_export_dir "$EXPORT_DIR"; then
            reclaim_die "$RECLAIM_EX_ADMISSION" "$RECLAIM_ERR" \
                "choose an export destination under \$HOME and outside the data root"
        fi
        export_dest="$RECLAIM_EXPORT_DEST"
        # The admitted export dir may already exist (a rerun, or an operator's
        # backup dir). Admit the COMPLETE output set before mutating anything:
        # no output may already exist in any form, so this run can never
        # truncate or delete a path it did not create.
        for id in "${ids[@]}"; do
            if ! reclaim_admit_export_output "$export_dest" "${id}.json"; then
                reclaim_die "$RECLAIM_EX_ERROR" "$RECLAIM_ERR" \
                    "move or remove the existing export output, or choose an empty export dir" \
                    "integrity"
            fi
        done
    fi

    # Private staging directory: each export is written here under its exact
    # final basename, then published into the export dir by identity. Staging in
    # a directory only this run owns keeps the publish primitive unambiguous.
    local stage=""
    if [[ -n "$export_dest" ]]; then
        stage="$export_dest/.reclaim-stage.$$"
        if [[ -e "$stage" || -L "$stage" ]]; then
            reclaim_die "$RECLAIM_EX_ERROR" "staging path already exists: $stage" \
                "clear the stale staging directory and retry" "integrity"
        fi
        if ! (umask 077 && mkdir "$stage" 2> /dev/null); then
            reclaim_die "$RECLAIM_EX_ERROR" "cannot create staging directory: $stage" \
                "check permissions on the export destination"
        fi
    fi

    local ok=0 fail=0 exportfail=0
    for id in "${ids[@]}"; do
        # A failed export blocks that session's delete.
        if [[ -n "$export_dest" ]]; then
            local tmp="$stage/${id}.json"
            if ! reclaim_admit_export_output "$export_dest" "${id}.json"; then
                _cleanup_stage "$stage"
                reclaim_die "$RECLAIM_EX_ERROR" "$RECLAIM_ERR" \
                    "export output appeared mid-run; re-check the destination" "integrity"
            fi
            if ! (umask 077 && set -o noclobber && opencode export "$id" > "$tmp" 2> /dev/null); then
                rm -f "$tmp"
                exportfail=$((exportfail + 1))
                reclaim_log "block: export failed for $id — not deleting"
                continue
            fi
            chmod 600 "$tmp" 2> /dev/null || true
            # Publish with an atomic no-replace primitive whose second operand is
            # the exact pathname. Anything that appeared at the destination
            # during the export — a file, a directory, or a symlink to one —
            # fails publication instead of redirecting or replacing it.
            if ! reclaim_publish_no_replace "$tmp" "$export_dest" "${id}.json"; then
                rm -f "$tmp"
                exportfail=$((exportfail + 1))
                reclaim_log "block: ${RECLAIM_ERR:-could not publish export} for $id — not deleting"
                continue
            fi
            rm -f "$tmp"
        fi
        if opencode session delete "$id" > /dev/null 2>&1; then
            ok=$((ok + 1))
        else
            fail=$((fail + 1))
        fi
    done

    _cleanup_stage "$stage"

    local status=complete
    if [[ "$fail" -gt 0 || "$exportfail" -gt 0 ]]; then
        status=failed
        reclaim_record_failure $((fail + exportfail))
    fi
    reclaim_emit "phase" phase sessions status "$status" deleted "$ok" delete_failed "$fail" export_blocked "$exportfail" \
        next_required_operation "run --phase vacuum --execute to reclaim DB pages"
}

phase_vacuum() {
    local db="$DATA_DIR/opencode.db"
    [[ -f "$db" ]] || reclaim_die "$RECLAIM_EX_ERROR" "missing $db"
    command -v sqlite3 > /dev/null 2>&1 || reclaim_die "$RECLAIM_EX_ERROR" "sqlite3 required"
    local before
    before=$(reclaim_dir_bytes "$db") || before=""

    if [[ "$RECLAIM_MODE" != "execute" ]]; then
        if [[ -n "$before" ]]; then
            reclaim_emit "phase" phase vacuum status pending bytes_before "$before" db "$(_h "$before")" \
                action "PRAGMA wal_checkpoint(TRUNCATE); VACUUM"
        else
            reclaim_emit "phase" phase vacuum status pending basis unknown \
                detail "database size could not be measured" \
                action "PRAGMA wal_checkpoint(TRUNCATE); VACUUM"
        fi
        return 0
    fi
    if ! sqlite3 "$db" "PRAGMA wal_checkpoint(TRUNCATE); VACUUM;"; then
        reclaim_record_failure
        reclaim_emit "phase" phase vacuum status failed detail "VACUUM failed"
        return 0
    fi
    local after
    after=$(reclaim_dir_bytes "$db") || after=""
    if [[ -z "$before" || -z "$after" ]]; then
        reclaim_emit "phase" phase vacuum status complete basis unknown \
            detail "VACUUM completed; reclaimed bytes could not be measured"
        return 0
    fi
    local reclaimed=0
    [[ "$before" -gt "$after" ]] && reclaimed=$((before - after))
    reclaim_emit "phase" phase vacuum status complete \
        bytes_before "$before" bytes_after "$after" bytes_reclaimed "$reclaimed" \
        reclaimed "$(_h "$reclaimed")"
}

phase_snapshots() {
    local snap="$DATA_DIR/snapshot"
    [[ -d "$snap" ]] || {
        reclaim_emit "phase" phase snapshots status skipped detail "no snapshot dir"
        return 0
    }
    local b
    if b=$(reclaim_dir_bytes "$snap"); then
        reclaim_emit "phase" phase snapshots status pending bytes "$b" total "$(_h "$b")" \
            note "list-only; never bulk-deletes undo history"
    else
        reclaim_emit "phase" phase snapshots status pending basis unknown \
            detail "size could not be measured" \
            note "list-only; never bulk-deletes undo history"
    fi
    [[ "$RECLAIM_MODE" == "execute" ]] &&
        reclaim_die "$RECLAIM_EX_USAGE" "snapshots refuses --execute; remove specific project ids by hand after review"
    return 0
}

phase_bin() {
    local b="$DATA_DIR/bin"
    [[ -d "$b" ]] || {
        reclaim_emit "phase" phase bin status skipped detail "no bin dir"
        return 0
    }
    local sz
    sz=$(reclaim_dir_bytes "$b") || sz=""
    if [[ "$RECLAIM_MODE" != "execute" ]]; then
        if [[ -n "$sz" ]]; then
            reclaim_emit "phase" phase bin status pending bytes "$sz" size "$(_h "$sz")" note "tool binaries; redownloadable"
        else
            reclaim_emit "phase" phase bin status pending basis unknown \
                detail "size could not be measured" note "tool binaries; redownloadable"
        fi
        return 0
    fi
    reclaim_assert_within "$b"
    if ! rm -rf "$b"; then
        reclaim_record_failure
        reclaim_emit "phase" phase bin status failed detail "could not remove $b"
        return 0
    fi
    if [[ -n "$sz" ]]; then
        reclaim_emit "phase" phase bin status complete removed "$b" bytes_reclaimed "$sz"
    else
        reclaim_emit "phase" phase bin status complete removed "$b" basis unknown
    fi
}

phase_logs() {
    local l="$DATA_DIR/log"
    [[ -d "$l" ]] || {
        reclaim_emit "phase" phase logs status skipped detail "no log dir"
        return 0
    }
    local sz
    if sz=$(reclaim_dir_bytes "$l"); then
        [[ "$RECLAIM_MODE" != "execute" ]] && {
            reclaim_emit "phase" phase logs status pending bytes "$sz" size "$(_h "$sz")"
            return 0
        }
    else
        [[ "$RECLAIM_MODE" != "execute" ]] && {
            reclaim_emit "phase" phase logs status pending basis unknown detail "size could not be measured"
            return 0
        }
    fi
    reclaim_assert_within "$l"

    # Report what could not be removed instead of completing regardless.
    local f removed=0 failed=0
    for f in "$l"/*.log; do
        [[ -e "$f" || -L "$f" ]] || continue
        if rm -f "$f" 2> /dev/null; then
            removed=$((removed + 1))
        else
            failed=$((failed + 1))
        fi
    done
    if [[ "$failed" -gt 0 ]]; then
        reclaim_record_failure "$failed"
        reclaim_emit "phase" phase logs status failed deleted "$removed" delete_failed "$failed" \
            detail "could not remove every log file"
        return 0
    fi
    reclaim_emit "phase" phase logs status complete deleted "$removed" detail "cleared *.log"
}

hook_phase() {
    case "$1" in
        inventory) : ;; # inventory already ran
        legacy-json) phase_legacy_json ;;
        sessions) phase_sessions ;;
        vacuum) phase_vacuum ;;
        snapshots) phase_snapshots ;;
        bin) phase_bin ;;
        logs) phase_logs ;;
        all)
            phase_legacy_json
            phase_sessions
            phase_vacuum
            phase_snapshots
            phase_bin
            phase_logs
            ;;
        *) reclaim_die "$RECLAIM_EX_USAGE" "unknown --phase '$1'" ;;
    esac
    return 0
}

# =========================================================================
# Registration + entry
# =========================================================================
reclaim_register_check opencode-not-running 1 "legacy-json,sessions,vacuum,bin,logs" _ck_opencode_not_running
reclaim_register_check wal-quiescent 1 "sessions,vacuum" _ck_wal_quiescent
reclaim_register_check free-space-for-vacuum 1 "vacuum" _ck_free_space_for_vacuum
reclaim_register_check live-store-populated 1 "legacy-json" _ck_live_store_populated
reclaim_register_check migration-evidence 1 "legacy-json" _ck_migration_evidence
reclaim_register_check cli-store-binding 1 "sessions" _ck_cli_store_binding
reclaim_register_check export-destination 1 "sessions" _ck_export_destination

# Fetch the value for an option, routing a missing value through the lifecycle
# (header + terminal, exit 64) instead of a raw shell diagnostic.
_arg_value() {
    # _arg_value <flag> <index> <array-name...>: echoes the value
    local flag="$1" idx="$2"
    shift 2
    local -a arr=("$@")
    if [[ "$idx" -ge "${#arr[@]}" || -z "${arr[$idx]}" ]]; then
        return 1
    fi
    printf '%s\n' "${arr[$idx]}"
}

hook_args() {
    local rest=("$@")
    local i=0 v
    while [[ $i -lt ${#rest[@]} ]]; do
        local flag="${rest[$i]}"
        case "$flag" in
            --data-dir | --older-than | --export-dir | --keep-session | --quarantine-dir | --confirm-delete-sessions)
                if ! v=$(_arg_value "$flag" "$((i + 1))" ${rest[@]+"${rest[@]}"}); then
                    reclaim_die "$RECLAIM_EX_USAGE" "$flag needs a value"
                fi
                case "$flag" in
                    --data-dir) DATA_DIR="$v" ;;
                    --older-than) OLDER_THAN="$v" ;;
                    --export-dir) EXPORT_DIR="$v" ;;
                    --keep-session) KEEP_SESSION="${KEEP_SESSION:+$KEEP_SESSION,}$v" ;;
                    --quarantine-dir) QUARANTINE_ROOT="$v" ;;
                    --confirm-delete-sessions) CONFIRM_DELETE_SESSIONS="$v" ;;
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

    # Validate option values here, at the top level, so the conversions used
    # later inside command substitutions can never need to die.
    [[ "$OLDER_THAN" =~ ^[0-9]+[dwmy]$ ]] ||
        reclaim_die "$RECLAIM_EX_USAGE" "invalid --older-than '$OLDER_THAN' (e.g. 90d, 12w, 6m, 1y)"
    if [[ -n "$CONFIRM_DELETE_SESSIONS" && ! "$CONFIRM_DELETE_SESSIONS" =~ ^[0-9]+$ ]]; then
        reclaim_die "$RECLAIM_EX_USAGE" "--confirm-delete-sessions needs a count, got '$CONFIRM_DELETE_SESSIONS'"
    fi

    DATA_DIR="${DATA_DIR/#\~/$HOME}"
    [[ -n "$EXPORT_DIR" ]] && EXPORT_DIR="${EXPORT_DIR/#\~/$HOME}"
    [[ -n "$QUARANTINE_ROOT" ]] && QUARANTINE_ROOT="${QUARANTINE_ROOT/#\~/$HOME}"
    # consumed by reclaim_main in the sourced harness
    # shellcheck disable=SC2034
    RECLAIM_ROOT_REQUEST="$DATA_DIR"
    return 0
}

# Called by the harness immediately after admission: every later path derives
# from the canonical admitted root, never from the operator's request string.
hook_bind() {
    DATA_DIR="$RECLAIM_ROOT"
    return 0
}

reclaim_register_hook args hook_args
reclaim_register_hook bind hook_bind
reclaim_register_hook inventory hook_inventory
reclaim_register_hook phase hook_phase
reclaim_register_hook usage usage

# reclaim_main emits the header, admits, inventories, gates, dispatches, and
# owns the terminal record + exit code.
reclaim_main "$@"

#!/usr/bin/env bash
# The Make tag guards refuse a generic RELEASE_TAG from the environment or the
# command line, so a value left from an earlier release cannot override
# SPANWIT_RELEASE_TAG. Runs the two guard rules extracted from the real Makefile
# in a scratch directory with a stub version guard.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd -P)"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
mkdir -p "$scratch/scripts"
printf '1.2.3\n' > "$scratch/VERSION"
cat > "$scratch/scripts/release-guard-tag-version.sh" << 'SH'
#!/usr/bin/env bash
[[ -z "${RELEASE_TAG+x}" ]] || { echo 'stub: generic RELEASE_TAG reached the script' >&2; exit 1; }
[[ "$SPANWIT_RELEASE_TAG" == "v$(cat VERSION)" ]] || { echo 'stub: tag does not match VERSION' >&2; exit 1; }
SH
chmod +x "$scratch/scripts/release-guard-tag-version.sh"
{
    # Literal make syntax, not a shell expansion.
    # shellcheck disable=SC2016
    printf 'VERSION := $(shell cat VERSION)\n'
    grep -E '^RELEASE_TAG \?=' "$root/Makefile"
    sed -n '/^release-guard-tag-name:/,/^$/p;/^release-guard-tag-version:/,/^$/p' "$root/Makefile"
} > "$scratch/Makefile"
for rule in release-guard-tag-name release-guard-tag-version; do
    grep -q "^$rule:" "$scratch/Makefile" || {
        echo "error: $rule not found in Makefile" >&2
        exit 1
    }
done

run() { # target, then env assignments, then optional make args after --
    local target="$1"
    shift
    local envs=() args=()
    while [[ $# -gt 0 && "$1" != -- ]]; do
        envs+=("$1")
        shift
    done
    [[ "${1:-}" == -- ]] && shift
    args=("$@")
    env -u RELEASE_TAG -u SPANWIT_RELEASE_TAG "${envs[@]+"${envs[@]}"}" \
        make --no-print-directory -s -C "$scratch" "$target" "${args[@]+"${args[@]}"}" > "$scratch/out" 2>&1
}
pass() {
    run "$@" || {
        echo "error: expected pass: $*" >&2
        cat "$scratch/out" >&2
        exit 1
    }
}
refuse() {
    local reason="$1"
    shift
    if run "$@"; then
        echo "error: expected refusal ($reason): $*" >&2
        exit 1
    fi
    grep -q "$reason" "$scratch/out" || {
        echo "error: missing named refusal ($reason): $*" >&2
        cat "$scratch/out" >&2
        exit 1
    }
}

for target in release-guard-tag-name release-guard-tag-version; do
    pass "$target" SPANWIT_RELEASE_TAG=v1.2.3
    # A stale generic value is refused even when the prefixed one is right, even
    # when it matches VERSION, and even when empty.
    refuse 'generic RELEASE_TAG is set' "$target" RELEASE_TAG=v1.2.2 SPANWIT_RELEASE_TAG=v1.2.3
    refuse 'generic RELEASE_TAG is set' "$target" RELEASE_TAG=v1.2.3
    refuse 'generic RELEASE_TAG is set' "$target" RELEASE_TAG= SPANWIT_RELEASE_TAG=v1.2.3
    refuse 'generic RELEASE_TAG is set' "$target" SPANWIT_RELEASE_TAG=v1.2.3 -- RELEASE_TAG=v1.2.3
    refuse 'SPANWIT_RELEASE_TAG not set' "$target"
done
refuse 'must be canonical' release-guard-tag-name SPANWIT_RELEASE_TAG=1.2.3
refuse 'does not match VERSION' release-guard-tag-version SPANWIT_RELEASE_TAG=v1.2.4
echo '[ok] Make tag guards refuse a generic RELEASE_TAG and require SPANWIT_RELEASE_TAG'

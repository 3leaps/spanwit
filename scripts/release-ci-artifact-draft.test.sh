#!/usr/bin/env bash
# Stub-gh controls for release-fetch-ci-artifacts.sh and release-create-draft.sh.
# No live GitHub call is made; the stub records every invocation.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd -P)"
scratch="$(mktemp -d)"
trap 'rm -rf "$scratch"' EXIT
repo="$scratch/repo"
git init -q -b main "$repo"
git -C "$repo" config user.name 'Fixture'
git -C "$repo" config user.email fixture@example.invalid
printf 'x\n' > "$repo/file"
git -C "$repo" add file
mkdir -p "$repo/docs/releases"
printf '# fixture v1.2.3\n\nCurated notes.\n' > "$repo/docs/releases/v1.2.3.md"
git -C "$repo" add docs/releases/v1.2.3.md
git -C "$repo" commit -qm fixture
git -C "$repo" tag -am 'Fixture release' v1.2.3
commit="$(git -C "$repo" rev-parse HEAD)"
mkdir -p "$repo/scripts"
cp "$root/scripts/release-fetch-ci-artifacts.sh" "$root/scripts/release-create-draft.sh" "$repo/scripts/"
object="$(git -C "$repo" rev-parse refs/tags/v1.2.3)"
# Stand-in for the trusted published-tag gate (its own suite uses real
# signatures). It reports the fixture anchor, records any expected anchor it is
# asked to enforce, and fails when told the remote tag was replaced.
cat > "$repo/scripts/release-verify-published-tag.sh" << SH
#!/usr/bin/env bash
printf 'reverify %s expect=%s/%s\\n' "\$SPANWIT_RELEASE_TAG" "\${SPANWIT_EXPECTED_TAG_OBJECT:-}" "\${SPANWIT_EXPECTED_COMMIT:-}" >> "\$FIXTURE_LOG"
[[ "\$SPANWIT_RELEASE_TAG" == v1.2.3 ]] || { echo 'error: tag absent' >&2; exit 1; }
[[ "\${FIXTURE_TAG_REPLACED:-0}" == 0 ]] || { echo 'error: tag replaced' >&2; exit 1; }
if [[ -n "\${SPANWIT_ANCHOR_OUT:-}" ]]; then
    printf 'tag=v1.2.3\\nobject=%s\\ncommit=%s\\n' "$object" "$commit" > "\$SPANWIT_ANCHOR_OUT"
fi
SH
chmod +x "$repo/scripts/release-verify-published-tag.sh"

# Stub gh. FIXTURE_RUNS is the JSON the run list returns before jq filtering;
# FIXTURE_RELEASE_EXISTS controls `gh release view`. Every call is logged.
mkdir -p "$scratch/bin"
cat > "$scratch/bin/gh" << 'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FIXTURE_LOG"
case "$1 $2" in
"run list")
    jq_filter=""
    while [[ $# -gt 0 ]]; do
        [[ "$1" == --jq ]] && jq_filter="$2"
        shift
    done
    jq -c "$jq_filter" <<< "$FIXTURE_RUNS"
    ;;
"run download")
    dir=""
    name=""
    while [[ $# -gt 0 ]]; do
        [[ "$1" == --dir ]] && dir="$2"
        [[ "$1" == --name ]] && name="$2"
        shift
    done
    [[ "$name" == "release-packages-v1.2.3" ]] || exit 1
    printf 'pkg\n' > "$dir/spanwit_1.2.3_linux_amd64.tar.gz"
    ;;
"release view") [[ "${FIXTURE_RELEASE_EXISTS:-0}" == 1 ]] ;;
"release create") exit 0 ;;
*) exit 1 ;;
esac
SH
chmod +x "$scratch/bin/gh"
export PATH="$scratch/bin:$PATH" FIXTURE_LOG="$scratch/gh.log"

fail() {
    local reason="$1"
    shift
    if (cd "$repo" && "$@") > "$scratch/out" 2>&1; then
        echo "error: expected rejection: $reason" >&2
        exit 1
    fi
    grep -q "$reason" "$scratch/out" || {
        echo "error: missing named rejection: $reason" >&2
        cat "$scratch/out" >&2
        exit 1
    }
}
run_json() { printf '[%s]' "$1"; }
good="{\"databaseId\":11,\"headSha\":\"$commit\",\"conclusion\":\"success\",\"event\":\"push\"}"
other="{\"databaseId\":12,\"headSha\":\"$(printf '%040d' 0)\",\"conclusion\":\"success\",\"event\":\"push\"}"
failed="{\"databaseId\":13,\"headSha\":\"$commit\",\"conclusion\":\"failure\",\"event\":\"push\"}"
dispatch="{\"databaseId\":14,\"headSha\":\"$commit\",\"conclusion\":\"success\",\"event\":\"workflow_dispatch\"}"

# Fetch: refuses a non-canonical tag, a missing local tag, zero or multiple
# matching runs, runs on another commit, failed runs and non-push events.
fail 'usage' ./scripts/release-fetch-ci-artifacts.sh 1.2.3 "$scratch/d0"
fail 'published tag v9.9.9 failed verification' ./scripts/release-fetch-ci-artifacts.sh v9.9.9 "$scratch/d0"
for runs in "$(run_json "$other")" "$(run_json "$failed")" "$(run_json "$dispatch")" '[]'; do
    FIXTURE_RUNS="$runs" fail 'expected exactly one successful release run' \
        ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$scratch/d1"
done
FIXTURE_RUNS="$(run_json "$good,${good/11/15}")" fail 'found 2' \
    ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$scratch/d1"
# Refuses a non-empty destination.
mkdir -p "$scratch/full"
printf 'stale\n' > "$scratch/full/stale"
FIXTURE_RUNS="$(run_json "$good,$other,$failed")" fail 'is not empty' \
    ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$scratch/full"
# Downloads only the named artifact from the one run on the tagged commit.
: > "$FIXTURE_LOG"
(cd "$repo" && FIXTURE_RUNS="$(run_json "$good,$other,$failed,$dispatch")" \
    ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$scratch/d2") > /dev/null
grep -q '^run download 11 --repo 3leaps/spanwit --name release-packages-v1.2.3 ' "$FIXTURE_LOG"
[[ "$(cat "$scratch/d2.anchor")" == "$(printf 'tag=v1.2.3\nobject=%s\ncommit=%s\nrun=11' "$object" "$commit")" ]]
[[ -f "$scratch/d2/spanwit_1.2.3_linux_amd64.tar.gz" ]]

# Draft. Packages are staged against the anchor that fetch recorded.
stage() {
    local dir="$1" name
    rm -rf "$dir"
    mkdir -p "$dir"
    for name in spanwit_1.2.3_linux_amd64.tar.gz spanwit_1.2.3_linux_arm64.tar.gz \
        spanwit_1.2.3_windows_amd64.zip spanwit_1.2.3_windows_arm64.zip spanwit_1.2.3_darwin_arm64.tar.gz; do
        printf '%s\n' "$name" > "$dir/$name"
    done
    (cd "$dir" && shasum -a 256 ./*.tar.gz ./*.zip | sed 's| \./| |' > SHA256SUMS &&
        shasum -a 512 ./*.tar.gz ./*.zip | sed 's| \./| |' > SHA512SUMS)
    printf 'tag=v1.2.3\nobject=%s\ncommit=%s\nrun=11\n' "$object" "$commit" > "$dir.anchor"
}
no_create() {
    if grep -q '^release create' "$FIXTURE_LOG"; then
        echo "error: draft created despite: $1" >&2
        exit 1
    fi
}
d="$scratch/draft"
draft_fail() {
    local reason="$1"
    : > "$FIXTURE_LOG"
    fail "$reason" ./scripts/release-create-draft.sh v1.2.3 "$d"
    no_create "$reason"
}

stage "$d"
FIXTURE_RELEASE_EXISTS=1 draft_fail 'already exists'
# Unlisted stale archive from another version, present alongside a valid set.
stage "$d"
printf 'old\n' > "$d/spanwit_0.1.0_linux_amd64.tar.gz"
draft_fail 'unexpected file'
# Extra, unexpected file of any kind (tooling residue).
stage "$d"
printf 'tool\n' > "$d/goneat"
draft_fail 'unexpected file'
# Hidden extra files, including names starting with "..", refuse.
for hidden in .stale '..stale' '...x'; do
    stage "$d"
    printf 'x\n' > "$d/$hidden"
    draft_fail 'unexpected file'
done
# A subdirectory is not a regular file and refuses.
stage "$d"
mkdir "$d/extra-dir"
draft_fail 'unexpected file'

# Missing expected archive.
stage "$d"
rm "$d/spanwit_1.2.3_darwin_arm64.tar.gz"
draft_fail 'missing release file'
# Manifest that omits an archive, and one listing an extra name.
stage "$d"
grep -v darwin "$d/SHA256SUMS" > "$d/x" && mv "$d/x" "$d/SHA256SUMS"
draft_fail 'does not list exactly'
stage "$d"
printf '%064d  spanwit_0.1.0_linux_amd64.tar.gz\n' 0 >> "$d/SHA512SUMS"
draft_fail 'malformed or duplicate\|does not list exactly'
# Archive whose bytes no longer match the manifest.
stage "$d"
printf 'tampered\n' > "$d/spanwit_1.2.3_linux_amd64.tar.gz"
draft_fail 'digest mismatch'
# Tag replaced between the earlier checks and the API call.
stage "$d"
FIXTURE_TAG_REPLACED=1 draft_fail 'no longer matches the verified anchor'
# Direct invocation with an empty directory fails closed.
rm -rf "$d"
mkdir -p "$d"
draft_fail 'missing release file'

# Missing, mismatched-tag or malformed anchor refuses.
stage "$d"
rm "$d.anchor"
draft_fail 'missing'
stage "$d"
sed -i.bak 's/^tag=v1.2.3$/tag=v1.2.4/' "$d.anchor"
draft_fail 'different tag'
stage "$d"
printf 'tag=v1.2.3\nobject=zz\ncommit=zz\n' > "$d.anchor"
draft_fail 'malformed'

# The draft body must come from the committed per-version notes.
stage "$d"
mv "$repo/docs/releases/v1.2.3.md" "$scratch/notes.hold"
draft_fail 'missing release notes'
mv "$scratch/notes.hold" "$repo/docs/releases/v1.2.3.md"
stage "$d"
printf 'edited after tagging\n' >> "$repo/docs/releases/v1.2.3.md"
draft_fail 'differs from the tagged commit'
git -C "$repo" checkout -q -- docs/releases/v1.2.3.md

# Valid creation: exactly the five archives plus both manifests, re-verified
# immediately before the create call.
stage "$d"
: > "$FIXTURE_LOG"
(cd "$repo" && ./scripts/release-create-draft.sh v1.2.3 "$d") > /dev/null
create="$(grep '^release create' "$FIXTURE_LOG")"
[[ "$create" == *' --verify-tag --draft '* ]]
[[ "$create" == *"--notes-file $repo/docs/releases/v1.2.3.md"* || "$create" == *'--notes-file '*'/docs/releases/v1.2.3.md'* ]]
[[ "$create" != *'--generate-notes'* ]]
[[ "$(grep -o 'spanwit_1\.2\.3_[a-z0-9_]*\.\(tar\.gz\|zip\)' <<< "$create" | sort -u | wc -l | tr -d ' ')" == 5 ]]
[[ "$create" == *'SHA256SUMS'* && "$create" == *'SHA512SUMS'* && "$create" != *'0.1.0'* ]]
[[ "$(grep -n '' "$FIXTURE_LOG" | grep -E 'reverify|release create' | cut -d: -f2 | head -1)" == reverify* ]]
# The pre-API re-verification is bound to exactly the staged anchor.
grep -q "^reverify v1.2.3 expect=$object/$commit\$" "$FIXTURE_LOG"
echo '[ok] CI-artifact fetch and local draft controls passed'

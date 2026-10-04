#!/usr/bin/env bash
# Download the release packages that the tag-triggered workflow built for the
# verified tag, from the workflow run on that exact tagged commit. The workflow
# holds no write authority; the draft release is created locally afterwards.
# Usage: release-fetch-ci-artifacts.sh vX.Y.Z [dest_dir]
set -euo pipefail
die() {
    echo "error: $*" >&2
    exit 1
}
tag="${1:-}"
dest="${2:-dist/release}"
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || die 'usage: release-fetch-ci-artifacts.sh vX.Y.Z [dest_dir]'
command -v gh > /dev/null 2>&1 || die 'gh (GitHub CLI) not found in PATH'
root="$(cd "$(dirname "$0")/.." && pwd -P)"
mkdir -p "$dest"
[[ -z "$(find "$dest" -mindepth 1 -maxdepth 1 2> /dev/null)" ]] || die "$dest is not empty; run make release-clean first"
# Verify the published tag now and record the verified object and commit. Every
# later step, including draft creation, is bound to this anchor.
anchor="$(mktemp)"
trap 'rm -f "$anchor"' EXIT
SPANWIT_RELEASE_TAG="$tag" SPANWIT_ANCHOR_OUT="$anchor" "$root/scripts/release-verify-published-tag.sh" > /dev/null ||
    die "published tag $tag failed verification"
commit="$(awk -F= '$1=="commit" {print $2}' "$anchor")"
object="$(awk -F= '$1=="object" {print $2}' "$anchor")"
[[ "$commit" =~ ^[0-9a-f]{40}$ && "$object" =~ ^[0-9a-f]{40}$ ]] || die 'verification did not return an anchor'
# Exactly one successful Release run for this tag on the tagged commit.
runs="$(gh run list --repo 3leaps/spanwit --workflow release.yml --branch "$tag" \
    --json databaseId,headSha,conclusion,event \
    --jq "[.[] | select(.headSha == \"$commit\" and .conclusion == \"success\" and .event == \"push\") | .databaseId]")" ||
    die 'could not list release workflow runs'
count="$(jq 'length' <<< "$runs")"
[[ "$count" == 1 ]] || die "expected exactly one successful release run for $tag at $commit, found $count"
run_id="$(jq -r '.[0]' <<< "$runs")"
gh run download "$run_id" --repo 3leaps/spanwit --name "release-packages-$tag" --dir "$dest" ||
    die "could not download release-packages-$tag from run $run_id"
# The anchor lives outside the staged directory so it is never uploaded.
printf 'tag=%s\nobject=%s\ncommit=%s\nrun=%s\n' "$tag" "$object" "$commit" "$run_id" > "$dest.anchor"
echo "[ok] downloaded release packages for $tag (object $object, commit $commit) from run $run_id into $dest"

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
for tool in gh jq python3 shasum; do
    command -v "$tool" > /dev/null 2>&1 || die "$tool not found in PATH"
done
root="$(cd "$(dirname "$0")/.." && pwd -P)"
mkdir -p "$dest"
[[ -z "$(find "$dest" -mindepth 1 -maxdepth 1 2> /dev/null)" ]] || die "$dest is not empty; run make release-clean first"
# The run is named with the SPANWIT_ prefix, like every other ceremony input.
# A generic RELEASE_RUN_ID/ATTEMPT left in the shell from an earlier release
# would silently name the wrong run, so its presence is refused outright, before
# any network call.
for generic in RELEASE_RUN_ID RELEASE_RUN_ATTEMPT; do
    [[ -z "${!generic+x}" ]] ||
        die "generic $generic is set; unset it and use SPANWIT_$generic (it may be left over from an earlier release)"
done
# Verify the published tag now and record the verified object and commit. Every
# later step, including draft creation, is bound to this anchor.
anchor="$(mktemp)"
trap 'rm -f "$anchor"' EXIT
SPANWIT_RELEASE_TAG="$tag" SPANWIT_ANCHOR_OUT="$anchor" "$root/scripts/release-verify-published-tag.sh" > /dev/null ||
    die "published tag $tag failed verification"
commit="$(awk -F= '$1=="commit" {print $2}' "$anchor")"
object="$(awk -F= '$1=="object" {print $2}' "$anchor")"
[[ "$commit" =~ ^[0-9a-f]{40}$ && "$object" =~ ^[0-9a-f]{40}$ ]] || die 'verification did not return an anchor'
# The maintainer names the exact run and attempt (re-runs share a run id). The
# named attempt must be a successful push-triggered release.yml run for this tag
# on the tagged commit.
run_id="${SPANWIT_RELEASE_RUN_ID:-}"
run_attempt="${SPANWIT_RELEASE_RUN_ATTEMPT:-}"
[[ "$run_id" =~ ^[1-9][0-9]*$ ]] || die 'SPANWIT_RELEASE_RUN_ID required (the Release workflow run for this tag)'
[[ "$run_attempt" =~ ^[1-9][0-9]*$ ]] || die 'SPANWIT_RELEASE_RUN_ATTEMPT required (the successful attempt of that run)'
run="$(gh api "repos/3leaps/spanwit/actions/runs/$run_id/attempts/$run_attempt" \
    --jq '{path, event, head_branch, head_sha, status, conclusion, run_attempt}')" ||
    die "could not read run $run_id attempt $run_attempt"
jq -e --arg tag "$tag" --arg commit "$commit" --argjson attempt "$run_attempt" '
    .path == ".github/workflows/release.yml" and .event == "push" and .head_branch == $tag and
    .head_sha == $commit and .status == "completed" and .conclusion == "success" and
    .run_attempt == $attempt' <<< "$run" > /dev/null ||
    die "run $run_id attempt $run_attempt is not a successful release run for $tag at $commit"
# `gh run download` cannot select an attempt, so bind to one artifact instead:
# record its id and digest while the named attempt is the latest, download that
# exact id, check the bytes against the digest, then confirm nothing moved.
artifact_name="release-packages-$tag"
snapshot() {
    local latest listing
    latest="$(gh api "repos/3leaps/spanwit/actions/runs/$run_id" --jq '.run_attempt')" ||
        die "could not read run $run_id"
    [[ "$latest" == "$run_attempt" ]] ||
        die "run $run_id has a later attempt ($latest); artifacts belong to the latest attempt, name it"
    listing="$(gh api "repos/3leaps/spanwit/actions/runs/$run_id/artifacts")" ||
        die "could not list artifacts for run $run_id"
    # One page: refuse a run whose artifacts do not all fit on it, so "exactly
    # one" below is exact for the whole run.
    jq -e '.total_count == (.artifacts | length)' <<< "$listing" > /dev/null ||
        die "artifact list for run $run_id is incomplete"
    listing="$(jq --arg n "$artifact_name" '[.artifacts[] | select(.name == $n and .expired == false)]' <<< "$listing")" ||
        die "could not read the artifact list for run $run_id"
    jq -e --arg c "$commit" --argjson r "$run_id" \
        'length == 1 and (.[0].workflow_run.id == $r) and (.[0].workflow_run.head_sha == $c)' <<< "$listing" > /dev/null ||
        die "run $run_id does not hold exactly one unexpired $artifact_name for $commit"
    jq -r '.[0] | "\(.id) \(.digest // "")"' <<< "$listing"
}
# A failed snapshot exits its subshell only; stop here rather than read nothing.
before="$(snapshot)" || exit 1
read -r artifact_id artifact_digest <<< "$before"
[[ "$artifact_id" =~ ^[1-9][0-9]*$ ]] || die 'artifact id is malformed'
[[ "$artifact_digest" =~ ^sha256:[0-9a-f]{64}$ ]] || die "artifact $artifact_id has no sha256 digest"
work="$(mktemp -d)"
trap 'rm -f "$anchor"; rm -rf "$work"' EXIT
gh api "repos/3leaps/spanwit/actions/artifacts/$artifact_id/zip" > "$work/artifact.zip" ||
    die "could not download artifact $artifact_id"
actual="sha256:$(shasum -a 256 "$work/artifact.zip" | awk '{print $1}')"
[[ "$actual" == "$artifact_digest" ]] || die "artifact $artifact_id bytes do not match its recorded digest"
after="$(snapshot)" || exit 1
[[ "$after" == "$before" ]] || die "run $run_id changed during download (artifact $before -> $after); restage"
# Extract into private scratch first: flat, unique, plain names only. Nothing
# reaches the destination unless every entry passes.
mkdir "$work/x"
python3 -I - "$work/artifact.zip" "$work/x" << 'PY' || die "artifact $artifact_id has an unexpected layout"
import pathlib, re, sys, zipfile
dest = pathlib.Path(sys.argv[2])
seen = set()
with zipfile.ZipFile(sys.argv[1]) as z:
    for info in z.infolist():
        name = info.filename
        if info.is_dir() or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._-]*', name) or name in seen:
            raise SystemExit(f'error: unexpected entry in artifact: {name!r}')
        # Unix mode bits in the high word: refuse anything but a regular file.
        mode = info.external_attr >> 16
        if mode and (mode & 0o170000) not in (0, 0o100000):
            raise SystemExit(f'error: non-regular entry in artifact: {name!r}')
        seen.add(name)
        # 'xb' refuses an existing file, so a name that only aliases an earlier
        # one (case-only on a case-insensitive filesystem) cannot overwrite it.
        try:
            with open(dest / name, 'xb') as out:
                out.write(z.read(info))
        except FileExistsError:
            raise SystemExit(f'error: entry collides with an earlier one: {name!r}')
if not seen:
    raise SystemExit('error: artifact is empty')
PY
mv "$work/x"/* "$dest/"
# The anchor lives outside the staged directory so it is never uploaded, and is
# written only after the packages are in place.
printf 'tag=%s\nobject=%s\ncommit=%s\nrun=%s\nattempt=%s\nartifact=%s\ndigest=%s\n' \
    "$tag" "$object" "$commit" "$run_id" "$run_attempt" "$artifact_id" "$artifact_digest" > "$dest.anchor"
echo "[ok] downloaded release packages for $tag (object $object, commit $commit) from run $run_id attempt $run_attempt, artifact $artifact_id into $dest"

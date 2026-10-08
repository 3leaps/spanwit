#!/usr/bin/env bash
# Create the draft GitHub release for a verified tag from locally staged,
# checksum-verified packages. Runs on the maintainer machine only; CI holds no
# write authority. Fails closed when invoked directly: every guard below runs
# here, not only as Make prerequisites.
#
# Usage: release-create-draft.sh vX.Y.Z [source_dir]
#
# The upload set is exactly the expected tag-versioned archives, each listed in
# both manifests and matching them; any missing, unlisted, extra or
# wrong-version file refuses. The published tag object is re-verified
# immediately before the API call. A tag ref can still move between that check
# and the API call; promotion re-verifies again.
set -euo pipefail
die() {
    echo "error: $*" >&2
    exit 1
}
tag="${1:-}"
src="${2:-dist/release}"
[[ "$tag" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || die 'usage: release-create-draft.sh vX.Y.Z [source_dir]'
version="${tag#v}"
root="$(cd "$(dirname "$0")/.." && pwd -P)"
command -v gh > /dev/null 2>&1 || die 'gh (GitHub CLI) not found in PATH'
[[ -d "$src" && ! -L "$src" ]] || die "source dir not found: $src"
binary="${SPANWIT_BINARY_NAME:-spanwit}"
# Must match the build matrix in scripts/package-artifacts.sh.
expected=(
    "${binary}_${version}_linux_amd64.tar.gz"
    "${binary}_${version}_linux_arm64.tar.gz"
    "${binary}_${version}_windows_amd64.zip"
    "${binary}_${version}_windows_arm64.zip"
    "${binary}_${version}_darwin_arm64.tar.gz"
)
manifests=(SHA256SUMS SHA512SUMS)

# The directory holds exactly the expected archives and manifests. Signatures,
# keys and notes are uploaded later by the provenance step, not here.
# Every entry, including any hidden name (".x", "..x"), is checked: the find
# below lists all of them, so any extra file refuses.
while IFS= read -r -d '' entry; do
    name="${entry##*/}"
    allowed=0
    for want in "${expected[@]}" "${manifests[@]}"; do
        [[ "$name" == "$want" ]] && allowed=1
    done
    [[ "$allowed" == 1 ]] || die "unexpected file in $src: $name (run make release-clean and restage)"
    [[ -f "$entry" && ! -L "$entry" ]] || die "not a regular file: $name"
done < <(find "$src" -mindepth 1 -maxdepth 1 -print0)
for want in "${expected[@]}" "${manifests[@]}"; do
    [[ -f "$src/$want" && -s "$src/$want" ]] || die "missing release file: $want"
done

# Each manifest lists exactly the expected archives, and each matches.
python3 - "$src" "${expected[@]}" << 'PY'
import hashlib
import pathlib
import sys

src = pathlib.Path(sys.argv[1])
expected = sorted(sys.argv[2:])
for manifest, algorithm, width in (('SHA256SUMS', 'sha256', 64), ('SHA512SUMS', 'sha512', 128)):
    entries = {}
    for line in (src / manifest).read_text().splitlines():
        if not line.strip():
            continue
        digest, sep, name = line.partition('  ')
        name = name.lstrip('*')
        if not sep or len(digest) != width or name in entries:
            raise SystemExit(f'error: malformed or duplicate {manifest} line')
        entries[name] = digest.lower()
    if sorted(entries) != expected:
        raise SystemExit(f'error: {manifest} does not list exactly the expected tag-versioned archives')
    for name in expected:
        actual = hashlib.new(algorithm, (src / name).read_bytes()).hexdigest()
        if actual != entries[name]:
            raise SystemExit(f'error: {manifest} digest mismatch for {name}')
PY

if gh release view "$tag" --repo 3leaps/spanwit > /dev/null 2>&1; then
    die "a release for $tag already exists; do not replace it"
fi
# The packages were staged against a verified tag object and commit. Bind the
# pre-API re-verification to that exact anchor: a replacement tag, even one
# signed by the approved key, must not attach these packages to another object.
anchor="$src.anchor"
[[ -f "$anchor" && ! -L "$anchor" ]] || die "missing $anchor; stage packages with make release-download"
[[ "$(awk -F= '$1=="tag" {print $2}' "$anchor")" == "$tag" ]] || die 'staged anchor is for a different tag'
anchor_object="$(awk -F= '$1=="object" {print $2}' "$anchor")"
anchor_commit="$(awk -F= '$1=="commit" {print $2}' "$anchor")"
[[ "$anchor_object" =~ ^[0-9a-f]{40}$ && "$anchor_commit" =~ ^[0-9a-f]{40}$ ]] || die 'staged anchor is malformed'
# Re-verify the published tag immediately before the API call, against the anchor.
SPANWIT_RELEASE_TAG="$tag" SPANWIT_EXPECTED_TAG_OBJECT="$anchor_object" SPANWIT_EXPECTED_COMMIT="$anchor_commit" \
    "$root/scripts/release-verify-published-tag.sh" > /dev/null ||
    die 'published tag no longer matches the verified anchor the packages were staged against'
# The draft body is the curated per-version notes committed in the repository,
# never generated from pull-request history.
notes="$root/docs/releases/$tag.md"
[[ -f "$notes" && ! -L "$notes" && -s "$notes" ]] || die "missing release notes: docs/releases/$tag.md"
# The notes must be byte-identical to the file at the anchored tag commit, so an
# edit made after tagging (or after preflight) cannot reach the release body.
tagged_notes="$(git -C "$root" rev-parse --verify --quiet "$anchor_commit:docs/releases/$tag.md")" ||
    die "docs/releases/$tag.md is absent at the tagged commit"
[[ "$(git -C "$root" hash-object -- "$notes")" == "$tagged_notes" ]] ||
    die "docs/releases/$tag.md differs from the tagged commit"
files=()
for want in "${expected[@]}" "${manifests[@]}"; do
    files+=("$src/$want")
done
gh release create "$tag" --repo 3leaps/spanwit --verify-tag --draft \
    --title "$tag" --notes-file "$notes" "${files[@]}"
echo "[ok] draft release $tag created from ${#expected[@]} verified package(s)"

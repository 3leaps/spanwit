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

# Stub gh. FIXTURE_ATTEMPT is the run-attempt JSON the API returns and
# FIXTURE_LATEST is the latest attempt number of that run;
# FIXTURE_RELEASE_EXISTS controls `gh release view`. Every call is logged.
mkdir -p "$scratch/bin"
cat > "$scratch/bin/gh" << 'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$FIXTURE_LOG"
case "$1 $2" in
"api "*)
    path="$2"
    jq_filter=""
    while [[ $# -gt 0 ]]; do
        [[ "$1" == --jq ]] && jq_filter="$2"
        shift
    done
    case "$path" in
    */attempts/*) body="${FIXTURE_ATTEMPT:-}" ;;
    */runs/*/artifacts)
        # Listing calls are counted so a test can change the answer between the
        # before- and after-download snapshots (a re-run landing mid-download).
        n="$(($(cat "$FIXTURE_DIR/listings" 2> /dev/null || echo 0) + 1))"
        printf '%s\n' "$n" > "$FIXTURE_DIR/listings"
        if [[ -n "${FIXTURE_LISTING:-}" ]]; then
            body="$FIXTURE_LISTING"
        else
            id="${FIXTURE_ARTIFACT_ID:-501}"
            [[ "$n" -ge 2 && -n "${FIXTURE_ARTIFACT_ID_AFTER:-}" ]] && id="$FIXTURE_ARTIFACT_ID_AFTER"
            digest="${FIXTURE_DIGEST:-sha256:$(shasum -a 256 "$FIXTURE_DIR/artifact.zip" | awk '{print $1}')}"
            [[ "$n" -ge 2 && -n "${FIXTURE_DIGEST_AFTER:-}" ]] && digest="$FIXTURE_DIGEST_AFTER"
            body="$(jq -n --argjson id "$id" --arg d "$digest" --arg c "${FIXTURE_ARTIFACT_SHA:-$FIXTURE_COMMIT}" \
                --argjson e "${FIXTURE_EXPIRED:-false}" \
                '{total_count: 1, artifacts: [{id: $id, name: "release-packages-v1.2.3", expired: $e, digest: $d,
                  workflow_run: {id: 11, head_sha: $c}}]}')"
        fi
        ;;
    */artifacts/*/zip)
        [[ "$path" == */artifacts/"${FIXTURE_ARTIFACT_ID:-501}"/zip ]] || exit 1
        cat "${FIXTURE_SERVED_ZIP:-$FIXTURE_DIR/artifact.zip}"
        exit 0
        ;;
    *)
        m="$(($(cat "$FIXTURE_DIR/runs" 2> /dev/null || echo 0) + 1))"
        printf '%s\n' "$m" > "$FIXTURE_DIR/runs"
        latest="${FIXTURE_LATEST:-1}"
        [[ "$m" -ge 2 && -n "${FIXTURE_LATEST_AFTER:-}" ]] && latest="$FIXTURE_LATEST_AFTER"
        body="{\"run_attempt\":$latest}"
        ;;
    esac
    [[ -n "$body" ]] || exit 1
    if [[ -n "$jq_filter" ]]; then jq -c "$jq_filter" <<< "$body"; else printf '%s\n' "$body"; fi
    ;;
"run download") exit 1 ;;
"release view") [[ "${FIXTURE_RELEASE_EXISTS:-0}" == 1 ]] ;;
"release create") exit 0 ;;
*) exit 1 ;;
esac
SH
chmod +x "$scratch/bin/gh"
export PATH="$scratch/bin:$PATH" FIXTURE_LOG="$scratch/gh.log" FIXTURE_DIR="$scratch/fx" FIXTURE_COMMIT="$commit"
mkdir -p "$FIXTURE_DIR"
# make_zip OUT NAME=CONTENT... ; a NAME of @link:TARGET adds a symlink entry.
make_zip() {
    python3 -I - "$@" << 'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], 'w') as z:
    for spec in sys.argv[2:]:
        name, _, body = spec.partition('=')
        if name.startswith('@link:'):
            info = zipfile.ZipInfo(name[len('@link:'):])
            info.external_attr = (0o120777 << 16)
            z.writestr(info, body)
        else:
            z.writestr(name, body)
PY
}
# The CI artifact: a zip holding one release package.
make_zip "$FIXTURE_DIR/artifact.zip" 'spanwit_1.2.3_linux_amd64.tar.gz=pkg'
reset_listings() { rm -f "$FIXTURE_DIR/listings" "$FIXTURE_DIR/runs"; }

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
attempt_json() { # conclusion event head_sha attempt [path] [branch]
    printf '{"path":"%s","event":"%s","head_branch":"%s","head_sha":"%s","status":"completed","conclusion":"%s","run_attempt":%s}' \
        "${5:-.github/workflows/release.yml}" "$2" "${6:-v1.2.3}" "$3" "$1" "$4"
}
good="$(attempt_json success push "$commit" 2)"
export RELEASE_RUN_ID=11 RELEASE_RUN_ATTEMPT=2 FIXTURE_LATEST=2

# Fetch: refuses a non-canonical tag, a missing local tag, a missing run id or
# attempt, and any named attempt that is not a successful push-triggered
# release run for this tag on the tagged commit or is not the latest attempt.
fail 'usage' ./scripts/release-fetch-ci-artifacts.sh 1.2.3 "$scratch/d0"
fail 'published tag v9.9.9 failed verification' ./scripts/release-fetch-ci-artifacts.sh v9.9.9 "$scratch/d0"
RELEASE_RUN_ID='' FIXTURE_ATTEMPT="$good" fail 'RELEASE_RUN_ID required' \
    ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$scratch/d1"
RELEASE_RUN_ATTEMPT='' FIXTURE_ATTEMPT="$good" fail 'RELEASE_RUN_ATTEMPT required' \
    ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$scratch/d1"
for bad in \
    "$(attempt_json failure push "$commit" 2)" \
    "$(attempt_json success workflow_dispatch "$commit" 2)" \
    "$(attempt_json success push "$(printf '%040d' 0)" 2)" \
    "$(attempt_json success push "$commit" 1)" \
    "$(attempt_json success push "$commit" 2 .github/workflows/ci.yml)" \
    "$(attempt_json success push "$commit" 2 .github/workflows/release.yml v1.2.4)" \
    "$(attempt_json success push "$commit" 2 | sed 's/"completed"/"in_progress"/')"; do
    FIXTURE_ATTEMPT="$bad" fail 'is not a successful release run' \
        ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$scratch/d1"
done
FIXTURE_ATTEMPT="$good" FIXTURE_LATEST=3 fail 'has a later attempt' \
    ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$scratch/d1"

# Artifact identity. Every refusal below must leave no anchor and no package.
fetch_refuses() {
    local reason="$1" dir="$scratch/dA"
    shift
    rm -rf "$dir" "$dir.anchor"
    reset_listings
    (cd "$repo" && env FIXTURE_ATTEMPT="$good" "$@" ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$dir") \
        > "$scratch/out" 2>&1 && {
        echo "error: expected rejection: $reason" >&2
        exit 1
    }
    grep -q "$reason" "$scratch/out" || {
        echo "error: missing named rejection: $reason" >&2
        cat "$scratch/out" >&2
        exit 1
    }
    [[ ! -e "$dir.anchor" ]] || {
        echo "error: anchor written despite: $reason" >&2
        exit 1
    }
    [[ -z "$(find "$dir" -mindepth 1 2> /dev/null)" ]] || {
        echo "error: files staged despite: $reason" >&2
        exit 1
    }
}
listing() { # one JSON artifact object per argument, wrapped as a complete listing
    local IFS=,
    printf '{"total_count":%s,"artifacts":[%s]}' "$#" "$*"
}
artifact() { # id head_sha [expired] [name] [run]
    printf '{"id":%s,"name":"%s","expired":%s,"digest":"sha256:%s","workflow_run":{"id":%s,"head_sha":"%s"}}' \
        "$1" "${4:-release-packages-v1.2.3}" "${3:-false}" \
        "$(shasum -a 256 "$FIXTURE_DIR/artifact.zip" | awk '{print $1}')" "${5:-11}" "$2"
}
# A re-run attempt appears while the artifact downloads.
fetch_refuses 'has a later attempt' FIXTURE_LATEST_AFTER=3
# The artifact is replaced by another of the same name mid-download.
fetch_refuses 'changed during download' FIXTURE_ARTIFACT_ID_AFTER=502
fetch_refuses 'changed during download' FIXTURE_DIGEST_AFTER="sha256:$(printf '%064d' 7)"
# The bytes served are not the bytes the listing's digest names.
make_zip "$scratch/other.zip" 'spanwit_1.2.3_linux_amd64.tar.gz=other'
fetch_refuses 'do not match its recorded digest' FIXTURE_SERVED_ZIP="$scratch/other.zip"
fetch_refuses 'do not match its recorded digest' FIXTURE_DIGEST="sha256:$(printf '%064d' 9)"
# No digest, or a malformed one.
fetch_refuses 'has no sha256 digest' FIXTURE_LISTING="$(listing "$(artifact 501 "$commit" | sed 's/"digest":"[^"]*"/"digest":null/')")"
fetch_refuses 'has no sha256 digest' FIXTURE_LISTING="$(listing "$(artifact 501 "$commit" | sed 's/sha256:/md5:/')")"
# Built from another commit, by another run, expired, absent, or ambiguous.
fetch_refuses 'does not hold exactly one unexpired' FIXTURE_ARTIFACT_SHA="$(printf '%040d' 3)"
fetch_refuses 'does not hold exactly one unexpired' FIXTURE_LISTING="$(listing "$(artifact 501 "$commit" false release-packages-v1.2.3 12)")"
fetch_refuses 'does not hold exactly one unexpired' FIXTURE_EXPIRED=true
fetch_refuses 'does not hold exactly one unexpired' FIXTURE_LISTING='{"total_count":0,"artifacts":[]}'
# A run whose artifacts span more than the one page read is refused.
fetch_refuses 'artifact list for run 11 is incomplete' \
    FIXTURE_LISTING="$(listing "$(artifact 501 "$commit")" | sed 's/"total_count":1/"total_count":31/')"
fetch_refuses 'does not hold exactly one unexpired' FIXTURE_LISTING="$(listing "$(artifact 501 "$commit")" "$(artifact 502 "$commit")")"
# An expired twin does not make a live artifact ambiguous; a differently named
# one is ignored.
FIXTURE_LISTING="$(listing "$(artifact 501 "$commit")" "$(artifact 499 "$commit" true)" "$(artifact 498 "$commit" false release-packages-v1.2.2)")"
reset_listings
rm -rf "$scratch/dB" "$scratch/dB.anchor"
(cd "$repo" && FIXTURE_ATTEMPT="$good" FIXTURE_LISTING="$FIXTURE_LISTING" ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$scratch/dB") > /dev/null
[[ -f "$scratch/dB/spanwit_1.2.3_linux_amd64.tar.gz" && -f "$scratch/dB.anchor" ]]
# Zip layouts that are not flat, unique, plain files refuse before staging.
cp "$FIXTURE_DIR/artifact.zip" "$scratch/good.zip"
for layout in \
    'nested/spanwit_1.2.3_linux_amd64.tar.gz=pkg' \
    '../spanwit_1.2.3_linux_amd64.tar.gz=pkg' \
    '.hidden=pkg' \
    '@link:spanwit_1.2.3_linux_amd64.tar.gz=/etc/passwd'; do
    make_zip "$FIXTURE_DIR/artifact.zip" 'spanwit_1.2.3_darwin_arm64.tar.gz=ok' "$layout"
    fetch_refuses 'unexpected layout'
done
make_zip "$FIXTURE_DIR/artifact.zip" 'spanwit_1.2.3_linux_amd64.tar.gz=a' 'spanwit_1.2.3_linux_amd64.tar.gz=b' 2> /dev/null
fetch_refuses 'unexpected layout'
make_zip "$FIXTURE_DIR/artifact.zip"
fetch_refuses 'unexpected layout'
# Names that differ only in case alias on a case-insensitive filesystem; on a
# case-sensitive one both land, and the draft's exact-set check refuses the
# extra file. Either way nothing is silently overwritten.
make_zip "$FIXTURE_DIR/artifact.zip" 'SHA256SUMS=real' 'sha256sums=shadow'
probe="$scratch/case-probe"
mkdir -p "$probe"
printf 'a' > "$probe/CaseProbe"
if [[ -e "$probe/caseprobe" ]]; then
    fetch_refuses 'unexpected layout'
fi
cp "$scratch/good.zip" "$FIXTURE_DIR/artifact.zip"
reset_listings
# Refuses a non-empty destination.
mkdir -p "$scratch/full"
printf 'stale\n' > "$scratch/full/stale"
FIXTURE_ATTEMPT="$good" fail 'is not empty' ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$scratch/full"
# Downloads exactly the bound artifact id from the named, verified attempt, and
# re-reads the run and the listing after the download.
: > "$FIXTURE_LOG"
(cd "$repo" && FIXTURE_ATTEMPT="$good" ./scripts/release-fetch-ci-artifacts.sh v1.2.3 "$scratch/d2") > /dev/null
grep -q '^api repos/3leaps/spanwit/actions/artifacts/501/zip$' "$FIXTURE_LOG"
if grep -q '^run download' "$FIXTURE_LOG"; then exit 1; fi
[[ "$(grep -c '^api repos/3leaps/spanwit/actions/runs/11/artifacts$' "$FIXTURE_LOG")" == 2 ]]
[[ "$(grep -n '' "$FIXTURE_LOG" | grep -E 'artifacts(/501/zip)?$' | tail -1)" == *'/runs/11/artifacts' ]]
digest="sha256:$(shasum -a 256 "$FIXTURE_DIR/artifact.zip" | awk '{print $1}')"
[[ "$(cat "$scratch/d2.anchor")" == "$(printf 'tag=v1.2.3\nobject=%s\ncommit=%s\nrun=11\nattempt=2\nartifact=501\ndigest=%s' "$object" "$commit" "$digest")" ]]
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
    printf 'tag=v1.2.3\nobject=%s\ncommit=%s\nrun=11\nattempt=2\n' "$object" "$commit" > "$dir.anchor"
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

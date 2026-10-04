#!/usr/bin/env bash

set -euo pipefail

# Upload provenance assets (manifests, signatures, public keys, release notes)
# to an existing GitHub release. Does NOT upload binaries (CI already does that).
#
# Usage: release-upload-provenance.sh <tag> [dir]

TAG="${1:-}"
SOURCE_DIR="${2:-dist/release}"

if [[ -z "${TAG}" ]]; then
    echo "usage: $0 vX.Y.Z [source_dir]" >&2
    exit 1
fi

if ! command -v gh > /dev/null 2>&1; then
    echo "❌ gh (GitHub CLI) not found in PATH" >&2
    echo "Install: https://cli.github.com/" >&2
    exit 1
fi

if [[ ! -d "${SOURCE_DIR}" ]]; then
    echo "❌ Source dir not found: ${SOURCE_DIR}" >&2
    exit 1
fi

shopt -s nullglob

# Provenance-only: manifests, signatures, keys, notes. NO binaries.
assets=()
assets+=("${SOURCE_DIR}/SHA256SUMS" "${SOURCE_DIR}/SHA512SUMS")
assets+=("${SOURCE_DIR}/SHA256SUMS."* "${SOURCE_DIR}/SHA512SUMS."*)
assets+=("${SOURCE_DIR}"/*.pub)
assets+=("${SOURCE_DIR}"/*release-signing-key.asc)
assets+=("${SOURCE_DIR}"/release-notes-*.md)

final_assets=()
for f in "${assets[@]}"; do
    if [[ -f "$f" ]]; then
        final_assets+=("$f")
    fi
done

if [[ ${#final_assets[@]} -eq 0 ]]; then
    echo "❌ No provenance assets found to upload from ${SOURCE_DIR}" >&2
    exit 1
fi

echo "→ Uploading ${#final_assets[@]} provenance asset(s) to ${TAG} (clobber)"
gh release upload "${TAG}" "${final_assets[@]}" --clobber

echo "✅ Provenance upload complete"

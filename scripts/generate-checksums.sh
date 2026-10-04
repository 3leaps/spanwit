#!/usr/bin/env bash

set -euo pipefail

# Generate SHA256SUMS and SHA512SUMS over the release archives.
# Usage: generate-checksums.sh [dir] <binary_name>
#
# Operates on the packaged archives produced by package-artifacts.sh
# (<binary>_<version>_<os>_<arch>.{tar.gz,zip}), which are the artifacts that
# actually ship in the GitHub release.
#
# POSIX-portable on purpose (no `shopt`, no bash arrays): runs the same under
# sh or bash so it never depends on how the recipe shell resolves the shebang.

DIR=${1:-dist/release}
BINARY_NAME=${2:-}

if [ -z "${BINARY_NAME}" ]; then
    echo "usage: $0 [dir] <binary_name>" >&2
    exit 1
fi

if [ ! -d "${DIR}" ]; then
    echo "error: directory ${DIR} not found" >&2
    exit 1
fi

cd "${DIR}"

rm -f SHA256SUMS SHA512SUMS

if command -v sha256sum > /dev/null 2>&1; then
    SHA256="sha256sum"
else
    SHA256="shasum -a 256"
fi
if command -v sha512sum > /dev/null 2>&1; then
    SHA512="sha512sum"
else
    SHA512="shasum -a 512"
fi

found=0
for f in "${BINARY_NAME}"_*.tar.gz "${BINARY_NAME}"_*.zip; do
    # Skip the literal glob when nothing matches (no nullglob needed).
    [ -e "$f" ] || continue
    found=1
    ${SHA256} "$f" >> SHA256SUMS
    ${SHA512} "$f" >> SHA512SUMS
done

if [ "${found}" -ne 1 ]; then
    echo "error: no archives found matching ${BINARY_NAME}_*.{tar.gz,zip} in ${DIR} (run 'make package' first)" >&2
    exit 1
fi

echo "✅ Wrote ${DIR}/SHA256SUMS and ${DIR}/SHA512SUMS"

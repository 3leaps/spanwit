#!/usr/bin/env bash
set -euo pipefail

# Package release archives for the microtool (one .tar.gz / .zip per OS/arch).
#
# Checksums and signing are intentionally NOT done here — they are owned by the
# release ceremony (make release-checksums / release-sign; see RELEASE_CHECKLIST.md)
# so there is a single manifest/signature owner.
#
# Notes:
# - Expects binaries already built in ./bin (use `make build-all` first).
# - BINARY_NAME can be overridden via env; defaults to "spanwit".
# - Archives land in dist/release as <binary>_<version>_<os>_<arch>.{tar.gz,zip}.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

PROJECT="${BINARY_NAME:-spanwit}"
VERSION="$(cat VERSION)"
BIN_DIR="bin"
OUT_DIR="dist/release"
OUT_DIR_ABS="$(mkdir -p "${OUT_DIR}" && cd "${OUT_DIR}" && pwd)"

mkdir -p "${OUT_DIR}"

package() {
    local os="$1" arch="$2"
    local ext="" archive_ext="tar.gz"
    local bin="${BIN_DIR}/${PROJECT}-${os}-${arch}"

    if [[ "${os}" == "windows" ]]; then
        ext=".exe"
        bin="${bin}${ext}"
        archive_ext="zip"
    fi

    local archive_name="${PROJECT}_${VERSION}_${os}_${arch}.${archive_ext}"

    if [[ ! -f "${bin}" ]]; then
        echo "Skipping ${os}/${arch}: binary not found: ${bin}" >&2
        return
    fi

    tmpdir="$(mktemp -d)"
    trap 'rm -rf "${tmpdir}"' RETURN

    local bin_name="${PROJECT}${ext}"
    cp "${bin}" "${tmpdir}/${bin_name}"
    chmod +x "${tmpdir}/${bin_name}"

    case "${archive_ext}" in
        tar.gz)
            (cd "${tmpdir}" && tar -czf "${OUT_DIR_ABS}/${archive_name}" "${bin_name}")
            ;;
        zip)
            # Prefer the `zip` CLI; fall back to python3 (the goneat-tools
            # runner image ships python3 but not zip).
            if command -v zip > /dev/null 2>&1; then
                (cd "${tmpdir}" && zip -q "${OUT_DIR_ABS}/${archive_name}" "${bin_name}")
            elif command -v python3 > /dev/null 2>&1; then
                (cd "${tmpdir}" && python3 -c 'import sys,zipfile; z=zipfile.ZipFile(sys.argv[1],"w",zipfile.ZIP_DEFLATED); z.write(sys.argv[2]); z.close()' "${OUT_DIR_ABS}/${archive_name}" "${bin_name}")
            else
                echo "Neither zip nor python3 available to create ${archive_name}" >&2
                exit 1
            fi
            ;;
    esac

    echo "Packaged ${archive_name}"
}

# Build matrix (aligns with make build-all):
# linux amd64/arm64, windows amd64/arm64, darwin arm64 (no darwin amd64).
package linux amd64
package linux arm64
package windows amd64
package windows arm64
package darwin arm64

echo "Artifacts in ${OUT_DIR_ABS}:"
ls -lh "${OUT_DIR_ABS}"

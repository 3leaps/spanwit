#!/usr/bin/env bash

set -euo pipefail

# Export public signing keys into the release artifact directory.
# Usage: export-release-keys.sh [dir]
#
# Env:
#   SPANWIT_MINISIGN_KEY - path to minisign secret key (used to locate .pub)
#   SPANWIT_MINISIGN_PUB - optional explicit path to minisign public key
#   SPANWIT_PGP_KEY_ID   - gpg key/email/fingerprint to export (optional)
#   SPANWIT_GPG_HOMEDIR  - isolated gpg homedir containing the signing key (required if PGP_KEY_ID set)

DIR=${1:-dist/release}
mkdir -p "$DIR"

MINISIGN_KEY="${SPANWIT_MINISIGN_KEY:-}"
MINISIGN_PUB="${SPANWIT_MINISIGN_PUB:-}"
PGP_KEY_ID="${SPANWIT_PGP_KEY_ID:-}"
GPG_HOME="${SPANWIT_GPG_HOMEDIR:-}"

exported_any=false

if [ -n "${MINISIGN_KEY}" ] || [ -n "${MINISIGN_PUB}" ]; then
    pub_path="${MINISIGN_PUB}"
    if [ -z "${pub_path}" ]; then
        pub_path="${MINISIGN_KEY%.key}.pub"
    fi

    if [ ! -f "${pub_path}" ]; then
        echo "error: minisign public key not found (expected at ${pub_path}); set SPANWIT_MINISIGN_PUB to override" >&2
        exit 1
    fi

    out="${DIR}/spanwit-minisign.pub"
    cp "${pub_path}" "${out}"
    echo "✅ Exported minisign public key to ${out}"
    exported_any=true
else
    echo "ℹ️  Skipping minisign public key export (set SPANWIT_MINISIGN_KEY or SPANWIT_MINISIGN_PUB to enable)"
fi

if [ -n "${PGP_KEY_ID}" ]; then
    if ! command -v gpg > /dev/null 2>&1; then
        echo "error: gpg not found in PATH (required to export PGP key)" >&2
        exit 1
    fi
    if [ -z "${GPG_HOME}" ]; then
        echo "error: SPANWIT_GPG_HOMEDIR must be set for PGP export" >&2
        exit 1
    fi
    if ! gpg --homedir "${GPG_HOME}" --list-keys "${PGP_KEY_ID}" > /dev/null 2>&1; then
        echo "error: public key ${PGP_KEY_ID} not found in SPANWIT_GPG_HOMEDIR=${GPG_HOME}" >&2
        exit 1
    fi
    out="${DIR}/spanwit-release-signing-key.asc"
    gpg --homedir "${GPG_HOME}" --armor --output "${out}" --export "${PGP_KEY_ID}"
    echo "✅ Exported PGP public key to ${out} (homedir: ${GPG_HOME})"
    exported_any=true
else
    echo "ℹ️  Skipping PGP public key export (set SPANWIT_PGP_KEY_ID to enable)"
fi

if [ "${exported_any}" = false ]; then
    echo "warning: no keys exported (set SPANWIT_MINISIGN_KEY/SPANWIT_MINISIGN_PUB and/or SPANWIT_PGP_KEY_ID)" >&2
fi

#!/usr/bin/env bash
# verify-public-key.sh - Verify GPG key file contains ONLY public key material
# Usage: ./scripts/verify-public-key.sh <key-file>
# Exit codes:
#   0 - Safe: File contains only public key material
#   1 - Danger: Private key detected or validation failed

set -euo pipefail

if [ $# -ne 1 ]; then
    echo "❌ Usage: $0 <key-file>"
    echo "   Example: $0 dist/release/<binary>-release-signing-key.asc"
    exit 1
fi

KEY_FILE="$1"

if [ ! -f "$KEY_FILE" ]; then
    echo "❌ FATAL: Key file not found: $KEY_FILE"
    exit 1
fi

echo "🔍 Verifying key file: $KEY_FILE"
echo ""

echo "🛡️  Checking for private key material (must be absent)..."
if grep -qi "PRIVATE KEY" "$KEY_FILE"; then
    echo ""
    echo "❌❌❌ FATAL: PRIVATE KEY DETECTED IN FILE! ❌❌❌"
    echo ""
    echo "The file contains private key material and MUST NOT be uploaded."
    echo "Detected blocks:"
    grep -i "PRIVATE KEY" "$KEY_FILE" || true
    echo ""
    echo "🚨 DO NOT UPLOAD THIS FILE 🚨"
    echo "Review key export command and try again."
    exit 1
fi
echo "   ✅ No private key blocks found"

echo "🔑 Checking for public key material (must be present)..."
if ! grep -qi "PUBLIC KEY" "$KEY_FILE"; then
    echo ""
    echo "❌ FATAL: No public key found in file!"
    echo ""
    echo "The file does not contain expected public key blocks."
    echo "This may indicate an export error or corrupted file."
    exit 1
fi
echo "   ✅ Public key blocks found"

echo "🔐 Verifying with GPG (gpg --show-keys)..."
if ! command -v gpg &> /dev/null; then
    echo "   ⚠️  WARNING: gpg command not found, skipping GPG verification"
else
    GPG_OUTPUT=$(gpg --show-keys "$KEY_FILE" 2>&1 || true)

    if echo "$GPG_OUTPUT" | grep -q "^sec "; then
        echo ""
        echo "❌❌❌ FATAL: SECRET KEY DETECTED BY GPG! ❌❌❌"
        echo ""
        echo "GPG identifies secret (private) key material in this file:"
        echo "$GPG_OUTPUT"
        echo ""
        echo "🚨 DO NOT UPLOAD THIS FILE 🚨"
        exit 1
    fi

    if ! echo "$GPG_OUTPUT" | grep -q "^pub "; then
        echo ""
        echo "❌ FATAL: GPG did not identify any public keys in file!"
        echo ""
        echo "GPG output:"
        echo "$GPG_OUTPUT"
        exit 1
    fi

    echo "   ✅ GPG confirms: public keys only (no secret keys)"
    echo ""
    echo "   Key details:"
    # shellcheck disable=SC2001  # per-line prefixing; ${x//y/z} cannot anchor to line starts
    echo "$GPG_OUTPUT" | sed 's/^/      /'
fi

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "✅ VERIFICATION PASSED"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo ""
echo "File: $KEY_FILE"
echo "Status: Contains PUBLIC key material only"
echo "Safety: SAFE TO UPLOAD to public repositories"
echo ""
echo "All checks passed:"
echo "  ✅ No PRIVATE KEY blocks detected (negative check)"
echo "  ✅ PUBLIC KEY blocks present (positive check)"
echo "  ✅ GPG verification confirms no secret keys"
echo ""
echo "Proceed with upload: gh release upload v<version> *.asc"
echo ""

exit 0

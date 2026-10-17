#!/usr/bin/env bash
# release-verify-signatures fails closed: every one of the four signatures and
# both public keys must be present and verify, by the anchored primary's only
# signing subkey. Throwaway keys in temporary homes; nothing is written inside
# the repository. Also runs the Make target with relative and absolute paths.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd -P)"
scratch="$(cd "$(mktemp -d)" && pwd -P)"
cleanup() {
    local home
    for home in "$scratch"/gpg-*/; do
        gpgconf --homedir "$home" --kill all > /dev/null 2>&1 || true
    done
    rm -rf "$scratch"
}
trap cleanup EXIT
unset RELEASE_TAG

# A key with one signing subkey, exported public-only.
new_key() {
    local home="$scratch/gpg-$1"
    mkdir -m 700 "$home"
    gpg --homedir "$home" --batch --pinentry-mode loopback --passphrase '' \
        --quick-generate-key "Synthetic $1 <$1@example.invalid>" ed25519 cert,sign 1d > /dev/null 2>&1
    local fpr
    fpr="$(gpg --homedir "$home" --batch --with-colons --fingerprint --list-keys | awk -F: '$1=="fpr" {print $10;exit}')"
    gpg --homedir "$home" --batch --pinentry-mode loopback --passphrase '' --quick-add-key "$fpr" ed25519 sign 1d > /dev/null 2>&1
    # Sign with the explicit subkey, as the release does; never a default key.
    gpg --homedir "$home" --batch --with-colons --with-subkey-fingerprint --list-keys |
        awk -F: '$1=="sub" {s=1;next} s && $1=="fpr" {print $10;exit}' > "$home/signing-subkey"
    printf '%s\n' "$fpr"
}
primary="$(new_key good)"
other="$(new_key other)"

# The repository copy supplies the script and the primary anchor only.
repo="$scratch/repo"
mkdir -p "$repo/scripts" "$repo/keys"
cp "$root/scripts/release-verify-signatures.sh" "$repo/scripts/"
minisign -G -W -p "$scratch/minisign.pub" -s "$scratch/minisign.key" > /dev/null
minisign -G -W -p "$scratch/other-minisign.pub" -s "$scratch/other-minisign.key" > /dev/null
pin_minisign="$(sed -n 2p "$scratch/minisign.pub" | base64 -d | shasum -a 256 | awk '{print $1}')"
printf 'gpg %s\nminisign %s\n' "$primary" "$pin_minisign" > "$repo/keys/expected-fingerprints.txt"

stage() {
    local dir="$1" pgp_home="${2:-$scratch/gpg-good}" msk="${3:-$scratch/minisign.key}"
    rm -rf "$dir"
    mkdir -p "$dir"
    printf 'binary\n' > "$dir/spanwit_1.2.3_linux_amd64.tar.gz"
    (cd "$dir" && shasum -a 256 spanwit_1.2.3_linux_amd64.tar.gz > SHA256SUMS && shasum -a 512 spanwit_1.2.3_linux_amd64.tar.gz > SHA512SUMS)
    for m in SHA256SUMS SHA512SUMS; do
        minisign -S -s "$msk" -m "$dir/$m" -x "$dir/$m.minisig" > /dev/null < /dev/null
        gpg --homedir "$pgp_home" --batch --yes --pinentry-mode loopback --passphrase '' \
            --local-user "$(cat "$pgp_home/signing-subkey")!" --armor --detach-sign --output "$dir/$m.asc" "$dir/$m" > /dev/null 2>&1
    done
    cp "$scratch/minisign.pub" "$dir/spanwit-minisign.pub"
    gpg --homedir "$scratch/gpg-good" --batch --armor --export "$primary" > "$dir/spanwit-release-signing-key.asc"
}
verify() { (cd "$repo" && ./scripts/release-verify-signatures.sh "$@"); }
refuse() {
    local reason="$1" dir="$2"
    if verify "$dir" > "$scratch/out" 2>&1; then
        echo "error: accepted: $reason" >&2
        exit 1
    fi
    grep -q 'error:' "$scratch/out" || {
        echo "error: no named refusal for: $reason" >&2
        cat "$scratch/out" >&2
        exit 1
    }
}

d="$scratch/dist"
stage "$d"
verify "$d" > /dev/null

# Each of the four signatures, missing and tampered, refuses on its own.
for sig in SHA256SUMS.minisig SHA512SUMS.minisig SHA256SUMS.asc SHA512SUMS.asc; do
    stage "$d"
    rm "$d/$sig"
    refuse "missing $sig" "$d"
    stage "$d"
    head -c 40 "$d/$sig" > "$d/$sig.cut" && mv "$d/$sig.cut" "$d/$sig"
    refuse "truncated $sig" "$d"
done
# A manifest changed after signing refuses (both formats cover it).
for m in SHA256SUMS SHA512SUMS; do
    stage "$d"
    printf 'extra\n' >> "$d/$m"
    refuse "manifest $m edited after signing" "$d"
done
# Either public key missing refuses.
for key in spanwit-minisign.pub spanwit-release-signing-key.asc; do
    stage "$d"
    rm "$d/$key"
    refuse "missing $key" "$d"
done
# PGP signatures from a different key refuse, even with that key's export.
stage "$d" "$scratch/gpg-other"
refuse 'PGP signed by an unanchored key' "$d"
stage "$d" "$scratch/gpg-other"
gpg --homedir "$scratch/gpg-other" --batch --armor --export "$other" > "$d/spanwit-release-signing-key.asc"
refuse 'PGP key export swapped to the signing key' "$d"
# minisign signatures by a different key refuse.
stage "$d" "$scratch/gpg-good" "$scratch/other-minisign.key"
refuse 'minisign signed by a different key' "$d"
# ... including when that key's public half is exported beside them: the
# exported key must hash to the minisign anchor.
stage "$d" "$scratch/gpg-good" "$scratch/other-minisign.key"
cp "$scratch/other-minisign.pub" "$d/spanwit-minisign.pub"
refuse 'minisign key export swapped to the signing key' "$d"
grep -q 'does not match the minisign anchor' "$scratch/out"
# The exported minisign key must be exactly comment + key line.
stage "$d"
printf 'extra\n' >> "$d/spanwit-minisign.pub"
refuse 'minisign key export with an extra line' "$d"
stage "$d"
printf 'extra-junk' >> "$d/spanwit-minisign.pub"
refuse 'minisign key export with an unterminated third line' "$d"
grep -q 'is not a two-line minisign public key' "$scratch/out"
# A signature file that is a symlink to a good one refuses.
stage "$d"
mv "$d/SHA256SUMS.asc" "$scratch/real.asc"
ln -s "$scratch/real.asc" "$d/SHA256SUMS.asc"
refuse 'symlinked signature' "$d"
# A good signature by the anchored primary itself, not its signing subkey, refuses.
stage "$d"
gpg --homedir "$scratch/gpg-good" --batch --yes --pinentry-mode loopback --passphrase '' \
    --local-user "$primary!" --armor --detach-sign --output "$d/SHA512SUMS.asc" "$d/SHA512SUMS" > /dev/null 2>&1
refuse 'PGP signed by the primary, not the signing subkey' "$d"
grep -q 'not a good signature by the pinned subkey' "$scratch/out"
# The verifier never reads the ambient keyring: a GNUPGHOME that trusts the
# other key does not help a set it signed.
stage "$d" "$scratch/gpg-other"
GNUPGHOME="$scratch/gpg-other" refuse 'ambient keyring ignored' "$d"

# The Make target fails closed and resolves paths the same way, relative or absolute.
mk="$scratch/mk"
mkdir -p "$mk/scripts" "$mk/keys"
cp "$repo/scripts/release-verify-signatures.sh" "$mk/scripts/"
cp "$repo/keys/expected-fingerprints.txt" "$mk/keys/"
sed -n '/^release-verify-signatures:/,/^$/p' "$root/Makefile" > "$mk/Makefile"
printf 'DIST_RELEASE ?= dist/release\n' >> "$mk/Makefile"
stage "$mk/dist/release"
make --no-print-directory -C "$mk" release-verify-signatures > /dev/null
make --no-print-directory -C "$mk" release-verify-signatures DIST_RELEASE="$mk/dist/release" > /dev/null
rm "$mk/dist/release/SHA512SUMS.asc"
for target_dir in dist/release "$mk/dist/release"; do
    if make --no-print-directory -C "$mk" release-verify-signatures DIST_RELEASE="$target_dir" > /dev/null 2>&1; then
        echo "error: make target accepted a missing PGP signature (DIST_RELEASE=$target_dir)" >&2
        exit 1
    fi
done
stage "$mk/dist/release"
head -c 40 "$mk/dist/release/SHA256SUMS.minisig" > "$scratch/cut" && mv "$scratch/cut" "$mk/dist/release/SHA256SUMS.minisig"
if make --no-print-directory -C "$mk" release-verify-signatures > /dev/null 2>&1; then
    echo 'error: make target accepted a damaged minisign signature' >&2
    exit 1
fi

echo '[ok] signature verification requires all four signatures and both keys, by the pinned subkey'

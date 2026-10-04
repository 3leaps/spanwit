# ADR-0005: Release Publication Gate

> **Status**: Accepted
> **Date**: 2026-10-02
> **Authors**: Release engineering (supervised AI draft; maintainer review)

## Context

A pushed `v*` tag triggers the release workflow. Before this decision the
workflow built binaries and opened a draft GitHub release with a write token,
and any annotated tag pushed by an account with write access could start it. The release
artifacts were signed later, but nothing bound the tag itself to a reviewed
key.

## Decision

Starting with the first signed version tag, the tag signature authorizes the
exact commit and the release artifacts built from it.

**Trust root: the committed public pin at the tagged commit.** The signature is
verified against `docs/security/release-signing-keys.asc` as it exists in the
tagged commit, using an isolated, temporary keyring that holds only that pin.
The verifier reads the pin, the fingerprint anchors and the tagger identity
from the tagged commit's tree, never from the working tree.
The pin and `keys/expected-fingerprints.txt` land through their own reviewed
pull request before the first signed tag. Review and merge of the pin,
combined with the fixed infosec tagger, is the authorization boundary.

**The authorization boundary is the maintainer machine, not CI.** A tag push
runs the release workflow defined in the tagged commit, so anyone able to push
a tag also controls the code that checks it. CI therefore cannot authorize a
release, and it is given no authority to create one. The workflow is
read-only (`contents: read` throughout). It holds no signing material and no
write token. It builds the verified commit and uploads the packages only as a
workflow artifact. Its signature and tag-object checks are diagnostic and
consistency controls that stop a bad tag early; they are not independent
authorization.

**The maintainer creates the release.** On the maintainer machine, the
ceremony first re-verifies the published tag. It then downloads the packages
from the successful workflow run on that exact tagged commit, regenerates and
verifies checksums, re-verifies the tag immediately before the release API
call against the exact tag object and commit the packages were staged from (a
replacement tag, even one signed by the approved key, is refused before any
local ref changes), and creates the **draft** release from exactly the expected,
manifest-verified, tag-versioned packages; any missing, unlisted or
wrong-version file refuses. It signs, uploads provenance and finally promotes the draft. The
reviewed workflow therefore never creates, modifies or publishes a GitHub
release.

**Threat scope (maintainer-confirmed).** These controls are designed for an
attacker who can push or move a tag but cannot change repository settings or
workflow policy. Repository and organization administrators, workflow authors,
and holders of maintainer-issued automation credentials with workflow write
access are trusted. Removing
write permission from the reviewed workflow does not set a ceiling on a
_replacement_ workflow defined by an attacker-controlled tag: such a workflow
can request any `GITHUB_TOKEN` permission that repository policy allows. The
regression test on the workflow file guards the reviewed definition, not a
replacement. An attacker who controls workflow definitions, repository or
organization administration, or the maintainer's GitHub account is out of
scope. The external boundary is the repository tag ruleset on `refs/tags/v*`,
which blocks creation, update, deletion and force-push for everyone except
organization administrators, so a tag-only attacker cannot push a release tag
at all. Every
workflow in this repository declares or needs only read permission, and the
repository default `GITHUB_TOKEN` permission is set to read-only. That default
narrows what an undeclared job receives; it is not a ceiling, because a
workflow can still request write permission in its own definition. Even within
scope, the official release is established only by the maintainer ceremony
and its re-verification, never by anything a workflow produces.

**Every publication step re-verifies the published tag, using trusted code.**
Downloading, signing, uploading and promoting release assets each first verify
the remote annotated tag object on the maintainer machine. That check runs the
verifier from the maintainer's own checkout, reads the tagged commit only as
inert git objects, verifies the signature against the pin committed in that
commit, and requires GitHub to report it Verified. The approved primary
fingerprint is an independent input from the maintainer environment
(`SPANWIT_GPG_SIGNING_FINGERPRINT`), never inferred from the tagged commit: a
commit can carry any key together with a matching fingerprint file, so
internal consistency alone does not prove it is the approved key. A historical
release is verified with the primary that was approved for it. It never executes code from
the tag target and does not depend on the current `main`. Promotion of the
draft to public is the authorization step.

**GitHub Verified is required but not sufficient.** GitHub must report the tag
object Verified with reason `valid`, but a key registered on a GitHub account
does not replace the committed pin.

**Reconsider if CI gains authority.** If CI ever holds signing material or can
publish, a trusted publication path is required first: for example a workflow
dispatched from the protected default branch, with constrained permissions and
required approval, that treats the tag only as data.

**Residual risk is explicit.** A remote tag name is a mutable ref. These checks
narrow, but cannot remove, the window between a check and a later GitHub API
call. Tag-protection rules reduce that risk, but they are reported only as an
advisory, so this design does not claim that tags are immutable.

**Primary named, subkey selected exactly.** The `gpg` line in
`keys/expected-fingerprints.txt` names the **primary** key. The pin carries one
primary and exactly one signing-capable subkey, which is the only key
permitted to sign release tags; the verifier enforces this from the committed
pin, so CI needs no operator selector. The maintainer selects that subkey
exactly with the `<40-hex fingerprint>!` form. Verification rejects a signature
made directly by the primary, by an expired or revoked key, or by any key not
in the pin.

**Signing happens only on the maintainer machine, never in CI.** Message
preparation, local signing and remote push are separate make targets. The
complete public tag message is prepared and reviewed outside the repository.
The local tag must match that message, the infosec tagger in
`config/release/tagger-identity.txt` and the exact commit on `origin/main`. CI
holds no signing credentials. Tag-protection rules are reported as a read-only
advisory and are not part of the signed gate.

**Public material only.** The repository holds only public keys and public
fingerprints. Private signing material stays outside the working tree, as
required by the
[3 Leaps OSS Sensitive Local Data Policy](https://github.com/3leaps/oss-policies/blob/main/SENSITIVE-LOCAL-DATA.md).

Earlier unsigned annotated releases remain historical.

## Fingerprint anchors

`keys/expected-fingerprints.txt` is exactly two lines, each ending in a newline:

```text
gpg <40 uppercase hex>
minisign <64 lowercase hex>
```

- `gpg`: the OpenPGP v4 fingerprint of the single primary key in the pin, read
  from a temporary keyring into which only the pin was imported.
- `minisign`: the SHA-256, in lowercase hex, of the minisign public key blob.
  The blob is the base64-decoded key line of the minisign public key file:
  2 bytes of algorithm (`Ed` or `ED`), an 8-byte key ID and a 32-byte Ed25519
  public key, 42 bytes in total. The optional single `untrusted comment:` line
  and surrounding whitespace are not hashed. Anything other than exactly one
  such key is refused. This matches the `minisign-public-blob-sha256-v1` scheme
  used by other 3 Leaps repositories, so the anchors can be compared across
  them.

`make release-insert-anchors` derives both lines twice and compares the
results. It refuses to overwrite an existing anchor file. Reviewers re-derive
both lines independently. `scripts/validate-release-anchors.sh` checks the
format strictly and confirms that the pin holds exactly one primary equal to
the `gpg` line.

## Rotation

Rotating a key requires a prior reviewed change that replaces both the pin and
the anchors. Tags signed before a rotation remain verifiable from their own
commits, which still carry the earlier pin.

## Consequences

- The reviewed CI workflow never creates, modifies or publishes a release, and
  holds no write permission. Only the maintainer ceremony does, after verifying the published tag against the approved key,
  the pin committed in the tagged commit, and GitHub.
- Release tags are cut from a maintainer machine that holds the signing
  subkey. Without the committed pin, the release tooling fails closed.
- The existing artifact signing ceremony (minisign and optional GPG signatures
  over the checksum manifests) is unchanged. It accepts the same exact subkey
  selector.

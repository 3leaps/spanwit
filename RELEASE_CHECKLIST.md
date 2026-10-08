# Release Checklist

Standard checklist for spanwit releases to ensure consistency and quality.

## Pre-Release Phase

### Version Planning

- [ ] All planned features implemented and tested
- [ ] Breaking changes documented
- [ ] Migration guide written (if applicable)
- [ ] Version number decided (semantic versioning: MAJOR.MINOR.PATCH)

### Code Quality

- [ ] All tests passing: `make test`
- [ ] Code formatted: `make fmt`
- [ ] Lint checks clean: `make lint`
- [ ] Application builds: `make build`
- [ ] Manual smoke tests completed (against `bin/spanwit` after `make build`):
  - [ ] `./bin/spanwit version` — version + build metadata
  - [ ] `./bin/spanwit --help` — CLI surface intact
  - [ ] `./bin/spanwit doctor` — environment + dependency checks
  - [ ] `./bin/spanwit envinfo` — runtime introspection
  - [ ] `./bin/spanwit scan ~/dev` — read-only scan path
  - [ ] `./bin/spanwit prune --format json` — dry-run plan emits valid JSON
  - [ ] `./bin/spanwit validate --help` — schema validation path

### Documentation

- [ ] `README.md` reviewed and updated
- [ ] Feature documentation added to `docs/` (if applicable)
- [ ] CLI help text accurate
- [ ] `CHANGELOG.md` updated for this version
- [ ] `docs/releases/v<version>.md` written (the per-version release notes)

### Dependencies

- [ ] `go.mod` dependencies reviewed
- [ ] Local replace directives removed (switch to published releases)
- [ ] `go mod tidy` executed and `git diff --exit-code go.mod go.sum` confirms no module drift; if drift appears, handle it in a dedicated dependency PR before continuing
- [ ] `goneat dependencies --licenses --vuln` reports `Passed: true` and `violations: 0`
- [ ] SBOM/vulnerability output under `sbom/` spot-checked; raw scanner noise is documented, not ignored silently
- [ ] No reachable high/critical security vulnerabilities in dependencies

## Release Preparation

### Version Updates

- [ ] Update `VERSION` file
- [ ] Update `.fulmen/app.yaml` version and re-sync the embedded mirror: `make sync-embedded-identity` (then `make verify-embedded-identity`)
- [ ] Version sanity check: `make release-guard-tag-version SPANWIT_RELEASE_TAG=v<version>` (or `RELEASE_TAG=v<version>` for one-off invocations)
- [ ] Search for hardcoded version references (`grep -rE "0\.1\.0" --include="*.go" --include="*.md" --include="*.yaml"`)

### Git Hygiene

- [ ] All changes committed
- [ ] Commit messages follow the attribution standard (trailers: Co-Authored-By `noreply@3leaps.net`, `Role:`, `Committer-of-Record:`)
- [ ] No uncommitted changes: `git status` clean
- [ ] Pre-push checks run: `make prepush`
- [ ] Final PR checks run: `make pr-final`

### Final Validation

- [ ] `make pr-final` passes from a clean working tree
- [ ] Fresh clone test: clone repo fresh, run `make build && make test`

## Release Execution

### Signed Tag (maintainer machine only; triggers the read-only CI build)

Release tags are annotated and GPG-signed by the infosec tagger with the
signing subkey of the primary key committed in
`docs/security/release-signing-keys.asc`. CI checks the signature as an early
warning, builds the packages and uploads them only as a workflow artifact; it
never creates a release and cannot authorize one, because a tag push runs the
workflow defined in the tagged commit. The maintainer creates the draft
locally. The authorization step is
promotion on the maintainer machine, which re-verifies the published tag with
trusted local code. See
[ADR-0005](docs/decisions/ADR-0005-release-publication-gate.md). Signing never
happens in CI.

Environment for the signed tag (set on the maintainer machine; never commit
any of it):

| Variable                          | Value                                                                                                                                                                               |
| --------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `SPANWIT_RELEASE_TAG`             | `v<version>`; must equal `v` + `VERSION`                                                                                                                                            |
| `SPANWIT_TAG_MESSAGE_DIR`         | Absolute directory outside the repo, ending in the tag (e.g. `<dir>/v<version>`)                                                                                                    |
| `SPANWIT_TAGGER_NAME`             | `3 Leaps Infosec Team` (must match `config/release/tagger-identity.txt`)                                                                                                            |
| `SPANWIT_TAGGER_EMAIL`            | `infosec@3leaps.net`                                                                                                                                                                |
| `SPANWIT_GPG_SIGNING_FINGERPRINT` | 40 uppercase hex of the approved **primary**, kept in the maintainer environment as the independent trust anchor; post-tag targets refuse a tag whose committed pin differs from it |
| `SPANWIT_PGP_KEY_ID`              | 40 uppercase hex of the **signing subkey** followed by `!` (exact selection)                                                                                                        |
| `SPANWIT_GPG_HOMEDIR`             | Isolated GPG home outside the repository                                                                                                                                            |
| `SPANWIT_APPROVED_ENV_LOADER`     | Optional absolute path to an operator env file outside the repo, sourced by the targets                                                                                             |

The same `SPANWIT_PGP_KEY_ID` / `SPANWIT_GPG_HOMEDIR` values are used for the
artifact signing ceremony below; `gpg` honors the `!` form there too.

- [ ] Public pin and fingerprints are already on `main` (see
      [One-time: public pin and fingerprints](#one-time-public-pin-and-fingerprints))
- [ ] The release commit is merged and `main` is the exact tree that passed `make pr-final`
- [ ] Check out `main` at fetched `origin/main` with a clean tree (the targets refuse anything else)
- [ ] Run the pre-tag gate: `make release-preflight`. It refuses a dirty or unsynced tree, a private repository (the release workflow reads the tag anonymously), missing changelog or release notes, unpinned or Node 20 actions, a failing dates check, tests that fail from a clone outside the home directory, and a failing `pr-final`
- [ ] Prepare the public tag message outside the repo, then review it (no-clobber; edit the file if you want custom public text): `make release-prepare-tag-message`
- [ ] Create the signed tag locally (does not push): `make release-tag`
- [ ] Re-verify against the committed pin: `make release-verify-tag`
- [ ] Push only the tag and confirm it is unchanged: `make release-push-tag`
- [ ] Confirm the remote object equals the local one and GitHub reports Verified: `make release-verify-remote-tag`
- [ ] The Release workflow (`.github/workflows/release.yml`) checks the signature (early warning), then builds and packages binaries and uploads them as a workflow artifact. It is read-only and never creates a release

Tag-protection rules are reported by the tag targets as a read-only advisory
(`FOUND` / `ABSENT` / `UNKNOWN`); they are not a signing gate. Because a tag
name is a mutable ref, every publication target below first runs
`make release-verify-published-tag`. It re-verifies the remote tag object
against the pin committed in its own commit, plus GitHub verification,
independent of the current `main`. Post-tag targets check only that `RELEASE_TAG`
is canonical, not that it matches the checked-out `VERSION`; `release-notes`
still reads `docs/releases/<tag>.md` from the checkout, so stage the notes from
the tagged commit. If a signed tag
is pushed with a mistake, do not move or re-sign it; fix forward with the next
patch version.

### Signing Ceremony (operator: devlead, manual)

Follow the Fulmen "manifest-only" provenance pattern: sign the checksum manifests
(not every binary) with both minisign and PGP, and ship the
public keys as trust anchors. CI never creates a release: the ceremony creates
the **draft** from verified CI packages and promotes it only after signing, so
consumers never see an unsigned release window.

- [ ] Source the operator-private release env once (sets `SPANWIT_MINISIGN_KEY`, `SPANWIT_MINISIGN_PUB`, `SPANWIT_PGP_KEY_ID`, `SPANWIT_GPG_HOMEDIR`):

  ```bash
  source <operator-private-release-env>
  ```

- [ ] Set the release tag once for the whole ceremony (resolves `SPANWIT_RELEASE_TAG` → `RELEASE_TAG`; never auto-defaults to `v<VERSION>`):

  ```bash
  export SPANWIT_RELEASE_TAG=v<version>
  ```

- [ ] Download the CI-built packages for the verified tag, regenerate and verify manifests, then create the draft release:

  ```bash
  make release-clean
  export RELEASE_RUN_ID=<release run id> RELEASE_RUN_ATTEMPT=<successful attempt>
  make release-download        # verifies the tag and the named run attempt, records the anchor, fetches the artifact
  make release-checksums
  make release-verify-checksums
  make release-create-draft    # re-verifies against that anchor; exact manifest-verified packages only
  ```

- [ ] Sign manifests with minisign and PGP (publish refuses either missing): `make release-sign`
- [ ] Export public keys: `make release-export-keys`
- [ ] Verify exported keys are public-only: `make release-verify-keys`
- [ ] Verify signatures: `make release-verify-signatures`
- [ ] Copy release notes: `make release-notes`
- [ ] Upload provenance assets (manifests + sigs + keys + notes): `make release-upload`
- [ ] **Review the draft** (devlead): binaries present, notes accurate, signatures + keys attached
- [ ] **Promote draft → public** (final step, the authorization step): `make release-publish` re-verifies the published tag, the manifests, both signatures against the committed pins, and downloads the draft to confirm every asset is byte-identical to the verified local set before promoting

For one-off invocations without sourcing the env file, pass `RELEASE_TAG=v<version>`
to each `make` invocation. The guard target `release-guard-tag-version` (wired as a dep
of the release targets) fails loud if neither is set, or if the tag is not a
canonical `vX.Y.Z` equal to `v` + `VERSION`.

### One-time: public pin and fingerprints

The committed public pin and its fingerprint anchors land in **their own
reviewed PR before the first signed tag**, and again before any key rotation.
The tag of a release is verified against the pin in the tagged commit itself,
so the pin must already be on `main`. Only public material is committed.

On the maintainer machine, with `SPANWIT_GPG_HOMEDIR`, `SPANWIT_PGP_KEY_ID`
(`<subkey>!`), `SPANWIT_GPG_SIGNING_FINGERPRINT` and `SPANWIT_MINISIGN_PUB`
(path to the minisign **public** key) set, on a fresh branch:

- [ ] Export the public key to `docs/security/release-signing-keys.asc` (refuses to overwrite; refuses the default `~/.gnupg`): `make release-export-pin`
- [ ] Generate `keys/expected-fingerprints.txt` (validates the pin first, derives twice and compares, refuses to overwrite): `make release-insert-anchors`
- [ ] Re-run the read-only validation and inspect the displayed expiry and revocation status: `make release-validate-pin`
- [ ] Reviewers re-derive both lines independently (primary fingerprint of the pin; SHA-256 of the decoded minisign public key blob, see ADR-0005)
- [ ] Merge that PR, then cut releases from the merged `main`

### Rotation

- [ ] A new or rotated key lands as a reviewed change to `docs/security/release-signing-keys.asc` **and** `keys/expected-fingerprints.txt` before any tag it signs
- [ ] Remove the old files in that same change, then re-run `make release-export-pin` and `make release-insert-anchors` (neither overwrites existing files)
- [ ] Update `SPANWIT_GPG_SIGNING_FINGERPRINT` / `SPANWIT_PGP_KEY_ID` on the maintainer machine to the new primary and `<subkey>!`
- [ ] A key registered on a GitHub account never substitutes for the committed pin; register the new signing subkey on GitHub before signing so the Verified check passes
- [ ] Tags signed before the rotation stay verifiable from their own commits, which still hold the old pin

### Distribution

- [ ] Verify `go install github.com/3leaps/spanwit/cmd/spanwit@v<version>` works
- [ ] (After taps are live) Homebrew + scoop formula/manifest updated

## Post-Release

### Communication

- [ ] Announce the release
- [ ] Notify downstream consumers if the CLI surface or config schema changed

### Housekeeping

- [ ] Plan next version

### Monitoring

- [ ] Monitor GitHub issues for release-related bugs

## Version-Specific Checklists

### For Major Releases (x.0.0)

- [ ] Breaking changes documented with upgrade guide
- [ ] Deprecation warnings added to old APIs

### For Minor Releases (0.x.0)

- [ ] New features documented with examples

### For Patch Releases (0.0.x)

- [ ] Bug fixes documented with issue references
- [ ] Regression tests added for fixed bugs
- [ ] No new features or breaking changes

## Recovery

- **The Release workflow failed, and the tag and source are correct** (for example a
  transient runner failure, or the repository was private): re-run the failed
  workflow on the unchanged tag, then pass the new attempt as `RELEASE_RUN_ATTEMPT`.
  Do not delete or re-sign the tag.
- **The tag points at a commit that needs a source fix:** fix forward. Merge the fix
  and cut the next patch version. A pushed release tag is never moved, re-signed or
  recreated.
- **The draft is wrong** (assets, notes or signatures): delete the draft release only
  (the tag stays), restage from `make release-clean` and re-run the ceremony from
  `make release-download`.
- **`make release-publish` refused:** treat the draft as untrusted. Do not promote it by
  hand; find which check failed, then restage.

## Emergency Hotfix Process

- [ ] Critical bug or security issue identified and severity assessed
- [ ] Hotfix branch created: `hotfix/v<version>`
- [ ] Minimal fix + regression test; quality gates still enforced (no shortcuts)
- [ ] Version bumped (patch); tag pushed after merge
- [ ] Root cause analysis documented

## Notes

- This checklist may evolve with project maturity; use judgment on items that don't apply.
- Prioritize quality over speed — never skip tests or code review.
- If the signing ceremony hits issues after a tag is pushed, fix forward and catch it in the next patch (e.g. v0.1.1) rather than re-cutting a published tag.
- When in doubt, consult @3leapsdave before proceeding.

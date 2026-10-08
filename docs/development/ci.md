# CI/CD Configuration

This document explains the CI/CD setup for this repository.

## Container-Based CI Pattern

This repository uses the **goneat-tools-runner** container (`ghcr.io/fulmenhq/goneat-tools-runner-glibc:v0.5.6`) for CI jobs. This is the recommended "low friction" approach from goneat v0.3.14+.

### Why Containers?

The container provides all foundation tools pre-installed:

- `prettier` - Markdown/JSON formatting
- `yamlfmt` - YAML formatting
- `jq` / `yq` - JSON/YAML processing
- `rg` (ripgrep) - Fast search
- `curl` / `wget` - HTTP tools

This eliminates tool installation friction in CI - no package manager setup, no version conflicts, no install failures.

### Container Permissions (`--user root`)

We run container jobs as `root` and fix temp directory permissions after checkout to avoid `actions/checkout` failures in containerized jobs.

```yaml
container:
  image: ghcr.io/fulmenhq/goneat-tools-runner-glibc:v0.5.6
  # actions/checkout writes to /__w/_temp/_runner_file_commands/ in containers.
  options: --user root

- name: Fix temp permissions
  run: |
    install -d -m 0777 /__w/_temp || true
    install -d -m 0777 /__w/_temp/_runner_file_commands || true
    chown -R "$(id -u)":"$(id -g)" /__w/_temp || true
    chmod -R 777 /__w/_temp || true
```

#### Why is this required?

GitHub Actions initializes `/__w/_temp` before container steps run, which can leave it owned by a different uid/gid. Normalizing permissions ensures checkout and state saving work consistently.

### Runner Variants (musl vs glibc)

- `goneat-tools-runner` (musl/Alpine): default CI runner image.
- `goneat-tools-runner-glibc` (Debian): use when you need `CGO_ENABLED=1` or glibc-only dependencies.

### CI Jobs

1. **format-check**: Validates formatting using container tools (yamlfmt, prettier)
2. **build-test**: Builds and tests the application using container tools + goneat binary

### Local Development

For local development, you have two options:

1. **Use the container** (recommended for consistency):

   ```bash
   docker run --rm -v "$(pwd)":/work -w /work --entrypoint "" \
     ghcr.io/fulmenhq/goneat-tools-runner-glibc:v0.5.6 yamlfmt -lint .
   ```

2. **Install tools locally (sfetch → goneat)**:

   ```bash
   # Install sfetch (trust anchor) if missing
   curl -sSfL https://github.com/3leaps/sfetch/releases/latest/download/install-sfetch.sh | bash

   # Install goneat + required tools
   make bootstrap
   ```

## Action and Runner Pins

Every `uses:` is pinned by full commit SHA with a version comment, and hosted
jobs run on an explicit image (`ubuntu-24.04`), not a moving `-latest` label.
`scripts/workflow-pins.test.sh` (part of `make release-tag-tests`) enforces this
and refuses pins known to target Node 20.

## Release Workflow and Repository Visibility

The tag-triggered release workflow restores the annotated tag with an
anonymous `git ls-remote` (checkout runs with `persist-credentials: false`), so
it requires the repository to be public. On a private repository or fork the
verify job fails with `remote release tag is absent`.

## References

- [Fulmen toolbox containers](https://github.com/fulmenhq/fulmen-toolbox)
- [goneat documentation](https://github.com/fulmenhq/goneat)
- [GitHub Actions container jobs](https://docs.github.com/en/actions/using-jobs/running-jobs-in-a-container)

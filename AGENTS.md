# Spanwit – AI Agents Startup Guide

## 🔑 Read First

- **Prioritize Product Quality & Safety**: Spanwit deletes files (only with explicit `--execute`). All changes must maintain clarity, standard compliance, and safe-by-default (dry-run) behavior.
- Confirm your agentic interface. If the session does not name an interface adapter (e.g., Codex CLI, Claude Code), pause and request guidance from the human supervisor before taking action.
- Do not assume an agentic role. Roles are assigned in `MAINTAINERS.md`. Request assignment if none is provided.

### Known Interface Adapters

| Agentic Interface | Definitive Prompt / Rules File                       |
| ----------------- | ---------------------------------------------------- |
| Codex CLI         | `CODEX.md` (this file + interface notes)             |
| Claude Code       | `CLAUDE.md` (if present) or `AGENTS.md`              |
| Cursor            | `AGENTS.md`                                          |
| Cline             | `.cline/rules/PROJECT.md` (must reference this file) |
| KiloCode          | `AGENTS.md`                                          |
| OpenCode          | `AGENTS.md`                                          |

**Project**: spanwit
**Purpose**: Context-aware, signature-driven disk-reclamation CLI (dry-run by default)
**Governance**: [3 Leaps Open Source Policies](https://github.com/3leaps/oss-policies)

## Agent Roles

Role assignments, specializations, and supervision details are maintained in `MAINTAINERS.md`. Refer to that file for the current roster of agentic roles.

## Role Catalog Access

Role prompts live in the Crucible role catalog and are referenced from this repo.

| Role       | Prompt Catalog Reference             |
| ---------- | ------------------------------------ |
| `devlead`  | `config/agentic/roles/devlead.yaml`  |
| `devrev`   | `config/agentic/roles/devrev.yaml`   |
| `infoarch` | `config/agentic/roles/infoarch.yaml` |
| `entarch`  | `config/agentic/roles/entarch.yaml`  |
| `cicd`     | `config/agentic/roles/cicd.yaml`     |
| `secrev`   | `config/agentic/roles/secrev.yaml`   |
| `dataeng`  | `config/agentic/roles/dataeng.yaml`  |

See `config/agentic/roles/README.md` for access instructions when the local catalog is not present.

## Commit Attribution

Use concise, semantic commit messages (`feat:`, `fix:`, `docs:`, `chore:`, …).
Every commit message **and** every PR body ends with exactly these three
trailer lines, after one blank line:

```text
Role: devlead
Co-authored-by: Claude Opus 4.5 <noreply@3leaps.net>
Committer-of-Record: @3leapsdave
```

- `Role:` is the bare catalog role slug (`devlead`, `devrev`, `secrev`, …).
  Never a team-prefixed form, bot or account username, or any runtime or
  session identity.
- `Co-authored-by:` names the model actually used, at `noreply@3leaps.net`.
  Not a generic product identity and not a vendor noreply address.
- `Committer-of-Record:` is always `@3leapsdave`, the human account owner.
- The PR body needs the footer too: squash merges keep only the PR body.

Do not put operational identifiers, roster labels, internal task ids or
coordination-system names in commits, PR text or branch names. Run
`make check-all` before committing.

**Example:**

```text
feat: report container engine storage headroom

Adds an opt-in container_runtime section to the space report: engine
detection, guest disk fill, storage by kind, and cost-ordered relief.

Role: devlead
Co-authored-by: Claude Opus 4.5 <noreply@3leaps.net>
Committer-of-Record: @3leapsdave
```

## Session Startup Protocol

1. **Context Review**
   - **REQUIRED**: Read `Makefile` to understand available targets and build workflow
   - Read `MAINTAINERS.md`, `README.md`, `docs/spanwit-overview.md`, and `.fulmen/app.yaml`

2. **Environment Check**
   - Confirm required tools (`go >= 1.25`, `goneat`, `make`) are available
   - Run `make bootstrap` if tools are missing

3. **Plan**
   - For multi-step features, outline steps in `.plans/` (gitignored) or the session transcript before modifying files

4. **Quality Assurance**
   - Run `make pr-final` before opening or updating a PR (non-mutating full gate)
   - Verify the binary builds: `make build`
   - Test CLI commands manually when adding/modifying behavior

5. **Execute Safely**
   - Use prescribed Make targets: `make test`, `make fmt`, `make lint`, `make build`
   - Follow Go best practices and idioms
   - Preserve dry-run-by-default semantics; deletion paths require explicit `--execute`

6. **Attribution**
   - End every commit and PR body with the three-line footer in Commit Attribution

7. **Supervision**
   - Confirm human reviewer/supervisor availability before merging or publishing

## Operational Guidelines

### Generated Assets Policy

- Treat `internal/assets/**` as **generated/read-only**.
- Edit the SSOT source instead (`.fulmen/app.yaml`) and run `make sync-embedded-identity`.
- Code review: reject direct edits to `internal/assets/**` unless explicitly part of a sync step.

### DO

- **Quality First**: Run `make pr-final` before opening/updating a PR
- **Use Make Targets**: Prefer `make test`, `make build` over raw commands
- **Follow the Microtool Standard**: Adhere to the Fulmen Forge Microtool Standard
- **Maintain Test Coverage**: Add tests for all new functionality
- **Use App Identity**: Never hardcode app name, env-var prefix, or config paths — read them from `appidentity`
- **Integrate gofulmen**: Use gofulmen modules for logging, identity, signals, exit codes, schema
- **Keep deletion safe**: Default to dry-run; gate destructive actions behind `--execute`

### DO NOT

- **Edit App Identity Casually**: Change `.fulmen/app.yaml` only deliberately; re-sync the embedded mirror
- **Commit Planning Files**: Never commit files in `.plans/` — it is gitignored
- **Skip Tests / Ignore Linting**: All code must pass tests and lint before commit
- **Commit Without Formatting**: Run `make fmt` (or confirm `make fmt-check`) before committing
- **Hardcode Application Names**: Use `appidentity` for all app-specific values
- **Store Secrets**: Never commit API keys, tokens, or credentials

## Product Guidelines

### What Spanwit Is

A context-aware, signature-driven disk-reclamation CLI: it recognizes regenerable
build output by context (a `target/` next to a `Cargo.toml`), not by name alone.
Dry-run by default; pipeline-friendly; built on the Fulmen microtool chassis.

### Key Components

- **App Identity**: Single-file identity (`.fulmen/app.yaml`) → binary `spanwit`, env prefix `SPANWIT_`
- **CLI Framework**: Cobra-based commands (`scan`, `prune`, `validate`, `version`, `envinfo`, `doctor`)
- **Signature Engine**: `internal/config` — a directory is reclaimable only when its context matches
- **Pathfinder**: Safe filesystem traversal (gofulmen)
- **Logging**: gofulmen structured logging to stderr (stdout stays pure)
- **Schema Validation**: gofulmen/schema for config and prune-plan JSON
- **Exit Codes / Signals**: standardized foundry exit codes; graceful shutdown

### Standard Compliance

- **REQUIRED Commands**: `version` (`--extended`), `envinfo`, `doctor` (product: also `scan`, `space`, `prune`)
- **REQUIRED Module**: Pathfinder for safe filesystem operations
- **Makefile Standard**: required targets plus `pr-final` (PR gate) and `prepush` (tag/pushtag gate)
- **Quality Tooling**: goneat for format, lint, security, SBOM

## Reference Documents

- `MAINTAINERS.md` (SSOT for agent roles)
- `README.md` (product overview and usage)
- `docs/spanwit-overview.md` (architecture and command reference)
- `.fulmen/app.yaml` (app identity definition)
- [Fulmen Forge Microtool Standard](https://github.com/fulmenhq/crucible/blob/main/docs/architecture/fulmen-forge-microtool-standard.md)
- [Fulmen Makefile Standard](https://github.com/fulmenhq/crucible/blob/main/docs/standards/makefile-standard.md)

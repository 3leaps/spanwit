# Spanwit – Maintainers

**Project**: spanwit
**Purpose**: Context-aware, signature-driven disk-reclamation CLI (dry-run by default)
**Governance Model**: [3 Leaps Open Source Policies](https://github.com/3leaps/oss-policies)

## Human Maintainers

### @3leapsdave (Dave Thompson)

- **Role**: Project Lead & Primary Maintainer
- **Responsibilities**: Product direction, signature-engine design, microtool standard compliance, gofulmen integration, quality assurance
- **Contact**: dave.thompson@3leaps.net | GitHub [@3leapsdave](https://github.com/3leapsdave) | X [@3leapsdave](https://x.com/3leapsdave)
- **Supervision**: All AI agent contributions

## Agentic Roles

Role prompts are defined in the Crucible role catalog and referenced locally when available.

| Role ID    | Focus                                             |
| ---------- | ------------------------------------------------- |
| `devlead`  | Implementation and feature delivery               |
| `devrev`   | Code review and risk analysis                     |
| `infoarch` | Documentation, standards, and schemas             |
| `entarch`  | Cross-repo coordination and ecosystem alignment   |
| `cicd`     | Pipelines, build automation, and CI quality gates |
| `secrev`   | Security analysis and vulnerability review        |
| `dataeng`  | Data pipelines and data model review              |

### Role Responsibilities

**devlead**

- Maintain compliance with the Fulmen Forge Microtool Standard
- Integrate gofulmen modules (App Identity, Logging, Signals, Exit Codes, Pathfinder)
- Keep quality gates green (`make pr-final`, `make prepush`, `make check-all`)

**devrev**

- Review changes for bugs, regressions, and standard compliance
- Verify tests and documentation stay current

**infoarch**

- Maintain documentation clarity and accuracy
- Coordinate standards alignment with Crucible docs

**entarch**

- Coordinate cross-repo changes and ecosystem consistency

**cicd**

- Maintain CI pipeline health and runner configuration
- Ensure goneat and build tooling remain current

**secrev**

- Review security-sensitive changes (filesystem deletion paths, secrets, crypto)
- Ensure secure, dry-run-by-default behavior

**dataeng**

- Review signature/config examples and schema handling

**Supervision**: All agentic roles operate under @3leapsdave.

## Agent Attribution Guidelines

- Follow the [Agentic Attribution Standard](https://github.com/fulmenhq/crucible/blob/main/docs/standards/agentic-attribution.md)
- Use assigned roles in commits, PRs, and issues
- Include `Co-Authored-By` lines with the canonical `noreply@3leaps.net` email for internal agents
- `Role:` must be the **bare catalog slug** only (for example `devlead`); do not
  copy runtime/session identity, team-prefixed labels, or bot usernames into trailers
- Always include a full `Committer-of-Record` line for the human GitHub account
  owner: `Dave Thompson <dave.thompson@3leaps.net> [@3leapsdave]` (last trailer)

## Governance Structure

- Human maintainers approve architecture, releases, and supervise AI agents
- AI co-maintainers execute tasks and uphold microtool standards under supervision

## Communication Channels

- **Primary**: GitHub Issues and Pull Requests
- **Escalation**: Direct contact with @3leapsdave for critical issues

## Contribution Guidelines

All contributors (human and AI) must:

- Follow the Fulmen Forge Microtool Standard
- Follow Go coding standards from Crucible `docs/standards/coding/go.md`
- Run `make pr-final` before opening or updating a PR
- Maintain all REQUIRED commands (`version`, `envinfo`, `doctor`)
- Keep the build, tests, and quality gates green

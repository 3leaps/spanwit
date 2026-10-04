# Role Catalog

Baseline agentic role prompts for AI agent sessions in this repository,
referenced by slug from [`AGENTS.md`](../../../AGENTS.md) and
[`MAINTAINERS.md`](../../../MAINTAINERS.md).

These roles derive from the 3leaps baseline catalog in
[3leaps/crucible](https://github.com/3leaps/crucible/tree/main/config/agentic/roles),
which also defines the `role-prompt` schema they conform to.

## Available Roles

| Role                                   | Slug       | Category   | Purpose                                      |
| -------------------------------------- | ---------- | ---------- | -------------------------------------------- |
| [Development Lead](devlead.yaml)       | `devlead`  | agentic    | Implementation, architecture                 |
| [Development Reviewer](devrev.yaml)    | `devrev`   | review     | Four-eyes code review                        |
| [Information Architect](infoarch.yaml) | `infoarch` | agentic    | Documentation, schemas                       |
| [Enterprise Architect](entarch.yaml)   | `entarch`  | governance | Cross-repo coordination, ecosystem alignment |
| [CI/CD Automation](cicd.yaml)          | `cicd`     | automation | Pipelines, GitHub Actions                    |
| [Security Review](secrev.yaml)         | `secrev`   | review     | Security analysis, vulnerabilities           |
| [Data Engineering](dataeng.yaml)       | `dataeng`  | agentic    | Database design, data pipelines              |

## Usage

Reference roles by slug in `AGENTS.md`:

```markdown
## Roles

| Role      | Prompt                                            | Notes           |
| --------- | ------------------------------------------------- | --------------- |
| `devlead` | [devlead.yaml](config/agentic/roles/devlead.yaml) | Implementation  |
| `secrev`  | [secrev.yaml](config/agentic/roles/secrev.yaml)   | Security review |
```

## Extending Roles

To extend a baseline role:

```yaml
slug: devlead
extends: https://schemas.3leaps.dev/roles/devlead.yaml
# Add or override fields
scope:
  - ...additional scope items...
```

## References

- [3leaps baseline roles & role-prompt schema](https://github.com/3leaps/crucible/tree/main/config/agentic/roles)

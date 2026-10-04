# Standards

Normative contracts that Spanwit's CLI (and future surfaces) must uphold. These
are the _rules_; the _why_ behind them lives in the
[Architecture Decision Records](../decisions/).

| Standard                                        | Summary                                                                                         | Backing decision                                                         |
| ----------------------------------------------- | ----------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| [CLI Output Contract](./CLI-Output-Contract.md) | stdout is reserved for program output; stderr carries logs, diagnostics, progress, help/version | [ADR-0001: Stdout Purity](../decisions/ADR-0001-stdout-purity.md)        |
| [Exit Codes](./Exit-Codes.md)                   | Stable, semantic exit codes for humans and automation                                           | [ADR-0002: Dry-Run by Default](../decisions/ADR-0002-dry-run-default.md) |

## Conventions

- Standards use descriptive file names (not numeric IDs); this index defines the
  set. If the set grows enough to need ordering, add a numeric prefix then.
- Each standard links back to the ADR(s) that justify it, and each ADR links
  forward to the standards it drives — keep both directions in sync when adding
  either.

# ADR-0001: Stdout Purity for spanwit

> **Status**: Accepted  
> **Date**: 2026-05-25  
> **Authors**: Architecture (via tool bootstrap)

## Context

`spanwit` is a CLI tool designed for safe, composable disk space analysis and reclamation on development machines. Users (and future automation) will want to:

- Pipe output to other tools (`spanwit scan ~/dev --format json | jq ...`)
- Capture structured results while still seeing errors/progress on the terminal
- Run the tool in scripts and CI without output pollution

This aligns with the established **stdout purity** pattern used across 3leaps projects.

## Decision

`spanwit` will strictly follow the stdout purity convention:

### Stdout

- Reserved exclusively for requested program output.
- Examples:
  - Human-readable tables/lists of reclaimable directories (default `scan` output)
  - JSON when `--format json` (or equivalent) is used
  - Future: CSV, NDJSON, etc. for machine consumption

### Stderr

- All logging, diagnostics, errors, warnings, and progress information.
- This includes structured/JSON logging from gofulmen (even when enabled).
- `--help`, `--version`, and similar metadata.

### Implementation Guidelines for This Tool

1. **Logging via gofulmen**
   - All use of gofulmen logging (info, debug, warn, error, structured events) must target stderr by default.
   - The SIMPLE logging profile (per [Crucible ADR-0003](https://github.com/fulmenhq/crucible/blob/main/docs/architecture/decisions/ADR-0003-progressive-logging-profiles.md)) is appropriate for a CLI tool.

2. **Output Functions**
   - Use explicit functions (e.g., `fmt.Fprintln(os.Stdout, ...)` or a dedicated output writer) for results.
   - Never mix logging calls with result output.

3. **Flags and Behavior**
   - `--format json` (or similar) must produce _only_ the JSON payload on stdout in normal operation.
   - Verbose/debug flags increase stderr noise but must never leak to stdout.
   - In non-interactive/CI contexts, users can redirect `2>/dev/null` or `2>&1` as needed.

4. **Verification**
   - Integration or acceptance tests should assert stdout purity (e.g., in default mode, successful `scan` produces parseable output on stdout with no diagnostic text).

## Rationale

- Enables safe composition in Unix pipelines and scripts.
- Matches the patterns already established and battle-tested in 3leaps tools (docprims, seclusor, etc.).
- Reduces user confusion when capturing tool output.
- Consistent with Unix philosophy and 12-factor CLI guidance.

## Consequences

### Positive

- Tool becomes safely usable in automation and larger workflows.
- Clear mental model for users: "results on stdout, noise on stderr".
- Easier testing of output vs. diagnostics.

### Negative

- Users capturing version or help must remember `2>&1`.
- Requires discipline in code and reviews (especially when adding new output paths).

### Neutral

- Aligns with gofulmen observability patterns once the SIMPLE profile is fully available.

## References

- Stdout purity is established across 3leaps tools (e.g. docprims and seclusor).
- [Crucible ADR-0003: Progressive Logging Profiles](https://github.com/fulmenhq/crucible/blob/main/docs/architecture/decisions/ADR-0003-progressive-logging-profiles.md)
- [12 Factor CLI Apps](https://medium.com/@jdxcode/12-factor-cli-apps-dd3c227a0e46)

## Related Work

This decision is realized by, and kept in sync with:

- [CLI Output Contract](../standards/CLI-Output-Contract.md) — the normative contract
- [Exit Codes](../standards/Exit-Codes.md)
- [ADR-0002: Dry-Run by Default](./ADR-0002-dry-run-default.md) — plan output rides on stdout under this contract

It influences the `scan` and `prune` commands, any `--format` options, logging
configuration, and the output-correctness test strategy.

# CLI Output Contract

**Status**: Draft  
**Applies to**: `spanwit` CLI and all subcommands  
**Related ADR**: [ADR-0001: Stdout Purity](../decisions/ADR-0001-stdout-purity.md)

## Purpose

This standard defines how `spanwit` separates program output from diagnostic information so that the tool remains safely composable in pipelines, scripts, and automation while still providing good human feedback.

## Core Rule (from ADR-0001)

- **stdout** = Only the requested program output.
- **stderr** = All logging, diagnostics, errors, warnings, progress, help, and version information.

## Output Streams

| Content Type                         | Stream | Notes                                                   |
| ------------------------------------ | ------ | ------------------------------------------------------- |
| Scan results (table or structured)   | stdout | The actual data the user asked for                      |
| `--format json` / machine output     | stdout | Must be the only thing on stdout when this flag is used |
| Errors that prevent normal operation | stderr |                                                         |
| Warnings and non-fatal issues        | stderr |                                                         |
| Progress / status messages           | stderr |                                                         |
| Debug / trace logging                | stderr | Controlled by `--log-level`                             |
| `--help`, `--version`                | stderr |                                                         |

## Implementation Rules

1. **Logging must go to stderr**
   - All logging through gofulmen (or direct `zap` / `log` usage) must target stderr by default.
   - The SIMPLE logging profile is the default for CLI usage.

2. **Results must go to stdout**
   - Use dedicated output paths or helpers for anything that represents "the answer" to the user's request.
   - Never mix `fmt.Print*` result output with logging calls.

3. **JSON / Structured Output**
   - When `--format json` (or future structured formats) is used, the payload written to stdout must contain **only** the requested data.
   - No log lines, no progress, no warnings may appear on stdout in this mode (they must go to stderr).

4. **Quiet Mode**
   - A future `--quiet` / `-q` flag may suppress non-essential stderr output, but must never suppress actual results on stdout.

5. **Error Handling**
   - Fatal errors should still produce a clear message on stderr before exiting with a non-zero code.
   - Partial success scenarios (e.g., some paths failed during scan) should still emit usable results on stdout when possible, with errors on stderr.

## Acceptance Criteria / Proof

- In normal operation, `spanwit scan <path> > results.txt` must produce only results in `results.txt` (no log noise).
- `spanwit scan <path> 2>/dev/null | jq .` must succeed when using structured output.
- Running with `--log-level debug` must not pollute stdout.
- Integration tests exist that verify stdout contains only expected output in default and JSON modes.

## Related Standards & Decisions

- [Standards index](./README.md)
- [Exit / Return Codes](./Exit-Codes.md)
- [ADR-0002: Dry-Run by Default](../decisions/ADR-0002-dry-run-default.md) — a `prune` plan (including `--format json`) is program output and rides on stdout under this contract; the dry-run/executed mode banner goes to stderr.
- Logging Configuration (future, when gofulmen progressive logging is adopted)

## Open Questions

- Should there be an explicit `--no-progress` flag, or is log level sufficient?
- How should we handle very large result sets (streaming vs buffering)?

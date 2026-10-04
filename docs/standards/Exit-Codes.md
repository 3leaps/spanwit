# Exit / Return Codes

**Status**: Draft  
**Owner**: spanwit maintainers  
**Related ADRs**: [ADR-0001 (Stdout Purity)](../decisions/ADR-0001-stdout-purity.md), [ADR-0002 (Dry-Run by Default)](../decisions/ADR-0002-dry-run-default.md)

## Rationale

`spanwit` performs sensitive operations (discovery and, in the future, deletion of build artifacts and other data). Callers — whether humans or automation — need **clear, stable, and unambiguous feedback** via exit codes.

Ambiguous exit codes lead to fragile scripts and loss of trust in the tool.

## Standard Exit Code Contract

| Code | Name                    | Meaning                                                               | When `scan` uses it                                                                    |
| ---- | ----------------------- | --------------------------------------------------------------------- | -------------------------------------------------------------------------------------- |
| 0    | Success                 | Command completed successfully. All requested work was performed.     | No errors during scan, results (or "nothing found") emitted cleanly.                   |
| 1    | Partial Success         | Some work succeeded, but not all. Useful output may still be present. | Some paths were inaccessible or errored, but others were processed successfully.       |
| 2    | Error                   | Command failed. Output (if any) should be treated as unreliable.      | Fatal error during scanning (e.g. invalid path root, permission denied on root).       |
| 3    | Usage Error             | Incorrect usage, bad arguments, or configuration problem.             | Bad flags, missing required input, etc.                                                |
| 4    | Safety / Policy Refusal | The tool refused to proceed for safety or policy reasons.             | Not used by `scan`. Reserved for refusals — see [Future Evolution](#future-evolution). |

### Additional Rules

- Exit code **must** be set before any final output is written where possible.
- When exiting non-zero, a clear message **must** be emitted on stderr.
- Structured output on stdout (when using `--format json`) should still be emitted on exit code 1 when partial results are useful.
- Exit code 0 should only be used when the user got what they asked for with no failures.

## Inventory and Diagnose Commands

`inventory` (including `--directories`) and `diagnose` use the standard codes:

- `0` — complete for the declared scope and filters;
- `1` — usable results with partial coverage (affecting gaps or cancellation);
- `2` — runtime, output or budget failure, or a failed coverage-attestation
  publication; a withheld attestation does not change the exit status;
- `3` — invalid command usage, reported before any discovery.

## Space Command: Completed With Gaps

`space` returns **1** when it finishes a usable report that carries coverage
warnings (for example an unreadable subtree), and **0** only when the requested
observation completed without warnings. The distinction is explicit in every
output surface:

- JSON: `completion: {"lifecycle": "complete" | "partial", "warning_count": N}`
  (`partial` exactly when `warning_count > 0`).
- Text: a final `Result: complete` or `Result: partial — N coverage warning(s)` line.
- stderr: after the `Warning:` lines, one summary line stating the run is partial
  (exit 1) and the report is usable.

Exit status is never prune authorization: exit 0 means the observation
completed, not that anything is safe to delete. Partial is not collapsed into 0
to quiet automation.

## Scan Command Specific Behavior (Current)

| Situation                                 | Exit Code | Stdout                          | Stderr                  |
| ----------------------------------------- | --------- | ------------------------------- | ----------------------- |
| Clean scan, items found                   | 0         | Table of reclaimable dirs       | Scan progress / summary |
| Clean scan, no items above threshold      | 0         | "No reclaimable directories..." | Scan progress / summary |
| Some paths unreadable / permission errors | 1         | Results for successful paths    | Errors + summary        |
| Root path does not exist or is unreadable | 2         | (empty or minimal)              | Clear error             |
| Invalid flags or `--min-size`             | 3         | (empty)                         | Usage error             |

## Implementation Guidelines

- Prefer returning errors from commands and letting the root command decide the exit code when possible (better testability).
- For now, direct `os.Exit()` is acceptable in leaf commands (consistent with current template patterns), but should be documented.
- All new commands must declare their exit code behavior in their `--help` long description or in this standard.

## Verification

- Integration tests must cover each exit code path for `scan`.
- Redirection tests: `spanwit scan ... > out.txt 2> err.txt` must produce correct exit code while keeping stdout clean.
- CI should fail if a command exits 0 when it should have indicated partial failure.

## Future Evolution

`prune` defaults to dry-run per [ADR-0002](../decisions/ADR-0002-dry-run-default.md);
exit code 4 (Safety / Policy Refusal) becomes active for cases where execution
is refused (e.g. policy blocked deletion, an `--execute` request denied by
policy). A dry-run that completes normally is a success (code 0) — declining to
mutate is the safe default, not a refusal.

## References

- [Standards index](./README.md) and [Decisions](../decisions/)
- 12 Factor CLI Apps guidance on exit codes
- Existing patterns in other 3leaps / Fulmen microtools

# ADR-0001: Hook Delegation to Make Targets

**Status:** Accepted
**Date:** 2025-11-13
**Deciders:** @3leapsdave, Microtool Steward
**Tags:** hooks, validation, DRY, SSOT

## Context

Git hooks (pre-commit, pre-push) need to run validation checks before allowing commits and pushes. We need a single source of truth (SSOT) for validation rules to ensure:

1. `make precommit` and `git commit` execute identical validation
2. `make prepush` and `git push` execute identical validation
3. Developers can test locally with `make` before running `git` operations
4. Validation rules are defined once, not duplicated

Two approaches were possible:

- **Option A:** Hooks delegate to Make targets (Make is SSOT)
- **Option B:** Hooks call goneat directly using manifest (hooks.yaml is SSOT)

## Decision

**Use Make as the single source of truth for validation commands.**

Git hooks (`.goneat/hooks/pre-commit` and `.goneat/hooks/pre-push`) directly invoke Make targets:

```bash
# .goneat/hooks/pre-commit
make precommit

# .goneat/hooks/pre-push
make prepush
```

Makefile defines the complete validation commands:

```makefile
precommit:  ## Run pre-commit hooks
	@goneat assess --categories format,lint --fail-on critical

prepush:  ## Run pre-push hooks
	@goneat assess --categories format,lint,security --fail-on high
```

## Rationale

1. **DRY Principle:** Validation rules defined once in Makefile, used everywhere
2. **Predictability:** `make prepush` guarantees same behavior as `git push`
3. **Testability:** Developers run `make prepush` to verify before pushing
4. **Clarity:** developers understand Make targets (required by Fulmen Makefile Standard)
5. **Simplicity:** No recursive invocation, no manifest orchestration overhead

## Consequences

### Positive

- **Identical behavior:** Running `make precommit` locally produces exact same results as `git commit` hook
- **Clear SSOT:** Makefile is the definitive source for validation rules
- **Easy debugging:** `make prepush` shows exactly what git push will run
- **No recursion:** Hooks call Make, Make calls goneat (single path)
- **Standard compliance:** Makefile Standard already requires precommit/prepush targets

### Negative

- **hooks.yaml underutilized:** Manifest exists but is not used for orchestration (kept for documentation)
- **Less goneat-native:** Doesn't use goneat's manifest-driven hook architecture
- **Make dependency:** Hooks require Make installed (acceptable for development templates)

### Trade-offs Accepted

- Chose developer predictability over goneat manifest orchestration
- Chose Make Standard compliance over goneat-native patterns
- Chose simplicity over advanced manifest features

## Implementation

**Modified files:**

- `.goneat/hooks/pre-commit` - Changed to call `make precommit`
- `.goneat/hooks/pre-push` - Changed to call `make prepush`
- `Makefile` - Removed `--hook`, `--hook-manifest`, `--package-mode`, `--check` flags
- `Makefile` - Simplified to direct `goneat assess` with categories and thresholds

**Validation equivalence:**

```bash
# These are now identical:
make precommit == git commit (hook validation)
make prepush   == git push (hook validation)
```

## References

- Fulmen Makefile Standard - Requires precommit/prepush targets
- `.goneat/hooks.yaml` - Kept for documentation, delegates to Make

## Notes

When changing validation in this repo:

1. **Modify Makefile** - Change `precommit`/`prepush` targets to adjust validation
2. **Don't modify hooks** - Git hooks delegate to Make, no changes needed
3. **Test with Make** - Run `make prepush` to verify validation before pushing
4. **SSOT is Makefile** - All validation rules defined in Makefile targets

# ADR-0002: Dry-Run by Default for Mutating Operations

> **Status**: Accepted
> **Date**: 2026-05-29
> **Authors**: Architecture (devlead)

## Context

Spanwit performs destructive work: it deletes directories to reclaim disk
space. A wrong deletion is expensive and sometimes unrecoverable, so the cost
of an accidental mutation is high and asymmetric — a missed deletion is cheap
to redo, a wrong deletion may not be.

The product arc spans more than one surface:

1. **CLI (now)** — one-shot `scan` / `prune` on demand.
2. **Daemon (Phase 2, `spanwitd`)** — continuous, low-overhead capacity
   awareness that proposes (or, within policy, performs) reclamation on a
   schedule.
3. **Control plane (Phase 3)** — an HTTP surface for governing reclamation
   policy across a fleet, including CRUD over policies, schedules, and
   reclamation actions.

We want a single safety principle that holds identically across all three, so
that "what does the safe default do?" has the same answer whether a human types
a command, a daemon wakes on a timer, or a service calls an API.

## Decision

**Every mutating or destructive operation defaults to dry-run: a
non-destructive plan/preview of what _would_ change. Performing the change
requires an explicit, unambiguous opt-in. Omitting that opt-in never mutates
state.**

This is a product-wide invariant, not a CLI affordance. It applies to every
surface that can change durable state (the filesystem today; policies,
schedules, and fleet state tomorrow).

### Application by surface

**CLI**

- Commands that mutate (`prune`, and any future destructive command) default to
  producing a plan. Deletion happens only with an explicit `--execute` flag.
- Execution is its own single-purpose signal — never a side effect of another
  flag (e.g. setting `--format json` or `--verbose` must not cause execution).
- The plan is machine-readable (`prune --format json`, validated against
  `config/schema/spanwit-prune-plan.v1.schema.json`) so it can be reviewed,
  diffed, or fed to another tool before anyone runs the execute step.

**Daemon (Phase 2)**

- The default posture is _propose-only_: the daemon emits planned reclamation
  actions and performs nothing.
- Acting autonomously requires an explicit policy that grants execution within
  stated bounds (paths, signatures, size/age thresholds, rate). Absent such a
  policy, the daemon proposes and waits.

**Control plane / API (Phase 3)**

- Mutating endpoints (`DELETE`, and destructive `POST`/`PATCH`) default to
  plan/preview semantics. A request is treated as dry-run unless it carries an
  explicit execute intent — e.g. an explicit `execute=true` / `dry_run=false`
  field, or a two-step **plan → apply** flow where `apply` references a plan
  produced by a prior `plan` call.
- A dry-run request returns the plan (the set of changes that _would_ be made)
  and changes nothing. Only an explicit, authenticated, policy-checked `apply`
  mutates state, and that apply is auditable.

### Cross-cutting rules

1. **Safe default everywhere.** Omitting the execute signal never mutates
   durable state, on any surface.
2. **Explicit, single intent.** Execution is one clear, dedicated signal
   (`--execute`, `execute=true`, an `apply` call) — not inferred from other
   options.
3. **Labeled output.** Output declares its mode: a plan is clearly marked as a
   dry-run/preview, an executed result is clearly marked as executed (CLI: a
   banner on stderr and/or a `mode` field in JSON; API: a `mode`/`dry_run`
   field in the response).
4. **Auditable execution.** Executions log what changed (see
   [Exit Codes](../standards/Exit-Codes.md) and the stderr logging convention in
   [ADR-0001](./ADR-0001-stdout-purity.md)).
5. **Machine-readable plans.** Plans are structured so they compose and can be
   reviewed or approved before apply — essential to the control-plane
   plan→apply workflow and to fleet governance.

## Consequences

### Positive

- One safety mental model across CLI, daemon, and control plane.
- Enables review/approval workflows (plan, inspect, then apply) and fleet-level
  governance of destructive actions.
- Plans are composable artifacts (pipe, diff, store, approve).

### Negative

- Execution is a deliberate two-signal step; this friction is intentional.
- Every new mutating surface must implement the plan/execute split, and reviews
  must enforce it.

### Neutral

- Reinforces, and is reinforced by, stdout purity ([ADR-0001](./ADR-0001-stdout-purity.md)):
  the plan is program output on stdout; mode banners and logs go to stderr.

## Related

- [ADR-0001: Stdout Purity](./ADR-0001-stdout-purity.md)
- [CLI Output Contract](../standards/CLI-Output-Contract.md)
- [Exit Codes](../standards/Exit-Codes.md)

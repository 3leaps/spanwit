# ADR-0003: Core/Surface Separation — Commands and Endpoints Are Thin Adapters

> **Status**: Accepted
> **Date**: 2026-07-03
> **Authors**: Architecture

## Context

[ADR-0002](./ADR-0002-dry-run-default.md) commits spanwit to a multi-surface arc:
CLI today, a `spanwitd` daemon (Phase 2), and a control-plane HTTP API (Phase 3).
It establishes that a single safety principle must hold _identically_ across all
three — "what does the safe default do?" has the same answer whether a human types
a command, a daemon wakes on a timer, or a service calls an API.

That guarantee is only cheap to keep if the surfaces share the **same code**. If a
command's logic lives inside its CLI handler (argument parsing, cobra wiring, and
`os.Exit` interleaved with the actual work), then a future HTTP endpoint offering
the same capability must re-implement it — and every re-implementation is a chance
for the surfaces to drift on exactly the semantics ADR-0002 says must not drift
(dry-run vs execute, `prunable`/`withheld` state, exit/error mapping).

Spanwit is **CLI-first today, not CLI-only**. It is evolving from the Fulmen
microtool delivery profile into a **workhorse product shape**: one functional
core exposed through capability-parity CLI and control-plane surfaces. In this
ADR, workhorse names that shared-core, multi-surface posture. It does not claim
that Spanwit already satisfies every requirement of the draft
[Fulmen Forge Workhorse Standard](https://github.com/fulmenhq/crucible/blob/main/docs/architecture/fulmen-forge-workhorse-standard.md),
whose stated scope is workhorse forge templates.

This ADR makes Spanwit's workhorse direction and its structural requirement
explicit so they are enforced in code and review, not left to intent.

## Decision

**Every command's behavior lives in a surface-agnostic core function that takes a
typed options value and returns a typed result. CLI commands and network endpoints
are thin adapters over that core: they translate input in and render/serialize
output out, and contain no business logic themselves.**

### Shape

- **Core:** `func Verb(ctx context.Context, opts VerbOptions) (VerbReport, error)`,
  living in a package that imports **no** CLI framework and performs **no** process
  control (`os.Exit`), no direct writing to `os.Stdout`/`os.Stderr` for results, and
  no flag parsing. It is fully testable without cobra and without a running server.
- **CLI adapter:** the cobra command parses flags into `VerbOptions`, calls
  `Verb(...)`, renders `VerbReport` to stdout (human text or, under `--format json`,
  the serialized report), maps `error` to an exit code, and routes diagnostics to
  stderr per [ADR-0001](./ADR-0001-stdout-purity.md).
- **Network adapter (future):** an HTTP handler decodes the request into
  `VerbOptions`, calls the same `Verb(...)`, and serializes the same `VerbReport` as
  the response body.

### CLI/control-plane parity

The CLI is the first product surface, not the only product surface. As the control
plane comes online, product capabilities exposed through it have **capability
parity** with the CLI:

- both surfaces call the same core operation;
- options, defaults, validation, and safety policy have the same semantics;
- both surfaces use the same versioned report schema and outcome/error taxonomy;
- dry-run and explicit-execution behavior remain identical across surfaces.

Parity does not mean transport identity. Flag parsing, human-readable rendering,
and exit codes belong to the CLI adapter; authentication, HTTP status mapping,
headers, and other protocol concerns belong to the network adapter. Operational
HTTP endpoints such as health and metrics do not require synthetic CLI twins.

A capability may ship CLI-first while the control plane is not yet available, but
it is not cross-surface complete until the corresponding control-plane adapter
reaches parity. The network surface wraps the core; it never re-implements the
capability.

### The report schema is the shared contract

Where a command emits a machine-readable result, the result type carries a versioned
JSON Schema (as `prune --format json` already does). That schema is **simultaneously**
the CLI `--format json` contract and the future API response body. There is one
contract per capability, not one per surface.

### Adoption is incremental, not a big-bang

1. **New commands land on the seam.** Any command added from this point implements
   its logic in a surface-agnostic core; the cobra command is an adapter only.
2. **Existing commands migrate opportunistically.** Commands whose logic currently
   lives in the command package migrate to the seam when they are next substantially
   reworked, or when the first network surface is built and a second consumer makes
   the migration load-bearing — whichever comes first. Migrating green, tested code
   ahead of a real second consumer buys nothing and is explicitly _not_ required by
   this ADR.
3. **Control-plane parity lands capability by capability.** The CLI may lead during
   the transition, but each network adapter reuses the existing core and shared
   report contract. No parallel HTTP-only implementation is permitted.

## Consequences

### Positive

- ADR-0002's cross-surface safety guarantee becomes a code-structure property, not a
  discipline re-earned on every surface.
- Command logic is testable without cobra or a server; adapters stay trivial.
- One schema per capability serves CLI JSON and the API response body — no
  surface-specific result formats to keep in sync.
- The CLI can remain the first delivery surface without becoming the only
  implementation of product behavior.
- The daemon/control-plane arc (ADR-0002 Phases 2–3) can wrap existing cores rather
  than re-implementing them.

### Negative

- Adds one indirection (core + adapter) over putting logic directly in the handler.
- Reviews must enforce the boundary: no business logic in cobra commands, no
  `os.Exit`/result-printing in cores.

### Neutral

- Existing commands are not required to migrate immediately; the codebase may carry a
  mix of migrated and not-yet-migrated commands during the transition, by design.

## Related

- [ADR-0001: Stdout Purity](./ADR-0001-stdout-purity.md) — adapters own output
  routing (results on stdout, diagnostics on stderr); cores never write results.
- [ADR-0002: Dry-Run by Default](./ADR-0002-dry-run-default.md) — the cross-surface
  invariant this ADR makes structurally cheap to uphold.
- [Fulmen Forge Workhorse Standard](https://github.com/fulmenhq/crucible/blob/main/docs/architecture/fulmen-forge-workhorse-standard.md)
  — draft forge-template standard; this ADR adopts the shared-core, multi-surface
  direction without claiming full template compliance.

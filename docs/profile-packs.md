# Experimental profile packs

A profile pack groups named recipes into personas such as `developer` and
`media`. Each recipe uses a supported capability and an existing Spanwit
configuration policy. Editing membership or policy parameters requires no
scanner code. This first slice validates and previews only: it does not activate
profiles, schedule scans, authorize execution, or delete anything.

```sh
spanwit --read-only profile validate docs/examples/profiles/developer.json
spanwit --read-only profile preview docs/examples/profiles/developer.json --name developer
spanwit --read-only profile diff before.json after.json
```

Policy roots must be absolute or home-relative (`~/`); cwd-relative roots are
rejected. Preview lists the resolved enabled roots so CLI and future daemon
clients can review the actual scope.

The example selects old Grype staging with the existing signature and a 24-hour
age floor. Its cache location is for macOS; select the appropriate cache root
for your machine. Signature patterns are relative to the configured root, so
retain the `grype/db` components beneath that root. The empty media persona
intentionally enables no cleanup rules.

A recipe currently supports `directory-prune` only, with signature-backed
policy targets. Custom signatures can use existing evidence checks; their
`safe_to_prune` value is policy, not proof of regenerability. Catalog overlays
remain diagnostic metadata. No arbitrary command text, unknown capabilities,
pattern-only target, or unknown fields are accepted in this pack version.

`profile diff` shows both sides of recipe and persona changes, including dormant
recipes, and binds the review to the two document revisions. It does not approve
or activate either revision.

Profiles select recipe IDs in order. Remove an ID to disable that recipe for a
persona. Every recipe is validated, including recipes not selected by a persona;
invalid dormant policy cannot be silently activated later. Documents are JSON,
limited to 1 MiB and 32 nested containers; duplicate object keys are rejected.
The revision is SHA-256 over the exact input bytes. Whitespace changes therefore
produce a different revision; it is a content identity, not a signature or grant.

Preview uses the existing prune planner. It reports matches and withholding
reasons by recipe, flags incomplete results, and always reports
`execution_authorized: false`. Candidates can overlap between recipes; do not
sum their sizes as expected reclaim. There is no executable exact-plan handoff
in preview. A `prunable` row means the planner's current conditions matched,
not that a profile has permission to delete it.

Draft generation, reviewed activation, recurring evaluation, consumer leases,
aggregate budgets and standing grants are not implemented in this slice.

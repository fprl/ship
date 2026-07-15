# ADR-0021: The Member Mutation Surface

- **Status**: Accepted (Franco, July 15, 2026; sol consult on the
  Vercel-UX bar, both recommendations converged)
- **Date**: 2026-07-15
- **Related**: ADR-0019 (grammar, onboarding), ADR-0020 (store is
  identity truth), spec §16/§17.

## Context

Three gaps surfaced the same week ADR-0020 landed. A `box setup` rerun
renamed the real box's member as a side effect (setup re-enrolls the
keys it is given, refreshing names — sanctioned for recovery, a footgun
as a rename interface). Key rotation was broken for exactly the person
who matters most: `member rm <name>` removes ALL of a member's keys and
refuses for the last owner, so the sole owner of a box could not retire
an old laptop key without a temp-key dance. And `member ls` was gated
by the member-mutation verb class, so a shipper could not even list who
can deploy. Vercel's member surface (the product bar) models rename,
role change, and removal as first-class row actions — never as
remove-and-reinvite.

## Decision

```
ship box member ls [<box>] [--json]
ship box member add <key|path|https-url> [<box>] --name <n> [--role r] [--confirm ...]
ship box member rename <old> <new> [<box>]
ship box member role <name> <owner|shipper|agent> [<box>]
ship box member rm <name> [--key <id>] [<box>]
```

- A member is one box-global name, one role, one or more keys. All keys
  of a member share its role — a schema invariant, not just an add-time
  check.
- **One invariant guards every mutation**: it must leave at least one
  effective owner key (record in `members.json` AND a line in
  `authorized_keys`). This single rule replaces the separate
  last-member / last-key / last-owner guards and covers rm, rm --key,
  role demotion, and rename edge cases uniformly.
- `rename` is identity-only: key material and role untouched; refuses
  unknown `<old>`; refuses a `<new>` that names ANY existing member
  (rename never merges principals); `<old> == <new>` is idempotent
  success. No `--confirm`: reversible, no access change.
- `role` updates every key of the member and re-renders role-dependent
  access lines (agent keys carry the forced agent-shell command; owner
  and shipper keys are plain). Idempotent on the current role. Approval
  flow as for other member mutations; no extra confirmation (a literal
  `member add --role owner` already grants without one).
- `rm --key <id>` retires exactly one key; the key must belong to
  `<name>`. Key ids: full `SHA256:...` fingerprint or an unambiguous
  prefix with a 12-base64-char floor (one-char prefixes on a small box
  are too permissive and unstable). Zero matches → no mutation, point
  at `member ls`; multiple → no mutation, print all matches.
- `member ls` is readable by every enrolled member (read verb class),
  groups keys under their member, prints the short key id and a
  CURRENT marker on the connecting key, and `--json` becomes the
  truthful nested shape `members[] → keys[]` (the flat shape modeled
  credential rows as members; zero users, no compat).
- `member add` of an additional key prints the new fingerprint and the
  rotation next steps: verify a fresh connection, then
  `rm --key` the old id.
- **Setup naming (amends ADR-0020's re-enrollment)**: a normal
  `box setup` rerun PRESERVES recorded names. Setup assigns a name only
  to new records or when rebuilding an invalid/missing store. Silent
  identity mutation during convergence fails the obvious-thing test;
  deliberate renames now have their own verb.
- Write order fails closed, per direction (refines ADR-0020): grants
  (add; role changes away from agent, owner↔shipper) write the store
  first, then the rendered file; revocations (rm, rm --key, role
  change TO agent — the forced command restricts shell access) write
  the rendered file first. A crash between the two writes never leaves
  a key line whose access exceeds what the store records. Idempotent
  paths verify the rendered file and re-render on drift instead of
  returning early.

## Rejected

- **`member rotate`** (atomic add+retire): atomicity can guarantee the
  files changed together but cannot prove the operator possesses the
  new private key — a typo'd paste could lock out the sole owner while
  satisfying every count guard. Add → verify connection → retire is
  the honest workflow. Revisit only with a proof-of-possession
  handshake.
- **`member update`/`member set` kitchen sink**: identity and material
  mutations have different guards and failure modes; ADR-0019's
  grammar keeps verbs single-purpose; agents compose commands fine.
- **Instance-path grammar** (`member <name> rename <new>`): subverbs
  follow the resource, never an instance; matches `member rm <name>`.
- **Role change via rm + re-add**: revokes access mid-change, breaks
  multi-key members, and cannot demote the last owner at all.

## Parked

- Trust-mutation journaling (actor + before/after for add/rm/rename/
  role) — audit-trail machinery, future RFD.

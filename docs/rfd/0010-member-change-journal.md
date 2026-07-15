# RFD-0010: Member change journal

**Status: draft, parked** (Franco, July 15, 2026 — ratified as
design-only at the v0.7.0 close; captured July 16). Builds on ADR-0020
(member store is identity truth), ADR-0021 (mutation surface, which
parked this), and RFD-0009's hardened journal contract.

## Pitch

Every change to who can use the box should leave a durable, readable
record: who made the change, what it was, and what the register looked
like before and after. Today `member ls` shows the present; nothing
shows how it got that way. For a box operated by a team (and their
agents), "when did that key appear and who added it" should be one read,
not an inference from shell history.

## Design

One append-only journal, `/var/lib/ship/members-journal.jsonl`,
following RFD-0009's journal contract (fsynced appends, torn-tail-safe
reader, explicit writer errors).

- **Choke points, not call sites**: entries are written inside the two
  functions every mutation already flows through — the grant writer and
  the revocation writer (`writeMemberGrant` / `writeMemberRevocation`)
  — so add, rm, rm --key, rename, and role changes are covered by
  construction, and a future verb cannot forget to journal.
- **Entry shape** (JSON per line): schema version, timestamp, `verb`,
  `actor` (the connecting member's name, role, and key id — the same
  identity the helper already resolved to authorize the call),
  `before` / `after` snapshots of the affected member (name, role, key
  ids only — never key material), and the approval id when the change
  rode a granted approval.
- **Ordering**: the entry is appended after the mutation completes (both
  the register and the rendered file), recording what actually happened;
  a failed append warns loudly and the mutation stands — history is
  evidence, not a gate.
- **Read surface**: `member ls --history <name>` (or similar; grammar
  decided at build time per ADR-0018/0019) renders the entries for one
  member; `--json` emits them raw. Doctor reports a torn tail like any
  other journal.

## Why parked

Audit machinery earns its keep when a box has multiple members changing
each other's access; the only box today has one owner. The design is
recorded now because RFD-0009's stage-1 journal hardening is the
foundation it assumes, and the choke points it needs were built by
ADR-0021. Build trigger: the first multi-member box in real use, or
members/roles work resuming (RFD-0003).

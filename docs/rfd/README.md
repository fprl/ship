# RFDs — post-v1 design directions

RFDs (requests for discussion) capture design directions agreed in
principle but **not scheduled**. `docs/ship-spec.md` remains authoritative
for what gets built now; nothing in this directory blocks or modifies
ship v1 (Phases 1–3 build exactly as written there).

Promoting an RFD to buildable work requires turning it into a spec
section with acceptance criteria, the same bar ship-spec sets.

- [RFD-0001](0001-data-doctrine-and-sqlite-forks.md) — data doctrine:
  SQLite-first or managed URL, the box never runs a database; preview
  data forks by file copy.
- [RFD-0002](0002-resident.md) — resident: the box reports in,
  diagnoses, and fixes within role bounds.
- [RFD-0003](0003-members-and-roles.md) — members: humans and agents as
  identities with preset roles.

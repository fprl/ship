# ADR-0012: Backups Are Data-Only and Land on the Laptop

- **Status**: Accepted (Franco, July 13, 2026 — decisions D1/D2/D3)
- **Date**: 2026-07-13
- **Related**: ADR-0007 (superseded by this ADR), RFD-0001 (SQLite
  doctrine), RFD-0007 (scheduled off-box backup, parked),
  `docs/ship-spec.md` §18.

## Context

ADR-0007 shipped a paired `save`/`restore` primitive: a tar of
`/data` + the applied manifest + plaintext secrets + the running
release id, stored **on the same VPS**. A July 13 product review found
it to be a façade against its own headline scenario ("VPS died, one
command back"):

1. The tar lives on the box it protects — the disaster it markets
   against is the one case it cannot serve.
2. Restore reaches "running" only if the saved image still exists on
   the box; release retention keeps 5, so older backups restore data
   but not a running app (ADR-0007 admitted this in its Notes).
3. `/data` was tarred live with no SQLite-safe copy, while ship's own
   `data fork` already does `VACUUM INTO`.
4. Secrets sat plaintext in a tar on the same disk.

Against the fear thesis (§0), false confidence is worse than no
feature.

## Decision

With branch=env, code lives in git and secrets' source of truth is the
laptop (`secret set --from .env` imports, never moves). The only state
on the box that can actually die is `/data` — so that is the backup
primitive, grouped under the existing `data` noun:

- `ship data save` snapshots `/data` (SQLite via `VACUUM INTO`, rest
  `cp -a` — the `data fork` doctrine), tars it, and **streams it to
  the laptop**; the box keeps nothing.
- `ship data restore <id|path>` uploads, validates before touching
  `/data` (the v0.4.1 lesson), and swaps with the fork bounce.
  Prod restore is owner-only + `--confirm`.
- `ship data ls` lists local snapshots.
- **Secrets are never in a snapshot.** Recovery is: `box setup` →
  `ship` → `secret set --from .env` → `data restore`.
- The whole-app `save`/`restore` verbs and their backup format are
  deleted outright (zero users, no shims).

## Deliberately not built ("not Y, because")

- **Not** a separate `data download` verb — a save that rests on the
  box until a second verb runs recreates the façade for everyone who
  runs only the first.
- **Not** secret bundles (even encrypted) — no users are asking, and
  the off-box source of truth already exists; revisit as an RFD if
  demand appears.
- **Not** scheduled/remote destinations (R2/S3, litestream) — parked
  as RFD-0007; box-side credentials, retention, and encryption at rest
  deserve their own design. Meanwhile cron + `ship data save` from a
  laptop or CI composes (ADR-0007's composability stance survives).
- **Not** cross-file snapshot consistency — per-file safety is the
  bar (documented); perfect consistency means stopping the app.

## Consequences

The recovery story is honest and marketable: your code is in git, your
data is the only thing that can die, and a copy of it lives on your
machine. Restore-to-running no longer depends on box-side image
retention (redeploy from git supplies the code). The `data` noun now
owns all state operations: `fork`, `save`, `restore`, `ls`, `rm`.

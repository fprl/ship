# RFD-0001: Data doctrine — SQLite-first, managed otherwise, forks by file copy

**Status: draft, post-v1.** Does not modify ship-spec v1.

## Context

Two threads converge here:

1. The engine has always refused database provisioning ("no first-class
   Postgres, Redis — use external/managed services") — long a buried
   non-goal, now stated as positive doctrine in docs/positioning.md.
2. The Rails 8 / Laravel 11 generation moved SQLite into **production** —
   the one-box pattern (Solid Queue/Cache/Cable: one server, SQLite in
   prod, no Redis). That architecture preserves dev/prod parity and wants
   exactly one thing ship's audience already has: a VPS with a persistent
   disk and Litestream-style backup. What keeps 1–5 person teams off it is
   fear of the VPS deploy story, and the serverless alternatives
   (Cloudflare D1, DO-SQLite) read as obscure and locked-in.

ship is positioned to be the missing deploy story for SQLite-everywhere.
The engine already carries most of it: Litestream artifact verification
(ADR-0004), backups that snapshot `/data` including SQLite-via-Litestream
(ADR-0007), the `/data` runtime contract (ADR-0008), and a django-sqlite
example.

## Doctrine

1. **State on a ship box has exactly two shapes:** SQLite files under
   `/data`, or a URL to a managed database. **The box never runs a
   database server.** Running durable Postgres on one box is the hardest
   ops problem in this space (backups, upgrades, corruption, tuning);
   refusing it deletes the biggest failure mode ship could own. Users may
   still run their own DB container outside ship — ship does not manage
   or back it up.
2. **Same engine in every environment.** SQLite path: a file everywhere —
   local dev, previews, prod. Managed path: the same engine everywhere,
   with per-env URLs via the existing secret scoping (ship-spec §6).
   ship never encourages sqlite-in-dev/postgres-in-prod drift — that is
   the classic 12-factor parity violation.
3. **SQLite is the default path ship optimizes for**; managed is fully
   supported and never punished.

Positioning line for the Phase-3 README rewrite: *your database is a file
on your box — forkable per branch, streamed to backup, never locked in.*

## Preview data forks

Previews are only trustworthy when they run against real-shaped data.

- **SQLite path:** on preview env creation, fork prod's data into the
  preview env's data dir. Each `*.db` / `*.sqlite*` file is copied with
  `VACUUM INTO` (consistent snapshot under live writes, WAL-safe);
  remaining `/data` files copy with `cp -a` (reflink when the filesystem
  supports it). Forks cost disk, not RAM — no extra server process
  exists.
- **Managed path:** no on-box fork. Previews resolve `DATABASE_URL`
  through §6 scoping (shared preview value, or per-branch value).
  Provider branch APIs (Neon, Supabase, PlanetScale) can slot behind the
  same seam later.
- **One internal seam:** `fork(prodEnv, previewEnv)` with
  implementations: `sqlite-copy` (v1), `managed-passthrough` (v1, no-op),
  `provider-branch` (later), `cow-filesystem` (not planned).

### Relation to the dropped CoW decision

The ship-pivot review dropped CoW data forks/rewind for two reasons: RAM
economics (a database server process per fork) and the cross-service fork
gap (forking Postgres but not Redis/queues gives an incoherent world).
SQLite file forks dodge both: there is no server process (zero RAM), and
in the one-box SQLite architecture the files under `/data` **are** the
whole stateful world, so copying `/data` forks everything coherently.
This RFD re-opens only that slice; CoW filesystems stay dropped.

## Open questions

- ~~Opt-in vs default~~ **Decided (Franco, July 8): opt-in.** Forks
  happen only when the manifest asks (`[data] fork = true` or similar):
  prod PII must never land in previews by default, some apps want
  clean-slate previews, and disk is not free. Default stays empty
  `/data`.
- Sanitization: an optional `fork_sanitize` command run inside the
  preview env against the forked data before its first release.
- Refresh semantics: fork once at env creation, or re-fork on every ship?
  Related (Franco, July 8): alongside the manifest opt-in there is an
  on-demand verb — run from a branch, it forks prod's current `/data`
  into this preview now; re-running refreshes. Surface name open
  (`ship fork` vs a `data` noun).
- Large `/data` (user uploads): copy budget, exclusion globs, or
  media-stays-shared conventions.
- Disk pressure: fork sizes in `ship status`, a doctor check on
  watermark, reaper already bounds fork count via preview TTL.

## Deferred adjacent idea

Preview sleep via systemd socket activation (previews idle at zero RAM,
wake on first request, Caddy holds the connection). Not needed while the
TTL reaper bounds the preview fleet; separate note if RAM ever hurts.

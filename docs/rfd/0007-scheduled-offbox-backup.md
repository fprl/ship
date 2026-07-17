# RFD-0007: Scheduled off-box backup (R2/S3)

**Status: draft, post-v1, parked** (Franco, July 13, 2026 — D3).
Builds on ADR-0012 (data-only snapshots) and ADR-0014 (box config
foundation). Do not build until promoted to a spec section.

## Pitch

`ship data save` puts a snapshot on your laptop — a real backup, but a
manual act. The box should also push snapshots somewhere durable on a
schedule, so "I forgot to back up" stops being a failure mode. Set one
config key, and every night your `/data` lands in R2.

## Sketch

- Config keys (ADR-0014 schema, owner-set, apply=converge):
  `backup.destination` (s3-compatible URL), `backup.schedule`
  (daily default), `backup.retention` (count), credentials via the
  secret store — never in the config document.
- Converge installs a systemd timer (the reaper/doctor-timer pattern)
  that runs the same snapshot path as `data save` box-side (SQLite via
  `VACUUM INTO` — per-file consistency, same documented bar) and
  pushes to the destination.
- `ship data ls` grows a remote column; `ship data restore` accepts a
  remote id.
- Doctor check: last successful push age; degraded → box webhook
  (§17).
- **Encryption at rest is a blocker, not an option**: snapshots leave
  the box, so they ship encrypted (age to the owner's key is the
  leading candidate). Secrets stay excluded regardless (ADR-0012).

## Open questions (for promotion time)

- Litestream for continuous SQLite replication vs periodic snapshots
  — litestream returns here *properly* or not at all (the deleted
  `--litestream` flag installed an unconfigured .deb: theater).
- Restore-from-remote UX on a fresh box (before any app exists).
- Cost/egress narration (`box status` showing backup destination
  health).

## Why parked (not built with ADR-0012)

Box-side credentials, retention policy, encryption, and a timer are a
real design surface; bolting them onto the first honest snapshot verb
would have delayed it. Meanwhile cron + `ship data save` from a laptop
or CI composes today.

## Design input from the July 17, 2026 durability consult (sol round 9)

Recorded so the eventual arc starts from settled ground; mechanics
argued, no scope committed.

- **Order**: scheduled, owner-encrypted, full `/data` snapshots land
  FIRST — they are the restore interface and the only coverage for
  ordinary files. Continuous SQLite streaming (Litestream-class) comes
  SECOND, behind the same interface, as an RPO upgrade from
  worst-case-a-day to worst-case-seconds. Streaming never replaces
  snapshots and never reaches zero: the last transaction can miss the
  final upload, and an object-store outage widens the window for its
  duration.
- **Streaming shape when built**: replicate base-snapshot +
  contiguous WAL-derived state per database (raw `*-wal` files alone
  are not a backup); ~10s target interval (upload-cost curve:
  ~2.59M PUTs/month at 1s vs ~259k at 10s); ship owns database
  enumeration under each env's `/data` and restore ordering (no
  built-in restore-all exists); surface the OBSERVED replication lag
  ("last remotely confirmed transaction"), not "service running"; do
  not overlay a seconds-fresh database onto midnight-fresh plain files
  silently — no cross-file transaction boundary exists; periodically
  restore into a scratch path and run PRAGMA integrity_check (upload
  success is not restorability); box gets append/write-only credentials
  with bucket lifecycle handling expiry, so the box cannot delete its
  own history.
- **Present blocker**: this RFD requires owner-held encryption;
  Litestream v0.5 has no client-side Age-compatible encryption
  (provider SSE hides nothing from the provider). Snapshots with Age
  can ship now; streaming waits for a compatible client or a different
  implementation.
- **Reconstructibility residue** (what a fresh box CANNOT recover
  today even with v0.9 committed state + a `/data` backup): application
  secrets (`/etc/ship/secrets` is deliberately outside backups —
  box-only rotations are lost), member registry + authorized keys
  beyond the bootstrap owner key, exact artifact identity (images/
  history are box-local; a redeploy mints new tuples; dirty releases
  may be unreproducible), preview identities + capability links, ACME/
  internal-CA state (public certs reissue in minutes absent rate
  limits; a lost internal CA changes the trust root), IP/DNS (TTL can
  dominate recovery), and all journals/audit evidence. The honest
  claim is "working service from Git + secrets + backup in 10-30
  minutes," not "the same box." Backup credentials and the decryption
  identity MUST live off-box or restore deadlocks.
- **The un-capturable property** (why none of this becomes a
  platform): failure-independent availability — serving requests while
  the machine is absent needs a second execution target, an external
  traffic switch, and an independent monitor, which is mechanically
  the beginning of a platform. Ship's line stays: seconds-scale data
  durability and minutes-scale rebuild on one owned box; independent
  dead-man monitoring is the one external piece worth having (a dead
  box cannot report its own death).

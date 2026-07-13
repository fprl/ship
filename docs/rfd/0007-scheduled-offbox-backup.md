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

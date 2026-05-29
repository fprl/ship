# ADR-0007: Backup and Restore as Paired Primitive

- **Status**: Accepted; amended by ADR-0008
- **Date**: 2026-05-25
- **Related**: ADR-0002 (state file layout), ADR-0005 (container
  runtime via required Dockerfile), ADR-0006 (cuts and composability),
  ADR-0008 (manifest v2/env root/runtime identity),
  [docs/positioning.md](../positioning.md).

> ADR-0008 supersedes this ADR's `shared/`, `[services.*]`, and nested
> `/var/apps/<app>/<env>/` examples. Current backups snapshot `data/`, the
> applied `simple-vps.toml`, secrets, and static release assets when present;
> restore writes the release manifest snapshot under
> `/var/apps/<app>.<env>/releases/<release>/simple-vps.toml`.

## Context

The positioning doc names "I lost my data" as the most common
horror story for the indie-hacker audience. ADR-0005's container
model handles deploy and runtime but leaves data persistence to
the user — "write a Dockerfile for Postgres." That is true, but
punts the hard parts: where the data lives, who backs it up, how
to restore.

For an indie hacker or small team running their stack on one VPS,
"the VPS died" is a survivable event only if the path back to
running takes one command, not twelve. This ADR commits to that
bar.

This ADR is a skeleton — it locks in the verbs, the contract, and
the bar. Implementation details (specific destination drivers,
snapshot mechanism, encryption format) are deferred until
simple-vps is shipping container deploys end-to-end per
ADR-0005/0006.

## Decision

### 1. Paired primitive: `backup` and `restore`

Backup and restore are paired verbs at the CLI level. `backup`
without `restore` is half a primitive; the bar is that the
restore path exists, is tested, and is exercised in fake-VPS
coverage from day one of this ADR's implementation.

```bash
simple-vps backup <env>                          # snapshot now to default destination
simple-vps backup --to=<destination> <env>       # snapshot to a specific destination
simple-vps backup list <env>                     # list available backups
simple-vps backup rm <env> <backup-id>           # delete a specific backup

simple-vps restore --from=<backup-id> <env>             # restore from backup
simple-vps restore --from=<backup-id> --dry-run <env>   # show what would change
```

### 2. The bar: fresh VPS, one command

The reference scenario is: VPS died, user has a backup, user is
serving again.

Concretely, the workflow from a fresh Ubuntu box:

```bash
# 1. Bootstrap the host
./install.sh --mode remote --host <new-ip> ...

# 2. Restore the app
simple-vps restore --from=file:///etc/simple-vps/backups/myapp/production/2026-05-25-1432.tar production
```

After step 2, the app is running. No "now configure Caddy," no
"now re-add your secrets," no "now create the systemd unit." The
restore re-creates the app's on-disk state, re-imports its secret
values from the backup, brings up its containers, and reconciles
its routes.

The shipped first implementation uses local filesystem backups and can restore
to running state when the saved image still exists on the host. Remote backup
destinations and portable encrypted secret bundles are the next hardening
layer, not part of the first shipped primitive.

### 3. Backup payload

A backup contains everything needed to restore one `(app, env)`
to running state:

- **Data:** snapshot of `/var/apps/<app>.<env>/data/`, mounted into
  containers as `/data`.
- **Manifest:** the `simple-vps.toml` as it stood at backup time,
  so route definitions, process shape, and env values are
  preserved.
- **Secret values:** the resolved secret values for `(app, env)`
  from the secret store. The first shipped format stores them in the local tar
  backup; encrypted portable bundles remain future scope.
- **Image release:** the release ID that was running when the backup was made.
  Restore starts the saved local image tag for that release. If the image no
  longer exists on the host, restore cannot bring the app to running state yet.

Backups do **not** include:

- The host itself (host hardening is `install.sh`'s job).
- Other `(app, env)` pairs on the same VPS (each is backed up
  independently).
- Container image layers.

### 4. Destinations

The shipped destination driver is local filesystem only:

- `file:///var/backups/simple-vps/` — local directory on the VPS.

Plain host paths are accepted too. S3-compatible object storage, restic, and
manifest-level backup destination config remain planned.

### 5. Scheduling is out of scope (here)

This ADR ships `simple-vps backup <env>` as an explicit verb. It
does **not** ship a scheduler. Users wanting daily backups invoke
`simple-vps backup` from cron, systemd timers, or any external
scheduler they prefer.

A future ADR may add host-side scheduling (`schedule = "daily"`
in the `[env.<env>.backups]` block); the manifest grammar reserves
that syntax. This ADR's implementation parses but ignores it.

This matches the composable-primitive framing in
[positioning.md](../positioning.md#1-hold-the-kamal-line) — the
bare CLI is complete; a scheduler is an optional layer on top.

### 6. Backup format

The on-the-wire format is intentionally generic so simple-vps's
restore does not depend on the destination driver:

```
<backup-id>.tar        # metadata.json, simple-vps.toml, secrets.json, data/
```

`secrets.json` is plaintext inside the local backup archive. That is acceptable
for the local-only first implementation because the backup sits under
`/etc/simple-vps/backups` with root-only archive permissions. Portable
encrypted secret bundles are still required before remote destinations ship.

### 7. Idempotent restore

Restore is idempotent against partial existing state on the
target VPS. Re-running `simple-vps restore --from=<backup> <env>`
after a partial failure (network blip, missing image,
permissions) picks up where it left off without leaving the env
in a worse state than before.

Concretely:

- Restore replaces `/var/apps/<app>.<env>/data/` with the backup's
  `data/` tree.
- Restore writes the backed-up secret values into the env secret store.
- If the container is already running on the expected image,
  restore reconciles routes and exits.

This is the same idempotency `simple-vps deploy` already provides
under ADR-0006's per-service rolling model; restore reuses the
same machinery.

## Consequences

### What this enables

- "VPS died, I have a backup" is a survivable event by design.
- One backup format covers SQLite-via-Litestream,
  Postgres-via-pg_dump, Redis dumps, and plain file data — they all
  live under `/var/apps/<app>.<env>/data/` and snapshot as one.
- Indie hackers have a real disaster-recovery story without
  learning restic, cron, and shell scripts on day one.

### What this gives up

- Cross-app, cross-env, or whole-host backups are not a thing.
  Each `(app, env)` is independent. "Back up the whole VPS" is
  N backup invocations.
- Container images are not part of the backup payload. The first shipped
  restore path needs the saved local image to exist on the host.
- Local backup archives contain plaintext secrets and must be protected like
  root-only host state until encrypted portable bundles ship.

### What becomes harder

- Adding a manifest knob now has to consider restore semantics. A
  new `[processes.*]` flag that changes the container's on-disk
  layout needs a corresponding migration in restore.

## Out of scope

- **Host backups.** simple-vps backs up apps; the host is
  `install.sh`'s responsibility to recreate from scratch.
- **Container image archival.** A registry is the right answer for keeping
  built images around — out of scope here, out of scope in ADR-0005.
- **Backup encryption with BYOK / user-managed keys.** Portable encrypted
  bundles are future work.
- **`simple-vps backup verify <id>`** (dry-run restore to a scratch
  directory). Clear v2 feature; not in this ADR's implementation.
- **Host-side scheduling.** See Decision 5.

## Cutover

The first implementation ships the local driver, public verbs, fake-VPS smoke
coverage, and real-box smoke coverage. Remote destinations, portable encrypted
secret bundles, and image archival remain future work.

## Notes

- The "restore brings up the app to running" bar currently assumes the saved
  local image still exists on the host.
- The local backup format is intentionally simple and should be migrated before
  remote destinations ship.
- The reserved secret key names (`backup_s3_key`,
  `backup_restic_password`, etc.) are added to ADR-0006's
  closed-set policy as values simple-vps will never let user
  manifests stomp on.

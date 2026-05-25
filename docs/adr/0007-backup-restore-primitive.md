# ADR-0007: Backup and Restore as Paired Primitive

- **Status**: Accepted (skeleton; implementation deferred)
- **Date**: 2026-05-25
- **Related**: ADR-0002 (state file layout), ADR-0005 (container
  runtime via required Dockerfile), ADR-0006 (cuts and composability),
  [docs/positioning.md](../positioning.md).

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
simple-vps backup <env> --to=<destination>       # snapshot to a specific destination
simple-vps backup list <env>                     # list available backups
simple-vps backup rm <env> <backup-id>           # delete a specific backup

simple-vps restore <env> --from=<backup-id>             # restore from backup
simple-vps restore <env> --from=<backup-id> --dry-run   # show what would change
```

### 2. The bar: fresh VPS, one command

The reference scenario is: VPS died, user has a backup, user is
serving again.

Concretely, the workflow from a fresh Ubuntu box:

```bash
# 1. Bootstrap the host
./install.sh --mode remote --host <new-ip> ...

# 2. Restore the app
simple-vps restore production --from=s3://my-backups/myapp/2026-05-25-1432
```

After step 2, the app is running. No "now configure Caddy," no
"now re-add your secrets," no "now create the systemd unit." The
restore re-creates the app's on-disk state, re-imports its secret
values from the backup, brings up its containers, and reconciles
its routes.

If step 2 does not get to "app serving" in one command on a fresh
box, the primitive is not shipped. This is the test, exercised in
fake-VPS coverage.

### 3. Backup payload

A backup contains everything needed to restore one `(app, env)`
to running state:

- **Data:** snapshot of `/var/apps/<app>/<env>/shared/` (the
  bind-mounted directory from ADR-0005 Section 4).
- **Manifest:** the `simple-vps.toml` as it stood at backup time,
  so route definitions, service shape, and env values are
  preserved.
- **Secret values:** the resolved secret values for `(app, env)`
  from the secret store, encrypted at rest in the backup. Without
  these, restore on a fresh VPS would land with secret *references*
  pointing at an empty store.
- **Image reference:** the container image tag (e.g.,
  `simple-vps/myapp-prod:abc1234`) and source git SHA. Restore
  tries to rebuild the image from the source git SHA, inheriting
  the git access configured during `install.sh` setup; users
  restoring private repositories must ensure the restore VPS has
  equivalent git credentials. If the source repository is not
  accessible from the restore VPS, the restore brings the app to
  "configured but not running" and prints a clear next step.
  See Notes.

Backups do **not** include:

- The host itself (host hardening is `install.sh`'s job).
- Other `(app, env)` pairs on the same VPS (each is backed up
  independently).
- Container image layers (rebuilt from source on restore; see
  Notes for the registry path).

### 4. Pluggable destinations

A destination is a URL-shaped reference to where backups land:

- `file:///var/backups/simple-vps/` — local directory on the VPS.
- `s3://<bucket>/<prefix>` — S3-compatible object storage
  (Backblaze B2, Cloudflare R2, MinIO, AWS S3).
- `restic://<repo>` — pre-existing restic repository, for users
  who already manage backups with restic.

The default destination per env is configured in the manifest:

```toml
[env.production.backups]
destination = "s3://my-bucket/myapp-prod"
keep = 30                       # retention count, optional
```

Secret values for the destination (S3 keys, restic password) are
stored in the secret store under reserved key names
(`@secret:backup_s3_key`, `@secret:backup_restic_password`) and
resolved by the helper at backup or restore time.

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
<backup-id>.tar.zst        # zstd-compressed tar of the payload
<backup-id>.metadata.json  # plaintext: app, env, timestamp, source git SHA, image ref, schema version
<backup-id>.secrets.enc    # encrypted blob: resolved secret values
```

The encryption key for `.secrets.enc` is derived from a
per-`(app, env)` master in the secret store. Restoring requires
the master key — which means restore from a backup requires either
the master key from the original VPS (recommended: capture it on
first backup with instructions to store offline) or accepting that
the restored app starts without its secrets and the user
re-populates them.

**First-backup must require explicit user acknowledgment that the
master key has been stored offline, not print-and-continue.**
Losing this step silently turns a backup into a partial backup;
the UX commitment in this ADR is that the user cannot complete
their first `simple-vps backup <env>` without confirming the key
is captured. Implementation may use an interactive prompt for
TTY invocations and require `--key-acknowledged` for scripted
ones.

This is the "without a registry, restore brings the app to
configured-but-not-running" mirror for secrets: a deliberate
asymmetry that keeps the primitive usable without forcing a key-
management product on the user.

### 7. Idempotent restore

Restore is idempotent against partial existing state on the
target VPS. Re-running `simple-vps restore <env> --from=<backup>`
after a partial failure (network blip, missing image,
permissions) picks up where it left off without leaving the env
in a worse state than before.

Concretely:

- If `/var/apps/<app>/<env>/shared/` exists and matches the
  backup, restore skips re-extraction.
- If the env's secrets are already present, restore does not
  overwrite them with the backup's values unless `--overwrite`
  is passed.
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
  live under `/var/apps/<app>/<env>/shared/` and snapshot as one.
- Indie hackers have a real disaster-recovery story without
  learning restic, cron, and shell scripts on day one.

### What this gives up

- Cross-app, cross-env, or whole-host backups are not a thing.
  Each `(app, env)` is independent. "Back up the whole VPS" is
  N backup invocations.
- Container images are not part of the backup payload. Restore on
  a fresh VPS without source-repo access leaves the app
  "configured but not running" until the user redeploys.
- Secret restore depends on the user keeping the per-`(app, env)`
  master key. Lose the key, restore the data without the secrets.

### What becomes harder

- Adding a manifest knob now has to consider restore semantics. A
  new `[services.*]` flag that changes the container's on-disk
  layout needs a corresponding migration in restore.

## Out of scope

- **Host backups.** simple-vps backs up apps; the host is
  `install.sh`'s responsibility to recreate from scratch.
- **Container image archival.** Images are rebuilt from the
  manifest + source on restore. A registry is the right answer for
  keeping built images around — out of scope here, out of scope in
  ADR-0005.
- **Backup encryption with BYOK / user-managed keys.** Backups are
  encrypted with a per-`(app, env)` master derived in the secret
  store. Bring-your-own-key for compliance scenarios is a future
  ADR if real demand surfaces.
- **`simple-vps backup verify <id>`** (dry-run restore to a scratch
  directory). Clear v2 feature; not in this ADR's implementation.
- **Host-side scheduling.** See Decision 5.

## Cutover

Implementation cutover is deferred to a follow-up ADR after
ADR-0005 and ADR-0006 have shipped. The verbs, the bar, the
payload, the format, and the UX commitments in this ADR are the
contract that future implementation work must honor; the
follow-up ADR specifies per-driver work (S3, restic, local
file), the encryption primitive, and the fake-VPS coverage
matrix.

## Notes

- The "restore brings up the app to running" bar assumes either
  (a) the user has a registry configured for built images, or (b)
  the source is in git and re-buildable on the restored VPS. Most
  simple-vps users fall into case (b). When neither is available,
  the restore command brings the app to "configured but not
  running" and prints a clear next step.
- The backup format (`tar.zst` + `metadata.json` + `secrets.enc`)
  is intentionally generic, not tied to restic or any specific
  tool. This keeps the restore path independent of what the user
  chose for the destination side.
- Implementation is deferred behind ADR-0005 + ADR-0006 cutover.
  Verbs and the bar are committed; the wire format details,
  destination drivers, and encryption primitive land later as
  implementation PRs that test against the bar.
- The reserved secret key names (`backup_s3_key`,
  `backup_restic_password`, etc.) are added to ADR-0006's
  closed-set policy as values simple-vps will never let user
  manifests stomp on.

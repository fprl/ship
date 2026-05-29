# ADR-0008: Manifest v2, env roots, and runtime identity

- **Status:** Accepted
- **Date:** 2026-05-28
- **Supersedes/amends:** ADR-0005 manifest shape, service naming,
  `shared/` storage layout, and static shape; ADR-0006 label/identity
  assumptions and closed set; ADR-0007 backup payload path.

## Context

The container cutover made the implementation safer, but the public
manifest leaked too much runtime machinery:

- `[services.*]` did not match the mental model of "one image, multiple
  long-running processes."
- `[env.production.env]` was visually awkward and conceptually noisy.
- `tmpfs`, `net_bind_service`, and stock-image writable paths exposed
  Podman hardening details.
- `/var/apps/<app>/<env>/shared` mixed user data and generated runtime
  config.
- Nested env roots made `/var/apps/<app>` look like a lifecycle boundary
  even though every mutating operation is scoped to `(app, env)`.

There are no users yet, so v1 should remove these surfaces instead of
carrying compatibility aliases.

## Decision

### Manifest v2

The manifest remains `simple-vps.toml`.

Public config uses `processes`, not `services`. A process is one
long-running container process from the app image. Multiple processes
share one Dockerfile and one built image.

For container apps, non-secret env vars live in `[vars]`, with
env-specific overrides under `[env.<name>.vars]`. Static-only apps reject
`[vars]` because there is no runtime env to inject.

Route entries live in one `[routes.*]` namespace. A route has exactly
one target:

- `process = "web"` for Caddy reverse proxy to a process.
- `serve = "dist"` for Caddy `file_server` from a static directory.
- `redirect = "https://example.com"` for permanent redirects.

`path` is optional and means prefix routing. The omitted path owns the
whole host. Static `path = "/docs"` strips the `/docs` prefix before
file lookup. `path = "/"` is rejected; the host root is expressed by
omitting `path`.

Container apps may mix `process` routes and `serve` routes. Mixed apps are
still container apps: one Dockerfile builds one image, while each `serve`
directory is uploaded and snapshotted as host-side static assets for the
same release ID. The client folds a static-tree hash into the release ID
when `serve` routes are present, so generated static output does not need to
be tracked by Git to create a distinct deploy release.

Runtime limits are product-level resources:

```toml
[processes.web]
resources = { memory = "512m", cpus = 0.5 }
```

They are not raw Podman flags and they do not belong in the Dockerfile.

Deploy-time migrations use:

```toml
[deploy]
release = "bun run migrate"
```

The release command runs from the built image with resolved vars,
secrets, and `/data` mounted, before routed web processes move traffic.

### Removed public fields

These fields are not part of v1:

- route `type`
- public `tmpfs`
- `healthcheck_status`
- `net_bind_service`
- arbitrary Podman/Docker flags
- arbitrary host mounts
- multiple Dockerfiles/images per app
- first-class Litestream
- cron/scheduled jobs
- managed Postgres/Redis
- custom Caddy snippets

`healthcheck` is renamed to `health`.

### Host layout

Each `(app, env)` has a flat, human-readable env root:

```text
/var/apps/<app>.<env>/
  data/             # mounted into containers as /data; included in backup/restore
  runtime/.env      # generated runtime config; not user data
  static/           # static releases/assets when static routes are deployed
  releases/<sha>/   # release metadata, including manifest snapshots
  simple-vps.toml   # applied manifest snapshot
  simple-vps.json   # env identity anchor
```

`/var/apps/<app>` is not a runtime identity primitive. The operational
unit is always `(app, env)`.

Containers mount only the data directory:

```text
/var/apps/<app>.<env>/data -> /data
```

The generated env file is passed through Podman's `--env-file`; it is
not mounted as user data.

Every successful deploy stores the manifest that produced that release:

```text
/var/apps/<app>.<env>/releases/<sha>/simple-vps.toml
```

Rollback loads the selected release snapshot to recover the old process,
route, port, static path, and var-reference shape. Secrets remain current in
the host secret store and are resolved again when a container release is
rolled back.

### Derived infra ID

Human paths stay readable. Linux, Podman, DNS, and lock identifiers use
a bounded derived ID:

```text
infra_id = "svps-" + sha256(app + "\0" + env)[0:12]
```

Example:

```text
Host path:      /var/apps/api.production
Infra ID:       svps-a8f9b2c4d6e8
System user:    svps-a8f9b2c4d6e8
Network:        svps-a8f9b2c4d6e8
Container DNS:  svps-a8f9b2c4d6e8-web-f927362
Lock:           svps-a8f9b2c4d6e8.lock
```

The ID is deterministic and inspectable, but never reverse-parsed. The
identity anchor records and validates it:

```json
{
  "version": 1,
  "app": "api",
  "env": "production",
  "infra_id": "svps-a8f9b2c4d6e8"
}
```

Runtime discovery uses labels, not parsed names:

```text
simple-vps.app=api
simple-vps.env=production
simple-vps.process=web
simple-vps.infra_id=svps-a8f9b2c4d6e8
simple-vps.release=f927362
```

### Secrets

Secrets remain root-owned plaintext files under:

```text
/etc/simple-vps/secrets/<app>/<env>/<KEY>
```

The public command is `simple-vps secret set <env> <key>`. Values are
read from stdin only. Deploy resolves all `@secret:KEY` references
before any build or container mutation. Missing secrets fail fast.

### Handoff semantics

Routed web processes use versioned containers:

1. Build the image.
2. Run `[deploy].release`, if present.
3. Snapshot any `serve` route directories under
   `static/releases/<release>/<route-name>/`.
4. Start the next versioned container.
5. Probe it from the Caddy/ingress network.
6. Render Caddy to the next container name and the next static release path.
7. Validate and reload Caddy.
8. Remove old containers for that process after reload succeeds.

Workers are stop-start by default. Overlapping workers are dangerous
without an explicit policy, so v1 does not try to make workers
zero-downtime.

### Static routes

Static-only apps and mixed container/static apps are part of v1. Caddy serves
uploaded static directories directly from the host; nginx is not involved.
For a route named `docs` on release `f927362`, the copied tree lives at:

```text
/var/apps/<app>.<env>/static/releases/f927362/docs/
```

Caddy points at the release path. `static/current` remains a bookkeeping
symlink for active-release discovery, backups, and rollback selection.

When a manifest has `serve` routes, clean Git deploys still append those
directories from the working tree. This keeps generated or ignored `dist/`
output deployable without switching the whole artifact away from `git archive`.
The release ID includes a static-tree hash suffix so static byte changes
produce a distinct release.

Backups include the active static release assets whenever the applied
manifest has `serve` routes, whether the app is static-only or mixed.
Rollback and restore move process containers, static assets, Caddy, and the
manifest snapshot as one release operation.

## Consequences

- The manifest is simpler and closer to the user intent.
- Runtime resources remain configurable without exposing raw Podman.
- SQLite and uploads have a clear default: write to `/data`.
- Backups snapshot `data/`, not generated runtime config.
- Static-only apps can exist without containers, so `simple-vps.json` is
  required for host-side env identity.
- Rollback is a release operation: it restores the older artifact together
  with the manifest shape that produced that artifact.
- App-wide operations scan metadata/labels instead of relying on a
  parent `/var/apps/<app>` directory.
- Old manifest names intentionally break before v1 users exist.

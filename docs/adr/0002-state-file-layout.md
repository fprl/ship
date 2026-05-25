# ADR-0002: State File Layout Under `/etc/simple-vps/`

- **Status**: Accepted
- **Date**: 2026-05-20
- **Depends on**: ADR-0001 (bounded Go provisioner). This ADR describes the
  files that provisioner reads and writes.
- **Related**: ADR-0001.

## Context

Three commands mutate persistent state on the host:

- `simple-vps host install` writes host-level configuration.
- `simple-vps setup` / `deploy` / `destroy` writes app registry state.
- `simple-vps route publish` / `remove` writes the ingress route table.
- Cloudflare commands write provider-specific external IDs.

These four concerns have different owners, different write cadences, and
different rollback semantics. Putting them in one file couples unrelated
commands and makes the audit signal (what intent changed when?) noisy.

A previous draft proposed a single `config.json`. Review converged on a
multi-file layout, with one base file split into clearly-named top-level
sections to preserve the audit signal.

## Decision

### 1. Directory layout

```text
/etc/simple-vps/
  host.json                    host-scope configuration
  apps.json                    registry of apps on this host
  routes.json                  ingress route table
  providers/
    cloudflare.json            Cloudflare account / tunnel / DNS state
  secrets/                     per-app/env secret value files
```

Filename convention: `{scope}.json`. `host.json` is host-scoped. `apps.json` is
the host's registry of apps. `routes.json` is the host's route table. Provider
state nests under `providers/` so adding `providers/tailscale.json` later is
not a layout change.

### 2. `host.json` Schema - Three Top-Level Sections

```json
{
  "version": 1,
  "desired": {
    "users": {
      "operator": "operator",
      "deploy": "deploy"
    },
    "ingress": {
      "expose": "private",
      "tunnel": "cloudflare"
    },
    "features": {
      "podman": true,
      "litestream": true
    },
    "packages": {
      "podman":     { "source": "ubuntu",         "track": "noble" },
      "litestream": { "source": "github-release", "version": "0.5.8" }
    }
  },
  "observed": {
    "packages": {
      "podman":     { "version": "4.9.3" },
      "litestream": { "version": "0.5.8" }
    },
    "ingress": {
      "ufw_80_443_allowed": false,
      "cloudflared_service_active": true,
      "caddy_container_active": true
    }
  },
  "meta": {
    "installed_at": "2026-05-20T15:00:00Z",
    "simple_vps_version": "0.3.0",
    "last_apply": {
      "id": "20260520T151200Z",
      "started_at": "2026-05-20T15:12:00Z",
      "finished_at": "2026-05-20T15:12:34Z",
      "status": "ok",
      "operations_changed": 3
    }
  }
}
```

### 3. Ingress as two internal fields, modes as CLI sugar

`desired.ingress` carries two orthogonal fields:

- **`expose`** - `"public"` | `"private"`. Does UFW open 80/443?
- **`tunnel`** - `"none"` | `"cloudflare"` | `"tailscale-funnel"`. Is a tunnel
  terminating traffic?

The CLI accepts `--ingress cloudflare|public|private` as a convenience preset
that expands to:

| `--ingress` | `expose` | `tunnel` |
|---|---|---|
| `cloudflare` | `private` | `cloudflare` |
| `public` | `public` | `none` |
| `private` | `private` | `none` |

Future ingress shapes (Cloudflare-DNS-only, Tailscale Funnel) become new
presets without changing the underlying two-field model.

### 4. Invariants

These rules apply to every file under `/etc/simple-vps/`:

1. **`desired` is never mutated by `apply`.** Only `host install`, `host
   configure`, or hand-editing changes the desired section. If apply needs to
   record something it observed, that goes in `observed:`, not `desired:`.
2. **Writes are atomic.** Tempfile in the same directory + `rename(2)`. No
   partial-write corruption windows.
3. **Keys are written in stable sorted order at each level.** `git diff` on
   any of these files is meaningful: key reordering never appears in diffs.
4. **`version` is always present at the top level.** A provisioner that
   reads a file with a `version` higher than it understands refuses to write
   the file rather than silently truncating fields it doesn't recognize.
5. **Files have explicit owners:**
   - `host.json` - root:root 0644
   - `apps.json`, `routes.json` - root:root 0644
   - `providers/cloudflare.json` - root:root 0600 (contains external IDs)
   - `secrets/<app>/<env>.env` - root:root 0600 (per-app/env secret values)

### 5. `apps.json` and `routes.json` stay separate

These are conceptually orthogonal:

- An app can exist in `apps.json` with zero routes.
- A route can outlive its app momentarily during a destroy that interleaves
  service teardown and route removal.
- They are written by different command paths: `apps.json` by the deploy
  lifecycle, `routes.json` by the privileged helper's route commands.

```json
// apps.json
{
  "version": 1,
  "apps": {
    "api": {
      "path": "/var/apps/api",
      "services": ["web"],
      "current_release": "20260520T150000Z"
    }
  }
}
```

```json
// routes.json
{
  "version": 1,
  "routes": [
    {
      "app": "api",
      "host": "api.example.com",
      "type": "proxy",
      "service": "web",
      "port": 3000
    }
  ]
}
```

App paths remain `/var/apps/<name>` (unchanged from current SPEC). Migrating
to `/srv/simple-vps/apps/<name>` is out of scope for this ADR.

### 6. Provider State - One File Per Provider

```json
// providers/cloudflare.json
{
  "version": 1,
  "account_id": "...",
  "tunnel_id": "...",
  "tunnel_name": "simple-vps-prod-1",
  "routes": {
    "api.example.com": {
      "app": "api",
      "zone_id": "zone-id",
      "dns_record_id": "record-id"
    }
  }
}
```

Future providers (`providers/tailscale.json`, etc.) follow the same shape:
one JSON file per external system, holding the IDs and state Simple VPS needs
to converge with that system.

## Consequences

### What this enables

- The `desired:` section of `host.json` continues to reflect intent changes
  after the file is rewritten by apply, because `desired:` is invariant under
  apply and key ordering is stable. The full file can still change when
  `observed:` or `meta:` is refreshed.
- Each command owns one file. `host install` writes `host.json`. `deploy`
  writes `apps.json`. `route publish` writes `routes.json`. Cloudflare
  commands write `providers/cloudflare.json`. Write contention is per-file,
  not global.
- Provider integrations slot in under `providers/` without renaming or
  restructuring existing files.
- The ingress two-field model survives future modes that don't fit today's
  three-preset enum.

### What this gives up

- One-file simplicity. Four files plus a directory is more to enumerate in
  `host doctor` output and in backup/restore tooling. Mitigation: a
  `host backup` command that tars the whole `/etc/simple-vps/` directory.
- Schema-version coupling across files. Bumping `host.json`'s `version` does
  not bump `apps.json`'s `version`. Per-file versions handle this, but the
  product must remember to bump only the file whose schema changed.

### What becomes harder

- Atomic writes across files. Adding an app simultaneously to `apps.json` and
  `routes.json` is not transactional. Mitigation: command sequences are
  ordered such that a partial failure leaves the system in a state
  `host doctor` can describe (e.g. "route for `api.example.com` references
  unknown app `api`"). Apps without routes and routes without apps are both
  valid intermediate states.

## Out of scope

- Moving the app root from `/var/apps/<name>` to `/srv/simple-vps/apps/<name>`.
- A daemon that watches these files for change. Files are read on command
  invocation; nothing observes them between invocations.
- Cross-command file locking. Each file write is atomic, but commands do not
  take a global lock across multiple store files.

## Notes

- The `desired` / `observed` / `meta` split was the central trade-off in
  this ADR. The audit-signal benefit of "diff on `host.json` means intent
  changed" is preserved by the invariant in section 4.1, which depends on the
  provisioner respecting the rule. A future test should assert that
  `apply` never writes a key outside `observed:` and `meta:`.
- Per-provider state files are the extensibility seam. Anything specific to
  Cloudflare, Tailscale, or a future provider lives under `providers/`, not
  inside `host.json`.

# ADR-0002: State File Layout Under `/etc/simple-vps/`

- **Status**: Accepted
- **Date**: 2026-05-20
- **Updated**: 2026-05-28
- **Depends on**: ADR-0001 (bounded Go provisioner).
- **Amended by**: ADR-0005, ADR-0006.

## Context

The first state layout split host intent, app registry, route table, and
provider IDs into separate JSON files. ADR-0005 and ADR-0006 removed the
host-side app registry and route table:

- App inventory is now live state from labelled Podman containers.
- Routes are now per-app Caddy fragments under `/etc/caddy/conf.d/`.
- Cloudflare still needs provider state because DNS records and tunnel IDs
  live outside the VPS.

This ADR records the current state contract, not the pre-cutover draft.

## Decision

### 1. Directory layout

```text
/etc/simple-vps/
  host.json                    host-scope desired/observed/meta state
  providers/
    cloudflare.json            Cloudflare account / tunnel / DNS state
  secrets/
    <app>/<env>/<KEY>          one secret value per file, mode 0600
```

Non-JSON runtime artifacts deliberately live outside this state directory:

```text
/etc/caddy/conf.d/
  simple-vps-<app>-<env>.caddy per-(app, env) route fragment

/var/apps/<app>/<env>/
  shared/.env                  resolved runtime env file

/run/simple-vps/locks/
  <app>-<env>.lock             host-side mutation lock
```

`apps.json` and `routes.json` no longer exist. Reintroducing durable app or
route registry state requires a new ADR, because it changes the public state
contract and the backup/restore surface.

### 2. `host.json` schema

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
      "docker": false,
      "litestream": true
    },
    "packages": {
      "podman": { "source": "ubuntu", "track": "noble" },
      "litestream": { "source": "github-release", "version": "0.5.8" }
    }
  },
  "observed": {
    "packages": {
      "podman": { "version": "4.9.3" },
      "litestream": { "version": "0.5.8" }
    },
    "ingress": {
      "ufw_80_443_allowed": false,
      "cloudflared_service_active": true
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

`desired` is host intent. `observed` and `meta` are host facts written by the
provisioner. App deploy state does not live in `host.json`.

### 3. Ingress fields

`desired.ingress` carries two orthogonal fields:

- **`expose`** - `"public"` | `"private"`. Does UFW open 80/443?
- **`tunnel`** - `"none"` | `"cloudflare"` | `"tailscale-funnel"`. Is a tunnel
  terminating traffic?

The current CLI exposes provider-specific flags directly. Preset flags such
as `--ingress cloudflare|public|private` can be added later without changing
the state model.

### 4. Provider state

```json
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

Future providers (`providers/tailscale.json`, etc.) follow the same pattern:
one JSON file per external system, holding only the IDs and state simple-vps
needs to converge with that system.

### 5. Invariants

These rules apply to every JSON file under `/etc/simple-vps/`:

1. **Writes are atomic.** Tempfile in the same directory + `rename(2)`.
2. **Keys are stable.** Generated JSON uses deterministic formatting so diffs
   are meaningful.
3. **`version` is always present.** A binary that sees a future version refuses
   to write rather than truncating fields it does not understand.
4. **Owners are explicit.**
   - `host.json` - root:root 0644
   - `providers/cloudflare.json` - root:root 0600
   - `secrets/<app>/<env>/<KEY>` - root:root 0600
5. **No compatibility ballast before the first schema bump.** Version is still
   `1`; migration code lands with the first real state schema bump, not before.

## Consequences

### What this enables

- Host install state stays small and auditable.
- Provider integrations slot in under `providers/` without changing host state.
- App/route inspection is sourced from the runtime truth: Podman labels and
  Caddy fragments, not a second registry that can drift.
- Backup/restore has a clear split: `/etc/simple-vps/` for control state,
  `/etc/caddy/conf.d/` for route fragments, `/var/apps/` for app data.

### What this gives up

- There is no durable app registry to query. `status` and the planned
  `app list --json` must inspect Podman labels and Caddy fragments.
- Route state is not a standalone JSON table. Cloudflare DNS state remains in
  provider state; local Caddy routing state is the fragment itself.
- Migration discipline starts when schema version `2` exists. Until then,
  adding migration code would create compatibility surface for no users.

## Out of scope

- A daemon that watches state files.
- Cross-provider transactionality.
- Reintroducing `apps.json` or `routes.json`.

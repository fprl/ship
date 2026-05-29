# Primitive Freeze Review Brief

We are preparing `simple-vps` for a primitive freeze before doing DX work such
as `simple-vps init` or a docs site.

Audience: another LLM or senior engineer reviewing the product contract.

Important constraint: there are no users yet. We do not need backwards
compatibility. If a primitive is unclear or wrong, recommend deleting or
reshaping it now.

## Product Goal

`simple-vps` should make a single VPS feel like a small PaaS:

- install/converge a VPS
- deploy Dockerfile-backed apps
- deploy static directories
- serve both through Caddy with TLS
- manage secrets
- run release commands for migrations
- support rollback
- support backup/restore
- support destroy/purge

It should expose good product primitives, not raw Docker Compose, raw Podman
flags, or raw Caddy snippets.

## Current Public Config Shape

```toml
name = "api"

[env.production]
server = "deploy@example.com"

[vars]
LOG_LEVEL = "info"
DATABASE_PATH = "/data/app.db"
DATABASE_URL = "@secret:DATABASE_URL"

[deploy]
release = "bun run migrate"

[processes.web]
command = "bun run src/server.ts"
port = 3000
health = "/health"
resources = { memory = "512m", cpus = 0.5 }

[processes.worker]
command = "bun run worker"
resources = { memory = "1g", cpus = 1 }

[routes.app]
host = "api.example.com"
process = "web"

[routes.docs]
host = "api.example.com"
path = "/docs"
serve = "docs-dist"

[routes.old]
host = "old.example.com"
redirect = "https://api.example.com"
```

Route rule: every route must set exactly one of:

- `process = "web"`
- `serve = "dist"`
- `redirect = "https://..."`

Old names like `[services.*]`, `route.service`, `[env.production.env]`,
`healthcheck`, public `tmpfs`, and route `type` were deleted. Do not preserve
or reintroduce backwards compatibility.

## Current Host Layout

```text
/var/apps/<app>.<env>/
  data/             # mounted as /data, included in backup/restore
  runtime/.env      # generated runtime config, not user data
  static/           # static release assets when used
  releases/<sha>/   # release metadata, including manifest snapshots
  simple-vps.toml   # active applied manifest snapshot
  simple-vps.json   # durable env identity anchor
```

Runtime names use derived infra IDs instead of parsing human names:

```text
Host path:      /var/apps/api.production
Infra ID:       svps-a8f9b2
System user:    svps-a8f9b2
Network:        svps-a8f9b2
Container DNS:  svps-a8f9b2-web-f927362
```

Runtime discovery should come from labels and identity files, not reverse
parsing generated names.

## Current Public CLI Surface

The public CLI currently treats the app as the current repo/manifest and asks
for the env as the required positional argument on mutating app commands:

```text
simple-vps init
simple-vps check [env]
simple-vps setup <env>
simple-vps deploy <env> [--dirty] [--rebuild] [--include-dotenv]
simple-vps status <env> [--json]
simple-vps restart <env> [process] [--json]
simple-vps rollback <env> [release] [--json]
simple-vps backup <env> [--to path]
simple-vps backup list <env> [--json]
simple-vps backup rm <env> <backup-id>
simple-vps restore --from <backup-id|path> <env> [--dry-run]
simple-vps destroy <env> [--app app] [--server target] [--confirm app] [--yes] [--purge]
simple-vps logs <env> [process] [--follow|-f] [--tail 100]
simple-vps app list [--server target] [--json]
simple-vps secret set <env> <key>
simple-vps secret list <env> [--json]
simple-vps secret rm <env> <key>
simple-vps ssh <env>
simple-vps host status [--server target] [--json]
simple-vps host doctor [--server target] [--json]
simple-vps host install [flags]
simple-vps version
```

`host install` flags:

```text
--mode auto|local|remote
--host
--bootstrap-user
--ssh-key
--ssh-public-key-file
--operator-ssh-public-key-file
--deploy-ssh-public-key-file
--shared-key
--operator-user
--deploy-user
--timezone
--locale
--ingress public|cloudflare|private
--admin public-ssh|tailscale
--[no-]tailscale
--tailscale-auth-key
--tailscale-hostname
--[no-]cloudflare-tunnel
--cloudflare-api-token
--cloudflare-account-id
--cloudflare-tunnel-token
--cloudflare-tunnel-config
--[no-]docker
--[no-]litestream
--check
--yes
```

Open naming question: should app commands continue to be env-positioned and
manifest-inferred, or should they become app-centric with `--env`?

Current style:

```bash
simple-vps deploy production
simple-vps backup production
simple-vps secret set production DATABASE_URL
```

Possible app-centric style:

```bash
simple-vps deploy api --env production
simple-vps backup create api --env production
simple-vps secret set api DATABASE_URL --env production
```

Why the current style exists:

- Most app commands are intended to run from inside the app repo.
- `name = "api"` already identifies the app in `simple-vps.toml`.
- The thing the user changes most often from that repo is the env:
  `production`, `staging`, `preview`, etc.
- Requiring both app and env everywhere repeats information already in the
  manifest.

Why app-centric may be clearer:

- The host state is actually scoped to `(app, env)`, not env alone.
- Backups, secrets, destroy, and app list feel app-owned as a user concept.
- Commands can run outside the app repo more naturally.
- It makes the operational target explicit: app plus env.

Possible compromise:

```bash
simple-vps deploy production                    # from app repo
simple-vps deploy api --env production --server deploy@example.com
```

Review question: which command model is the right primitive for v1? If both are
supported, is that useful flexibility or unnecessary surface area?

## Plain-English Explanations Of Open Primitive Areas

### 1. Command Contract Consistency

This means: command flags and output should be predictable.

Example rough edge today:

```bash
simple-vps backup production
simple-vps backup --json list production
simple-vps restore --from <backup-id> production
```

`--json` exists for `backup list`, but not for backup creation or restore.
That is not a runtime bug; the commands work. The question is whether this is
an acceptable product contract or a confusing primitive.

Options:

- Keep JSON only where machine-readable output is needed now.
- Add JSON output to backup creation and restore.
- Remove/avoid JSON on mutating backup commands until there is a stronger
  reason.

Review question: should every mutating command that has useful output support
`--json`, or is the current narrower contract better for v1?

### 2. Route Semantics

Routes answer: for this `host` and optional `path`, what handles traffic?

Current contract:

```toml
[routes.app]
host = "example.com"
process = "web"

[routes.docs]
host = "example.com"
path = "/docs"
serve = "docs-dist"
```

`path = "/docs"` means a prefix route:

- `/docs` matches
- `/docs/` matches
- `/docs/getting-started` matches

The alternative would be exact-only matching, where `/docs` matches but
`/docs/getting-started` does not. That would be bad for docs sites and SPAs, so
prefix matching is probably right.

Static route path stripping:

```toml
[routes.docs]
host = "example.com"
path = "/docs"
serve = "docs-dist"
```

Suppose `docs-dist/index.html` exists.

Current intended behavior:

- `https://example.com/docs` serves `docs-dist/index.html`
- `https://example.com/docs/guide.html` serves `docs-dist/guide.html`

That means Caddy strips `/docs` before file lookup. Without stripping,
`/docs/guide.html` would look for `docs-dist/docs/guide.html`, forcing users to
build static assets with an extra nested `docs/` directory. Stripping seems
more natural for route-mounted static directories.

Longest path wins:

```toml
[routes.app]
host = "example.com"
process = "web"

[routes.docs]
host = "example.com"
path = "/docs"
serve = "docs-dist"
```

Both routes could match `/docs`. The more specific one should win:

- `/docs` -> `routes.docs`
- `/docs/page` -> `routes.docs`
- `/api` -> `routes.app`

That is what "longest path wins" means. More specific path first.

Ambiguous overlap:

```toml
[routes.docs]
host = "example.com"
path = "/docs"
serve = "docs-dist"

[routes.docs2]
host = "example.com"
path = "/docs"
process = "web"
```

Two routes own the exact same host/path. Current validation rejects duplicate
host/path pairs. The open question is whether we should also reject tricky
overlaps such as `/docs` and `/docs-v2`, or rely on Caddy matching rules and
tests.

Review questions:

- Is prefix matching the right default?
- Should static `path = "/docs"` strip `/docs` before file lookup?
- Should the root route be expressed only by omitting `path`, with
  `path = "/"` rejected?
- Do we need stronger validation for overlapping paths, or are duplicate
  host/path checks plus longest-path ordering enough?

### 3. TLS Semantics

TLS is HTTPS certificate behavior.

Current route field:

```toml
[routes.app]
host = "api.example.com"
process = "web"
tls = "internal"
```

Current values:

- omitted / `tls = "auto"`: Caddy uses public HTTPS automation, normally
  Let's Encrypt. This is what a real public domain should use.
- `tls = "internal"`: Caddy generates a private/internal certificate. This is
  useful for smoke tests, private DNS, and throwaway hosts. Browsers/curl will
  not trust it by default, so tests use `curl -k`.

Current deliberate non-feature:

- no `tls = "off"` yet. HTTP-only routes are not part of v1.

Constraint:

- all routes on the same host must use the same TLS mode. Mixing
  `api.example.com` with `auto` and `internal` in the same Caddy host block is
  confusing and currently rejected.

Review questions:

- Should `tls = "internal"` be a public v1 primitive, or only a smoke/test
  escape hatch?
- Should `tls = "off"` exist in v1, or is "HTTPS by default" the better
  product line?
- Is per-host TLS consistency the right rule?

### 4. Destroy And Cleanup Semantics

Destroy is a dangerous primitive, so the contract should be explicit.

Current command:

```bash
simple-vps destroy production --confirm <app> [--purge]
```

Desired meaning:

- destroy without `--purge`: remove running app infrastructure for this env,
  especially containers and Caddy routes, while preserving durable state where
  possible.
- destroy with `--purge`: remove env-owned app state too, including data,
  static releases, runtime files, identity, and secrets.
- backups are separate artifacts under `/etc/simple-vps/backups/...`; do not
  delete them implicitly unless the product explicitly chooses that.

Real smoke detail:

- after a `--purge` smoke, app env state was gone, but the backup tar remained;
  we removed the smoke backup manually.

Review questions:

- Should `--purge` delete backups too, or should backups always require
  `backup rm` / manual deletion?
- Should there be a separate `destroy --purge --backups` or `backup rm --all`
  instead?
- Is `--confirm <app>` enough protection, or should env/app be confirmed
  differently?

### 5. Env Removed From Local Manifest

Scenario:

1. User deploys `[env.production]`.
2. Later they delete `[env.production]` from local `simple-vps.toml`.
3. The VPS still has `api.production` running.

Current behavior should be: nothing auto-deletes. `simple-vps app list --server`
can still show remote envs, because remote state is real until destroyed.

Review questions:

- Is "manifest removal does not destroy remote envs" the right contract?
- Should `simple-vps check` warn when local config cannot see a remote env?
- Should there eventually be `simple-vps reconcile` or `simple-vps prune`, or
  is explicit destroy enough for v1?

### 6. Resource Limits

Current config:

```toml
[processes.web]
resources = { memory = "512m", cpus = 0.5 }
```

Intent:

- process-level limits, because web and worker processes often need different
  resources.
- product-level values, not raw Podman flags.

Review questions:

- Are `memory` and `cpus` the right v1 resources?
- Should there be defaults, warnings, or hard validation for tiny VPS boxes?
- Should resource limits be optional and sparse, or encouraged in examples?

## Other Locked Decisions To Challenge

- Public config uses `processes`, not `services`.
- There is one `[routes.*]` namespace.
- `serve = "dist"` is route-level, not a fake static process.
- Mixed container/static releases are supported.
- `[deploy].release` exists for migrations.
- Cron/scheduled jobs are out of v1.
- Litestream is user-managed in v1.
- Multiple Dockerfiles/images are out of v1.
- Mutating commands require explicit env; no default `production`.
- Static route assets are copied into host-side release snapshots and served by
  Caddy, not by the app container.

## What We Need From The Reviewer

Please answer:

1. Are these the right primitives for a first stable version?
2. Which primitives are under-specified or likely to surprise users?
3. Which current choices should be deleted before there are users?
4. Is any primitive drifting too close to Docker Compose, Caddy config, or a
   hosted-PaaS lock-in model?
5. What edge cases should become tests before we start DX work?
6. Should `simple-vps init` wait until after these contracts are frozen?

Bias toward simple, durable contracts. Avoid compatibility concerns.

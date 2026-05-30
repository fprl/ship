# Simple VPS Spec

Source of truth for the Simple VPS product. Implementation details live in
`cmd/`, `internal/`, and `docs/adr/`; this file documents the public contract.

## Product

Simple VPS is one CLI for deploying containerized apps to a single hardened
VPS — built for solo developers and small teams. Audience, scope, and the
design discipline that gates new features live in
[docs/positioning.md](docs/positioning.md).

```text
fresh Ubuntu VPS  ->  install.sh           ->  hardened box
your app repo     ->  simple-vps deploy    ->  live app
```

Two responsibilities, one CLI:

- **Host operations** prepare and maintain the VPS. Rare. Mostly done once
  per box.
- **App operations** deploy, observe, and manage apps on a prepared VPS.
  Frequent. The 90% case.

The DX is flyctl-shaped: the manifest is the source of truth, one
declarative `deploy` verb reconciles state, no daemon between commands,
no opaque magic.

## Out of scope today

Things simple-vps doesn't ship in the CLI today. Any of these could
become right for the product later; if so, they'd warrant their own
design, not feature creep into the current shape. See
[docs/positioning.md](docs/positioning.md) for the full reasoning.

- Not Kubernetes-shaped.
- Not multi-host fleet management.
- No first-class Postgres, Redis, or object-storage provisioning. Use
  external/managed services, or operate those containers outside the v1
  simple-vps app primitive and pass connection URLs as secrets.
- No multi-provider abstraction.
- No git-push deploy.
- No dashboard UI shipped by us. The state-in-files + JSON-CLI surface
  is composable so someone (community, future product, you later) can
  build one on top.
- No built-in recurring scheduler. Framework schedulers (Sidekiq,
  Oban, BullMQ, Laravel scheduler) and host cron / systemd timers
  cover that surface today.
- No multiple Dockerfiles/images per app. One app config builds one image;
  multiple processes can run from that image.
- No first-class database provisioning or Litestream orchestration. SQLite
  and uploads belong under `/data`; external Postgres/Redis-style services
  are referenced through secrets. Host install does not install Litestream unless
  `--litestream` is explicitly passed.
- No sanctioned plugin system. Extension happens through the
  composable primitive.

## Public CLI

The user-facing surface. `simple-vps --help` lists exactly these verbs.
The CLI is repo-centric: app commands infer the app from `simple-vps.toml`.
Env-scoped app commands require `--env` / `-e`; positional env aliases are
not supported. App commands that read a manifest accept
`--config path/to/simple-vps.toml`. Relative paths inside the manifest resolve
relative to that manifest's directory.

### App lifecycle — shipping today

```bash
simple-vps init [--config <path>] [--template container|static|php|hono] [--name <app>] [--env <env>] [--server <ssh-target>] [--host <host>] [--tls auto|internal] [--port <port>] # scaffold simple-vps.toml plus starter files
simple-vps check [--env <env>]                        # validate manifest; with --env also checks local deploy blockers
simple-vps setup --env <env>                          # create per-env user, paths, Podman network
simple-vps deploy --env <env> [--dirty] [--rebuild] [--include-dotenv] # build image or publish static assets, route via Caddy
simple-vps status --env <env> [--json]                # runtime process table
simple-vps app list --server <ssh-target> [--json]    # env identity + podman labels app/env list
simple-vps restart [process] --env <env>              # bounce running processes in place (same image)
simple-vps rollback [release] --env <env>             # run an older image or static release
simple-vps backup create --env <env> [--to <path>] [--json] # local tar backup of data/static assets, manifest, secrets, release metadata
simple-vps backup list --env <env> [--json]           # list local backups
simple-vps backup rm <backup-id> --env <env>          # remove one local backup
simple-vps restore --from=<backup-id|path> --env <env> [--dry-run] # restore local backup and run saved image
simple-vps destroy --env <env> --confirm <app> [--purge] # tear down one environment; --yes for automation
simple-vps destroy --env <env> --app <app> --server <ssh-target> --confirm <app> [--purge] # destroy env removed from local TOML
simple-vps logs [process] --env <env> [--follow] [--tail N] # podman logs against the labelled container
simple-vps secret set <KEY> --env <env>               # stdin-only write to /etc/simple-vps/secrets/<app>/<env>/<key>
simple-vps secret list --env <env> [--json]           # keys only, never values
simple-vps secret rm <KEY> --env <env>                # remove one key
simple-vps ssh --env <env>                            # SSH into the host
```

`restart` uses `podman restart` — container config is preserved (same
image, env, mounts, labels). To pick up manifest changes use
`deploy`. Whole-env restart is rolling, one process at a time, and
fails fast if a container doesn't come back to `running`.

`destroy` removes the running containers, per-env Caddy fragment,
per-env directory, Linux user/group, and Podman network. Secrets are
kept by default; pass `--purge` to remove
`/etc/simple-vps/secrets/<app>/<env>` too. To prevent accidental
teardown, the client requires either `--confirm <app>` or `--yes`.
If the env was already removed from `simple-vps.toml`, pass both
`--app` and `--server` so destroy can target the remote env without
reintroducing dead local config.

`secret set` reads stdin only, never argv. `secret list` prints names
only, never values. Writes are atomic via the privileged server API
and whole-value `@secret:KEY` references resolve on the host during
deploy. No auto-restart on secret change — re-deploy or restart
explicitly.

All mutating helper operations for the same `(app, env)` are serialized
by a host-side file lock. `setup`, `deploy`, `restart`, `destroy`, and
secret writes/removals cannot interleave against the same environment.
Different environments can proceed independently.

Deploy artifacts reject `.env`-style files by default. `--include-dotenv`
exists for deliberate checked-in dotenv artifacts; real secrets should use
`[vars]` plus whole-value `@secret:KEY` references.

For container apps, non-secret runtime values live in `[vars]`, with
env-specific overrides in `[env.<env>.vars]`. Secret values are referenced
by whole-value `@secret:KEY` references and resolved on the host before
deploy execution. Static-only apps reject `[vars]` because there is no
runtime env to inject.

Backup and restore are paired primitives. The bar is: fresh VPS, host
bootstrap, one `restore`, app running again when the saved image is still
available locally. The shipped destination driver is local filesystem
(`file://` or a plain host path); S3/restic destination drivers and encrypted
portable secret bundles remain future scope. See ADR-0007.
ADR-0009 locks the current CLI grammar and primitive contract.

### Host operations — shipping today

```bash
simple-vps host status --server <ssh-target> [--json]
simple-vps host doctor --server <ssh-target> [--json]
simple-vps host install [install options]
simple-vps host install --ingress public|cloudflare|private
simple-vps host install --admin public-ssh|tailscale
```

`status` and `doctor` report on host readiness through SSH. `host install`
runs the bounded Go host provisioner from the Go binary. The public
`install.sh` entrypoint is a tiny bootstrap for the one-line install path.

`host install` accepts individual provider flags today:

```bash
simple-vps host install --cloudflare-tunnel --cloudflare-tunnel-token=...
simple-vps host install --tailscale --tailscale-auth-key=...
```

See [docs/security-model.md](docs/security-model.md) for the supported modes.

The ingress preset model (`--ingress`) and the admin-access mode
(`--admin`) are the durable contract from [docs/security-model.md](docs/security-model.md);
they map to the individual flags above.

### Diagnostics

There is no client-side `route` verb. The helper-side `route list`
reader pointed at a registry the new deploy flow does not populate
and was removed together with `apps.json` / `routes.json`. Host app
discovery is now `simple-vps app list`, sourced from env identity files
and Podman labels.

## Internal CLI (server-side)

The Go `simple-vps` binary serves both the public app-deploy CLI and the
privileged server-side API installed at `/usr/local/bin/simple-vps`.
Public verbs run on a laptop or CI runner. Internal verbs run on the host
through SSH via `sudo` and are not user-facing product commands.

`simple-vps --help` advertises the public verbs. The internal API remains
documented here because it is the contract between the deploy client,
installer, and host helper.

Shipping today:

```bash
sudo -n /usr/local/bin/simple-vps server status [--json]
sudo -n /usr/local/bin/simple-vps server doctor [--json]

sudo -n /usr/local/bin/simple-vps server app setup-env <app> <env>
sudo -n /usr/local/bin/simple-vps server app preflight [--secret <key> ...] [--json] <app> <env>
sudo -n /usr/local/bin/simple-vps server app destroy-env [--purge] <app> <env>
sudo -n /usr/local/bin/simple-vps server app apply --tarball <path> --manifest <path> --sha <release> --base-commit <sha> --created-at <rfc3339> [--dirty] <app> <env>
sudo -n /usr/local/bin/simple-vps server app list [--json]
sudo -n /usr/local/bin/simple-vps server app status [--json] <app> <env>
sudo -n /usr/local/bin/simple-vps server app restart <app> <env> [process]
sudo -n /usr/local/bin/simple-vps server app rollback <app> <env> [release]
sudo -n /usr/local/bin/simple-vps server app backup create [--json] [--to=<destination>] <app> <env>
sudo -n /usr/local/bin/simple-vps server app backup list [--json] <app> <env>
sudo -n /usr/local/bin/simple-vps server app backup rm <app> <env> <backup-id>
sudo -n /usr/local/bin/simple-vps server app backup restore --from=<backup-id> [--dry-run] <app> <env>
sudo -n /usr/local/bin/simple-vps server app logs [--follow] [--tail=N] <app> <env> [process]
sudo -n /usr/local/bin/simple-vps server app secret set <app> <env> <key>
sudo -n /usr/local/bin/simple-vps server app secret list [--json] <app> <env>
sudo -n /usr/local/bin/simple-vps server app secret rm <app> <env> <key>

```

Cloudflare server commands exist for host provisioning internals. They are not
inside the deploy user's sudoers contract.

The sudoers contract is one line for app lifecycle commands plus read-only
host status/doctor commands, installed at `/etc/sudoers.d/simple-vps`. The
grant belongs to the deploy user:

```text
/etc/sudoers.d/simple-vps
  deploy ALL=(root) NOPASSWD: /usr/local/bin/simple-vps server app *, /usr/local/bin/simple-vps server status, /usr/local/bin/simple-vps server status *, /usr/local/bin/simple-vps server doctor, /usr/local/bin/simple-vps server doctor *
```

## Manifest

The manifest is `simple-vps.toml` at the app repo root.

Schema, validation rules, env blocks, command rules, secret references, and
backup config are owned by the Go config package and covered by the Go test
suite.

The manifest describes one app env shape. A container app has a `Dockerfile`
at the app repo root. The Dockerfile is the build contract; Node, Bun,
Python, Ruby, Go, and other runtimes belong inside the image, not on the
host. A static-only app can omit the Dockerfile and use route-level
`serve = "dist"` entries. A container app may also declare `serve` routes;
those static directories are snapshotted beside the image as part of the
same release ID. For clean Git deploys, the release ID includes a static-tree
hash suffix when `serve` routes are present, so generated or ignored static
output still participates in deploy diffing.

Processes are long-running containers from the app image. Each process can
override the Dockerfile `CMD` with `command`, expose one internal `port`,
and define an HTTP `health` path. Runtime limits are product-level resource
knobs:

```toml
[processes.web]
command = "bun run src/server.ts"
port = 3000
health = "/health"
resources = { memory = "512m", cpus = 0.5 }
```

Routes live in one namespace. A route has exactly one target:

```toml
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

`path` is a prefix route. Omit it for the host root; `path = "/"` is
invalid because it is the same route expressed two ways. Static
`path = "/docs"` serves `/docs` from the static root index and serves
`/docs/*` with `/docs` stripped before file lookup. Longest path wins
within a host.

`serve` directories must be real directory trees under the app root. Symlinks
inside static assets are rejected so a release cannot point Caddy outside the
snapshotted tree.

Container runtime values are declared once in `[vars]` and overridden per
env with `[env.<env>.vars]`. Whole-value `@secret:KEY` references resolve
from the host secret store during deploy.

```toml
[vars]
LOG_LEVEL = "info"
DATABASE_PATH = "/data/app.db"
DATABASE_URL = "@secret:DATABASE_URL"

[env.staging.vars]
LOG_LEVEL = "debug"
```

Deploy-time release commands are declared under `[deploy]`:

```toml
[deploy]
release = "bun run migrate"
```

The release command runs from the built image with resolved vars/secrets and
`/data` mounted before routed web processes move traffic.

Container app data lives under `/data` inside the container. On the host,
the env root is flat and scoped to `(app, env)`:

```text
/var/apps/<app>.<env>/
  data/             # mounted as /data, included in backup/restore
  runtime/.env      # generated runtime config, not user data
  static/           # static release assets when used
  releases/<sha>/   # release metadata, including manifest snapshots
  simple-vps.toml   # applied manifest snapshot
  simple-vps.json   # env identity anchor
```

Every successful deploy stores the manifest that produced that release at
`releases/<release>/simple-vps.toml` and release metadata at
`releases/<release>/release.json`, then updates `simple-vps.toml` to the active
manifest. Dirty deploy IDs are shaped like
`<short-sha>-dirty-<yyyymmdd>t<hhmmss><nanoseconds>z`, optionally followed by
the static tree suffix. The nanosecond timestamp keeps repeated dirty deploys
from the same base commit from reusing image tags or container names. Clean
release IDs are the base commit short SHA, optionally followed by that same
static-tree suffix. Rollback uses the selected release's manifest snapshot for
process ports, routes, static paths, and runtime var references; it does not
infer the old shape from the latest local checkout.

Static route assets are copied to:

```text
/var/apps/<app>.<env>/static/releases/<release>/<route-name>/
```

Caddy fragments point at the active release path, not at the source checkout
or the app container. `static/current` is a bookkeeping symlink for active
static-release discovery; rollback and restore move it together with the
Caddy fragment.

In monorepos, Git dirtiness and clean archives are scoped to the directory that
contains the selected `simple-vps.toml`; sibling apps and repository-root files
do not enter that app's deploy artifact.

Before deploy uploads anything, the client runs a read-only remote preflight:
SSH reachability, deploy-user `rsync`, helper availability, host state validity,
setup-env layout, env identity validity, running ingress Caddy container, full
Caddy config validation, app network, and required secret presence. Setup and
repair stay explicit in `setup` and `host install`.

See [ADR-0008](docs/adr/0008-manifest-v2-env-root-and-runtime-identity.md)
for the manifest v2, env-root, and derived infra ID contract.

## Installation

Bootstrapping a fresh Ubuntu 24.04/26.04 host starts with `install.sh`.
The script finds, downloads, or builds a Go binary, then execs
`simple-vps host install`.

```text
# on a fresh box, ssh'd as root:
VERSION=v0.7.0
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  gh api -H 'Accept: application/vnd.github.raw' \
    "/repos/fprl/simple-vps/contents/install.sh?ref=$VERSION" > install.sh
else
  curl -fsSL "https://raw.githubusercontent.com/fprl/simple-vps/$VERSION/install.sh" \
    -o install.sh
fi
chmod 0755 install.sh
SIMPLE_VPS_VERSION="$VERSION" ./install.sh \
    --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub

# or from a laptop, against a fresh box:
SIMPLE_VPS_VERSION="$VERSION" ./install.sh \
    --host <ip> \
    --ssh-key ~/.ssh/id_ed25519 \
    --operator-ssh-public-key-file ~/.ssh/id_ed25519.pub \
    --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub \
    --yes
```

The default install opens host ports 80 / 443 publicly (the ADR-0002
"public" ingress preset, today reached by omitting any tunnel flag).

`install.sh` supports both remote-from-laptop and local-on-box modes.
The Go command underneath accepts the same flags:

```bash
simple-vps host install --mode remote --host <ip> --bootstrap-user root
```

Cloudflare Tunnel and Tailscale are opt-in, switched on by the `--ingress`
and `--admin` presets from [docs/security-model.md](docs/security-model.md):

```bash
# Cloudflare Tunnel terminates ingress instead of the public 80/443:
simple-vps host install --ingress cloudflare --cloudflare-tunnel-token=...

# Tailscale admin access (operator user reachable over the tailnet):
simple-vps host install --admin tailscale --tailscale-auth-key=...
```

After install, the primary checks are:

```bash
simple-vps host status --server deploy@100.x.y.z
simple-vps host doctor --server deploy@100.x.y.z    # if chasing a problem
```

The expected host security posture is documented in
[docs/security-model.md](docs/security-model.md).

## Boundary With Internal Packages

```text
SPEC.md                          public product contract (this file)
cmd/, internal/                  active Go implementation
docs/adr/                        architecture decisions
```

The active implementation lives in the root Go module. The CLI, host
provisioner, and privileged server API are served by the compiled Go
`simple-vps` binary.

## Implementation Direction

The root Go module owns all product behavior: the public deploy CLI, the host
provisioner, and the privileged host API.

New behavior lands in Go.

## Versioning

Standard SemVer.

```text
0.x      preview line
         contract changes acceptable between minors

1.0.0    much later
         "manifest schema, CLI verbs, server layout are stable enough
         that breaking them would be a real product event"
```

Pre-1.0 minors may include breaking changes. Patch versions are
non-breaking by intent. Tag `1.0.0` once the product has survived real use
for a meaningful window without needing contract changes.

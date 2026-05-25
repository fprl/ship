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
- No managed services tier (Postgres, Redis, object storage) — those
  run as containers like everything else.
- No multi-provider abstraction.
- No git-push deploy.
- No dashboard UI shipped by us. The state-in-files + JSON-CLI surface
  is composable so someone (community, future product, you later) can
  build one on top.
- No built-in recurring scheduler. Framework schedulers (Sidekiq,
  Oban, BullMQ, Laravel scheduler) and host cron / systemd timers
  cover that surface today.
- No sanctioned plugin system. Extension happens through the
  composable primitive.

## Public CLI

The user-facing surface. `simple-vps --help` lists exactly these verbs.

### App lifecycle

```bash
simple-vps init                                       # scaffold simple-vps.toml
simple-vps check [env]                                # validate manifest
simple-vps setup <env>                                # create app on the host
simple-vps deploy <env> [--dirty] [--rebuild]
simple-vps rollback <env> [release]
simple-vps destroy <env> [--yes] [--confirm <name>] [--purge]
simple-vps restart <env> <service>
simple-vps status <env> [--json]
simple-vps logs <env> [service] [--tail] [--json]
simple-vps ssh <env>
```

### Secrets and env

```bash
simple-vps secret put <env> <KEY>
simple-vps secret list <env> [--json]
simple-vps secret rm <env> <KEY>
```

`secret put` reads stdin only, never argv.
`secret list` prints names only, never values.
Writes are atomic via the privileged server API. No auto-restart.

Non-secret env values live in `[env.<env>.env]` blocks in the manifest.
Secret values are referenced by whole-value `@secret:KEY` references and
resolved on the host before deploy execution.

### Backup and restore

```bash
simple-vps backup <env>
simple-vps backup <env> --to=<destination>
simple-vps backup list <env> [--json]
simple-vps backup rm <env> <backup-id>

simple-vps restore <env> --from=<backup-id>
simple-vps restore <env> --from=<backup-id> --dry-run
```

Backup and restore are paired primitives. The bar is: fresh VPS, host
bootstrap, one `restore`, app running again when source access and secret
master key requirements are satisfied. See ADR-0007.

### Host operations

```bash
simple-vps host status [--server <ssh-target>] [--json]
simple-vps host doctor [--server <ssh-target>] [--json]
simple-vps host install [install options]
```

`status` and `doctor` report on host readiness through SSH. `host install`
runs the bounded Go host provisioner from the Go binary. The public
`install.sh` entrypoint is a tiny bootstrap for the one-line install path.

`host install` supports ingress / admin modes:

```bash
simple-vps host install --ingress public
simple-vps host install --ingress cloudflare --cloudflare-tunnel-token=...
simple-vps host install --ingress private --admin tailscale --tailscale-auth-key=...
```

See [docs/security-model.md](docs/security-model.md) for the supported modes.

### Diagnostics

```bash
simple-vps route list [--json] [--server <ssh-target>]
```

Read-only view of the route table.

## Internal CLI (server-side)

The Go `simple-vps` binary serves both the public app-deploy CLI and the
privileged server-side API installed at `/usr/local/bin/simple-vps`.
Public verbs run on a laptop or CI runner. Internal verbs run on the host
through SSH via `sudo` and are not user-facing product commands.

`simple-vps --help` advertises the public verbs. The internal API remains
documented here because it is the contract between the deploy client,
installer, and host helper.

```bash
sudo simple-vps server status
sudo simple-vps server doctor

sudo simple-vps server app apply --from-manifest <app> <env>
sudo simple-vps server app destroy <app> <env>
sudo simple-vps server app service <action> <app> <env> <service>
sudo simple-vps server app read-env <app> <env>

sudo simple-vps server route list [--json]
sudo simple-vps server route apply --from-manifest <app> <env>

sudo simple-vps server cloudflare publish --app <name> <host>
sudo simple-vps server cloudflare remove <host>
sudo simple-vps server cloudflare remove --app <name>
sudo simple-vps server cloudflare setup-tunnel --name <name>
sudo simple-vps server generate-caddy
```

The sudoers contract is one line for the whole server binary, installed at
`/etc/sudoers.d/simple-vps`. The grant belongs to the deploy user:

```text
/etc/sudoers.d/simple-vps
  deploy ALL=(root) NOPASSWD: /usr/local/bin/simple-vps
```

## Manifest

The manifest is `simple-vps.toml` at the app repo root.

Schema, validation rules, app shape detection, env blocks, command rules,
secret references, and backup config are owned by the Go config package
and covered by the Go test suite.

The manifest describes one app. App shape is inferred from the repo:

- `Dockerfile` present, no `static = "..."`: container app.
- `static = "..."` present, no `Dockerfile`: static app.
- Both present or both absent: validation error.

Container apps build on the VPS with Podman. The Dockerfile is the runtime
contract; Node, Bun, Python, Ruby, Go, and other runtimes belong inside the
image, not on the host. Static apps upload a pre-built directory and Caddy
serves it directly.

Services are long-running containers from the app image. Service commands
accept string form and array form; array form is recommended for commands
with arguments because it avoids shell quoting.

Migrations are a deploy concern, not a separate verb. v1 users can run
migrations from the image's startup wrapper (`migrate && serve`). A future
ADR may add a `predeploy` hook.

## Installation

Bootstrapping a fresh Ubuntu 24.04 host starts with `install.sh`. The script
finds, downloads, or builds a Go binary, then execs `simple-vps host install`.

```text
# on a fresh box, ssh'd as root:
curl -fsSL https://simple-vps.dev/install.sh | bash \
    --ingress public \
    --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub

# or from a laptop, against a fresh box:
./install.sh --mode remote --host <ip> --bootstrap-user root \
    --ssh-key ~/.ssh/id_ed25519 \
    --ingress public \
    --operator-ssh-public-key-file ~/.ssh/id_ed25519.pub \
    --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub
```

`install.sh` supports both remote-from-laptop and local-on-box modes.
The Go command underneath accepts the same flags:

```bash
simple-vps host install --mode remote --host <ip> --bootstrap-user root
```

Cloudflare Tunnel and Tailscale admin access are supported install modes,
not required product identity:

```bash
simple-vps host install --ingress cloudflare --cloudflare-tunnel-token=...
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

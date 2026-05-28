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

The user-facing surface. `simple-vps --help` lists exactly the verbs
under **Shipping today**. Anything under **Planned** is the durable
contract this product is being built toward, but the binary does not
implement it yet.

### App lifecycle — shipping today

```bash
simple-vps init                                       # scaffold simple-vps.toml + Dockerfile
simple-vps check [env]                                # validate manifest
simple-vps setup <env>                                # create per-env user, paths, Podman network
simple-vps deploy <env> [--dirty]                     # build image on the host, run services, route via Caddy
simple-vps status <env> [--json]                      # podman ps-sourced service table
simple-vps restart <env> [service] [--json]           # bounce running services in place (same image)
simple-vps destroy <env> --confirm <app> [--purge]    # tear down one environment; --yes for automation
simple-vps logs <env> [service] [--follow] [--tail N] # podman logs against the labelled container
simple-vps secret put <env> <KEY>                     # stdin-only write to /etc/simple-vps/secrets/<app>/<env>/<key>
simple-vps secret list <env>                          # keys only, never values
simple-vps secret rm <env> <KEY>                      # remove one key
simple-vps ssh <env>                                  # SSH into the host
```

`restart` uses `podman restart` — container config is preserved (same
image, env, mounts, labels). To pick up manifest changes use
`deploy`. Whole-env restart is rolling, one service at a time, and
fails fast if a container doesn't come back to `running`.

`destroy` removes the running containers, per-env Caddy fragment,
per-env directory, Linux user/group, and Podman network. Secrets are
kept by default; pass `--purge` to remove
`/etc/simple-vps/secrets/<app>/<env>` too. To prevent accidental
teardown, the client requires either `--confirm <app>` or `--yes`.

`secret put` reads stdin only, never argv. `secret list` prints names
only, never values. Writes are atomic via the privileged server API
and whole-value `@secret:KEY` references resolve on the host during
deploy. No auto-restart on secret change — re-deploy or restart
explicitly.

### App lifecycle — planned (post-cutover backlog)

These are part of the durable contract above. They were removed in the
ADR-0005 cutover and will come back wired to the container/Podman flow:

```bash
simple-vps deploy <env> --rebuild                     # --no-cache --pull=always (planned)
simple-vps rollback <env> [release]                   # planned
```

Non-secret env values live in `[env.<env>.env]` blocks in the manifest
today. Secret values are referenced by whole-value `@secret:KEY`
references and resolved on the host before deploy execution.

### Backup and restore — planned

```bash
simple-vps backup <env>                               # planned
simple-vps backup <env> --to=<destination>            # planned
simple-vps backup list <env> [--json]                 # planned
simple-vps backup rm <env> <backup-id>                # planned

simple-vps restore <env> --from=<backup-id>           # planned
simple-vps restore <env> --from=<backup-id> --dry-run # planned
```

Backup and restore are paired primitives. The bar is: fresh VPS, host
bootstrap, one `restore`, app running again when source access and secret
master key requirements are satisfied. See ADR-0007.

### Host operations — shipping today

```bash
simple-vps host status [--server <ssh-target>]
simple-vps host doctor [--server <ssh-target>]
simple-vps host install [install options]
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

### Host operations — planned

```bash
simple-vps host status --json                         # planned
simple-vps host doctor --json                         # planned
simple-vps host install --ingress public|cloudflare|private   # planned (ADR-0002 ingress preset)
simple-vps host install --admin public-ssh|tailscale          # planned
```

The ingress preset model (`--ingress`) and the admin-access mode
(`--admin`) are the durable contract from [docs/security-model.md](docs/security-model.md);
they map to the individual flags above and ship together with the
`--json` rollout.

### Diagnostics

There is no client-side `route` verb. Route inspection on the host is
also pending: the helper-side `route list` reader pointed at a
registry the new deploy flow does not populate and was removed
together with the legacy `apps.json` / `routes.json` state files. A
podman-labels-sourced replacement is planned as `app list`.

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
sudo simple-vps server status
sudo simple-vps server doctor

sudo simple-vps server app setup-env <app> <env>
sudo simple-vps server app destroy-env [--purge] <app> <env>
sudo simple-vps server app apply --tarball <path> --manifest <path> --sha <sha> <app> <env>
sudo simple-vps server app status [--json] <app> <env>
sudo simple-vps server app restart [--json] <app> <env> [service]
sudo simple-vps server app logs [--follow] [--tail=N] <app> <env> [service]
sudo simple-vps server app secret put <app> <env> <key>
sudo simple-vps server app secret list <app> <env>
sudo simple-vps server app secret rm <app> <env> <key>

sudo simple-vps server cloudflare publish --app <name> <host>
sudo simple-vps server cloudflare remove <host>
sudo simple-vps server cloudflare remove --app <name>
sudo simple-vps server cloudflare setup-tunnel --name <name>
```

Planned (paired with the client-side "planned" verbs above):

```bash
sudo simple-vps server app list [--json]                            # planned; sourced from podman labels / Caddy fragments
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
    --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub

# or from a laptop, against a fresh box:
./install.sh --mode remote --host <ip> --bootstrap-user root \
    --ssh-key ~/.ssh/id_ed25519 \
    --operator-ssh-public-key-file ~/.ssh/id_ed25519.pub \
    --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub
```

The default install opens host ports 80 / 443 publicly (the ADR-0002
"public" ingress preset, today reached by omitting any tunnel flag).

`install.sh` supports both remote-from-laptop and local-on-box modes.
The Go command underneath accepts the same flags:

```bash
simple-vps host install --mode remote --host <ip> --bootstrap-user root
```

Cloudflare Tunnel and Tailscale are opt-in, switched on by their own
negatable flags. The `--ingress` and `--admin` preset model from
[docs/security-model.md](docs/security-model.md) is the durable
contract and the planned shape; today you reach the same outcomes
through the individual flags:

```bash
# Cloudflare Tunnel terminates ingress instead of the public 80/443:
simple-vps host install --cloudflare-tunnel --cloudflare-tunnel-token=...

# Tailscale admin access (operator user reachable over the tailnet):
simple-vps host install --tailscale --tailscale-auth-key=...
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

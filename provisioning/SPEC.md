# Simple VPS Spec

This is the source of truth for the project. Keep this file updated before
spreading details into code comments, README snippets, or release notes.

## Goal

Simple VPS should turn a fresh Ubuntu VPS into a secure production app host:

```text
fresh Ubuntu VPS -> run one script -> secure production box ready for apps
```

The tool is opinionated. It should optimize for shipping small production apps
quickly and safely, not for becoming a generic server framework.

## Non-Goals

- No Kubernetes.
- No plugin marketplace.
- No multi-provider abstraction.
- No dashboard UI.
- No generic profile system until the base flow is boring.
- No Brewfile-based production server setup.

## Target Production Architecture

Public traffic:

```text
Browser
  -> Cloudflare
  -> Cloudflare Tunnel
  -> local ingress on 127.0.0.1
  -> app runtime on 127.0.0.1
```

Admin traffic:

```text
Laptop
  -> Tailscale
  -> VPS SSH
```

The VPS should not expose public `22`, `80`, `443`, or random app ports.

Simple VPS intentionally chooses Tailscale and Cloudflare Tunnel. Other mesh
VPNs, tunnel providers, and public-Caddy ingress are out of scope for v1.

Default local ingress is Caddy on `127.0.0.1:8080`. Container-based deploy
tools are outside the v1 product direction, but Simple VPS should still prepare
the box and keep public ports closed.

Simple VPS owns server readiness. App repositories own app deployment,
environment variables, migrations, app-specific users, processes, and database
backup configuration.

## Default Install

The default install should create:

- Operator user with key-based access
- Deploy user with key-based access and narrow helper sudo
- SSH hardened and reachable only through Tailscale after bootstrap
- UFW deny-all inbound by default
- Optional Hetzner firewall automation, also deny-all inbound
- Tailscale for private admin access
- Cloudflare Tunnel for public web ingress
- Caddy listening locally, not on the public interface
- Node.js LTS
- Bun
- pnpm
- Litestream binary for SQLite backup workflows
- `/usr/local/bin/simple-vps`
- `/etc/simple-vps/state.json`
- Basic server packages: `git`, `curl`, `jq`, `htop`, `tmux`, `rsync`, `unzip`, `ncdu`
- `/usr/local/bin/simple-vps` for server-local management

## Optional Installs

Personal comfort tools should not be able to break the production baseline.

Docker is optional and off by default:

```bash
./install.sh --docker
```

Simple VPS does not require apps to use Docker.

Dev tools:

```bash
simple-vps devtools install
```

This can install:

- Zsh + Oh My Zsh
- Powerlevel10k
- Zsh autosuggestions/highlighting
- `fzf`, `zoxide`, `atuin`, `lsd`, `bat`
- uv, Go, Rust
- AI CLIs: Codex, Claude, Gemini, OpenCode

Litestream is installed by default because SQLite backup/restore is part of the
intended production story. Simple VPS installs the binary, but app repositories
own the database path, replica credentials, and systemd service configuration.

## CLI Shape

Keep the CLI tiny:

```bash
sudo simple-vps server status
sudo simple-vps server route list
sudo simple-vps server route list --json
sudo simple-vps server route proxy example.com --port 3000
sudo simple-vps server route static data.example.com --root /var/apps/data/current/public
sudo simple-vps server route redirect old.example.com --to https://new.example.com
sudo simple-vps server route remove example.com
sudo simple-vps server route remove --app my-app
sudo simple-vps server app create my-app
sudo simple-vps server app destroy my-app
sudo simple-vps server app read-env my-app
sudo simple-vps server app install-env my-app /tmp/simple-vps-deploy/.env
sudo simple-vps server app install-unit my-app web /tmp/simple-vps-deploy/simple-my-app-web.service
sudo simple-vps server app uninstall-unit my-app web
sudo simple-vps server app daemon-reload
sudo simple-vps server app service start my-app web
sudo simple-vps server app run-as my-app --cwd /var/apps/my-app/releases/a1b2c3d -- bun install --production --frozen-lockfile
```

`route proxy` means expose a local service through the production ingress stack.

Examples:

```bash
sudo simple-vps server route proxy example.com --port 3000
sudo simple-vps server route proxy api.example.com --port 8080
sudo simple-vps server route static data.example.com --root /var/apps/data-feed/current/public
```

## Routing State

Simple VPS should maintain one source of truth on the server:

```text
/etc/simple-vps/state.json
```

Generated files:

```text
/etc/caddy/Caddyfile
/etc/caddy/simple-vps/routes.caddy
/etc/cloudflared/config.yml
```

Rules:

- Existing matching route: no-op
- Existing conflicting route: fail unless `--force`
- Existing Caddy host: fail unless `--force`
- Validate Caddy before reload
- Validate Cloudflare/cloudflared config before reload where possible
- Keep backups before changing generated files
- Preserve user-owned Caddy snippets under `/etc/caddy/conf.d`
- Detect manual edits in Simple VPS generated Caddy files and fail unless
  `--force`

## Simple VPS CLI Server API

The Go `simple-vps` CLI drives app deploys from a developer laptop or CI
runner over SSH. It needs narrow root privileges on the server to create
per-app system users, install systemd units, manage `simple-*.service`
units, and publish routes. Simple VPS exposes that capability as
subcommands of the same `simple-vps` binary — there is no separate helper.

```text
SPEC.md              -> "these are the simple-vps subcommands we invoke"
provisioning/SPEC.md -> "this is the API we expose, with validation"
```

### Sudoers and Host Identities

The intended model has three identities:

```text
bootstrap user   root or provider-created initial user
operator user    human/admin identity for host convergence and recovery
deploy user      CLI/CI identity for app deploys
```

The operator identity needs a passwordless root path because remote install
phase 2 connects as `operator` and runs Ansible with `become: true`.

```text
/etc/sudoers.d/simple-vps
  deploy ALL=(root) NOPASSWD: /usr/local/bin/simple-vps
```

`simple-vps` is the gatekeeper. Every privileged subcommand validates its
arguments (app/service name shape, unit file path and ownership, host, port,
etc.) before performing the action. There are no glob-matched commands in
sudoers; validation lives in code where it can be meaningful.

This is a load-bearing maintenance rule: because sudoers grants
`/usr/local/bin/simple-vps`, every mutating `simple-vps` subcommand must
validate inputs and be designed as a safe root API. New subcommands must not
assume interactive trust just because the caller is `deploy`. Adding a
subcommand that shells out to user-supplied arguments without validation
silently broadens the deploy sudoers surface.

### App Subcommands

The `simple-vps server app ...` namespace covers per-app lifecycle operations:

```bash
sudo simple-vps server app create <name>
sudo simple-vps server app destroy <name>
sudo simple-vps server app read-env <name>
sudo simple-vps server app install-env <name> <path-to-env-file>
sudo simple-vps server app install-unit <name> <service> <path-to-unit-file>
sudo simple-vps server app uninstall-unit <name> <service>
sudo simple-vps server app daemon-reload
sudo simple-vps server app service <action> <name> <service>
  # action: start | stop | restart | status | is-active | enable | disable
sudo simple-vps server app run-as <name> --cwd <path> -- <command> [args...]
  # used for: bun install --production, npm ci --omit=dev, etc.
```

`app destroy` is the destructive purge primitive: it removes
`/var/apps/<name>` and the `app-<name>` system user. The data-preserving
`simple-vps destroy` path does not call it.

`app install-env` is the only writer for `/var/apps/<name>/shared/.env`.
It requires the source file to live under `/tmp/simple-vps-deploy/`, validates
EnvironmentFile syntax, writes atomically, and sets `0600 app-<name>:app-<name>`.
`app read-env` prints the current file for `simple-vps secret list|put|rm`.

`/tmp/simple-vps-deploy` is an internal staging path used for env and unit uploads.

### Route Subcommands

Routes use the `simple-vps server route` namespace:

```bash
sudo simple-vps server route proxy <host> --port <port> --app <name>
sudo simple-vps server route static <host> --root <path> --app <name>
sudo simple-vps server route redirect <host> --to <url> --app <name>
sudo simple-vps server route remove --app <name>
```

### Validation Rules

Enforced by `simple-vps` before any privileged action:

- `<name>` matches `^[a-z][a-z0-9-]{1,40}$`.
- `<service>` matches `^[a-z][a-z0-9-]{0,30}$`.
- `<host>` matches a DNS-1123 hostname (no schemes, no paths, no ports).
- `<port>` is an integer in `[1, 65535]`.
- `<path>` for static routes must resolve under `/var/apps/<name>/`.
- `<url>` for redirects must be `http://...` or `https://...`.
- Unit file paths must live under `/tmp/simple-vps-deploy/` and be owned by the
  invoking deploy user.
- Unit file contents must start with `[Unit]` and reference `User=app-<name>`.
  Units that try to escalate are refused.
- `app run-as --cwd <path>` refuses any working directory outside
  `/var/apps/<name>/`.

`app create` adds the invoking sudo user to the app's group and makes
`/var/apps/<name>` plus `/var/apps/<name>/releases` setgid group-writable
(`2775`). That is the upload contract: the client CLI can rsync release
artifacts as the deploy user, while services still run as `app-<name>`.
It also ensures `/tmp/simple-vps-deploy` exists with mode `1777`; unit uploads
land there before `app install-unit` validates ownership and content.

### Failure Mode

If the sudoers entry or the `app` subcommands are missing on the server,
the client CLI fails at `setup` time with a clear pointer to re-run the
Simple VPS install. The CLI never installs server-side capability itself.

## Cloudflare Model

Use one Cloudflare Tunnel per server:

```text
prod-1 tunnel -> apps on prod-1
prod-2 tunnel -> apps on prod-2
staging-1 tunnel -> apps on staging-1
```

Many domains can live on the same server:

```text
example.com        -> prod-1 tunnel -> Caddy -> app on :3000
www.example.com    -> prod-1 tunnel -> Caddy -> app on :3000
anotherapp.com     -> prod-1 tunnel -> Caddy -> app on :3001
api.anotherapp.com -> prod-1 tunnel -> Caddy -> app on :3002
```

One-time Cloudflare setup:

- Domain on Cloudflare DNS
- Cloudflare account secured with passkeys/2FA
- API token for Simple VPS automation
- Optional Cloudflare Zero Trust/Access policies for private dashboards

Per-server Cloudflare setup should be automated by Simple VPS.

## Installer Model

`install.sh` should be the public bootstrap entrypoint.

Target one-liner:

```bash
curl -fsSL https://simple-vps.dev/install.sh | bash
```

The hosted script should download a pinned release/tarball, find or build the
Go binary, then exec `simple-vps host install` from that extracted checkout.

The current implementation has that bootstrap path. v1 still needs the full
production flow validated on a fresh VPS.

## Ansible Decision

Use Ansible for now.

Why:

- It is already in the repo.
- It gives idempotent reruns.
- It is good at users, packages, services, files, handlers, and system config.
- The existing playbooks already syntax-check.

Boundaries:

- Do not turn this into a generic Ansible framework.
- Do not add roles/profiles unless they remove immediate complexity.
- Keep `install.sh` as the one-line bootstrap entrypoint.

Why not Brewfile:

- Brewfile is good for local developer machines.
- It is not the right tool for Ubuntu production server state.
- It does not own SSH hardening, UFW, users, systemd units, Cloudflare Tunnel
  config, Tailscale service setup, or rollback/validation semantics.

If Ansible starts slowing down the one-script path, port the narrow role logic
to the Go installer path later. Do not restart from scratch just to change the
engine.

## Language Choice

The privileged helper target is the compiled Go `simple-vps` binary.

The `/usr/local/bin/simple-vps` CLI is granted passwordless sudo by
`/etc/sudoers.d/simple-vps`, so it lives at the privilege boundary. Go is a
better fit for that boundary than TS/Bun or Python here: one static binary,
no npm/PyPI runtime, and shared validation code between the public client and
the server API.

Ansible installs the compiled Go binary supplied by the installer. There is no
Python helper fallback.

## Current Implementation

Current default apply path installs:

- System essentials
- Operator and deploy users
- Security baseline
- Tailscale package and `tailscaled`
- `cloudflared` package for Cloudflare Tunnel ingress
- Caddy local-only
- `/etc/caddy/simple-vps` for generated Caddy snippets
- `/etc/caddy/conf.d` for user-owned Caddy snippets
- Node.js LTS
- Bun
- pnpm
- Litestream

Current sudo behavior:

- `operator` is the default operator user.
- `deploy` is the default deploy user.
- `operator ALL=(ALL) NOPASSWD:ALL` is used by Ansible phase 2 convergence via
  `/etc/sudoers.d/operator`.
- `deploy ALL=(root) NOPASSWD: /usr/local/bin/simple-vps` is the narrow deploy
  API used by app operations via `/etc/sudoers.d/simple-vps`.

Current optional variables:

- `simple_vps_install_docker=true` or `--docker` to install Docker
- `simple_vps_install_litestream=false` or `--no-litestream` to disable
  Litestream binary installation
- `simple_vps_install_devtools=true`
- `security_enable_tailscale=false` or `--no-tailscale` to disable Tailscale
- `simple_vps_enable_cloudflare_tunnel=false` or `--no-cloudflare-tunnel`
  to disable Cloudflare Tunnel setup

Current Tailscale behavior:

- Tailscale is enabled by default.
- `SIMPLE_VPS_TAILSCALE_AUTH_KEY` or `--tailscale-auth-key` enables
  non-interactive `tailscale up`.
- Public SSH remains allowed until the server has a Tailscale IP.
- Once Tailscale is authenticated, UFW allows SSH on `tailscale0` and removes
  the public SSH allow rule.

Current Cloudflare Tunnel behavior:

- `cloudflared` is installed by default from Cloudflare's apt repository.
- `SIMPLE_VPS_CLOUDFLARE_TUNNEL_TOKEN` or `--cloudflare-tunnel-token` enables
  a remotely-managed tunnel service using `/etc/cloudflared/tunnel-token`.
- `SIMPLE_VPS_CLOUDFLARE_TUNNEL_CONFIG` or `--cloudflare-tunnel-config`
  enables a service using an existing local `cloudflared` config path.
- With tunnel token or config-path setup, users create Cloudflare public
  hostnames manually and point them at `http://127.0.0.1:8080`. This is the
  default recommended trust boundary: Simple VPS owns the box, Cloudflare owns
  Cloudflare.
- `SIMPLE_VPS_CLOUDFLARE_API_TOKEN` or `--cloudflare-api-token` is an advanced
  opt-in. It creates or reuses a remotely-managed tunnel, stores the API token
  on the server at `/etc/simple-vps/cloudflare-api-token`, stores tunnel state
  at `/etc/simple-vps/cloudflare.json`, writes `/etc/cloudflared/tunnel-token`,
  and enables the `cloudflared` service.
- If the API token can access multiple Cloudflare accounts,
  `SIMPLE_VPS_CLOUDFLARE_ACCOUNT_ID` or `--cloudflare-account-id` selects the
  account.
- If no API token, tunnel token, or config path is provided, `cloudflared` is
  installed but the service is not enabled.
- Without API-managed mode, `simple-vps server cloudflare publish HOST --app APP`
  prints the manual Cloudflare public-hostname settings and leaves Cloudflare
  unchanged.
- With API-managed mode, `simple-vps server cloudflare publish HOST --app APP`
  ensures the tunnel public hostname routes to `http://127.0.0.1:8080` and a
  CNAME points to `<tunnel-id>.cfargotunnel.com`.
- With API-managed mode, `simple-vps server cloudflare remove --app APP` removes
  Cloudflare hostnames and CNAME records tracked for that app. Without
  API-managed mode it is a no-op.

Current CLI behavior:

- `simple-vps server status` prints state path, route count, service status, and
  installed tool status for runtime primitives.
- `simple-vps server route list` lists routes from state.
- `simple-vps server route list --json` emits route state as JSON.
- `simple-vps server route proxy HOST --port PORT` writes a proxy route and
  regenerates managed Caddy files.
- `simple-vps server route static HOST --root PATH` writes a static file route
  and regenerates managed Caddy files.
- `simple-vps server route redirect HOST --to URL` writes a redirect route and
  regenerates managed Caddy files.
- `simple-vps server route remove HOST` removes a route by host and regenerates
  managed Caddy files.
- `simple-vps server route remove --app APP` removes all routes for an app and
  regenerates managed Caddy files.
- `simple-vps server generate-caddy` regenerates managed Caddy files from state.
- Mutating commands require root, validate the generated Caddyfile, keep
  backups under `/etc/simple-vps/backups`, and reload Caddy.

Known gaps:

- Hosted installer needs fresh-VPS validation.
- Public SSH is still needed during bootstrap unless Tailscale auth succeeds.
- Hosted installer smoke coverage should keep expanding as fresh-host cases
  surface edge conditions.

## Implementation Plan

1. Keep the README short and this spec authoritative.
2. Make the hosted one-liner real and tested.
3. Make Tailscale part of the secure baseline.
4. Add Cloudflare Tunnel install and service setup.
5. Add `/usr/local/bin/simple-vps`.
6. Keep Cloudflare API-managed route coverage opt-in and expand it only as real
   hosts expose edge cases.
7. Add fresh Ubuntu 24.04 smoke testing and idempotency testing.
8. Only then tag v1.

## Validation

Local checks:

```bash
go test ./...
go build ./...
make build-release
bash -n install.sh
bash -n provisioning/install.sh
provisioning/tests/install_plan_test.sh
provisioning/tests/bootstrap_tarball_smoke.sh
ANSIBLE_CONFIG=provisioning/ansible.cfg ansible-playbook --syntax-check -i provisioning/inventory/hosts.ini provisioning/playbooks/vps-bootstrap.yml
ANSIBLE_CONFIG=provisioning/ansible.cfg ansible-playbook --syntax-check -i provisioning/inventory/hosts.ini provisioning/playbooks/vps-apply.yml
```

CI should also run:

```bash
shellcheck install.sh
ansible-lint playbooks/vps-bootstrap.yml playbooks/vps-apply.yml
```

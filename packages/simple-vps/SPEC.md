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

Default local ingress is Caddy on `127.0.0.1:8080`. Container-based deploy
tools are outside the v1 product direction, but Simple VPS should still prepare
the box and keep public ports closed.

Simple VPS owns server readiness. App repositories own app deployment,
environment variables, migrations, app-specific users, processes, and database
backup configuration.

## Default Install

The default install should create:

- Admin user with key-based access
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
simple-vps status
simple-vps route list
simple-vps route list --json
simple-vps route proxy example.com --port 3000
simple-vps route static data.example.com --root /var/apps/data/current/public
simple-vps route redirect old.example.com --to https://new.example.com
simple-vps route remove example.com
simple-vps route remove --app my-app
simple-vps app create my-app
simple-vps app destroy my-app
simple-vps app install-unit my-app web /tmp/simple-deploy/simple-my-app-web.service
simple-vps app uninstall-unit my-app web
simple-vps app daemon-reload
simple-vps app service start my-app web
simple-vps app run-as my-app --cwd /var/apps/my-app/releases/a1b2c3d -- bun install --production --frozen-lockfile
simple-vps devtools install
```

`route proxy` means expose a local service through the production ingress stack.

Examples:

```bash
simple-vps route proxy example.com --port 3000
simple-vps route proxy api.example.com --port 8080
simple-vps route static data.example.com --root /var/apps/data-feed/current/public
```

`publish`, `unpublish`, and `routes` remain compatibility aliases for simple
proxy routes.

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

## Simple Deploy Server API

Simple Deploy is the sibling package that drives app deploys from a developer
laptop or CI runner over SSH. It needs narrow root privileges on the server
to create per-app system users, install systemd units, manage
`simple-*.service` units, and publish routes. Simple VPS exposes that
capability as subcommands of the existing `simple-vps` binary — there is no
separate helper.

```text
simple-deploy/SPEC.md -> "these are the simple-vps subcommands we invoke"
simple-vps/SPEC.md    -> "this is the API we expose, with validation"
```

### Sudoers

One sudoers line, granting passwordless `sudo` for `simple-vps`:

```text
/etc/sudoers.d/simple-deploy
  admin ALL=(root) NOPASSWD: /usr/local/bin/simple-vps
```

`simple-vps` is the gatekeeper. Every privileged subcommand validates its
arguments (app/service name shape, unit file path and ownership, host, port,
etc.) before performing the action. There are no glob-matched commands in
sudoers; validation lives in code where it can be meaningful.

This is a load-bearing maintenance rule: because sudoers grants
`/usr/local/bin/simple-vps`, every mutating `simple-vps` subcommand must
validate inputs and be designed as a safe root API. New subcommands must not
assume interactive trust just because the caller is `admin`. Adding a
subcommand that shells out to user-supplied arguments without validation
silently broadens the deploy sudoers surface.

### App Subcommands

The `simple-vps app ...` namespace covers per-app lifecycle operations:

```bash
sudo simple-vps app create <name>
sudo simple-vps app destroy <name>
sudo simple-vps app install-unit <name> <service> <path-to-unit-file>
sudo simple-vps app uninstall-unit <name> <service>
sudo simple-vps app daemon-reload
sudo simple-vps app service <action> <name> <service>
  # action: start | stop | restart | status | is-active | enable | disable
sudo simple-vps app run-as <name> --cwd <path> -- <command> [args...]
  # used for: bun install --production, npm ci --omit=dev, etc.
```

`app destroy` is the destructive purge primitive: it removes
`/var/apps/<name>` and the `app-<name>` system user. The data-preserving
`simple-deploy destroy` path does not call it.

### Route Subcommands

Routes use the existing `simple-vps route` namespace:

```bash
sudo simple-vps route proxy <host> --port <port> --app <name>
sudo simple-vps route static <host> --root <path> --app <name>
sudo simple-vps route redirect <host> --to <url> --app <name>
sudo simple-vps route remove --app <name>
```

### Validation Rules

Enforced by `simple-vps` before any privileged action:

- `<name>` matches `^[a-z][a-z0-9-]{1,40}$`.
- `<service>` matches `^[a-z][a-z0-9-]{0,30}$`.
- `<host>` matches a DNS-1123 hostname (no schemes, no paths, no ports).
- `<port>` is an integer in `[1, 65535]`.
- `<path>` for static routes must resolve under `/var/apps/<name>/`.
- `<url>` for redirects must be `http://...` or `https://...`.
- Unit file paths must live under `/tmp/simple-deploy/` and be owned by the
  invoking admin user.
- Unit file contents must start with `[Unit]` and reference `User=app-<name>`.
  Units that try to escalate are refused.
- `app run-as --cwd <path>` refuses any working directory outside
  `/var/apps/<name>/`.

`app create` adds the invoking sudo user to the app's group and makes
`/var/apps/<name>` plus `/var/apps/<name>/releases` setgid group-writable
(`2775`). That is the upload contract: `simple-deploy` can rsync release
artifacts as the deploy user, while services still run as `app-<name>`.
It also ensures `/tmp/simple-deploy` exists with mode `1777`; unit uploads
land there before `app install-unit` validates ownership and content.

### Failure Mode

If the sudoers entry or the `app` subcommands are missing on the server,
Simple Deploy fails at `setup` time with a clear pointer to re-run the
Simple VPS install. Simple Deploy never installs server-side capability
itself.

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

`install.sh` should be the public entrypoint.

Target one-liner:

```bash
curl -fsSL https://simple-vps.dev/install.sh | bash
```

The hosted script should download a pinned release/tarball, then run the real
installer from that extracted checkout.

Current implementation already has a bootstrap-download path, but v1 still needs
the full production flow validated on a fresh VPS.

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
- Keep `install.sh` as the only user-facing install entrypoint.

Why not Brewfile:

- Brewfile is good for local developer machines.
- It is not the right tool for Ubuntu production server state.
- It does not own SSH hardening, UFW, users, systemd units, Cloudflare Tunnel
  config, Tailscale service setup, or rollback/validation semantics.

If Ansible starts slowing down the one-script path, port the narrow role logic to
a small Bash/Python installer later. Do not restart from scratch just to change
the engine.

## Language Choice

Simple VPS is Python, stdlib only.

The `/usr/local/bin/simple-vps` CLI is granted passwordless sudo by
`/etc/sudoers.d/simple-deploy`, so it lives at the privilege boundary. Python
stdlib covers the root API this tool needs (`argparse`, `json`, `pathlib`, `re`,
`shutil`, `subprocess`) without npm, PyPI, or transitive dependencies to audit.

Adding a non-stdlib dependency is the trigger to revisit this decision. Until
then, contributor preference is not enough reason to move the root-owned server
API to TS/Bun.

## Current Implementation

Current default apply path installs:

- System essentials
- Admin user
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
- If neither token nor config path is provided, `cloudflared` is installed but
  the service is not enabled.
- Until Cloudflare API automation lands, configure the tunnel public hostname in
  Cloudflare Zero Trust to route to `http://127.0.0.1:8080`.

Current CLI behavior:

- `simple-vps status` prints state path, route count, service status, and
  installed tool status for runtime primitives.
- `simple-vps route list` lists routes from state.
- `simple-vps route list --json` emits route state as JSON.
- `simple-vps route proxy HOST --port PORT` writes a proxy route and regenerates
  managed Caddy files.
- `simple-vps route static HOST --root PATH` writes a static file route and
  regenerates managed Caddy files.
- `simple-vps route redirect HOST --to URL` writes a redirect route and
  regenerates managed Caddy files.
- `simple-vps route remove HOST` removes a route by host and regenerates managed
  Caddy files.
- `simple-vps route remove --app APP` removes all routes for an app and
  regenerates managed Caddy files.
- `simple-vps publish`, `simple-vps unpublish`, and `simple-vps routes` remain
  compatibility aliases.
- `simple-vps generate-caddy` regenerates managed Caddy files from state.
- Mutating commands require root, validate the generated Caddyfile, keep
  backups under `/etc/simple-vps/backups`, and reload Caddy.

Known gaps:

- Cloudflare Tunnel API automation is not implemented yet.
- Cloudflare config generation is not implemented yet.
- Hosted installer needs fresh-VPS validation.
- Public SSH is still needed during bootstrap unless Tailscale auth succeeds.
- Static inventory/direct Ansible path is legacy and should not drive the product.

## Implementation Plan

1. Keep the README short and this spec authoritative.
2. Make the hosted one-liner real and tested.
3. Make Tailscale part of the secure baseline.
4. Add Cloudflare Tunnel install and service setup.
5. Add `/usr/local/bin/simple-vps`.
6. Add Cloudflare API/config automation for tunnel public hostnames.
7. Add fresh Ubuntu 24.04 smoke testing and idempotency testing.
8. Only then tag v1.

## Validation

Local checks:

```bash
bash -n install.sh
PYTHONDONTWRITEBYTECODE=1 python3 -m py_compile roles/infra/files/simple-vps
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tests
tests/bootstrap_tarball_smoke.sh
ansible-playbook --syntax-check playbooks/vps-bootstrap.yml
ansible-playbook --syntax-check playbooks/vps-apply.yml
```

CI should also run:

```bash
shellcheck install.sh
ansible-lint playbooks/vps-bootstrap.yml playbooks/vps-apply.yml
```

# Simple VPS

Opinionated production VPS setup for getting apps online quickly and securely.

Goal:

```text
fresh Ubuntu VPS -> run one script -> secure production box ready for apps
```

Simple VPS is intentionally narrow. It is not a VPS framework.

Read [SPEC.md](SPEC.md) for the product direction, security model, architecture,
CLI shape, implementation decisions, and current status.

## Current State

The repo currently uses `install.sh` as the entrypoint and Ansible as the
converge engine.

Default install path:

- Operator user with broad sudo and deploy user with narrow helper sudo
- SSH hardening, UFW, fail2ban, unattended upgrades
- Tailscale installed and started for private admin access
- cloudflared installed for Cloudflare Tunnel ingress
- Caddy listening on `127.0.0.1:8080`
- Node.js LTS, Bun, pnpm
- Litestream binary for SQLite backup workflows
- `/usr/local/bin/simple-vps` for status and route management

Explicit optional installs:

- Install Docker: `simple_vps_install_docker=true` or `--docker`
- Disable Litestream: `simple_vps_install_litestream=false` or `--no-litestream`
- Dev tools / shell / AI CLIs: `simple_vps_install_devtools=true`

Tailscale is on by default. Provide `SIMPLE_VPS_TAILSCALE_AUTH_KEY` or
`--tailscale-auth-key` for unattended login. Public SSH is only removed after
Tailscale is authenticated, so bootstrap runs do not lock themselves out.

Cloudflare Tunnel is installed by default. The recommended path is to provide
`SIMPLE_VPS_CLOUDFLARE_TUNNEL_TOKEN` / `--cloudflare-tunnel-token` or
`SIMPLE_VPS_CLOUDFLARE_TUNNEL_CONFIG` / `--cloudflare-tunnel-config`, then
create Cloudflare public hostnames manually with service
`http://127.0.0.1:8080`. For teams that want Simple VPS to manage Cloudflare
public hostnames and CNAMEs, `SIMPLE_VPS_CLOUDFLARE_API_TOKEN` /
`--cloudflare-api-token` remains available as an advanced opt-in.

Server-local route management:

```bash
simple-vps status
simple-vps route list
simple-vps route proxy example.com --port 3000
simple-vps route static data.example.com --root /var/apps/data/current/public
simple-vps route redirect old.example.com --to https://new.example.com
simple-vps route remove example.com
simple-vps route remove --app my-app
```

`publish`, `unpublish`, and `routes` remain compatibility aliases for simple
proxy routes.

## Quick Start

Remote mode from this checkout:

```bash
./install.sh \
  --mode remote \
  --host 203.0.113.10 \
  --ssh-key ~/.ssh/id_ed25519 \
  --operator-ssh-public-key-file ~/.ssh/id_ed25519.pub \
  --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub
```

Local mode from this checkout on the VPS:

```bash
./install.sh \
  --mode local \
  --operator-ssh-public-key-file ~/.ssh/id_ed25519.pub \
  --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub
```

Use separate SSH keys for the operator and deploy users. `--shared-key` is
available for small single-person hosts, but it gives that one key access to both
identities.

The hosted installer bootstrap path exists, but the final v1 one-liner is still
part of the work tracked in [SPEC.md](SPEC.md).

## Validation

From the repo root:

```bash
make provisioning-test
```

CI also runs `shellcheck` and `ansible-lint`.

## License

MIT

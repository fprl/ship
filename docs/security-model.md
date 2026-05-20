# Security Model

Simple VPS is not a general compliance hardening framework. It has one
security model: expose apps through Cloudflare Tunnel, keep direct host access
on Tailscale, and make the expected host state inspectable.

## Opinionated Choices

Simple VPS picks Tailscale for admin access and Cloudflare Tunnel for public
ingress. Alternatives such as Headscale, ZeroTier, NetBird, public Caddy,
Fastly Tunnels, or ngrok are out of scope for v1.

The host bootstrap should not install or configure other mesh VPN or tunnel
providers. The point is a small, inspectable production path, not a provider
matrix.

## Ingress

Public app traffic should enter through Cloudflare Tunnel and reach Caddy on
the VPS over loopback:

```text
internet -> Cloudflare -> cloudflared -> 127.0.0.1:8080 -> Caddy -> app
```

The VPS firewall should not expose public `80` or `443`. Caddy is still the
local ingress router, but Cloudflare Tunnel is the public edge.

## SSH

The target steady state is:

- SSH keys only.
- `PasswordAuthentication no`.
- Root SSH disabled with `PermitRootLogin no`.
- SSH reachable through Tailscale once Tailscale is authenticated.
- Public SSH allowed only as a bootstrap/recovery fallback.

The installer uses a bootstrap compromise: it creates the operator and deploy
users and copies SSH keys first, then applies hardening after operator access is
expected to work. Today the role sets `PermitRootLogin prohibit-password`, which
blocks root password login but still allows root key login. That is safer than
root passwords, but it is still not the desired steady state.

`simple-vps host doctor` should make this visible. A host with public SSH open,
password SSH enabled, or root key login still enabled should be reported as
degraded unless the system is explicitly in bootstrap or recovery mode.

## Firewall

The expected UFW state is:

- Incoming traffic denied by default.
- Outgoing traffic allowed by default.
- SSH allowed on `tailscale0` when Tailscale is ready.
- Public SSH removed when Tailscale SSH access is ready.
- Tailscale WireGuard UDP allowed when Tailscale is enabled.
- Public `80` and `443` removed.

Firewall changes must prefer recoverability over theoretical neatness. If
Tailscale is installed but not authenticated, public SSH stays open so the
operator is not locked out.

## Host Services

The expected security services are:

- `unattended-upgrades` installed and enabled.
- `fail2ban` installed and enabled for SSH while public SSH can exist during
  bootstrap or recovery.
- `tailscaled` installed and enabled when Tailscale is enabled.
- `cloudflared` installed, isolated as its own user, and enabled only when a
  Cloudflare tunnel token, API token, or config path is provided.
- Caddy installed and serving generated Simple VPS route config.

The default Cloudflare trust boundary is tunnel-token or config-file access:
Simple VPS can run the tunnel on the VPS, while users configure Cloudflare
public hostnames and DNS in Cloudflare. Cloudflare API-managed hostname and DNS
publication is an advanced opt-in for teams comfortable storing that API token
on the server.

Once Tailscale-only SSH is the normal post-bootstrap state, fail2ban should be
reassessed. It is not a generic protection layer for arbitrary ports users open
by hand; service-specific exposure needs service-specific policy.

## Privileged API

The intended privilege model has three identities:

```text
bootstrap user   root or provider-created initial user
                 used by install.sh phase 1 only
                 not a steady-state Simple VPS identity

operator user    human/admin identity for host convergence and recovery
                 allowed to run Ansible with root privileges

deploy user      identity used by the app CLI and CI
                 allowed to invoke only the server-side Simple VPS helper
```

The operator user keeps the root path host convergence needs. The deploy user
gets only the `/usr/local/bin/simple-vps` grant.

The server-side helper owns privileged app operations such as systemd unit
installation, env writes, Caddy route generation, and app cleanup. Keep that
API narrow and auditable instead of adding ad hoc sudo commands to the public
CLI.

## State and Drift

The host should be checkable from the host itself. `simple-vps host status`
should report current state; `simple-vps host doctor` should compare that state
against this model and report drift.

Initial doctor checks should cover:

- SSH password login disabled.
- Root login disabled after bootstrap.
- Public SSH closed when Tailscale is ready.
- Public `80` and `443` closed.
- Tailscale, cloudflared, Caddy, fail2ban, and unattended upgrades state.
- The deploy sudoers grant points at `/usr/local/bin/simple-vps`.
- The operator/deploy split is healthy.
- Generated Caddy config validates.

Later, if Simple VPS grows an app registry, app and route drift should be
reported from that source of truth too.

## External Hardening References

[`devsec.hardening`](https://github.com/dev-sec/ansible-collection-hardening)
is a useful checklist, not a default dependency. The collection provides
hardening roles for Linux, SSH, nginx, and MySQL, and its SSH role deliberately
disables root login.

Do not add it blindly to the default install. It overlaps with Simple VPS-owned
behavior: SSH bootstrap order, Tailscale reachability, UFW policy, optional
Docker networking, and recovery access. If Simple VPS adopts pieces from it,
they should be copied into explicit local tasks or exposed behind an optional
strict hardening profile with VM coverage for bootstrap, rerun idempotency,
deploy, rollback, Tailscale access, Cloudflare Tunnel, and recovery.

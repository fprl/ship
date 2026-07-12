# Security Model

ship is not a general compliance hardening framework. It has one
host posture: least-open-by-default, explicit ingress/admin modes, and an
expected host state that is inspectable.

## Opinionated Choices

ship always uses Caddy as the local ingress router and OpenSSH for
bootstrap/recovery access. The operator chooses ingress and admin modes:

- `--ingress public`: Caddy listens on public `80`/`443`.
- `--ingress cloudflare`: Cloudflare Tunnel reaches Caddy over loopback;
  public `80`/`443` stay closed.
- `--ingress private`: no public HTTP ingress.
- `--admin public-ssh`: public SSH remains available with keys only.
- `--admin tailscale`: SSH moves to Tailscale once Tailscale is ready.

Alternatives such as Headscale, ZeroTier, NetBird, Fastly Tunnels, or ngrok
are out of scope for v1. The host bootstrap should not install or configure a
provider matrix.

## Ingress

Public app traffic reaches Caddy through the selected ingress mode.

Public mode:

```text
internet -> VPS:80/443 -> Caddy -> app
```

Cloudflare mode:

```text
internet -> Cloudflare -> cloudflared -> 127.0.0.1:8080 -> Caddy -> app
```

Private mode exposes no public HTTP entrypoint. Caddy may still serve local or
private routes for future internal use, but the firewall does not open public
`80` or `443`.

## SSH

The target steady state is:

- SSH keys only.
- `PasswordAuthentication no`.
- Root SSH disabled with `PermitRootLogin no`.
- If `--admin tailscale` is selected, SSH reachable through Tailscale once
  Tailscale is authenticated.
- Public SSH allowed as bootstrap/recovery access, or as the steady state when
  `--admin public-ssh` is selected.

The installer uses a bootstrap compromise: it creates the operator and deploy
users and copies SSH keys first, then applies hardening after operator access is
expected to work. Today the role sets `PermitRootLogin prohibit-password`, which
blocks root password login but still allows root key login. That is safer than
root passwords, but it is still not the desired steady state.

`ship box doctor` should make this visible. A host with password SSH
enabled or root key login still enabled should be reported as degraded. Public
SSH is degraded only when `--admin tailscale` is the desired steady state and
Tailscale is ready.

## Firewall

The expected UFW state is:

- Incoming traffic denied by default.
- Outgoing traffic allowed by default.
- Public `80` and `443` allowed only for `--ingress public`.
- Public `80` and `443` removed for `--ingress cloudflare` and
  `--ingress private`.
- SSH allowed on `tailscale0` when Tailscale admin mode is ready.
- Public SSH removed when Tailscale admin mode is ready.
- Tailscale WireGuard UDP allowed when Tailscale is enabled.

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
- Caddy running as the generated ship ingress container.

The default Cloudflare trust boundary is tunnel-token or config-file access:
ship can run the tunnel on the VPS, while users configure Cloudflare
public hostnames and DNS in Cloudflare. Cloudflare API-managed hostname and DNS
publication is an advanced opt-in for teams comfortable storing that API token
on the server.

When Tailscale-only SSH is the selected post-bootstrap state, fail2ban should
be reassessed. It is not a generic protection layer for arbitrary ports users
open by hand; service-specific exposure needs service-specific policy.

## Privileged API

The intended privilege model has three identities:

```text
bootstrap user   root or provider-created initial user
                 used by remote host install only
                 not a steady-state ship identity

operator user    human/admin identity for host convergence and recovery
                 allowed to run the Go host provisioner with root privileges

deploy user      identity used by the app CLI and CI
                 allowed to invoke only the server-side ship helper
```

The operator user keeps the root path host convergence needs. The deploy user
gets only the app lifecycle plus the narrow diagnostic helper grant.

The server-side helper owns privileged app operations such as per-env user and
network setup, resolved env-file writes, Podman container lifecycle, Caddy
fragment generation, and app cleanup. Keep that API narrow and auditable
instead of adding ad hoc sudo commands to the public CLI.

## State and Drift

The host should be checkable through the box surface. `ship box ls --json`
reports visible app environments. `ship box doctor --json` reports host state,
required service health, timer state, journal readability, and remediation
commands.

Future doctor hardening checks should cover:

- SSH password login disabled.
- Root login disabled after bootstrap.
- Public SSH closed when Tailscale admin mode is desired and ready.
- Public `80` and `443` match the desired ingress mode.
- Tailscale, cloudflared, Caddy, fail2ban, and unattended upgrades state in
  more detail than simple active/inactive reporting.
- The deploy sudoers grant is limited to `server app *`, `server doctor`,
  `server doctor *`, `server key *`, `server approval *`, and
  `server notify *`.
- The operator/deploy split is healthy.
- Generated Caddy config validates.

Later, if ship grows an app registry, app and route drift should be
reported from that source of truth too.

## External Hardening References

A strict hardening checklist is useful reference material, not a default
dependency. External hardening role collections often cover Linux, SSH, nginx,
and MySQL, and many deliberately disable root login.

Do not add those checks blindly to the default install. They overlap with ship-owned
behavior: SSH bootstrap order, Tailscale reachability, UFW policy, optional
Docker networking, and recovery access. If ship adopts pieces from it,
they should be copied into explicit Go primitives or exposed behind an optional
strict hardening profile with VM coverage for bootstrap, rerun idempotency,
deploy, rollback, Tailscale access, Cloudflare Tunnel, and recovery.

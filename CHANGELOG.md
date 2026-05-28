# Changelog

## v0.4.0 - 2026-05-28

This is the first real end-to-end Go implementation cut after the container
runtime pivot. It replaces the old pre-cutover shape with one root Go binary
that serves the public CLI, host installer, and privileged server API.

### Added

- Ubuntu 24.04 host install/converge through `simple-vps host install`.
- Podman-based container deploys from a required Dockerfile.
- Caddy-in-container ingress with per-app Caddy fragments.
- Per-env Linux users, Podman networks, app directories, and mutation locks.
- Manifest env blocks plus host-side `@secret:KEY` references.
- Host-side secret store under `/etc/simple-vps/secrets/<app>/<env>/<KEY>`.
- `status`, `logs`, `restart`, `destroy`, and `secret` app commands.
- `simple-vps version`.
- `--json` output for automation-facing commands:
  `status`, `restart`, `secret list`, `host status`, and `host doctor`.
- Fake-VPS smoke tests for container deploy and fresh host install.
- Real Ubuntu 24.04 VPS smoke verification on 2026-05-28.

### Changed

- Host/app state no longer uses the legacy `apps.json` / `routes.json`
  registries. Runtime truth comes from Podman labels and Caddy fragments.
- Generated Linux usernames and container DNS names are bounded internally
  with stable hashes when needed.
- Release builds embed the git/tag version in `host.json` metadata instead of
  always writing `dev`.

### Fixed

- Real-UFW install issues around `ufw --force allow`.
- Caddy startup ordering during host install.
- Podman bridge DNS/forwarding under Ubuntu's default UFW posture.
- Ubuntu Podman short-name image resolution via Docker Hub registry config.
- Read-only-rootfs compatibility for stock images through service tmpfs mounts.
- `tls = "internal"` route support for private/non-DNS smoke hosts.
- Remote install SSH preflight hardening for rebuilt hosts with stale
  `known_hosts` entries.
- `simple-vps init` now writes a valid default manifest app name.

### Known Gaps

- Rollback is planned but not shipped.
- Backup/restore is planned but not shipped.
- `app list --json` is planned but not shipped.
- `host install --ingress ...` and `--admin ...` preset flags are planned;
  current installs use the lower-level provider flags directly.

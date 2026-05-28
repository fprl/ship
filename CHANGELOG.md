# Changelog

## Unreleased

### Added

- Manifest v2 static-only deploys with `serve = "dist"` routes, host-side
  static releases, Caddy file serving, `app list` visibility, backup, destroy,
  and restore coverage.
- `[deploy].release` for deploy-time migration commands in container apps.
- Flat env roots at `/var/apps/<app>.<env>/` with `data/`, `runtime/`, and
  `static/` directories plus a durable `simple-vps.json` identity anchor.

### Changed

- Public manifest shape now uses `[processes.*]`, `[vars]`,
  `[env.<name>.vars]`, route `process = "web"`, and `health = "/health"`.
- Runtime identity now uses deterministic derived infra IDs for Linux users,
  Podman networks, containers, and locks while keeping host paths readable.
- Web process deploys start versioned containers, verify health, reload Caddy
  to the next container, then remove the old container.
- Secrets are written with `simple-vps secret set`; runtime env files now live
  under `runtime/.env` instead of app data.
- Backups now snapshot `data/` and active static release assets rather than
  generated runtime files.

### Removed

- Removed the old public manifest surface: `[services.*]`, route `service`,
  route `type`, `[env.<name>.env]`, `healthcheck`, `healthcheck_status`,
  public `tmpfs`, and `net_bind_service`.
- Removed the nested `/var/apps/<app>/<env>/shared` runtime/data layout.

## v0.4.3 - 2026-05-28

### Added

- `simple-vps app list [--server ...] [--json]` and the matching
  `server app list` helper command. App discovery now comes from Podman
  labels instead of the deleted legacy app/route registries.
- `simple-vps deploy <env> --rebuild`, which passes
  `--no-cache --pull=always` to host-side `podman build`.
- `simple-vps host install --ingress public|cloudflare|private` and
  `--admin public-ssh|tailscale` presets.
- `simple-vps rollback <env> [release] [--json]` and the matching
  `server app rollback` helper command for local image-based rollback.
- Local `simple-vps backup`, `backup list`, `backup rm`, and `restore`
  primitives covering shared data, applied manifest, secrets, and release
  metadata.

### Changed

- Spec installation examples now use the shipped raw-GitHub installer URL
  instead of the unprovisioned `simple-vps.dev/install.sh` placeholder.
- Host install defaults now use public ingress and public SSH admin access;
  Cloudflare Tunnel and Tailscale are enabled through the new presets.
- Removed the half-shipped static app manifest surface. `static = "..."`
  and `type = "static"` are now rejected until static deploys have an actual
  implementation.

### Fixed

- Local builds from `git describe` output such as `v0.4.2-7-g<sha>` no
  longer try to download nonexistent GitHub release helper assets during
  remote host install.

## v0.4.2 - 2026-05-28

### Fixed

- Release helper downloads now verify `simple-vps-linux-<arch>` against the
  release `SHA256SUMS` file before copying the helper to a VPS.

### Changed

- `install.sh` now detects the install host OS/architecture, downloads the
  matching release binary by default when no local build is available, and
  verifies it against `SHA256SUMS`.

## v0.4.1 - 2026-05-28

### Fixed

- Remote `host install` now works from a standalone downloaded release binary.
  The installer looks for a matching `simple-vps-linux-<arch>` helper beside
  the current binary or in `SIMPLE_VPS_HELPER_DIR`; if none exists and the
  current binary has a release version, it downloads the matching Linux helper
  from the GitHub release assets. Private release asset downloads honor
  `SIMPLE_VPS_RELEASE_TOKEN`, `GH_TOKEN`, or `GITHUB_TOKEN`.

### Changed

- README install instructions now start from release binaries instead of a
  source checkout.

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

- Remote backup destinations and portable encrypted secret bundles are still
  planned; the shipped backup driver is local filesystem.

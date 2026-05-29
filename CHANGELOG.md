# Changelog

## Unreleased

### Changed

- Documented the `v0.5.0-rc1` release installer smoke and private-repo
  installer fetch path.

## v0.5.0-rc1 - 2026-05-29

### Added

- Manifest v2 static-only deploys with `serve = "dist"` routes, host-side
  static releases, Caddy file serving, `app list` visibility, backup, destroy,
  restore, and rollback coverage.
- Mixed container/static apps: one release can now include the app image plus
  route-level static snapshots, with rollback and restore moving both together.
- `[deploy].release` for deploy-time migration commands in container apps.
- Flat env roots at `/var/apps/<app>.<env>/` with `data/`, `runtime/`, and
  `static/` directories plus a durable `simple-vps.json` identity anchor.
- Example apps for container-only, static-only, and mixed container/static
  deploys.

### Changed

- Public manifest shape now uses `[processes.*]`, `[vars]`,
  `[env.<name>.vars]`, route `process = "web"`, and `health = "/health"`.
- Runtime identity now uses deterministic derived infra IDs for Linux users,
  Podman networks, containers, Caddy fragments, and locks while keeping host
  paths readable.
- Web process deploys start versioned containers, verify health, reload Caddy
  to the next container, then remove the old container.
- Secrets are written with `simple-vps secret set`; runtime env files now live
  under `runtime/.env` instead of app data.
- Backups now snapshot `data/` and active static release assets rather than
  generated runtime files.
- Static route deploys include ignored/generated `serve` directories in the
  uploaded artifact, and static bytes participate in release IDs.
- Release-candidate checklist documents local, fake-VPS, release-build, and
  real-VPS smoke steps.
- Release publishing now runs from a tag-driven GitHub Actions workflow that
  builds checksummed assets and uploads them without local `gh` credentials.

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
- Derived host identity under `/etc/simple-vps/apps/<app>/<env>.json` and a
  privileged `simple-vps-server` helper invoked through restricted sudo.
- Public Caddy ingress from generated route fragments under
  `/etc/caddy/conf.d`.
- SSH-based deploy flow that streams a source tarball, builds on the VPS, and
  starts containers through the helper.

### Changed

- Deployment now requires a `Dockerfile`. Legacy non-container deploy paths,
  runtime adapters, and framework detection were removed.
- Remote provision now uses a root-owned helper plus a deploy user instead of
  broad SSH privileges.
- The helper owns app mutation with file locks, route generation, container
  lifecycle, and rollback state.

### Removed

- Removed the legacy Node/TypeScript implementation, adapters, and generated
  `runtime/` bundle.
- Removed Terraform/Ansible provisioning paths and generated infra templates.

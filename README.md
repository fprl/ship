# ship

> **Superseded:** This document describes the pre-ship surface and is not current.
> **Pending:** The Phase 3 rewrite will replace it; only broken commands are patched here.

ship is a tiny VPS runtime: point a repo at a Ubuntu box, get Dockerfile
builds, Podman containers, Caddy TLS routing, secrets, backup/restore, and
rollback without bringing Kubernetes or a hosted PaaS into the picture.

## Current Shape

- Ubuntu 24.04/26.04 host install/converge with one Go binary.
- Podman container deploys from a required `Dockerfile`.
- Static-only deploys with route-level `serve = "dist"`.
- Mixed container/static deploys: a Dockerfile-backed process can share one
  release with static route assets served directly by Caddy.
- Explicit envs. Mutating commands require an env argument.
- App/env host roots at `/var/apps/<app>.<env>/`.
- Deterministic derived infra IDs for users, networks, containers, routes, and
  locks.
- Runtime env files under `runtime/.env`; durable app data under `data/`.
- Secrets stored on the host and injected with `--env-file`.
- Web deploys start the next versioned container, health-check it, reload Caddy,
  then remove the old container.
- Backups include `data/`, active static release assets, applied manifest
  snapshots, and secrets.
- Rollback restores an older local image/static release plus the manifest
  snapshot that produced it.

## Quick Check

```bash
make test
make fake-vps-smoke
make fake-vps-install-smoke
```

Example apps live under `examples/`:

- `examples/hono-bun-api` - Dockerfile-backed Bun/Hono API.
- `examples/php-plain` - Dockerfile-backed PHP HTTP app.
- `examples/django-sqlite` - Django, SQLite under `/data`, and migrations via
  `[deploy].release`.
- `examples/astro-static` - real Astro app; run `npm run build`, then deploy
  generated `dist/`.
- `examples/mixed-api-docs` - container API plus host-served `/docs`.

For the fresh-VPS-to-first-app path, use
[docs/getting-started.md](docs/getting-started.md).

## Install The CLI

Install the local CLI on your laptop or CI machine:

```bash
curl -fsSL https://github.com/fprl/ship/releases/latest/download/install.sh | bash
ship version
```

The installer downloads the release asset for your OS/CPU, verifies it against
`SHA256SUMS`, and writes `ship` to `~/.local/bin`. If your shell cannot
find `ship`, the installer prints the exact `PATH` line to add.

The curl command assumes public release assets. For private release assets,
download `install.sh` with GitHub authentication first, then run it with
`SHIP_RELEASE_TOKEN`, `GH_TOKEN`, or `GITHUB_TOKEN`.

## Install A VPS

Create a deploy key if you do not already have one:

```bash
test -f ~/.ssh/ship-deploy || \
  ssh-keygen -q -t ed25519 -N '' -f ~/.ssh/ship-deploy
test -f ~/.ssh/ship-deploy.pub || \
  ssh-keygen -y -f ~/.ssh/ship-deploy > ~/.ssh/ship-deploy.pub
```

Then converge a fresh Ubuntu VPS:

```bash
ship box init root@<vps-ip>
```

The operator key is for human host recovery and rerunning host install. The
deploy key is what `deploy`, `status`, `secret`, and other app commands use
after install. By default, box init uses `~/.ssh/ship-deploy.pub` for
the deploy user and the VPS bootstrap user's existing authorized key for the
operator user.

`box init` accepts a new SSH host key for a never-seen VPS. If you rebuilt
a VPS at the same IP and SSH blocks because the host key changed, remove the
old remembered key with `ssh-keygen -R <vps-ip>` and rerun the command.

## Deploy An App

For a new project, scaffold a small deployable shape:

```bash
ship init --template php \
  --name api \
  --box deploy@example.com \
  --host api.example.com
```

Templates:

- `container` - minimal Python HTTP container.
- `static` - `dist/` static route, no Dockerfile.
- `php` - plain PHP HTTP container.
- `hono` - Bun/Hono HTTP container.

`init` never overwrites existing app files. If a `Dockerfile` already exists,
it creates the manifest and leaves the Dockerfile alone. Use
`--tls internal` for private DNS or disposable smoke hosts; omit it for normal
public Let's Encrypt routes.

`ship.toml`:

```toml
name = "api"

[env.production]
server = "deploy@example.com"

[vars]
LOG_LEVEL = "info"
DATABASE_PATH = "/data/app.db"

[deploy]
release = "bun run migrate"

[processes.web]
command = "bun run src/server.ts"
port = 3000
health = "/health"
resources = { memory = "512m", cpus = 0.5 }

[processes.worker]
command = "bun run worker"
resources = { memory = "1g", cpus = 1 }

[routes.app]
host = "api.example.com"
process = "web"

[routes.docs]
host = "api.example.com"
path = "/docs"
serve = "docs-dist"

[routes.old]
host = "old.example.com"
redirect = "https://api.example.com"

[env.staging]
server = "deploy@staging.example.com"

[env.staging.vars]
LOG_LEVEL = "debug"

[env.staging.routes.app]
host = "staging-api.example.com"
```

Then:

```bash
git init
git add .
git commit -m "initial ship app"
ship
ship status
```

`check --env` uses the same local deploy diagnostics as `deploy`: the app
directory must be a committed Git worktree, and dirty deploys must be explicit
with `deploy --dirty`. First deploy prepares the remote app environment before
uploading, building, or routing the release.

Deploy excludes dotenv files by default. Use `[vars]` and `@secret:` for real
secrets; pass `deploy --include-dotenv` only when you intentionally want dotenv
files in the uploaded release artifact.

The `serve` directory is uploaded into the same release as the container image,
so rollback and restore move the web process and static files together.

That works for static-only apps and for container apps that also proxy a
process route.

In monorepos, point commands at a manifest explicitly:

```bash
ship --config apps/api/ship.toml
```

Secrets are stored on the host and referenced from the manifest:

```bash
printf '%s' "$DATABASE_URL" | ship secret set DATABASE_URL
ship secret ls --json
```

## Release Builds

Build all release binaries:

```bash
make clean
make build-release VERSION=v0.7.0
```

Build artifacts land in `dist/`:

```text
ship-linux-amd64
ship-linux-arm64
ship-darwin-amd64
ship-darwin-arm64
SHA256SUMS
```

The release workflow uploads those files plus the root `install.sh` script.

## References

- [CHANGELOG.md](CHANGELOG.md)
- [docs/positioning.md](docs/positioning.md)
- [docs/getting-started.md](docs/getting-started.md)
- [docs/security-model.md](docs/security-model.md)
- [docs/release-checklist.md](docs/release-checklist.md)
- [docs/smoke-real-box.md](docs/smoke-real-box.md)
- [docs/adr/0001-replace-ansible-with-bounded-go-provisioner.md](docs/adr/0001-replace-ansible-with-bounded-go-provisioner.md)
- [docs/adr/0002-state-file-layout.md](docs/adr/0002-state-file-layout.md)
- [docs/adr/0003-apt-repo-key-trust-policy.md](docs/adr/0003-apt-repo-key-trust-policy.md)
- [docs/adr/0004-non-apt-release-artifact-verification.md](docs/adr/0004-non-apt-release-artifact-verification.md)
- [docs/adr/0005-container-runtime-via-required-dockerfile.md](docs/adr/0005-container-runtime-via-required-dockerfile.md)
- [docs/adr/0006-cuts-and-composability-commitments.md](docs/adr/0006-cuts-and-composability-commitments.md)
- [docs/adr/0007-backup-restore-primitive.md](docs/adr/0007-backup-restore-primitive.md)
- [docs/adr/0008-manifest-v2-env-root-and-runtime-identity.md](docs/adr/0008-manifest-v2-env-root-and-runtime-identity.md)
- [docs/adr/0009-v1-cli-and-primitive-contract.md](docs/adr/0009-v1-cli-and-primitive-contract.md)

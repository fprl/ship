# Simple VPS

Simple VPS is a tiny VPS runtime: point a repo at a Ubuntu box, get Dockerfile
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

Download the release asset for your laptop/CI machine and put it on `PATH`.
Release artifacts are named by OS and CPU architecture:

```text
simple-vps-linux-amd64
simple-vps-linux-arm64
simple-vps-darwin-amd64
simple-vps-darwin-arm64
```

See [docs/getting-started.md](docs/getting-started.md) for copy-paste install
commands with checksum verification.

## Install A VPS

`install.sh` is the host installer entrypoint. It finds, builds, or downloads a
matching `simple-vps` binary, then runs `simple-vps host install`. It does not
install the local CLI onto your laptop.

Download the installer from the same release you are installing:

```bash
VERSION=v0.7.0
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  gh api -H 'Accept: application/vnd.github.raw' \
    "/repos/fprl/simple-vps/contents/install.sh?ref=$VERSION" > install.sh
else
  curl -fsSL "https://raw.githubusercontent.com/fprl/simple-vps/$VERSION/install.sh" \
    -o install.sh
fi
chmod 0755 install.sh
```

The installer downloads the selected release asset that matches your platform
and verifies it against `SHA256SUMS`. From macOS, remote install also downloads
and verifies the matching Linux helper binary for the target VPS. Set
`SIMPLE_VPS_VERSION=vX.Y.Z` to pin a release.

```bash
SIMPLE_VPS_VERSION="$VERSION" ./install.sh \
  --host <vps-ip> \
  --ssh-key ~/.ssh/<root-key> \
  --operator-ssh-public-key-file ~/.ssh/<root-key>.pub \
  --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub \
  --yes
```

The operator key is for human host recovery and rerunning host install. The
deploy key is what `deploy`, `status`, `secret`, and other app commands use
after install.

If the release assets are private, authenticate `gh` before downloading the
installer and set `SIMPLE_VPS_RELEASE_TOKEN`, `GH_TOKEN`, or `GITHUB_TOKEN`
before running it. For local development, run `make build` first and the
installer will use `dist/simple-vps` instead of downloading a release.

To install from a source checkout instead of a release, run `make build`, pin a
release with `SIMPLE_VPS_VERSION=vX.Y.Z`, or point at a custom binary with
`SIMPLE_VPS_BINARY_URL`.

## Deploy An App

For a new project, scaffold a small deployable shape:

```bash
simple-vps init --template php \
  --name api \
  --server deploy@example.com \
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

`simple-vps.toml`:

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
git commit -m "initial simple-vps app"
simple-vps check --env production
simple-vps setup --env production
simple-vps deploy --env production
simple-vps status --env production
```

`check --env` uses the same local deploy diagnostics as `deploy`: the app
directory must be a committed Git worktree, and dirty deploys must be explicit
with `deploy --dirty`.

Deploy excludes dotenv files by default. Use `[vars]` and `@secret:` for real
secrets; pass `deploy --include-dotenv` only when you intentionally want dotenv
files in the uploaded release artifact.

The `serve` directory is uploaded into the same release as the container image,
so rollback and restore move the web process and static files together.

That works for static-only apps and for container apps that also proxy a
process route.

In monorepos, point commands at a manifest explicitly:

```bash
simple-vps deploy --config apps/api/simple-vps.toml --env production
```

Secrets are stored on the host and referenced from the manifest:

```bash
printf '%s' "$DATABASE_URL" | simple-vps secret set DATABASE_URL --env production
simple-vps secret list --json --env production
```

## Release Builds

Build all release binaries:

```bash
make clean
make build-release VERSION=v0.7.0
```

Artifacts land in `dist/`:

```text
simple-vps-linux-amd64
simple-vps-linux-arm64
simple-vps-darwin-amd64
simple-vps-darwin-arm64
```

Smoke a published release against a VPS:

```bash
scripts/release-smoke.sh --version v0.7.0 --host <ip>
```

## References

- [SPEC.md](SPEC.md)
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

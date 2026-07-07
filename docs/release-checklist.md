# Release Checklist

Use this before cutting preview or stable releases.

```bash
VERSION=v0.7.0
```

## Local Checks

```bash
git status --short
make clean
make test
make fake-vps-smoke
make fake-vps-install-smoke
make build-release VERSION="$VERSION"
make build VERSION="$VERSION"
```

## Example Manifest Checks

The Astro example check needs Node/npm and network access because it builds a
real static site before validating `serve = "dist"`.

```bash
(cd examples/hono-bun-api && ../../dist/simple-vps check --env production)
(cd examples/php-plain && ../../dist/simple-vps check --env production)
(cd examples/django-sqlite && ../../dist/simple-vps check --env production)
(cd examples/astro-static && npm install --no-package-lock && npm run build && ../../dist/simple-vps check --env production)
(cd examples/mixed-api-docs && ../../dist/simple-vps check --env production)
tmp=$(mktemp -d /tmp/simple-vps-init-check-XXXXXX)
./dist/simple-vps init --config "$tmp/simple-vps.toml" --template php --name init-php --server deploy@example.com --host init-php.example.com
(cd "$tmp" && git init && git add . && git -c user.email=test@example.com -c user.name=Test commit -m init)
./dist/simple-vps check --config "$tmp/simple-vps.toml" --env production

# Optional local container build coverage when Podman or Docker is available.
# Set SIMPLE_VPS_TEST_INIT_BUILDER=docker if Podman is installed but unavailable.
make init-template-builds
```

## Publish

```bash
git tag -a "$VERSION" -m "$VERSION"
git push origin "$VERSION"
```

The `Release` GitHub Actions workflow builds the release assets, generates
`SHA256SUMS`, creates or updates the GitHub release, and uploads the assets plus
`install.sh` with `--clobber`.

## Real VPS Smoke

Run against a freshly rebuilt Ubuntu 24.04 or 26.04 VPS after the GitHub release
assets exist. Requires `curl`, `git`, `jq`, and `ssh-keyscan` on the smoke
machine.

The old `scripts/release-smoke.sh` path was removed with the pre-ship CLI
surface. Use `make fake-vps-smoke` and `make fake-vps-install-smoke` as the
checked release gates until the Phase 3 real-box runbook is rewritten.

## Example Matrix Smoke

The old `scripts/example-matrix-smoke.sh` path was removed with the pre-ship CLI
surface. Keep example validation manual until the Phase 3 example matrix is
rewritten.

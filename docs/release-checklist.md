# Release Checklist

Use this before cutting preview or stable releases.

```bash
VERSION=v0.1.0
```

## Local Gates

```bash
git status --short
make clean
make test
make fake-vps-smoke
make fake-vps-install-smoke
make agent-evals-oracle
make build-release VERSION="$VERSION"
make build VERSION="$VERSION"
```

## Install Script Smoke

The checked shell smoke validates the public curl installer contract without
publishing a release:

```bash
bash scripts/install-smoke.sh
```

## Example Matrix

Run this against a real smoke box after editing each example `ship.toml` to use
the smoke box and route host. Use `--tls internal` unless public DNS is already
pointing at the box.

```bash
(cd examples/hono-bun-api && ../../dist/ship --tls internal)
```

```bash
(cd examples/php-plain && printf '%s' 'release-smoke' | ../../dist/ship secret set APP_SECRET)
(cd examples/php-plain && ../../dist/ship --tls internal)
```

```bash
(cd examples/django-sqlite && printf '%s' 'release-smoke' | ../../dist/ship secret set DJANGO_SECRET_KEY)
(cd examples/django-sqlite && ../../dist/ship --tls internal)
```

```bash
(cd examples/astro-static && npm install --no-package-lock)
(cd examples/astro-static && npm run build)
(cd examples/astro-static && ../../dist/ship --tls internal)
```

```bash
(cd examples/mixed-api-docs && ../../dist/ship --tls internal)
```

Confirm the fleet view before teardown:

```bash
./dist/ship box ls deploy@<IP> --json
```

Clean up each example app from the box:

```bash
./dist/ship box rm hono-api deploy@<IP> --confirm hono-api
./dist/ship box rm php-plain deploy@<IP> --confirm php-plain
./dist/ship box rm django-sqlite deploy@<IP> --confirm django-sqlite
./dist/ship box rm astro-site deploy@<IP> --confirm astro-site
./dist/ship box rm mixed-app deploy@<IP> --confirm mixed-app
```

## Init Template Coverage

Run optional local container build coverage when Podman or Docker is available.
Set `SHIP_TEST_INIT_BUILDER=docker` if Podman is installed but unavailable.

```bash
make init-template-builds
```

## Real VPS Smoke

Run [smoke-real-box.md](smoke-real-box.md) against a freshly rebuilt Ubuntu
24.04 or 26.04 VPS after release assets exist. The fake-VPS suites are the
required release gates; the real-box runbook catches provider, firewall, Podman,
and Caddy behavior the fake harness cannot prove.

## Publish

```bash
git tag -a "$VERSION" -m "$VERSION"
git push origin "$VERSION"
```

The GitHub release workflow builds release assets, generates `SHA256SUMS`,
creates or updates the GitHub release, and uploads the assets plus `install.sh`.

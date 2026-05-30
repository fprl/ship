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
`SHA256SUMS`, creates or updates the GitHub release, and uploads the assets with
`--clobber`.

## Real VPS Smoke

Run against a freshly rebuilt Ubuntu 24.04 or 26.04 VPS after the GitHub release
assets exist. Requires `curl`, `git`, `jq`, and `ssh-keyscan` on the smoke
machine.

```bash
scripts/release-smoke.sh --version "$VERSION" --host <ip>
```

For private release assets, export `SIMPLE_VPS_RELEASE_TOKEN`, `GH_TOKEN`, or
`GITHUB_TOKEN`.

By default the script uses:

- root bootstrap key: `~/.ssh/hetzner`
- operator public key: `~/.ssh/hetzner.pub`
- deploy public key: `~/.ssh/simple-vps-deploy.pub`
- deploy private key: `~/.ssh/simple-vps-deploy`

Override those with `SIMPLE_VPS_BOOTSTRAP_SSH_KEY`,
`SIMPLE_VPS_OPERATOR_PUBKEY`, `SIMPLE_VPS_DEPLOY_PUBKEY`, and
`SIMPLE_VPS_DEPLOY_SSH_KEY`.

## Example Matrix Smoke

After the host is installed, this deploys the checked-in PHP, Hono/Bun, mixed
API+static, and Astro static examples with the current local client binary:

```bash
make build VERSION="$VERSION"
scripts/example-matrix-smoke.sh --host <ip> --client ./dist/simple-vps
```

The Astro example runs `npm install --no-package-lock && npm run build` locally
before deploy. The script destroys each example env unless
`SIMPLE_VPS_EXAMPLE_MATRIX_SKIP_DESTROY=1` is set.

The Django example is heavier because it builds a Python framework image and
runs a migration release command. Include it with:

```bash
SIMPLE_VPS_EXAMPLE_MATRIX_INCLUDE_DJANGO=1 \
  scripts/example-matrix-smoke.sh --host <ip> --client ./dist/simple-vps
```

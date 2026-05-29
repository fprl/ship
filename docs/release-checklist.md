# Release Checklist

Use this before cutting preview releases.

```bash
VERSION=v0.5.0-rc3
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

```bash
(cd examples/hono-bun-api && ../../dist/simple-vps check --env production)
(cd examples/php-plain && ../../dist/simple-vps check --env production)
(cd examples/astro-static && ../../dist/simple-vps check --env production)
(cd examples/mixed-api-docs && ../../dist/simple-vps check --env production)
tmp=$(mktemp -d /tmp/simple-vps-init-check-XXXXXX)
./dist/simple-vps init --config "$tmp/simple-vps.toml" --template php --name init-php --server deploy@example.com --host init-php.example.com
./dist/simple-vps check --config "$tmp/simple-vps.toml" --env production

# Optional local container build coverage when Podman or Docker is available.
# Set SIMPLE_VPS_TEST_INIT_BUILDER=docker if Podman is installed but unavailable.
make init-template-builds
```

## Real VPS Smoke

Run against a freshly rebuilt Ubuntu 24.04 or 26.04 VPS.
Requires `curl`, `git`, `jq`, and `ssh-keyscan` on the smoke machine.

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

## Publish

```bash
git tag -a "$VERSION" -m "$VERSION"
git push origin "$VERSION"
```

The `Release` GitHub Actions workflow builds the release assets, generates
`SHA256SUMS`, creates or updates the GitHub release, and uploads the assets with
`--clobber`.

After publishing, run the real VPS smoke again:

```bash
scripts/release-smoke.sh --version "$VERSION" --host <ip>
```

# Release Checklist

Use this before cutting preview releases.

```bash
VERSION=v0.5.0-rc2
```

## Local Checks

```bash
git status --short
make clean
make test
make fake-vps-smoke
make fake-vps-install-smoke
make build-release VERSION="$VERSION"
```

## Example Manifest Checks

```bash
(cd examples/hono-bun-api && ../../dist/simple-vps check --env production)
(cd examples/php-plain && ../../dist/simple-vps check --env production)
(cd examples/astro-static && ../../dist/simple-vps check --env production)
(cd examples/mixed-api-docs && ../../dist/simple-vps check --env production)
```

## Real VPS Smoke

Run against a freshly rebuilt Ubuntu 24.04 or 26.04 VPS.

1. Install from the release artifact, not the source checkout:

   ```bash
   tmp=$(mktemp -d /tmp/simple-vps-release-smoke-XXXXXX)
   cd "$tmp"
   curl -fsSL https://raw.githubusercontent.com/fprl/simple-vps/main/install.sh -o install.sh
   chmod 0755 install.sh
   SIMPLE_VPS_VERSION="$VERSION" ./install.sh \
     --mode remote \
     --host <ip> \
     --bootstrap-user root \
     --ssh-key ~/.ssh/hetzner \
     --operator-ssh-public-key-file ~/.ssh/hetzner.pub \
     --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub \
     --ingress public \
     --admin public-ssh \
     --yes
   ```

   For private release assets, export `SIMPLE_VPS_RELEASE_TOKEN`, `GH_TOKEN`, or
   `GITHUB_TOKEN`. If the repository itself is private, fetch the installer
   through the GitHub Contents API instead of `raw.githubusercontent.com`:

   ```bash
   curl -fsSL \
     -H "Authorization: Bearer $SIMPLE_VPS_RELEASE_TOKEN" \
     -H "Accept: application/vnd.github.raw" \
     "https://api.github.com/repos/fprl/simple-vps/contents/install.sh?ref=main" \
     -o install.sh
   ```

2. Verify host health:

   ```bash
   SIMPLE_VPS_SSH_KEY="$(cat ~/.ssh/simple-vps-deploy)" \
   SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <ip> 2>/dev/null)" \
     ./simple-vps host status --json --server deploy@<ip>

   SIMPLE_VPS_SSH_KEY="$(cat ~/.ssh/simple-vps-deploy)" \
   SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <ip> 2>/dev/null)" \
     ./simple-vps host doctor --json --server deploy@<ip>
   ```

3. Deploy and verify one app of each shape:

   - `examples/hono-bun-api`
   - `examples/php-plain`
   - `examples/astro-static`
   - `examples/mixed-api-docs`

   `hono-bun-api` and `php-plain` are both container apps, but smoke both
   before a preview release so the examples cover more than one runtime.

4. For the mixed app, verify:

   ```bash
   simple-vps deploy --env production
   curl -k --resolve <host>:443:<ip> https://<host>/health
   curl -k --resolve <host>:443:<ip> https://<host>/docs
   simple-vps rollback --env production
   simple-vps backup create --env production
   simple-vps restore --from <backup-id> --env production
   simple-vps destroy --env production --confirm <app> --purge
   simple-vps app list --json --server deploy@<ip>
   ```

## Publish

```bash
git tag -a "$VERSION" -m "$VERSION"
git push origin "$VERSION"
```

The `Release` GitHub Actions workflow builds the release assets, generates
`SHA256SUMS`, creates or updates the GitHub release, and uploads the assets with
`--clobber`.

After publishing, run the real VPS smoke again from a temp directory with
`SIMPLE_VPS_VERSION="$VERSION"`.

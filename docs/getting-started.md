# Getting Started

This is the shortest path from a fresh Ubuntu VPS to a deployed app.

## 1. Install the local CLI

Install the release binary for the machine where you run deploy commands:

```bash
curl -fsSL https://github.com/fprl/ship/releases/download/v0.7.0/install.sh | bash
ship version
```

The installer downloads the right release asset, verifies `SHA256SUMS`, and
writes `ship` to `~/.local/bin`. If your shell cannot find `ship`,
the installer prints the exact `PATH` line to add.

The curl command assumes public release assets. For private release assets,
download `install.sh` with GitHub authentication first, then run it with
`SHIP_RELEASE_TOKEN`, `GH_TOKEN`, or `GITHUB_TOKEN`.

## 2. Prepare SSH keys

You need a root/bootstrap key for the fresh VPS and a deploy key that
ship will install on the host:

```bash
test -f "$HOME/.ssh/ship-deploy" || \
  ssh-keygen -q -t ed25519 -N '' -f "$HOME/.ssh/ship-deploy"
test -f "$HOME/.ssh/ship-deploy.pub" || \
  ssh-keygen -y -f "$HOME/.ssh/ship-deploy" > "$HOME/.ssh/ship-deploy.pub"
```

`~/.ssh/ship-deploy` is the key app commands use after host install.
Use the root/bootstrap key that is already registered with your VPS provider
for `~/.ssh/<root-key>` below.

## 3. Install the VPS host

Run this from your laptop against a fresh Ubuntu 24.04/26.04 VPS:

```bash
ship host install \
  --host <vps-ip> \
  --ssh-key ~/.ssh/<root-key>
```

The operator key is for human host recovery and rerunning host install. The
deploy key is what app commands use after install. By default, host install
uses `~/.ssh/ship-deploy.pub` for the deploy user and the VPS bootstrap
user's existing authorized key for the operator user.

`host install` accepts a new SSH host key for a never-seen VPS. If you rebuilt
a VPS at the same IP and SSH blocks because the host key changed, remove the
old remembered key and rerun the command:

```bash
ssh-keygen -R <vps-ip>
```

Host install is idempotent. Running it again is safe; unchanged hosts report
`changed 0 operations`.

Check the host through the deploy user:

```bash
ship host status --server deploy@<vps-ip>
```

## 4. Scaffold an app

For a simple PHP app:

```bash
mkdir api && cd api
ship init --template php \
  --name api \
  --server deploy@<vps-ip> \
  --host api.<vps-ip>.nip.io \
  --tls internal
```

The PHP template writes `public/index.php` because PHP's built-in server is
started with `-t public`; only files under that directory are web-visible.

Commit the generated project before deploy:

```bash
git init
git add .
git commit -m "initial ship app"
```

## 5. Configure and deploy it

```bash
ship check --env production
ship deploy --env production
ship status --env production
```

`check --env` is a local check. It validates the manifest, Git release identity,
static directories, Dockerfile shape, and lists required secrets with the exact
`secret set` commands. `deploy --env` does the remote read-only preflight and
hard-fails if required host secrets are missing. On first deploy, ship
prepares the app environment on the host before upload/build/routing starts.

Then hit Caddy on the VPS:

```bash
curl -k --resolve api.<vps-ip>.nip.io:443:<vps-ip> \
  https://api.<vps-ip>.nip.io/health

curl -k --resolve api.<vps-ip>.nip.io:443:<vps-ip> \
  https://api.<vps-ip>.nip.io/
```

The `--tls internal` scaffold uses Caddy's internal CA, so `curl -k` is
expected. For a real public domain that points to the VPS, omit `--tls
internal` and Caddy will use automatic public HTTPS.

## 6. Secret references

Secrets are stored on the VPS and injected during deploy. The manifest
references them as whole values. For example, an app that needs
`DATABASE_URL` would declare:

```toml
[vars]
DATABASE_URL = "@secret:DATABASE_URL"
```

Then write the value and redeploy:

```bash
printf '%s' "$DATABASE_URL" | ship secret set DATABASE_URL --env production
ship secret list --env production
ship deploy --env production
```

`secret list` shows key names only, never values.

## 7. Clean up

To remove this app environment and its host-side app state:

```bash
ship destroy --env production --confirm api --purge
```

`--purge` removes runtime state, identity, static releases, data, and secrets
for this app env. It does not delete backups.

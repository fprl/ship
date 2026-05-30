# Getting Started

This is the shortest path from a fresh Ubuntu VPS to a deployed app.

## 1. Install the local CLI

Download a release binary for the machine where you run deploy commands:

```bash
VERSION=v0.7.0
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$OS" in
  darwin|linux) ;;
  *) echo "unsupported OS: $OS" >&2; exit 1 ;;
esac
case "$ARCH" in
  x86_64|amd64) ARCH=amd64 ;;
  arm64|aarch64) ARCH=arm64 ;;
  *) echo "unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
ASSET="simple-vps-$OS-$ARCH"

mkdir -p "$HOME/.local/bin"
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  gh release download "$VERSION" --repo fprl/simple-vps \
    --pattern SHA256SUMS \
    --pattern "$ASSET" \
    --clobber
else
  curl -fsSL -O "https://github.com/fprl/simple-vps/releases/download/$VERSION/SHA256SUMS"
  curl -fsSL -O "https://github.com/fprl/simple-vps/releases/download/$VERSION/$ASSET"
fi
if command -v shasum >/dev/null 2>&1; then
  grep "  $ASSET$" SHA256SUMS | shasum -a 256 -c -
else
  grep "  $ASSET$" SHA256SUMS | sha256sum -c -
fi
install -m 0755 "$ASSET" "$HOME/.local/bin/simple-vps"
```

Make sure `~/.local/bin` is on `PATH`:

```bash
simple-vps version
```

If release assets are private, authenticate `gh` first. The curl fallback is
only for public assets.

## 2. Prepare SSH keys

You need a root/bootstrap key for the fresh VPS and a deploy key that
simple-vps will install on the host:

```bash
ssh-keygen -q -t ed25519 -N '' -f "$HOME/.ssh/simple-vps-deploy"
ssh-keygen -R <vps-ip>
ssh-keyscan -T 10 -t ed25519,rsa,ecdsa <vps-ip> >> "$HOME/.ssh/known_hosts"
```

`~/.ssh/simple-vps-deploy` is the key app commands use after host install.
Use the root/bootstrap key that is already registered with your VPS provider
for `~/.ssh/<root-key>` below.

## 3. Install the VPS host

Run this from your laptop against a fresh Ubuntu 24.04/26.04 VPS:

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

SIMPLE_VPS_VERSION="$VERSION" ./install.sh \
  --host <vps-ip> \
  --ssh-key ~/.ssh/<root-key> \
  --operator-ssh-public-key-file ~/.ssh/<root-key>.pub \
  --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub \
  --yes
```

The operator key is for human host recovery and rerunning host install. The
deploy key is what app commands use after install.

If release assets are private, authenticate `gh` before downloading the
installer and export `SIMPLE_VPS_RELEASE_TOKEN`, `GH_TOKEN`, or `GITHUB_TOKEN`
before running it.

The installer converges the host. Running it again is safe; unchanged hosts
report `changed 0 operations`.

Check the host through the deploy user:

```bash
SIMPLE_VPS_SSH_KEY="$(cat ~/.ssh/simple-vps-deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <vps-ip> 2>/dev/null)" \
  simple-vps host status --server deploy@<vps-ip>
```

## 4. Scaffold an app

For a simple PHP app:

```bash
mkdir api && cd api
simple-vps init --template php \
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
git commit -m "initial simple-vps app"
```

## 5. Configure and deploy it

Use the deploy key for app commands:

```bash
export SIMPLE_VPS_SSH_KEY="$(cat ~/.ssh/simple-vps-deploy)"
export SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <vps-ip> 2>/dev/null)"

simple-vps check --env production
simple-vps setup --env production
simple-vps deploy --env production
simple-vps status --env production
```

`check --env` is a local check. It validates the manifest, Git release identity,
static directories, Dockerfile shape, and lists required secrets with the exact
`secret set` commands. `deploy --env` does the remote read-only preflight and
hard-fails if required host secrets are missing.

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
printf '%s' "$DATABASE_URL" | simple-vps secret set DATABASE_URL --env production
simple-vps secret list --env production
simple-vps deploy --env production
```

`secret list` shows key names only, never values.

## 7. Clean up

To remove this app environment and its host-side app state:

```bash
simple-vps destroy --env production --confirm api --purge
```

`--purge` removes runtime state, identity, static releases, data, and secrets
for this app env. It does not delete backups.

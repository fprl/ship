# Real-box smoke runbook

The fake-VPS smoke (`make fake-vps-smoke`, `make fake-vps-install-smoke`)
proves simple-vps's internal shape is consistent against fake Podman
and fake Caddy. This runbook drives the same path against a real
Ubuntu 24.04 VPS with real Podman and real Caddy. Authored from a
live smoke session — every command below was actually run, and every
surprise was filed in [smoke-real-box-results.md](smoke-real-box-results.md).

Run this end-to-end after any change that touches the install path,
the helper-side `app apply` / `app setup-env` verbs, or the Caddy
fragment / Podman networking shape. The fake smoke catches a lot but
not everything — finding 1 (`ufw --force allow`) and finding 3 (UFW
blocking Podman bridge DNS) were both invisible to it.

## 0. Prereqs

- Fresh Ubuntu 24.04 VPS, public IPv4, root SSH from the laptop with
  a known key. Hetzner CX22 (4 GiB RAM, 80 GB disk) is the smallest
  thing that comfortably runs Caddy + a real app container.
- DNS hostname `smoke.<your-domain>` pointing at the VPS IP if you
  want real TLS via Let's Encrypt. **Routing alone works without DNS** —
  curl with a `Host:` header reaches Caddy on port 443 (Caddy auto-
  redirects 80 → 443, so plain HTTP is not the test). Use `tls
  internal` in the fragment for self-signed certs during the smoke;
  see finding 6.
- `simple-vps` built locally:

  ```sh
  make clean && make build
  ```

  `make clean` matters — `prepareGoHelperBinaries` prefers stale
  `dist/` over rebuilding (see PR #30), so always build fresh before
  a smoke run.

- Operator and deploy SSH keys generated for the smoke:

  ```sh
  mkdir -p /tmp/simple-vps-smoke-keys
  ssh-keygen -q -t ed25519 -N '' -f /tmp/simple-vps-smoke-keys/operator
  ssh-keygen -q -t ed25519 -N '' -f /tmp/simple-vps-smoke-keys/deploy
  ```

## 1. Host install

```sh
./dist/simple-vps host install \
  --mode remote \
  --host <IP> \
  --bootstrap-user root \
  --ssh-key ~/.ssh/<root-key> \
  --operator-ssh-public-key-file /tmp/simple-vps-smoke-keys/operator.pub \
  --deploy-ssh-public-key-file /tmp/simple-vps-smoke-keys/deploy.pub \
  --timezone UTC --locale en_US.UTF-8 \
  --no-tailscale --no-cloudflare-tunnel --no-litestream
```

Expected output ends with `==> Provisioning complete` and `Apply
<ID> changed N operations`. If you see `simple-vps: error: ...`
instead, capture the stderr line and add it to results.md.

After install, verify (over SSH as root):

```sh
systemctl is-active caddy           # → active
podman ps                            # → caddy Up, no app yet
podman network ls                    # → ingress, podman
cat /etc/caddy/Caddyfile             # → "import conf.d/*.caddy"
ls /etc/caddy/conf.d/                # → empty
cat /etc/simple-vps/host.json \
  | jq '.meta.last_apply.status'     # → "ok"
```

Verify the public host read surface from the laptop:

```sh
SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  ./dist/simple-vps host status --json --server deploy@<IP> | jq .

SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  ./dist/simple-vps host doctor --json --server deploy@<IP> | jq .
```

## 2. Build the test app

```sh
mkdir -p /tmp/simple-vps-smoke-app && cd /tmp/simple-vps-smoke-app

cat > Dockerfile <<'EOF'
FROM docker.io/library/nginx:alpine
RUN printf 'server {\n  listen 3000;\n  location = /health { add_header Content-Type text/plain; return 200 "ok"; }\n  location = / { add_header Content-Type text/plain; return 200 "smoke-ok-nginx"; }\n  location / { return 404; }\n}\n' > /etc/nginx/conf.d/default.conf
EXPOSE 3000
EOF

cat > simple-vps.toml <<'EOF'
name = "hello"

[env.production]
server = "deploy@<IP>"

[env.production.env]
SMOKE_SECRET = "@secret:smoke_key"

[services.web]
port = 3000
healthcheck = "/health"

# `--read-only` is on by default per ADR-0005 §7. Most stock images
# (nginx, Apache httpd, postgres, ...) need writable scratch beyond
# `/tmp:64m`. Declare the extra mounts here. Keys are absolute paths;
# values match `^[1-9][0-9]*(k|m|g)$`. The provisioner adds mode=1777
# so the per-env container user can actually write.
[services.web.tmpfs]
"/var/cache/nginx" = "32m"
"/var/run" = "16m"

[routes.app]
host = "smoke.<your-domain>"
type = "proxy"
service = "web"
tls = "internal"  # self-signed cert; drop or set to "auto" once DNS resolves
EOF

git init -q
git config user.email smoke@example.com
git config user.name "Smoke"
git add . && git commit -q -m "fixture"
```

`nginx:alpine` is the smallest image that:

- Has a stateless HTTP server.
- Has a well-known set of writable paths (`/var/cache/nginx`,
  `/var/run`) that we cover with manifest tmpfs entries.
- Exercises the `--read-only` + tmpfs combination, so a broken
  combination of the two would surface here before a real user hits
  it. (Earlier smokes used `python:3-alpine` because it didn't need
  any writable rootfs paths — fine as a sanity check, but doesn't
  exercise tmpfs.)

If you swap it for a different image, check what paths the image
writes to at startup and add them under `[services.<name>.tmpfs]`.

## 3. Setup and deploy

```sh
cd /tmp/simple-vps-smoke-app

SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps setup production

# Verify on the box (over SSH as root):
#   getent passwd app-hello-production         # → uid:gid created
#   ls /var/apps/hello/production/             # → shared/
#   podman network ls                           # → app-hello-production

printf 'smoke-secret-value' | \
SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps secret put production smoke_key

SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps secret list --json production | jq .

SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps deploy production
```

Expected last line: `Deployed hello (production) at <sha>`. If the
deploy errors with `wget: bad address`, the host installer didn't
write the UFW podman bridge rules — re-install with a build that
includes PR #33 (`addPodmanHostBaseline`).

Verify the app read surface:

```sh
SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps status --json production | jq .

SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps logs production web | tail -20
```

The fixture sets `tls = "internal"`, so the Caddy fragment lands as:

```
"smoke.<your-domain>" {
    tls internal
    reverse_proxy http://app-hello-production-web:3000
}
```

Self-signed cert, no ACME, no DNS dependency. Switch to `tls =
"auto"` (or drop the line — `auto` is the default) once DNS resolves
to the host.

## 4. Curl through Caddy — the actual test

```sh
curl -k -sS \
  --resolve smoke.<your-domain>:443:<IP> \
  -w "HTTP %{http_code}\n" \
  https://smoke.<your-domain>/health
```

Expected: `HTTP 200` + body `ok`.

```sh
curl -k -sS \
  --resolve smoke.<your-domain>:443:<IP> \
  -w "HTTP %{http_code}\n" \
  https://smoke.<your-domain>/
```

Expected: `HTTP 200` + body `smoke-ok-nginx`.

These two responses prove the full path:

```
your curl
  └→ HTTPS to <IP>:443
       └→ Caddy container (on `ingress`, self-signed via `tls internal`)
            └→ aardvark-dns resolves `app-hello-production-web`
                 └→ Podman bridge → app container
                      └→ nginx serves /health → 200 ok
```

## 5. Teardown

If the VPS is single-use for this smoke, just delete it from the
provider console.

If you're reusing the box, use the public teardown path:

```sh
SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps destroy production --confirm hello --purge

SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps status --json production | jq .
```

Expected destroy output names the removed container, removed route, and
purged secrets. Expected status after destroy has an empty `services` array.

# Real-box smoke runbook

For release-candidate validation, use the scripted smoke first:

```sh
scripts/release-smoke.sh --version v0.7.0 --host <IP>
```

This runbook is the lower-level debugging path when the scripted smoke fails
or when you need to inspect the host between steps.

The fake-VPS smoke (`make fake-vps-smoke`, `make fake-vps-install-smoke`)
proves simple-vps's internal shape is consistent against fake Podman
and fake Caddy. This runbook drives the same path against a real
Ubuntu 24.04/26.04 VPS with real Podman and real Caddy. Authored from a
live smoke session; every command below was actually run.

Run this end-to-end after any change that touches the install path,
the helper-side `app apply` / `app setup-env` verbs, or the Caddy
fragment / Podman networking shape. The fake smoke catches a lot but
not everything: it cannot prove host firewall behavior, Podman bridge DNS, or
the real Caddy container lifecycle.

## 0. Prereqs

- Fresh Ubuntu 24.04 or 26.04 VPS, public IPv4, root SSH from the laptop with
  a known key. Hetzner CX22 (4 GiB RAM, 80 GB disk) is the smallest
  thing that comfortably runs Caddy + a real app container.
- DNS hostname `smoke.<your-domain>` pointing at the VPS IP if you
  want real TLS via Let's Encrypt. **Routing alone works without DNS** —
  curl with a `Host:` header reaches Caddy on port 443 (Caddy auto-
  redirects 80 → 443, so plain HTTP is not the test). Use `tls
  internal` in the fragment for self-signed certs during the smoke.
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

- If the VPS was rebuilt at the same IP, refresh the laptop's SSH host key
  entry before running remote install:

  ```sh
  ssh-keygen -R <IP>
  ssh-keyscan -T 10 -t ed25519,rsa,ecdsa <IP> >> ~/.ssh/known_hosts
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
  --no-tailscale --no-cloudflare-tunnel
```

Expected output ends with `==> Provisioning complete` and `Apply
<ID> changed N operations`. If you see `simple-vps: error: ...`
instead, capture the stderr line in the release notes or issue you are working.

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

For release-candidate validation, also run the example matrix in
[release-checklist.md](release-checklist.md): Hono/Bun, plain PHP,
static-only, and mixed API/static routes. The single Python fixture below is
the smallest low-level repro when debugging a deploy-path failure.

## 2. Build the test app

```sh
mkdir -p /tmp/simple-vps-smoke-app && cd /tmp/simple-vps-smoke-app

cat > server.py <<'EOF'
from http.server import BaseHTTPRequestHandler, HTTPServer
import os

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
            return
        if self.path == "/":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(("smoke-ok:" + os.environ.get("SMOKE_SECRET", "missing")).encode())
            return
        self.send_response(404)
        self.end_headers()

HTTPServer(("0.0.0.0", 3000), Handler).serve_forever()
EOF

cat > Dockerfile <<'EOF'
FROM docker.io/library/python:3.12-alpine
WORKDIR /app
COPY server.py .
EXPOSE 3000
CMD ["python", "/app/server.py"]
EOF

cat > simple-vps.toml <<'EOF'
name = "hello"

[env.production]
server = "deploy@<IP>"

[vars]
SMOKE_SECRET = "@secret:smoke_key"

[processes.web]
port = 3000
health = "/health"
resources = { memory = "256m", cpus = 0.5 }

[routes.app]
host = "smoke.<your-domain>"
process = "web"
tls = "internal"  # self-signed cert; drop or set to "auto" once DNS resolves
EOF

git init -q
git config user.email smoke@example.com
git config user.name "Smoke"
git add . && git commit -q -m "fixture"
```

This fixture keeps the image boring: one Dockerfile, one web process, one
health path, one secret reference, and no image-specific writable-path knobs.

## 3. Setup and deploy

```sh
cd /tmp/simple-vps-smoke-app

SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps setup --env production

# Verify on the box (over SSH as root):
#   test -d /var/apps/hello.production/data
#   jq . /var/apps/hello.production/simple-vps.json
#   podman network ls                           # includes the derived infra_id

printf 'smoke-secret-value' | \
SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps secret set smoke_key --env production

SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps secret list --json --env production | jq .

SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps deploy --env production
```

Expected last line: `Deployed hello (production) at <sha>`. If the
deploy errors with `wget: bad address`, the host installer didn't
write the UFW podman bridge rules — re-install with a build that
includes PR #33 (`addPodmanHostBaseline`).

Verify the app read surface:

```sh
SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps status --json --env production | jq .

SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps logs web --env production | tail -20
```

The fixture sets `tls = "internal"`, so the Caddy fragment lands as:

```
"smoke.<your-domain>" {
    tls internal
    reverse_proxy http://<derived-infra-id>-web-<release>:3000
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

Expected: `HTTP 200` + body `smoke-ok:smoke-secret-value`.

These two responses prove the full path:

```
your curl
  └→ HTTPS to <IP>:443
           └→ Caddy container (on `ingress`, self-signed via `tls internal`)
            └→ aardvark-dns resolves the versioned web container
                 └→ Podman bridge → app container
                      └→ Python app serves /health → 200 ok
```

## 5. Teardown

If the VPS is single-use for this smoke, just delete it from the
provider console.

If you're reusing the box, use the public teardown path:

```sh
SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps destroy --env production --confirm hello --purge

SIMPLE_VPS_SSH_KEY="$(cat /tmp/simple-vps-smoke-keys/deploy)" \
SIMPLE_VPS_KNOWN_HOSTS="$(ssh-keyscan -t ed25519 -H <IP> 2>/dev/null)" \
  /path/to/simple-vps/dist/simple-vps status --json --env production | jq .
```

Expected destroy output names the removed container, removed route, and
purged secrets. Expected status after destroy has an empty `processes` array.

## 6. Example Matrix

For release DX hardening, also run the checked-in example matrix after the
host has the current helper:

```sh
make build build-linux
./dist/simple-vps host install --host <IP> --bootstrap-user root --ssh-key ~/.ssh/hetzner \
  --operator-user admin --deploy-user deploy \
  --operator-ssh-public-key-file ~/.ssh/hetzner.pub \
  --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub \
  --ingress public --admin public-ssh --no-tailscale --no-cloudflare-tunnel --yes
scripts/example-matrix-smoke.sh --host <IP> --client ./dist/simple-vps
```

The May 30, 2026 example-matrix run against `128.140.3.159` passed PHP, Hono/Bun,
mixed API plus static docs, and real `astro build` static deploys. Host doctor
was healthy before the run, and final `app list --json` was empty after manual
cleanup verification.

Issue found: the matrix script previously resolved relative `--client` paths
after `cd` into each example and ignored destroy failures during success
cleanup. The script now resolves the client path once up front and fails the
normal success path if cleanup cannot destroy deployed example envs.

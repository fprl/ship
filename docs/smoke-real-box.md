# Real-box smoke runbook

Use this when fake-VPS coverage is green but you need to prove the install,
Caddy, Podman, firewall, SSH, and deploy path on a real Ubuntu VPS.

The normal user path is [getting-started.md](getting-started.md). This runbook
uses temp keys, `SHIP_SSH_KEY`, and a locally built `dist/ship` binary so a
release candidate can be inspected between steps.

## 0. Prereqs

- Fresh Ubuntu 24.04 or 26.04 VPS with public IPv4.
- Root/bootstrap SSH access from the laptop.
- Optional DNS hostname `smoke.<your-domain>` pointing at the VPS IP. Without
  DNS, use `--tls internal` and `curl --resolve`.
- Local release candidate:

```sh
make clean
make build
```

- Operator and deploy keys for the smoke:

```sh
mkdir -p /tmp/ship-smoke-keys
ssh-keygen -q -t ed25519 -N '' -f /tmp/ship-smoke-keys/operator
ssh-keygen -q -t ed25519 -N '' -f /tmp/ship-smoke-keys/deploy
```

If the VPS was rebuilt at the same IP, rerun setup to re-establish ship's
host-key pin. Do not edit `~/.ssh/known_hosts`; ship uses
`~/.config/ship/known_hosts`.

```sh
./dist/ship box setup <IP>
```

## 1. Converge the box

```sh
./dist/ship box setup <IP> \
  --bootstrap-user root \
  --ssh-key ~/.ssh/<root-key> \
  --operator-ssh-public-key-file /tmp/ship-smoke-keys/operator.pub \
  --deploy-ssh-public-key-file /tmp/ship-smoke-keys/deploy.pub
```

Expected output ends with `==> Provisioning complete` and next steps containing
`ship box doctor <IP>`.

Verify from the laptop:

```sh
SHIP_SSH_KEY="$(cat /tmp/ship-smoke-keys/deploy)" \
  ./dist/ship box doctor <IP> --json
```

```sh
SHIP_SSH_KEY="$(cat /tmp/ship-smoke-keys/deploy)" \
  ./dist/ship box ls <IP> --json
```

Useful host checks over root SSH:

```sh
systemctl is-active caddy
podman ps
podman network ls
cat /etc/caddy/Caddyfile
ls /etc/caddy/conf.d/
```

## 2. Create the smoke app

```sh
mkdir -p /tmp/ship-smoke-app
cd /tmp/ship-smoke-app
```

```sh
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
```

```sh
cat > Dockerfile <<'EOF'
FROM docker.io/library/python:3.12-alpine
WORKDIR /app
COPY server.py .
EXPOSE 3000
CMD ["python", "/app/server.py"]
EOF
```

```sh
cat > ship.toml <<'EOF'
name = "hello"
box = "<IP>"
probe = "/health"

[env]
SMOKE_SECRET = "@secret"

[processes]
web = { port = 3000, resources = { memory = "256m", cpus = 0.5 } }

[routes]
"smoke.<your-domain>" = "web"
EOF
```

```sh
git init -q
git config user.email smoke@example.com
git config user.name "Smoke"
git add .
git commit -q -m "fixture"
```

## 3. Deploy

```sh
printf 'smoke-secret-value' | \
SHIP_SSH_KEY="$(cat /tmp/ship-smoke-keys/deploy)" \
  /path/to/ship/dist/ship secret set SMOKE_SECRET
```

```sh
SHIP_SSH_KEY="$(cat /tmp/ship-smoke-keys/deploy)" \
  /path/to/ship/dist/ship secret ls --json
```

```sh
SHIP_SSH_KEY="$(cat /tmp/ship-smoke-keys/deploy)" \
  /path/to/ship/dist/ship --tls internal
```

Expected stderr shape:

```text
preflight 0.4s
build 6.2s
release 1.1s
probe ok
live
```

Expected stdout shape:

```text
https://smoke.<your-domain>
```

If the route host does not resolve to the box yet, stderr should include:

```text
warning: A smoke.<your-domain> → <IP>
```

## 4. Verify through Caddy

The manifest route plus `--tls internal` gives a self-signed cert, so `curl -k`
is expected.

```sh
curl -k -sS \
  --resolve smoke.<your-domain>:443:<IP> \
  -w "HTTP %{http_code}\n" \
  https://smoke.<your-domain>/health
```

Expected body and status: `okHTTP 200`.

```sh
curl -k -sS \
  --resolve smoke.<your-domain>:443:<IP> \
  -w "HTTP %{http_code}\n" \
  https://smoke.<your-domain>/
```

Expected body and status: `smoke-ok:smoke-secret-valueHTTP 200`.

Inspect the public read surfaces:

```sh
SHIP_SSH_KEY="$(cat /tmp/ship-smoke-keys/deploy)" \
  /path/to/ship/dist/ship status --json
```

```sh
SHIP_SSH_KEY="$(cat /tmp/ship-smoke-keys/deploy)" \
  /path/to/ship/dist/ship logs web --tail 20
```

## 5. Failure checks

Break the probe port in `ship.toml`, commit, and deploy again. The deploy should
fail, the old route should keep serving, and `ship why` should explain the
probe failure.

```sh
SHIP_SSH_KEY="$(cat /tmp/ship-smoke-keys/deploy)" \
  /path/to/ship/dist/ship why
```

Expected output includes:

```text
failing step: probe
traffic: old release
next: fix the process port or probe path in ship.toml, then ship
```

## 6. Teardown

For a disposable smoke VPS, delete it from the provider.

For a reused box, remove the app and all of its environments:

```sh
SHIP_SSH_KEY="$(cat /tmp/ship-smoke-keys/deploy)" \
  /path/to/ship/dist/ship box rm hello <IP> --confirm hello
```

```sh
SHIP_SSH_KEY="$(cat /tmp/ship-smoke-keys/deploy)" \
  /path/to/ship/dist/ship box ls <IP> --json
```

## 7. Example matrix

After this smoke passes, run the checked-in examples against the same box by
editing each `ship.toml` to use `box = "<IP>"` and a smoke route host,
then following the example README.

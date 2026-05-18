#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
image="simple-deploy-fake-vps:local"
tmp="$(mktemp -d)"
container=""

cleanup() {
  if [[ "${KEEP_FAKE_VPS:-0}" == "1" ]]; then
    echo "keeping fake VPS container: $container"
    echo "keeping fake VPS temp dir: $tmp"
    return
  fi
  if [[ -n "$container" ]]; then
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmp"
}
trap cleanup EXIT

docker build -f "$repo_root/packages/simple-deploy/tests/fake-vps/Dockerfile" -t "$image" "$repo_root"
container="$(docker run -d -p 127.0.0.1::22 "$image")"

ssh-keygen -q -t ed25519 -N "" -f "$tmp/id_ed25519"
docker exec -i "$container" bash -lc "cat > /home/admin/.ssh/authorized_keys && chown admin:admin /home/admin/.ssh/authorized_keys && chmod 600 /home/admin/.ssh/authorized_keys" < "$tmp/id_ed25519.pub"

port="$(docker port "$container" 22/tcp | sed 's/.*://')"
mkdir -p "$tmp/home/.ssh"
cat > "$tmp/home/.ssh/config" <<EOF
Host fake-vps
  HostName 127.0.0.1
  Port $port
  User admin
  IdentityFile $tmp/id_ed25519
  IdentitiesOnly yes
  BatchMode yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
  LogLevel ERROR
EOF
chmod 600 "$tmp/home/.ssh/config"

mkdir -p "$tmp/bin"
host_ssh="$(command -v ssh)"
cat > "$tmp/bin/ssh" <<EOF
#!/usr/bin/env bash
exec "$host_ssh" -F "$tmp/home/.ssh/config" "\$@"
EOF
chmod 755 "$tmp/bin/ssh"
export PATH="$tmp/bin:$PATH"

ssh_ready=0
for _ in {1..30}; do
  if ssh fake-vps true >/dev/null 2>&1; then
    ssh_ready=1
    break
  fi
  sleep 1
done
if [[ "$ssh_ready" != 1 ]]; then
  echo "fake VPS ssh did not become ready" >&2
  exit 1
fi

app="$tmp/app"
mkdir -p "$app"
cat > "$app/package.json" <<'EOF'
{
  "name": "api",
  "version": "1.0.0",
  "scripts": {
    "start": "node server.js"
  }
}
EOF
cat > "$app/package-lock.json" <<'EOF'
{
  "name": "api",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "api",
      "version": "1.0.0"
    }
  }
}
EOF
cat > "$app/server.js" <<'EOF'
const http = require("http");
const port = Number(process.env.PORT || 3000);
http.createServer((req, res) => {
  if (req.url === "/health") {
    res.writeHead(200, { "content-type": "text/plain" });
    res.end("ok");
    return;
  }
  res.writeHead(200, { "content-type": "text/plain" });
  res.end("hello");
}).listen(port, "127.0.0.1");
EOF
cat > "$app/simple-deploy.toml" <<'EOF'
name = "api"

[env.production]
server = "fake-vps"
path = "/var/apps/api"
runtime = "node"

[services.web]
command = "node server.js"
port = 3000
healthcheck = "/health"

[routes.app]
host = "api.example.com"
type = "proxy"
service = "web"
EOF

git -C "$app" init -q
git -C "$app" config user.email smoke@example.com
git -C "$app" config user.name Smoke
git -C "$app" add .
git -C "$app" commit -q -m "fixture"

(cd "$app" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" setup production)
(cd "$app" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" deploy production)
ssh fake-vps test -L /var/apps/api/current
ssh fake-vps test -L /var/apps/api/current/db
ssh fake-vps curl -fsS http://127.0.0.1:3000/health >/dev/null
ssh fake-vps sudo simple-vps route list --json | grep -q '"host": "api.example.com"'

echo "fake VPS smoke passed"

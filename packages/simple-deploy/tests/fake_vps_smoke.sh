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

write_node_package() {
  local app_dir="$1"
  local name="$2"
  cat > "$app_dir/package.json" <<EOF
{
  "name": "$name",
  "version": "1.0.0",
  "scripts": {
    "start": "node server.js"
  }
}
EOF
  cat > "$app_dir/package-lock.json" <<EOF
{
  "name": "$name",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "$name",
      "version": "1.0.0"
    }
  }
}
EOF
}

write_server() {
  local app_dir="$1"
  local body="$2"
  cat > "$app_dir/server.js" <<EOF
const http = require("http");
const port = Number(process.env.PORT || 3000);
console.log("server:$body");
http.createServer((req, res) => {
  if (req.url === "/health") {
    res.writeHead(200, { "content-type": "text/plain" });
    res.end("ok");
    return;
  }
  res.writeHead(200, { "content-type": "text/plain" });
  res.end("$body");
}).listen(port, "127.0.0.1");
EOF
}

write_unhealthy_server() {
  local app_dir="$1"
  local body="$2"
  cat > "$app_dir/server.js" <<EOF
const http = require("http");
const port = Number(process.env.PORT || 3000);
console.log("server:$body");
http.createServer((req, res) => {
  if (req.url === "/health") {
    res.writeHead(500, { "content-type": "text/plain" });
    res.end("bad");
    return;
  }
  res.writeHead(200, { "content-type": "text/plain" });
  res.end("$body");
}).listen(port, "127.0.0.1");
EOF
}

commit_fixture() {
  local app_dir="$1"
  git -C "$app_dir" init -q
  git -C "$app_dir" config user.email smoke@example.com
  git -C "$app_dir" config user.name Smoke
  git -C "$app_dir" add .
  git -C "$app_dir" commit -q -m "fixture"
}

mode_a="$tmp/mode-a"
mkdir -p "$mode_a"
write_node_package "$mode_a" "api"
write_server "$mode_a" "mode-a"
cat > "$mode_a/simple-deploy.toml" <<'EOF'
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
commit_fixture "$mode_a"

(cd "$mode_a" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" setup production)
(cd "$mode_a" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" deploy production)
first_api_current="$(ssh fake-vps readlink -f /var/apps/api/current)"
ssh fake-vps test -L /var/apps/api/current
ssh fake-vps test -L /var/apps/api/current/db
ssh fake-vps curl -fsS http://127.0.0.1:3000/health >/dev/null
ssh fake-vps curl -fsS http://127.0.0.1:3000/ | grep -q '^mode-a$'
ssh fake-vps sudo simple-vps route list --json | grep -q '"host": "api.example.com"'
(cd "$mode_a" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" status production) | grep -q 'service web: active'
(cd "$mode_a" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" logs production web) | grep -q 'server:mode-a'

write_server "$mode_a" "mode-a-v2"
git -C "$mode_a" add server.js
git -C "$mode_a" commit -q -m "second fixture"
(cd "$mode_a" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" deploy production)
ssh fake-vps curl -fsS http://127.0.0.1:3000/ | grep -q '^mode-a-v2$'
(cd "$mode_a" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" rollback production)
test "$(ssh fake-vps readlink -f /var/apps/api/current)" = "$first_api_current"
ssh fake-vps curl -fsS http://127.0.0.1:3000/ | grep -q '^mode-a$'

write_unhealthy_server "$mode_a" "mode-a-bad"
git -C "$mode_a" add server.js
git -C "$mode_a" commit -q -m "bad fixture"
if (cd "$mode_a" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" deploy production); then
  echo "unhealthy deploy unexpectedly passed" >&2
  exit 1
fi
test "$(ssh fake-vps readlink -f /var/apps/api/current)" = "$first_api_current"
ssh fake-vps curl -fsS http://127.0.0.1:3000/ | grep -q '^mode-a$'

mode_b="$tmp/mode-b"
mkdir -p "$mode_b/public"
write_node_package "$mode_b" "web"
write_server "$mode_b" "mode-b"
printf 'asset\n' > "$mode_b/public/asset.txt"
cat > "$mode_b/simple-deploy.toml" <<'EOF'
name = "web"

[build]
command = "mkdir -p dist && cp server.js dist/server.js"
output = "dist"
include = ["public"]

[env.production]
server = "fake-vps"
path = "/var/apps/web"
runtime = "node"

[services.web]
command = "node server.js"
port = 3001
healthcheck = "/health"

[routes.app]
host = "web.example.com"
type = "proxy"
service = "web"
EOF
commit_fixture "$mode_b"

(cd "$mode_b" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" setup production)
(cd "$mode_b" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" deploy production)
ssh fake-vps curl -fsS http://127.0.0.1:3001/health >/dev/null
ssh fake-vps grep -q '^asset$' /var/apps/web/current/public/asset.txt
ssh fake-vps test -f /var/apps/web/current/package-lock.json
ssh fake-vps test ! -e /var/apps/web/current/simple-deploy.toml
ssh fake-vps sudo simple-vps route list --json | grep -q '"host": "web.example.com"'
(cd "$mode_b" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" destroy production --yes)
ssh fake-vps test -d /var/apps/web/shared
ssh fake-vps test -d /var/apps/web/releases
ssh fake-vps test ! -e /var/apps/web/current
if ssh fake-vps sudo simple-vps route list --json | grep -q '"app": "web"'; then
  echo "destroy left web routes behind" >&2
  exit 1
fi

mode_c="$tmp/mode-c"
mkdir -p "$mode_c"
write_server "$mode_c" "mode-c"
cat > "$mode_c/simple-deploy.toml" <<'EOF'
name = "bundle"

[build]
command = "mkdir -p dist && cp server.js dist/server.js"
output = "dist"
install = false

[env.production]
server = "fake-vps"
path = "/var/apps/bundle"
runtime = "node"

[services.web]
command = "node server.js"
port = 3002
healthcheck = "/health"

[routes.app]
host = "bundle.example.com"
type = "proxy"
service = "web"
EOF
commit_fixture "$mode_c"

(cd "$mode_c" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" setup production)
(cd "$mode_c" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" deploy production)
ssh fake-vps curl -fsS http://127.0.0.1:3002/health >/dev/null
ssh fake-vps test ! -e /var/apps/bundle/current/package.json
ssh fake-vps test ! -e /var/apps/bundle/current/simple-deploy.toml
ssh fake-vps sudo simple-vps route list --json | grep -q '"host": "bundle.example.com"'
(cd "$mode_c" && bun run "$repo_root/packages/simple-deploy/src/cli.ts" destroy production --purge --yes --confirm bundle)
ssh fake-vps test ! -e /var/apps/bundle
if ssh fake-vps sudo simple-vps route list --json | grep -q '"app": "bundle"'; then
  echo "purge left bundle routes behind" >&2
  exit 1
fi

echo "fake VPS smoke passed"

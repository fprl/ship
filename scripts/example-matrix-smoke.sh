#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage:
  scripts/example-matrix-smoke.sh --host 128.140.3.159 [--client ./dist/ship]

Deploys the checked-in examples against an already installed ship host:
  - php-plain
  - hono-bun-api
  - mixed-api-docs
  - astro-static (runs npm install + npm run build locally)
  - django-sqlite when SHIP_EXAMPLE_MATRIX_INCLUDE_DJANGO=1

Environment:
  SHIP_DEPLOY_SSH_KEY
      defaults to ~/.ssh/ship-deploy
  SHIP_EXAMPLE_MATRIX_WORKDIR
      defaults to a temp dir under /tmp
  SHIP_EXAMPLE_MATRIX_SKIP_DESTROY=1
      leave deployed example envs on the host for debugging
  SHIP_EXAMPLE_MATRIX_INCLUDE_DJANGO=1
      include the heavier Django + SQLite + migration example
  SHIP_EXAMPLE_MATRIX_REFRESH_KNOWN_HOSTS=0
      do not refresh ~/.ssh/known_hosts for the disposable VPS
USAGE
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
client="${SHIP_CLIENT:-$repo_root/dist/ship}"
host="${SHIP_EXAMPLE_MATRIX_HOST:-}"
workdir="${SHIP_EXAMPLE_MATRIX_WORKDIR:-$(mktemp -d /tmp/ship-example-matrix-XXXXXX)}"
deploy_key="${SHIP_DEPLOY_SSH_KEY:-$HOME/.ssh/ship-deploy}"
skip_destroy="${SHIP_EXAMPLE_MATRIX_SKIP_DESTROY:-0}"
include_django="${SHIP_EXAMPLE_MATRIX_INCLUDE_DJANGO:-0}"
refresh_known_hosts="${SHIP_EXAMPLE_MATRIX_REFRESH_KNOWN_HOSTS:-1}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)
      [[ $# -ge 2 ]] || die "--host requires a value"
      host="$2"
      shift 2
      ;;
    --client)
      [[ $# -ge 2 ]] || die "--client requires a value"
      client="$2"
      shift 2
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

[[ -n "$host" ]] || die "--host or SHIP_EXAMPLE_MATRIX_HOST is required"
if [[ "$client" != /* ]]; then
  client="$(cd "$(dirname "$client")" && pwd)/$(basename "$client")"
fi
[[ -x "$client" ]] || die "client binary is not executable: $client"
[[ -r "$deploy_key" ]] || die "deploy SSH key not readable: $deploy_key"

require_cmd curl
require_cmd git
require_cmd perl
require_cmd ssh-keygen
require_cmd ssh-keyscan

server="deploy@$host"
if [[ "$refresh_known_hosts" == "1" ]]; then
  ssh-keygen -R "$host" >/dev/null 2>&1 || true
  ssh-keyscan -T 10 -t ed25519,rsa,ecdsa "$host" >>"$HOME/.ssh/known_hosts"
fi
if [[ "$deploy_key" != "$HOME/.ssh/ship-deploy" ]]; then
  SHIP_SSH_KEY="$(cat "$deploy_key")"
  export SHIP_SSH_KEY
fi

mkdir -p "$workdir"
log="$workdir/example-matrix-smoke.log"
deployed=()

cleanup() {
  if [[ "$skip_destroy" == "1" ]]; then
    return
  fi
  local best_effort="${1:-0}"
  local failed=0
  for item in "${deployed[@]:-}"; do
    local_app="${item%%|*}"
    local_dir="${item#*|}"
    if ! "$client" destroy --config "$local_dir/ship.toml" --env production --confirm "$local_app" --purge >>"$log" 2>&1; then
      failed=1
      printf 'cleanup failed for %s; see %s\n' "$local_app" "$log" >&2
    fi
  done
  if [[ "$best_effort" != "1" && "$failed" != "0" ]]; then
    return 1
  fi
}
trap 'cleanup 1' EXIT

patch_manifest() {
  local app="$1"
  local route_host="$2"
  SVPS_APP="$app" SVPS_SERVER="$server" SVPS_ROUTE_HOST="$route_host" perl -0pi -e '
    s/^name = "[^"]+"/name = "$ENV{SVPS_APP}"/m;
    s/server = "deploy\@example\.com"/server = "$ENV{SVPS_SERVER}"/g;
    s/host = "[^"]+"/host = "$ENV{SVPS_ROUTE_HOST}"/g;
    s/(host = "[^"]+"\n)(?!tls = )/${1}tls = "internal"\n/g;
  ' ship.toml
}

commit_example() {
  git init >/dev/null
  git config user.email smoke@example.com
  git config user.name Smoke
  git add .
  git commit -m "example smoke" >/dev/null
}

curl_route() {
  local route_host="$1"
  local path="$2"
  local output="$3"
  curl -ksS --resolve "$route_host:443:$host" "https://$route_host$path" -o "$output"
}

run_example() {
  local key="$1"
  local source="$2"
  local probe_path="$3"
  local expected="$4"
  local app="svps-${key}-$(date -u +%H%M%S)"
  local route_host="$app.$host.nip.io"
  local app_dir="$workdir/$app"
  local body="$workdir/$app.body"

  printf '\n== %s ==\n' "$key"
  rm -rf "$app_dir"
  mkdir -p "$app_dir"
  cp -R "$repo_root/examples/$source/." "$app_dir/"
  cd "$app_dir"
  patch_manifest "$app" "$route_host"

  if [[ "$key" == "astro" ]]; then
    require_cmd npm
    npm install --no-package-lock
    npm run build
  fi

  commit_example
  "$client" check --config "$app_dir/ship.toml" --env production
  "$client" setup --config "$app_dir/ship.toml" --env production
  deployed+=("$app|$app_dir")

  if [[ "$key" == "php" ]]; then
    printf 'matrix-secret' | "$client" secret set --config "$app_dir/ship.toml" APP_SECRET --env production
  fi
  if [[ "$key" == "django" ]]; then
    printf 'matrix-django-secret' | "$client" secret set --config "$app_dir/ship.toml" DJANGO_SECRET_KEY --env production
  fi

  "$client" deploy --config "$app_dir/ship.toml" --env production
  "$client" status --config "$app_dir/ship.toml" --env production
  curl_route "$route_host" "$probe_path" "$body"
  grep -q "$expected" "$body"
  printf '%s%s -> %s\n' "$route_host" "$probe_path" "$(tr -d '\n' <"$body")"
}

run_matrix() {
  printf 'example matrix workdir: %s\n' "$workdir"
  printf 'host: %s\n' "$host"
  "$client" host status --json --server "$server" >/dev/null
  run_example php php-plain / '"secret":"matrix-secret"'
  run_example hono hono-bun-api /health '^ok$'
  run_example mixed mixed-api-docs /docs/ 'docs-ok'
  run_example astro astro-static / 'static-ok'
  if [[ "$include_django" == "1" ]]; then
    run_example django django-sqlite /health '^ok$'
  fi
}

run_matrix > >(tee "$log") 2>&1

trap - EXIT
cleanup
printf '\nexample matrix smoke passed\n'
printf 'workdir: %s\n' "$workdir"
printf 'log: %s\n' "$log"

#!/usr/bin/env bash
set -euo pipefail

SHIP_VERSION="${SHIP_VERSION:-latest}"
SHIP_RELEASE_BASE_URL="${SHIP_RELEASE_BASE_URL:-https://github.com/fprl/ship/releases/download}"
SHIP_RELEASE_API_BASE_URL="${SHIP_RELEASE_API_BASE_URL:-https://api.github.com/repos/fprl/ship}"
SHIP_INSTALL_DIR="${SHIP_INSTALL_DIR:-$HOME/.local/bin}"

tmp_dir=""

usage() {
  cat <<'USAGE'
Usage:
  curl -fsSL https://github.com/fprl/ship/releases/latest/download/install.sh | bash

  install.sh [-v latest|v0.8.0] [--bin-dir ~/.local/bin]

Installs the ship CLI on this machine. It does not provision a VPS.
After this, run:

  test -f ~/.ssh/ship-deploy || ssh-keygen -q -t ed25519 -N '' -f ~/.ssh/ship-deploy
  test -f ~/.ssh/ship-deploy.pub || ssh-keygen -y -f ~/.ssh/ship-deploy > ~/.ssh/ship-deploy.pub
  ship box init <ssh-target>

Environment:
  SHIP_VERSION
      release tag to install, default latest resolved through the GitHub API
  SHIP_INSTALL_DIR
      install directory, default ~/.local/bin
  SHIP_RELEASE_TOKEN, GH_TOKEN, or GITHUB_TOKEN
      optional token for private release assets
USAGE
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

info() {
  printf '==> %s\n' "$*" >&2
}

cleanup() {
  if [[ -n "$tmp_dir" ]]; then
    rm -rf "$tmp_dir"
  fi
}
trap cleanup EXIT

while [[ $# -gt 0 ]]; do
  case "$1" in
    --version)
      [[ $# -ge 2 ]] || die "--version requires a value"
      SHIP_VERSION="$2"
      shift 2
      ;;
    -v)
      [[ $# -ge 2 ]] || die "-v requires a value"
      SHIP_VERSION="$2"
      shift 2
      ;;
    --bin-dir)
      [[ $# -ge 2 ]] || die "--bin-dir requires a value"
      SHIP_INSTALL_DIR="$2"
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

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "$1 is required"
}

platform_asset() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  case "$os" in
    darwin|linux) ;;
    *) die "unsupported OS: $os" ;;
  esac

  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) die "unsupported architecture: $arch" ;;
  esac

  printf 'ship-%s-%s\n' "$os" "$arch"
}

token() {
  printf '%s' "${SHIP_RELEASE_TOKEN:-${GH_TOKEN:-${GITHUB_TOKEN:-}}}"
}

curl_api() {
  local url="$1"
  local output="$2"
  local auth_token
  auth_token="$(token)"
  if [[ -n "$auth_token" ]]; then
    curl -fsSL \
      -H "Authorization: Bearer $auth_token" \
      -H "Accept: application/vnd.github+json" \
      "$url" \
      -o "$output"
  else
    curl -fsSL \
      -H "Accept: application/vnd.github+json" \
      "$url" \
      -o "$output"
  fi
}

resolve_version() {
  local release_json tag
  if [[ "$SHIP_VERSION" != "latest" ]]; then
    return 0
  fi
  require_cmd python3
  release_json="$tmp_dir/latest-release.json"
  if ! curl_api "${SHIP_RELEASE_API_BASE_URL%/}/releases/latest" "$release_json"; then
    die "could not resolve latest release via GitHub API; rerun with -v <release-tag>"
  fi
  tag="$(python3 - "$release_json" <<'PY'
import json
import sys

with open(sys.argv[1], "r", encoding="utf-8") as f:
    release = json.load(f)
print(release.get("tag_name", ""))
PY
)"
  [[ -n "$tag" ]] || die "GitHub API latest release response did not include tag_name; rerun with -v <release-tag>"
  SHIP_VERSION="$tag"
}

curl_download_quiet() {
  local url="$1"
  local output="$2"
  local auth_token
  auth_token="$(token)"
  if [[ -n "$auth_token" ]]; then
    curl -fsSL -H "Authorization: Bearer $auth_token" "$url" -o "$output" 2>/dev/null
  else
    curl -fsSL "$url" -o "$output" 2>/dev/null
  fi
}

download_via_github_api() {
  local asset_name="$1"
  local output="$2"
  local auth_token
  local release_json
  local asset_url

  auth_token="$(token)"
  [[ -n "$auth_token" ]] || return 1
  require_cmd python3

  release_json="$tmp_dir/release.json"
  curl_api "${SHIP_RELEASE_API_BASE_URL%/}/releases/tags/$SHIP_VERSION" "$release_json"

  asset_url="$(python3 - "$asset_name" "$release_json" <<'PY'
import json
import sys

name = sys.argv[1]
path = sys.argv[2]
with open(path, "r", encoding="utf-8") as f:
    release = json.load(f)
for asset in release.get("assets", []):
    if asset.get("name") == name:
        print(asset["url"])
        break
PY
)"
  [[ -n "$asset_url" ]] || die "release $SHIP_VERSION does not contain $asset_name"

  curl -fsSL \
    -H "Authorization: Bearer $auth_token" \
    -H "Accept: application/octet-stream" \
    "$asset_url" \
    -o "$output"
}

download_release_asset() {
  local asset_name="$1"
  local output="$2"
  local url
  url="${SHIP_RELEASE_BASE_URL%/}/$SHIP_VERSION/$asset_name"

  if curl_download_quiet "$url" "$output"; then
    return 0
  fi
  download_via_github_api "$asset_name" "$output" || die "download failed: $url"
}

verify_checksum() {
  local binary="$1"
  local asset_name="$2"
  local sums="$3"
  local expected actual

  expected="$(awk -v asset="$asset_name" '$2 == asset || $2 == "*" asset { print $1; exit }' "$sums")"
  [[ -n "$expected" ]] || die "SHA256SUMS does not contain $asset_name"

  if command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$binary" | awk '{ print $1 }')"
  elif command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$binary" | awk '{ print $1 }')"
  else
    die "shasum or sha256sum is required"
  fi

  [[ "$actual" == "$expected" ]] || die "checksum mismatch for $asset_name"
}

main() {
  local asset binary sums target resolved
  require_cmd curl
  require_cmd awk
  require_cmd install

  asset="$(platform_asset)"
  tmp_dir="$(mktemp -d)"
  resolve_version
  binary="$tmp_dir/ship"
  sums="$tmp_dir/SHA256SUMS"
  target="$SHIP_INSTALL_DIR/ship"

  info "Installing ship $SHIP_VERSION for $asset"
  download_release_asset "$asset" "$binary"
  download_release_asset "SHA256SUMS" "$sums"
  verify_checksum "$binary" "$asset" "$sums"

  mkdir -p "$SHIP_INSTALL_DIR"
  install -m 0755 "$binary" "$target"

  info "Installed $target"
  resolved="$(command -v ship 2>/dev/null || true)"
  if [[ "$resolved" != "$target" ]]; then
    if [[ -n "$resolved" ]]; then
      printf '%s\n' "Your shell currently resolves ship to:" >&2
      printf '%s\n' "  $resolved" >&2
    fi
    printf '%s\n' "Add this to your shell profile so this install wins first:" >&2
    printf '%s\n' "  export PATH=\"$SHIP_INSTALL_DIR:\$PATH\"" >&2
    printf '%s\n' "Run this now for the current shell:" >&2
    printf '%s\n' "  export PATH=\"$SHIP_INSTALL_DIR:\$PATH\"" >&2
  fi
  "$target" version
}

main "$@"

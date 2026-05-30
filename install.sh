#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SIMPLE_VPS_REPO_TARBALL_URL="${SIMPLE_VPS_REPO_TARBALL_URL:-https://github.com/fprl/simple-vps/archive/refs/heads/main.tar.gz}"
SIMPLE_VPS_BOOTSTRAP_DOWNLOAD="${SIMPLE_VPS_BOOTSTRAP_DOWNLOAD:-true}"
SIMPLE_VPS_BOOTSTRAPPED="${SIMPLE_VPS_BOOTSTRAPPED:-false}"
SIMPLE_VPS_BINARY_URL="${SIMPLE_VPS_BINARY_URL:-}"
SIMPLE_VPS_VERSION="${SIMPLE_VPS_VERSION:-v0.7.0}"
SIMPLE_VPS_RELEASE_BASE_URL="${SIMPLE_VPS_RELEASE_BASE_URL:-https://github.com/fprl/simple-vps/releases/download}"
SIMPLE_VPS_RELEASE_API_BASE_URL="${SIMPLE_VPS_RELEASE_API_BASE_URL:-https://api.github.com/repos/fprl/simple-vps}"

TMP_FILES=()

err() {
  printf 'Error: %s\n' "$*" >&2
}

info() {
  printf '==> %s\n' "$*" >&2
}

trap 'rm -rf "${TMP_FILES[@]}"' EXIT

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    err "Required command not found: $cmd"
    exit 1
  fi
}

platform_binary_name() {
  local os
  local arch

  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"

  case "$os" in
    darwin|linux)
      ;;
    *)
      err "Unsupported install host OS: $os"
      exit 1
      ;;
  esac

  case "$arch" in
    x86_64|amd64)
      arch="amd64"
      ;;
    arm64|aarch64)
      arch="arm64"
      ;;
    *)
      err "Unsupported install host architecture: $arch"
      exit 1
      ;;
  esac

  printf 'simple-vps-%s-%s\n' "$os" "$arch"
}

find_simple_vps_binary() {
  local platform_binary
  local candidate

  platform_binary="$(platform_binary_name)"
  for candidate in \
    "$SCRIPT_DIR/dist/$platform_binary" \
    "$SCRIPT_DIR/dist/simple-vps" \
    "$SCRIPT_DIR/simple-vps"; do
    if [[ -x "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done

  return 1
}

download_simple_vps_binary() {
  local binary_url="$SIMPLE_VPS_BINARY_URL"
  local sums_url=""
  local asset_name
  local output_dir
  local output_path
  local sums_path

  if [[ -z "$binary_url" ]]; then
    if [[ "$SIMPLE_VPS_BOOTSTRAP_DOWNLOAD" != "true" ]]; then
      return 1
    fi
    asset_name="$(platform_binary_name)"
    binary_url="${SIMPLE_VPS_RELEASE_BASE_URL%/}/$SIMPLE_VPS_VERSION/$asset_name"
    sums_url="${SIMPLE_VPS_RELEASE_BASE_URL%/}/$SIMPLE_VPS_VERSION/SHA256SUMS"
  else
    asset_name="$(basename "$binary_url")"
  fi

  if [[ -z "$binary_url" ]]; then
    return 1
  fi

  require_cmd curl
  output_dir="$(mktemp -d)"
  TMP_FILES+=("$output_dir")
  output_path="$output_dir/simple-vps"

  info "Downloading Simple VPS binary from $binary_url"
  if [[ -n "$sums_url" ]]; then
    download_release_asset "$asset_name" "$binary_url" "$output_path"
  else
    curl_download "$binary_url" "$output_path"
  fi
  if [[ -n "$sums_url" ]]; then
    sums_path="$output_dir/SHA256SUMS"
    download_release_asset "SHA256SUMS" "$sums_url" "$sums_path"
    verify_sha256 "$output_path" "$asset_name" "$sums_path"
  fi
  chmod 0755 "$output_path"
  printf '%s\n' "$output_path"
}

curl_download() {
  local url="$1"
  local output="$2"
  local token="${SIMPLE_VPS_RELEASE_TOKEN:-${GH_TOKEN:-${GITHUB_TOKEN:-}}}"

  if [[ -n "$token" ]]; then
    curl -fsSL -H "Authorization: Bearer $token" "$url" -o "$output"
  else
    curl -fsSL "$url" -o "$output"
  fi
}

download_release_asset() {
  local asset_name="$1"
  local browser_url="$2"
  local output="$3"
  local token="${SIMPLE_VPS_RELEASE_TOKEN:-${GH_TOKEN:-${GITHUB_TOKEN:-}}}"

  if curl_download_quiet "$browser_url" "$output"; then
    return 0
  fi

  if [[ -z "$token" ]]; then
    return 1
  fi

  download_release_asset_via_api "$asset_name" "$output"
}

curl_download_quiet() {
  local url="$1"
  local output="$2"
  local token="${SIMPLE_VPS_RELEASE_TOKEN:-${GH_TOKEN:-${GITHUB_TOKEN:-}}}"

  if [[ -n "$token" ]]; then
    curl -fsSL -H "Authorization: Bearer $token" "$url" -o "$output" 2>/dev/null
  else
    curl -fsSL "$url" -o "$output" 2>/dev/null
  fi
}

download_release_asset_via_api() {
  local asset_name="$1"
  local output="$2"
  local token="${SIMPLE_VPS_RELEASE_TOKEN:-${GH_TOKEN:-${GITHUB_TOKEN:-}}}"
  local release_json
  local asset_url

  require_cmd python3

  release_json="$(mktemp)"
  TMP_FILES+=("$release_json")

  curl -fsSL \
    -H "Authorization: Bearer $token" \
    -H "Accept: application/vnd.github+json" \
    "${SIMPLE_VPS_RELEASE_API_BASE_URL%/}/releases/tags/$SIMPLE_VPS_VERSION" \
    -o "$release_json"

  asset_url="$(python3 -c '
import json
import sys

asset_name = sys.argv[1]
release_path = sys.argv[2]
with open(release_path, "r", encoding="utf-8") as f:
    release = json.load(f)
for asset in release.get("assets", []):
    if asset.get("name") == asset_name:
        print(asset["url"])
        break
' "$asset_name" "$release_json")"

  if [[ -z "$asset_url" ]]; then
    err "Release $SIMPLE_VPS_VERSION does not contain $asset_name"
    exit 1
  fi

  curl -fsSL \
    -H "Authorization: Bearer $token" \
    -H "Accept: application/octet-stream" \
    "$asset_url" \
    -o "$output"
}

verify_sha256() {
  local file="$1"
  local asset_name="$2"
  local sums_file="$3"
  local expected
  local actual

  expected="$(awk -v asset="$asset_name" '$2 == asset || $2 == "*" asset { print $1; exit }' "$sums_file")"
  if [[ -z "$expected" ]]; then
    err "SHA256SUMS does not contain $asset_name"
    exit 1
  fi

  if command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$file" | awk '{print $1}')"
  elif command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$file" | awk '{print $1}')"
  else
    err "Cannot verify $asset_name: shasum or sha256sum is required"
    exit 1
  fi

  if [[ "$actual" != "$expected" ]]; then
    err "Checksum mismatch for $asset_name"
    err "Expected: $expected"
    err "Actual:   $actual"
    exit 1
  fi
}

build_simple_vps_binary() {
  local output_path="$SCRIPT_DIR/dist/simple-vps"

  if [[ ! -f "$SCRIPT_DIR/go.mod" ]]; then
    return 1
  fi
  if ! command -v go >/dev/null 2>&1; then
    return 1
  fi

  info "Building Simple VPS Go binary"
  mkdir -p "$SCRIPT_DIR/dist"
  go -C "$SCRIPT_DIR" build -trimpath -o "$output_path" .
  printf '%s\n' "$output_path"
}

run_host_installer() {
  local binary_path="$1"
  shift

  exec "$binary_path" host install "$@"
}

bootstrap_checkout() {
  local tmp_dir
  local source_dir
  local archive_path
  local installer_path

  if [[ "$SIMPLE_VPS_BOOTSTRAP_DOWNLOAD" != "true" ]]; then
    err "Required Simple VPS files were not found beside install.sh."
    err "Run from a checkout, or allow bootstrap download with SIMPLE_VPS_BOOTSTRAP_DOWNLOAD=true."
    exit 1
  fi

  if [[ "$SIMPLE_VPS_BOOTSTRAPPED" == "true" ]]; then
    err "Simple VPS bootstrap download completed, but required files are still missing."
    err "Check SIMPLE_VPS_REPO_TARBALL_URL: $SIMPLE_VPS_REPO_TARBALL_URL"
    exit 1
  fi

  require_cmd curl
  require_cmd tar
  require_cmd mktemp

  tmp_dir="$(mktemp -d)"
  source_dir="$tmp_dir/simple-vps"
  archive_path="$tmp_dir/simple-vps.tar.gz"
  mkdir -p "$source_dir"

  info "Simple VPS checkout not found beside install.sh."
  info "Downloading Simple VPS from $SIMPLE_VPS_REPO_TARBALL_URL"

  curl -fsSL "$SIMPLE_VPS_REPO_TARBALL_URL" -o "$archive_path"
  tar -xzf "$archive_path" -C "$source_dir" --strip-components=1

  installer_path="$source_dir/install.sh"
  if [[ ! -f "$installer_path" ]]; then
    err "Downloaded Simple VPS archive did not contain install.sh."
    exit 1
  fi

  info "Re-running installer from downloaded checkout."
  export SIMPLE_VPS_BOOTSTRAPPED=true
  exec "$installer_path" "$@"
}

main() {
  local binary_path=""

  if [[ -n "$SIMPLE_VPS_BINARY_URL" ]]; then
    if binary_path="$(download_simple_vps_binary)"; then
      run_host_installer "$binary_path" "$@"
    fi
  fi

  if binary_path="$(build_simple_vps_binary)"; then
    run_host_installer "$binary_path" "$@"
  fi

  if binary_path="$(find_simple_vps_binary)"; then
    run_host_installer "$binary_path" "$@"
  fi

  if binary_path="$(download_simple_vps_binary)"; then
    run_host_installer "$binary_path" "$@"
  fi

  bootstrap_checkout "$@"

  err "No Simple VPS binary found and Go is not installed."
  err "Install Go, run make build-release, or set SIMPLE_VPS_BINARY_URL to a platform binary."
  exit 1
}

main "$@"

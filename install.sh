#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SIMPLE_VPS_REPO_TARBALL_URL="${SIMPLE_VPS_REPO_TARBALL_URL:-https://github.com/fprl/simple-vps/archive/refs/heads/main.tar.gz}"
SIMPLE_VPS_BOOTSTRAP_DOWNLOAD="${SIMPLE_VPS_BOOTSTRAP_DOWNLOAD:-true}"
SIMPLE_VPS_BOOTSTRAPPED="${SIMPLE_VPS_BOOTSTRAPPED:-false}"
SIMPLE_VPS_BINARY_URL="${SIMPLE_VPS_BINARY_URL:-}"

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
  local output_dir
  local output_path

  if [[ -z "$SIMPLE_VPS_BINARY_URL" ]]; then
    return 1
  fi

  require_cmd curl
  output_dir="$(mktemp -d)"
  TMP_FILES+=("$output_dir")
  output_path="$output_dir/simple-vps"

  info "Downloading Simple VPS binary from $SIMPLE_VPS_BINARY_URL"
  curl -fsSL "$SIMPLE_VPS_BINARY_URL" -o "$output_path"
  chmod 0755 "$output_path"
  printf '%s\n' "$output_path"
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

  if binary_path="$(download_simple_vps_binary)"; then
    run_host_installer "$binary_path" "$@"
  fi

  if binary_path="$(build_simple_vps_binary)"; then
    run_host_installer "$binary_path" "$@"
  fi

  if binary_path="$(find_simple_vps_binary)"; then
    run_host_installer "$binary_path" "$@"
  fi

  bootstrap_checkout "$@"

  err "No Simple VPS binary found and Go is not installed."
  err "Install Go, run make build-release, or set SIMPLE_VPS_BINARY_URL to a platform binary."
  exit 1
}

main "$@"

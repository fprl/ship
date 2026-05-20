#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SIMPLE_STACK_REPO_TARBALL_URL="${SIMPLE_STACK_REPO_TARBALL_URL:-${SIMPLE_VPS_REPO_TARBALL_URL:-https://github.com/fprl/simple-vps/archive/refs/heads/main.tar.gz}}"
SIMPLE_STACK_BOOTSTRAP_DOWNLOAD="${SIMPLE_STACK_BOOTSTRAP_DOWNLOAD:-${SIMPLE_VPS_BOOTSTRAP_DOWNLOAD:-true}}"
SIMPLE_STACK_BOOTSTRAPPED="${SIMPLE_STACK_BOOTSTRAPPED:-${SIMPLE_VPS_BOOTSTRAPPED:-false}}"

err() {
  printf 'Error: %s\n' "$*" >&2
}

info() {
  printf '==> %s\n' "$*"
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    err "Required command not found: $cmd"
    exit 1
  fi
}

run_simple_vps_installer() {
  local installer_path="$SCRIPT_DIR/provisioning/install.sh"

  if [[ -x "$installer_path" ]]; then
    exec "$installer_path" "$@"
  fi

  if [[ -f "$installer_path" ]]; then
    exec bash "$installer_path" "$@"
  fi
}

bootstrap_checkout() {
  local tmp_dir
  local source_dir
  local archive_path
  local installer_path

  if [[ "$SIMPLE_STACK_BOOTSTRAP_DOWNLOAD" != "true" ]]; then
    err "Required Simple Stack files were not found beside install.sh."
    err "Run from a checkout, or allow bootstrap download with SIMPLE_STACK_BOOTSTRAP_DOWNLOAD=true."
    exit 1
  fi

  if [[ "$SIMPLE_STACK_BOOTSTRAPPED" == "true" ]]; then
    err "Simple Stack bootstrap download completed, but required files are still missing."
    err "Check SIMPLE_STACK_REPO_TARBALL_URL: $SIMPLE_STACK_REPO_TARBALL_URL"
    exit 1
  fi

  require_cmd curl
  require_cmd tar
  require_cmd mktemp

  tmp_dir="$(mktemp -d)"
  source_dir="$tmp_dir/simple-stack"
  archive_path="$tmp_dir/simple-stack.tar.gz"
  mkdir -p "$source_dir"

  info "Simple Stack checkout not found beside install.sh."
  info "Downloading Simple Stack from $SIMPLE_STACK_REPO_TARBALL_URL"

  curl -fsSL "$SIMPLE_STACK_REPO_TARBALL_URL" -o "$archive_path"
  tar -xzf "$archive_path" -C "$source_dir" --strip-components=1

  if [[ -f "$source_dir/provisioning/install.sh" ]]; then
    installer_path="$source_dir/provisioning/install.sh"
  elif [[ -f "$source_dir/install.sh" ]]; then
    installer_path="$source_dir/install.sh"
  else
    err "Downloaded Simple Stack archive did not contain the Simple VPS installer."
    exit 1
  fi

  info "Re-running installer from downloaded checkout."
  export SIMPLE_STACK_BOOTSTRAPPED=true
  export SIMPLE_VPS_BOOTSTRAPPED=true
  exec "$installer_path" "$@"
}

run_simple_vps_installer "$@"
bootstrap_checkout "$@"

#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
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

sha256_file() {
  local file="$1"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{ print $1 }'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{ print $1 }'
  else
    die "shasum or sha256sum is required"
  fi
}

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
tmp_dir="$(mktemp -d /tmp/ship-install-smoke-XXXXXX)"
trap 'rm -rf "$tmp_dir"' EXIT

version="v-test"
asset="$(platform_asset)"
release_dir="$tmp_dir/release/$version"
install_dir="$tmp_dir/bin"
mkdir -p "$release_dir" "$install_dir"

cat > "$release_dir/$asset" <<'SH'
#!/usr/bin/env bash
printf 'v-test\n'
SH

printf '%s  %s\n' "$(sha256_file "$release_dir/$asset")" "$asset" > "$release_dir/SHA256SUMS"

SHIP_RELEASE_BASE_URL="file://$tmp_dir/release" \
  SHIP_INSTALL_DIR="$install_dir" \
  bash "$repo_root/install.sh" --version "$version" >/tmp/ship-install-smoke.out

got="$("$install_dir/ship" version)"
if [[ "$got" != "v-test" ]]; then
  die "installed binary returned $got"
fi

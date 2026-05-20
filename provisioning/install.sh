#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

if [[ -x "$REPO_ROOT/install.sh" ]]; then
  exec "$REPO_ROOT/install.sh" "$@"
fi

exec bash "$REPO_ROOT/install.sh" "$@"

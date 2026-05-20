#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d)"

cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT

OPERATOR_KEY="ssh-ed25519 AAAAoperator test-operator"
DEPLOY_KEY="ssh-ed25519 AAAAdeploy test-deploy"
OPERATOR_KEY_FILE="$TMP_DIR/operator.pub"
DEPLOY_KEY_FILE="$TMP_DIR/deploy.pub"

printf '%s\n' "$OPERATOR_KEY" > "$OPERATOR_KEY_FILE"
printf '%s\n' "$DEPLOY_KEY" > "$DEPLOY_KEY_FILE"

dump_plan() {
  SIMPLE_VPS_BOOTSTRAP_DOWNLOAD=false SIMPLE_VPS_INSTALLER_DUMP_PLAN=true "$ROOT_DIR/install.sh" --yes "$@"
}

assert_contains() {
  local haystack="$1"
  local needle="$2"

  if ! grep -Fq -- "$needle" <<<"$haystack"; then
    printf 'Expected output to contain: %s\n' "$needle" >&2
    printf '%s\n' "$haystack" >&2
    exit 1
  fi
}

OUTPUT="$(
  dump_plan \
    --mode remote \
    --host 203.0.113.10 \
    --bootstrap-user root \
    --operator-user ops \
    --deploy-user deployer \
    --operator-ssh-public-key-file "$OPERATOR_KEY_FILE" \
    --deploy-ssh-public-key-file "$DEPLOY_KEY_FILE" \
    --tailscale-auth-key tskey-auth-test \
    --cloudflare-api-token cf-token-test \
    --cloudflare-account-id account-test \
    --docker \
    --no-litestream \
    --check
)"

assert_contains "$OUTPUT" "plan.mode=remote"
assert_contains "$OUTPUT" "plan.target_host=203.0.113.10"
assert_contains "$OUTPUT" "plan.operator_user=ops"
assert_contains "$OUTPUT" "plan.deploy_user=deployer"
assert_contains "$OUTPUT" "plan.tailscale_auth_mode=auth-key"
assert_contains "$OUTPUT" "plan.cloudflare_service_mode=api"
assert_contains "$OUTPUT" "plan.docker=true"
assert_contains "$OUTPUT" "plan.litestream=false"
assert_contains "$OUTPUT" "plan.check_mode=true"
assert_contains "$OUTPUT" 'simple_vps_operator_user: "ops"'
assert_contains "$OUTPUT" 'simple_vps_deploy_user: "deployer"'
assert_contains "$OUTPUT" "simple_vps_tailscale_auth_key: 'tskey-auth-test'"
assert_contains "$OUTPUT" "simple_vps_cloudflare_api_token: 'cf-token-test'"
assert_contains "$OUTPUT" "simple_vps_cloudflare_account_id: 'account-test'"
assert_contains "$OUTPUT" "simple_vps_install_docker: true"
assert_contains "$OUTPUT" "simple_vps_install_litestream: false"
assert_contains "$OUTPUT" "  - '$OPERATOR_KEY'"
assert_contains "$OUTPUT" "  - '$DEPLOY_KEY'"

TOKEN_OUTPUT="$(
  dump_plan \
    --mode remote \
    --host 203.0.113.13 \
    --operator-ssh-public-key-file "$OPERATOR_KEY_FILE" \
    --deploy-ssh-public-key-file "$DEPLOY_KEY_FILE" \
    --cloudflare-tunnel-token tunnel-token-test
)"

assert_contains "$TOKEN_OUTPUT" "plan.cloudflare_service_mode=token"
assert_contains "$TOKEN_OUTPUT" "simple_vps_cloudflare_tunnel_token: 'tunnel-token-test'"
assert_contains "$TOKEN_OUTPUT" "simple_vps_cloudflare_api_token: ''"

SHARED_OUTPUT="$(
  dump_plan \
    --mode remote \
    --host 203.0.113.11 \
    --operator-ssh-public-key-file "$OPERATOR_KEY_FILE" \
    --shared-key \
    --no-tailscale \
    --no-cloudflare-tunnel
)"

assert_contains "$SHARED_OUTPUT" "plan.shared_key=true"
assert_contains "$SHARED_OUTPUT" "plan.tailscale_auth_mode=disabled"
assert_contains "$SHARED_OUTPUT" "plan.cloudflare_service_mode=disabled"
if [[ "$(grep -Fc "  - '$OPERATOR_KEY'" <<<"$SHARED_OUTPUT")" -ne 2 ]]; then
  printf 'Expected shared key to render for both operator and deploy.\n' >&2
  printf '%s\n' "$SHARED_OUTPUT" >&2
  exit 1
fi

set +e
INVALID_OUTPUT="$(
  dump_plan \
    --mode remote \
    --host 203.0.113.12 \
    --operator-ssh-public-key-file "$OPERATOR_KEY_FILE" \
    --deploy-ssh-public-key-file "$DEPLOY_KEY_FILE" \
    --no-cloudflare-tunnel \
    --cloudflare-api-token cf-token-test 2>&1
)"
INVALID_STATUS=$?
set -e

if [[ "$INVALID_STATUS" -eq 0 ]]; then
  printf 'Expected invalid Cloudflare option combination to fail.\n' >&2
  printf '%s\n' "$INVALID_OUTPUT" >&2
  exit 1
fi
assert_contains "$INVALID_OUTPUT" "--cloudflare-api-token requires Cloudflare Tunnel to be enabled."

printf 'Install plan tests passed.\n'

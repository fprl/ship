#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

SIMPLE_VPS_REPO_TARBALL_URL="${SIMPLE_VPS_REPO_TARBALL_URL:-https://github.com/fprl/simple-vps/archive/refs/heads/main.tar.gz}"
SIMPLE_VPS_BOOTSTRAP_DOWNLOAD="${SIMPLE_VPS_BOOTSTRAP_DOWNLOAD:-true}"
SIMPLE_VPS_BOOTSTRAPPED="${SIMPLE_VPS_BOOTSTRAPPED:-false}"

MODE="auto"
TARGET_HOST=""
BOOTSTRAP_USER="root"
SSH_KEY=""
SSH_PUBLIC_KEY_FILE=""
OPERATOR_SSH_PUBLIC_KEY_FILE="${SIMPLE_VPS_OPERATOR_SSH_PUBLIC_KEY_FILE:-}"
DEPLOY_SSH_PUBLIC_KEY_FILE="${SIMPLE_VPS_DEPLOY_SSH_PUBLIC_KEY_FILE:-}"
OPERATOR_USER="${SIMPLE_VPS_OPERATOR_USER:-operator}"
DEPLOY_USER="${SIMPLE_VPS_DEPLOY_USER:-deploy}"
TIMEZONE="UTC"
LOCALE="en_US.UTF-8"
TAILSCALE="true"
TAILSCALE_AUTH_KEY="${SIMPLE_VPS_TAILSCALE_AUTH_KEY:-}"
TAILSCALE_HOSTNAME="${SIMPLE_VPS_TAILSCALE_HOSTNAME:-}"
CLOUDFLARE_TUNNEL="true"
CLOUDFLARE_API_TOKEN="${SIMPLE_VPS_CLOUDFLARE_API_TOKEN:-}"
CLOUDFLARE_ACCOUNT_ID="${SIMPLE_VPS_CLOUDFLARE_ACCOUNT_ID:-}"
CLOUDFLARE_TUNNEL_TOKEN="${SIMPLE_VPS_CLOUDFLARE_TUNNEL_TOKEN:-}"
CLOUDFLARE_TUNNEL_CONFIG="${SIMPLE_VPS_CLOUDFLARE_TUNNEL_CONFIG:-}"
INSTALL_DOCKER="${SIMPLE_VPS_INSTALL_DOCKER:-false}"
INSTALL_LITESTREAM="${SIMPLE_VPS_INSTALL_LITESTREAM:-true}"
CHECK_MODE="false"
ASSUME_YES="false"
SHARED_KEY="false"
SHARED_KEY_WARNED="false"
INTERACTIVE_MODE="auto"
INSTALLER_DUMP_PLAN="${SIMPLE_VPS_INSTALLER_DUMP_PLAN:-false}"
PASSTHROUGH_ARGS=()
ORIGINAL_ARGC=0
BOOTSTRAP_USER_SET="false"
OPERATOR_USER_SET="false"
DEPLOY_USER_SET="false"
TAILSCALE_SET="false"
CLOUDFLARE_TUNNEL_SET="false"
INSTALL_DOCKER_SET="false"
INSTALL_LITESTREAM_SET="false"
CHECK_MODE_SET="false"
SHARED_KEY_SET="false"
TMP_FILES=()

PLAN_MODE=""
PLAN_TARGET_HOST=""
PLAN_BOOTSTRAP_USER=""
PLAN_SSH_KEY=""
PLAN_OPERATOR_SSH_PUBLIC_KEY_FILE=""
PLAN_DEPLOY_SSH_PUBLIC_KEY_FILE=""
PLAN_OPERATOR_USER=""
PLAN_DEPLOY_USER=""
PLAN_TIMEZONE=""
PLAN_LOCALE=""
PLAN_TAILSCALE=""
PLAN_TAILSCALE_AUTH_KEY=""
PLAN_TAILSCALE_HOSTNAME=""
PLAN_TAILSCALE_AUTH_MODE=""
PLAN_CLOUDFLARE_TUNNEL=""
PLAN_CLOUDFLARE_API_TOKEN=""
PLAN_CLOUDFLARE_ACCOUNT_ID=""
PLAN_CLOUDFLARE_TUNNEL_TOKEN=""
PLAN_CLOUDFLARE_TUNNEL_CONFIG=""
PLAN_CLOUDFLARE_SERVICE_MODE=""
PLAN_INSTALL_DOCKER=""
PLAN_INSTALL_LITESTREAM=""
PLAN_CHECK_MODE=""
PLAN_SHARED_KEY=""

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m'

usage() {
  cat <<USAGE
Simple VPS installer

Usage:
  ./install.sh [options]

Options:
  --mode <local|remote|auto>     Execution mode (default: auto)
  --host <ip-or-hostname>        Target VPS host (required for remote mode)
  --ip <ip-or-hostname>          Alias for --host
  --bootstrap-user <user>        SSH user for bootstrap phase in remote mode (default: root)
  --ssh-key <path>               SSH private key for remote mode
  --ssh-public-key-file <path>   Compatibility alias for --operator-ssh-public-key-file
  --operator-ssh-public-key-file <path>
                                  Explicit public key to add for operator user
  --deploy-ssh-public-key-file <path>
                                  Explicit public key to add for deploy user
  --shared-key                   Also install the operator SSH key for deploy
  --operator-user <name>         Operator user for host convergence (default: operator)
  --deploy-user <name>           Deploy user for app CLI/CI (default: deploy)
  --admin-user <name>            Compatibility alias for --operator-user
  --tailscale                    Enable Tailscale setup (default)
  --no-tailscale                 Disable Tailscale setup
  --tailscale-auth-key <key>     Tailscale auth key for non-interactive login
  --tailscale-hostname <name>    Optional Tailscale device hostname
  --cloudflare-tunnel            Enable Cloudflare Tunnel setup (default)
  --no-cloudflare-tunnel         Disable Cloudflare Tunnel setup
  --cloudflare-api-token <t>     Advanced: Cloudflare API token for tunnel/DNS automation
  --cloudflare-account-id <id>   Cloudflare account id when the token has multiple accounts
  --cloudflare-tunnel-token <t>  Cloudflare Tunnel token for managed tunnels
  --cloudflare-tunnel-config <p> Existing cloudflared config path
  --docker                       Install optional Docker runtime
  --no-docker                    Skip Docker runtime installation (default)
  --litestream                   Install Litestream binary (default)
  --no-litestream                Skip Litestream installation
  --check                        Run Ansible in check mode
  --interactive                  Force interactive wizard
  --no-interactive               Disable interactive wizard
  --yes                          Non-interactive mode (fail if required values are missing)
  -h, --help                     Show help

Examples:
  ./install.sh --mode remote --host 203.0.113.10 --ssh-key ~/.ssh/id_ed25519 --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub
  SIMPLE_VPS_TAILSCALE_AUTH_KEY=tskey-auth-... ./install.sh --mode local --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub
  SIMPLE_VPS_CLOUDFLARE_TUNNEL_TOKEN=... ./install.sh --mode local --deploy-ssh-public-key-file ~/.ssh/simple-vps-deploy.pub
  ./install.sh --interactive
USAGE
}

err() {
  echo -e "${RED}Error:${NC} $*" >&2
}

warn() {
  echo -e "${YELLOW}Warning:${NC} $*"
}

info() {
  echo -e "${GREEN}==>${NC} $*"
}

step() {
  echo -e "${BLUE}-->${NC} $*"
}

can_prompt() {
  [[ -t 0 && -t 1 ]]
}

setup_colors() {
  if [[ ! -t 1 ]]; then
    RED=''
    GREEN=''
    YELLOW=''
    BLUE=''
    CYAN=''
    BOLD=''
    DIM=''
    NC=''
  fi
}

ui_hr() {
  printf "%b%s%b\n" "$DIM" "------------------------------------------------------------" "$NC"
}

ui_title() {
  printf "\n%b%s%b\n" "${BOLD}${BLUE}" "$1" "$NC"
  ui_hr
}

ui_section() {
  printf "\n%b%s%b\n" "${BOLD}${CYAN}" "$1" "$NC"
}

ui_kv() {
  printf "  %b%-16s%b %s\n" "$DIM" "$1" "$NC" "$2"
}

present_or_missing() {
  local value="$1"
  local present_label="${2:-provided}"
  local missing_label="${3:-not provided}"

  if [[ -n "$value" ]]; then
    printf '%s' "$present_label"
  else
    printf '%s' "$missing_label"
  fi
}

prepare_ansible_env() {
  local ansible_tmp_dir

  if [[ -f "$SCRIPT_DIR/ansible.cfg" ]]; then
    export ANSIBLE_CONFIG="$SCRIPT_DIR/ansible.cfg"
  fi

  ansible_tmp_dir="${ANSIBLE_LOCAL_TEMP:-${TMPDIR:-/tmp}/simple-vps-ansible-tmp}"
  mkdir -p "$ansible_tmp_dir"
  export ANSIBLE_LOCAL_TEMP="$ansible_tmp_dir"
}

ensure_simple_vps_layout() {
  local required_files=(
    "$SCRIPT_DIR/playbooks/vps-bootstrap.yml"
    "$SCRIPT_DIR/playbooks/vps-apply.yml"
    "$SCRIPT_DIR/roles/system/tasks/main.yml"
  )
  local file

  for file in "${required_files[@]}"; do
    if [[ ! -f "$file" ]]; then
      bootstrap_simple_vps_checkout "$@"
    fi
  done
}

bootstrap_simple_vps_checkout() {
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

  if [[ -f "$source_dir/packages/simple-vps/install.sh" ]]; then
    installer_path="$source_dir/packages/simple-vps/install.sh"
  elif [[ -f "$source_dir/install.sh" ]]; then
    installer_path="$source_dir/install.sh"
  else
    err "Downloaded Simple VPS archive did not contain the Simple VPS installer."
    exit 1
  fi

  info "Re-running installer from downloaded checkout."
  export SIMPLE_VPS_BOOTSTRAPPED=true
  exec "$installer_path" "$@"
}

require_cmd() {
  local cmd="$1"
  if ! command -v "$cmd" >/dev/null 2>&1; then
    err "Required command not found: $cmd"
    exit 1
  fi
}

cleanup_tmp_files() {
  if [[ ${#TMP_FILES[@]} -gt 0 ]]; then
    rm -f "${TMP_FILES[@]}"
  fi
}

confirm_or_prompt() {
  local var_name="$1"
  local prompt="$2"
  local default_value="${3:-}"
  local force_prompt="${4:-false}"
  local current_value="${!var_name:-}"

  if [[ "$force_prompt" != "true" ]] && [[ -n "$current_value" ]]; then
    return
  fi

  if [[ "$ASSUME_YES" == "true" ]]; then
    err "$var_name is required in non-interactive mode."
    exit 1
  fi

  if ! can_prompt; then
    err "$var_name is required but interactive input is not available."
    exit 1
  fi

  if [[ -n "$default_value" ]]; then
    printf "%b?%b %s [%s]: " "$CYAN" "$NC" "$prompt" "$default_value"
    read -r current_value
    current_value="${current_value:-$default_value}"
  else
    printf "%b?%b %s: " "$CYAN" "$NC" "$prompt"
    read -r current_value
  fi

  if [[ -z "$current_value" ]]; then
    err "$var_name cannot be empty"
    exit 1
  fi

  printf -v "$var_name" '%s' "$current_value"
}

prompt_yes_no() {
  local var_name="$1"
  local prompt="$2"
  local default_value="${3:-false}"
  local force_prompt="${4:-false}"
  local current_value="${!var_name:-}"
  local answer=""
  local suffix="[y/N]"

  if [[ "$force_prompt" != "true" ]] && [[ -n "$current_value" && "$current_value" != "auto" ]]; then
    return
  fi

  if ! can_prompt; then
    err "Cannot prompt for $var_name in non-interactive terminal."
    exit 1
  fi

  if [[ "$default_value" == "true" ]]; then
    suffix="[Y/n]"
  fi

  while true; do
    printf "%b?%b %s %s: " "$CYAN" "$NC" "$prompt" "$suffix"
    read -r answer
    answer="$(printf '%s' "$answer" | tr '[:upper:]' '[:lower:]')"
    if [[ -z "$answer" ]]; then
      printf -v "$var_name" '%s' "$default_value"
      return
    fi
    case "$answer" in
      y|yes)
        printf -v "$var_name" '%s' "true"
        return
        ;;
      n|no)
        printf -v "$var_name" '%s' "false"
        return
        ;;
    esac
    echo "Please answer y or n."
  done
}

warn_shared_key() {
  if [[ "$SHARED_KEY_WARNED" == "true" ]]; then
    return
  fi
  SHARED_KEY_WARNED="true"
  warn "Using the same SSH public key for operator and deploy."
  warn "This is convenient, but a compromised deploy key can also access the operator account."
}

prompt_optional_secret() {
  local var_name="$1"
  local prompt="$2"
  local current_value="${!var_name:-}"

  if [[ -n "$current_value" ]]; then
    return
  fi
  if ! can_prompt; then
    return
  fi

  printf "%b?%b %s (leave blank to skip): " "$CYAN" "$NC" "$prompt"
  stty -echo
  IFS= read -r current_value
  stty echo
  printf "\n"
  printf -v "$var_name" '%s' "$current_value"
}

prompt_mode() {
  local default_mode="$1"
  local choice=""

  if [[ "$MODE" != "auto" ]]; then
    return
  fi

  if ! can_prompt; then
    return
  fi

  ui_section "Installation mode"
  echo "  1) remote   Run from this machine against a VPS host"
  echo "  2) local    Run directly on the target VPS"

  while true; do
    if [[ "$default_mode" == "local" ]]; then
      printf "%b?%b Choice [2]: " "$CYAN" "$NC"
      read -r choice
      choice="${choice:-2}"
    else
      printf "%b?%b Choice [1]: " "$CYAN" "$NC"
      read -r choice
      choice="${choice:-1}"
    fi
    case "$choice" in
      1)
        MODE="remote"
        return
        ;;
      2)
        MODE="local"
        return
        ;;
    esac
    echo "Please enter 1 or 2."
  done
}

interactive_wizard() {
  local default_mode="remote"
  local proceed="true"
  local set_ssh_key="false"
  local force_tailscale_prompt="false"
  local force_cloudflare_tunnel_prompt="false"
  local force_docker_prompt="false"
  local force_litestream_prompt="false"
  local force_check_prompt="false"
  local force_shared_key_prompt="false"

  if ! can_prompt; then
    err "Interactive wizard requested, but terminal is not interactive."
    exit 1
  fi

  ui_title "Simple VPS Setup Wizard"

  if [[ "$(id -u)" -eq 0 ]] && [[ -f /etc/os-release ]]; then
    default_mode="local"
  fi
  prompt_mode "$default_mode"

  if [[ "$MODE" == "remote" ]]; then
    ui_section "Remote target"
    confirm_or_prompt TARGET_HOST "Target VPS host (IP or DNS name)"
    if [[ "$BOOTSTRAP_USER_SET" != "true" ]]; then
      BOOTSTRAP_USER=""
    fi
    confirm_or_prompt BOOTSTRAP_USER "Bootstrap SSH user" "root"

    if [[ -z "$SSH_KEY" ]]; then
      prompt_yes_no set_ssh_key "Use SSH private key file?" "false" "true"
      if [[ "$set_ssh_key" == "true" ]]; then
        confirm_or_prompt SSH_KEY "Path to SSH private key (for example ~/.ssh/id_ed25519)"
      fi
    fi
  fi

  ui_section "Server settings"
  if [[ "$OPERATOR_USER_SET" != "true" ]]; then
    OPERATOR_USER=""
  fi
  if [[ "$DEPLOY_USER_SET" != "true" ]]; then
    DEPLOY_USER=""
  fi

  if [[ "$TAILSCALE_SET" != "true" ]]; then
    force_tailscale_prompt="true"
  fi
  if [[ "$CLOUDFLARE_TUNNEL_SET" != "true" ]]; then
    force_cloudflare_tunnel_prompt="true"
  fi
  if [[ "$INSTALL_DOCKER_SET" != "true" ]]; then
    force_docker_prompt="true"
  fi
  if [[ "$INSTALL_LITESTREAM_SET" != "true" ]]; then
    force_litestream_prompt="true"
  fi
  if [[ "$CHECK_MODE_SET" != "true" ]]; then
    force_check_prompt="true"
  fi
  if [[ "$SHARED_KEY_SET" != "true" ]]; then
    force_shared_key_prompt="true"
  fi

  confirm_or_prompt OPERATOR_USER "Operator username" "operator"
  confirm_or_prompt DEPLOY_USER "Deploy username" "deploy"
  ui_kv "timezone" "$TIMEZONE (fixed)"
  ui_kv "locale" "$LOCALE (fixed)"

  prompt_yes_no TAILSCALE "Enable Tailscale?" "$TAILSCALE" "$force_tailscale_prompt"
  if [[ "$TAILSCALE" == "true" ]]; then
    prompt_optional_secret TAILSCALE_AUTH_KEY "Tailscale auth key"
  fi
  prompt_yes_no CLOUDFLARE_TUNNEL "Enable Cloudflare Tunnel?" "$CLOUDFLARE_TUNNEL" "$force_cloudflare_tunnel_prompt"
  if [[ "$CLOUDFLARE_TUNNEL" == "true" ]]; then
    echo "  Cloudflare tunnel token: Cloudflare dashboard -> Zero Trust -> Networks -> Tunnels"
    if [[ -z "$CLOUDFLARE_API_TOKEN" && -z "$CLOUDFLARE_TUNNEL_CONFIG" ]]; then
      prompt_optional_secret CLOUDFLARE_TUNNEL_TOKEN "Cloudflare tunnel token"
    fi
    if [[ -z "$CLOUDFLARE_TUNNEL_TOKEN" && -z "$CLOUDFLARE_TUNNEL_CONFIG" ]]; then
      echo "  Advanced API mode: https://dash.cloudflare.com/profile/api-tokens"
      prompt_optional_secret CLOUDFLARE_API_TOKEN "Cloudflare API token for automatic DNS/hostname management"
    fi
  fi
  prompt_yes_no INSTALL_DOCKER "Install Docker?" "$INSTALL_DOCKER" "$force_docker_prompt"
  prompt_yes_no INSTALL_LITESTREAM "Install Litestream?" "$INSTALL_LITESTREAM" "$force_litestream_prompt"
  prompt_yes_no CHECK_MODE "Run in check (dry-run) mode?" "$CHECK_MODE" "$force_check_prompt"
  prompt_yes_no SHARED_KEY "Install operator SSH key for deploy too?" "$SHARED_KEY" "$force_shared_key_prompt"
  if [[ "$SHARED_KEY" == "true" ]]; then
    warn_shared_key
  fi

  ui_title "Provisioning Summary"
  ui_kv "mode" "$MODE"
  if [[ "$MODE" == "remote" ]]; then
    ui_kv "host" "$TARGET_HOST"
    ui_kv "bootstrap_user" "$BOOTSTRAP_USER"
    if [[ -n "$SSH_KEY" ]]; then
      ui_kv "ssh_key" "$SSH_KEY"
    else
      ui_kv "ssh_key" "<default SSH config>"
    fi
  fi
  ui_kv "operator_user" "$OPERATOR_USER"
  ui_kv "deploy_user" "$DEPLOY_USER"
  ui_kv "shared_key" "$SHARED_KEY"
  ui_kv "timezone" "$TIMEZONE"
  ui_kv "locale" "$LOCALE"
  ui_kv "tailscale" "$TAILSCALE"
  if [[ "$TAILSCALE" == "true" ]]; then
    ui_kv "tailscale_auth" "$(present_or_missing "$TAILSCALE_AUTH_KEY" "auth key provided" "manual login required")"
    if [[ -n "$TAILSCALE_HOSTNAME" ]]; then
      ui_kv "tailscale_name" "$TAILSCALE_HOSTNAME"
    fi
  fi
  ui_kv "cf_tunnel" "$CLOUDFLARE_TUNNEL"
  if [[ "$CLOUDFLARE_TUNNEL" == "true" ]]; then
    ui_kv "cf_api" "$(present_or_missing "$CLOUDFLARE_API_TOKEN" "api token provided" "manual setup")"
    ui_kv "cf_tunnel_auth" "$(present_or_missing "$CLOUDFLARE_TUNNEL_TOKEN" "token provided" "service not enabled")"
    if [[ -n "$CLOUDFLARE_TUNNEL_CONFIG" ]]; then
      ui_kv "cf_tunnel_cfg" "$CLOUDFLARE_TUNNEL_CONFIG"
    fi
  fi
  ui_kv "docker" "$INSTALL_DOCKER"
  ui_kv "litestream" "$INSTALL_LITESTREAM"
  ui_kv "check_mode" "$CHECK_MODE"
  ui_hr

  prompt_yes_no proceed "Proceed with provisioning?" "true" "true"
  if [[ "$proceed" != "true" ]]; then
    err "Aborted by user."
    exit 1
  fi
}

maybe_run_interactive_wizard() {
  if [[ "$ASSUME_YES" == "true" ]]; then
    return
  fi

  case "$INTERACTIVE_MODE" in
    true)
      interactive_wizard
      ;;
    false)
      ;;
    auto)
      if [[ "$ORIGINAL_ARGC" -eq 0 ]] && can_prompt; then
        interactive_wizard
      fi
      ;;
    *)
      err "Invalid interactive mode: $INTERACTIVE_MODE"
      exit 1
      ;;
  esac
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --mode)
        MODE="${2:-}"
        shift 2
        ;;
      --host|--ip)
        TARGET_HOST="${2:-}"
        shift 2
        ;;
      --bootstrap-user)
        BOOTSTRAP_USER="${2:-}"
        BOOTSTRAP_USER_SET="true"
        shift 2
        ;;
      --ssh-key)
        SSH_KEY="${2:-}"
        shift 2
        ;;
      --ssh-public-key-file)
        SSH_PUBLIC_KEY_FILE="${2:-}"
        shift 2
        ;;
      --operator-ssh-public-key-file)
        OPERATOR_SSH_PUBLIC_KEY_FILE="${2:-}"
        shift 2
        ;;
      --deploy-ssh-public-key-file)
        DEPLOY_SSH_PUBLIC_KEY_FILE="${2:-}"
        shift 2
        ;;
      --shared-key)
        SHARED_KEY="true"
        SHARED_KEY_SET="true"
        shift
        ;;
      --operator-user)
        OPERATOR_USER="${2:-}"
        OPERATOR_USER_SET="true"
        shift 2
        ;;
      --deploy-user)
        DEPLOY_USER="${2:-}"
        DEPLOY_USER_SET="true"
        shift 2
        ;;
      --admin-user)
        OPERATOR_USER="${2:-}"
        OPERATOR_USER_SET="true"
        shift 2
        ;;
      --tailscale)
        TAILSCALE="true"
        TAILSCALE_SET="true"
        shift
        ;;
      --no-tailscale)
        TAILSCALE="false"
        TAILSCALE_SET="true"
        shift
        ;;
      --tailscale-auth-key)
        TAILSCALE_AUTH_KEY="${2:-}"
        shift 2
        ;;
      --tailscale-hostname)
        TAILSCALE_HOSTNAME="${2:-}"
        shift 2
        ;;
      --cloudflare-tunnel)
        CLOUDFLARE_TUNNEL="true"
        CLOUDFLARE_TUNNEL_SET="true"
        shift
        ;;
      --no-cloudflare-tunnel)
        CLOUDFLARE_TUNNEL="false"
        CLOUDFLARE_TUNNEL_SET="true"
        shift
        ;;
      --cloudflare-api-token)
        CLOUDFLARE_API_TOKEN="${2:-}"
        shift 2
        ;;
      --cloudflare-account-id)
        CLOUDFLARE_ACCOUNT_ID="${2:-}"
        shift 2
        ;;
      --cloudflare-tunnel-token)
        CLOUDFLARE_TUNNEL_TOKEN="${2:-}"
        shift 2
        ;;
      --cloudflare-tunnel-config)
        CLOUDFLARE_TUNNEL_CONFIG="${2:-}"
        shift 2
        ;;
      --docker)
        INSTALL_DOCKER="true"
        INSTALL_DOCKER_SET="true"
        shift
        ;;
      --no-docker)
        INSTALL_DOCKER="false"
        INSTALL_DOCKER_SET="true"
        shift
        ;;
      --litestream)
        INSTALL_LITESTREAM="true"
        INSTALL_LITESTREAM_SET="true"
        shift
        ;;
      --no-litestream)
        INSTALL_LITESTREAM="false"
        INSTALL_LITESTREAM_SET="true"
        shift
        ;;
      --check)
        CHECK_MODE="true"
        CHECK_MODE_SET="true"
        shift
        ;;
      --interactive)
        INTERACTIVE_MODE="true"
        shift
        ;;
      --no-interactive)
        INTERACTIVE_MODE="false"
        shift
        ;;
      --yes)
        ASSUME_YES="true"
        shift
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      --)
        shift
        PASSTHROUGH_ARGS=("$@")
        break
        ;;
      *)
        err "Unknown option: $1"
        usage
        exit 1
        ;;
    esac
  done
}

auto_detect_mode() {
  if [[ "$MODE" != "auto" ]]; then
    return
  fi

  if [[ -n "$TARGET_HOST" ]]; then
    MODE="remote"
    return
  fi

  if [[ "$(id -u)" -eq 0 ]] && [[ -f /etc/os-release ]]; then
    MODE="local"
  else
    MODE="remote"
  fi
}

validate_mode() {
  case "$MODE" in
    local|remote)
      ;;
    *)
      err "Invalid mode: $MODE (expected local, remote, or auto)"
      exit 1
      ;;
  esac
}

validate_boolean() {
  local label="$1"
  local value="$2"

  case "$value" in
    true|false)
      ;;
    *)
      err "Invalid ${label} value: $value (expected true or false)"
      exit 1
      ;;
  esac
}

validate_tailscale_options() {
  validate_boolean "Tailscale" "$TAILSCALE"

  if [[ "$TAILSCALE" != "true" && -n "$TAILSCALE_AUTH_KEY" ]]; then
    err "--tailscale-auth-key requires Tailscale to be enabled."
    exit 1
  fi

  if [[ "$TAILSCALE" != "true" && -n "$TAILSCALE_HOSTNAME" ]]; then
    err "--tailscale-hostname requires Tailscale to be enabled."
    exit 1
  fi
}

validate_cloudflare_tunnel_options() {
  validate_boolean "Cloudflare Tunnel" "$CLOUDFLARE_TUNNEL"

  if [[ "$CLOUDFLARE_TUNNEL" != "true" && -n "$CLOUDFLARE_TUNNEL_TOKEN" ]]; then
    err "--cloudflare-tunnel-token requires Cloudflare Tunnel to be enabled."
    exit 1
  fi

  if [[ "$CLOUDFLARE_TUNNEL" != "true" && -n "$CLOUDFLARE_API_TOKEN" ]]; then
    err "--cloudflare-api-token requires Cloudflare Tunnel to be enabled."
    exit 1
  fi

  if [[ "$CLOUDFLARE_TUNNEL" != "true" && -n "$CLOUDFLARE_ACCOUNT_ID" ]]; then
    err "--cloudflare-account-id requires Cloudflare Tunnel to be enabled."
    exit 1
  fi

  if [[ "$CLOUDFLARE_TUNNEL" != "true" && -n "$CLOUDFLARE_TUNNEL_CONFIG" ]]; then
    err "--cloudflare-tunnel-config requires Cloudflare Tunnel to be enabled."
    exit 1
  fi

  if [[ -n "$CLOUDFLARE_API_TOKEN" && -n "$CLOUDFLARE_TUNNEL_TOKEN" ]]; then
    err "Use either --cloudflare-api-token or --cloudflare-tunnel-token, not both."
    exit 1
  fi

  if [[ -n "$CLOUDFLARE_API_TOKEN" && -n "$CLOUDFLARE_TUNNEL_CONFIG" ]]; then
    err "Use either --cloudflare-api-token or --cloudflare-tunnel-config, not both."
    exit 1
  fi

  if [[ -n "$CLOUDFLARE_TUNNEL_TOKEN" && -n "$CLOUDFLARE_TUNNEL_CONFIG" ]]; then
    err "Use either --cloudflare-tunnel-token or --cloudflare-tunnel-config, not both."
    exit 1
  fi
}

validate_install_options() {
  if [[ "$OPERATOR_USER" == "$DEPLOY_USER" ]]; then
    err "Operator and deploy users must be different."
    exit 1
  fi

  validate_boolean "shared-key" "$SHARED_KEY"
  validate_boolean "Docker" "$INSTALL_DOCKER"
  validate_boolean "Litestream" "$INSTALL_LITESTREAM"
}

ensure_ansible_local() {
  require_cmd ansible-playbook
}

ensure_ansible_inplace() {
  if command -v ansible-playbook >/dev/null 2>&1; then
    return
  fi

  require_cmd apt-get
  info "Ansible not found. Installing with apt-get..."
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y
  apt-get install -y ansible
}

read_public_key_file() {
  local out_var="$1"
  local file_path="$2"
  local key_contents=""

  if [[ -z "$file_path" ]]; then
    printf -v "$out_var" '%s' ""
    return
  fi

  if [[ ! -f "$file_path" ]]; then
    err "SSH public key file not found: $file_path"
    exit 1
  fi

  key_contents="$(tr -d '\r' < "$file_path" | sed '/^\s*$/d' | head -n 1)"
  if [[ -z "$key_contents" ]]; then
    err "SSH public key file is empty: $file_path"
    exit 1
  fi

  printf -v "$out_var" '%s' "$key_contents"
}

resolve_public_key_paths() {
  PLAN_OPERATOR_SSH_PUBLIC_KEY_FILE="$OPERATOR_SSH_PUBLIC_KEY_FILE"
  PLAN_DEPLOY_SSH_PUBLIC_KEY_FILE="$DEPLOY_SSH_PUBLIC_KEY_FILE"

  if [[ -n "$SSH_PUBLIC_KEY_FILE" ]]; then
    if [[ -z "$PLAN_OPERATOR_SSH_PUBLIC_KEY_FILE" ]]; then
      PLAN_OPERATOR_SSH_PUBLIC_KEY_FILE="$SSH_PUBLIC_KEY_FILE"
    fi
    if [[ "$SHARED_KEY" == "true" && -z "$PLAN_DEPLOY_SSH_PUBLIC_KEY_FILE" ]]; then
      PLAN_DEPLOY_SSH_PUBLIC_KEY_FILE="$SSH_PUBLIC_KEY_FILE"
    fi
    return
  fi

  if [[ -n "$PLAN_OPERATOR_SSH_PUBLIC_KEY_FILE" ]]; then
    return
  fi

  if [[ -n "$SSH_KEY" ]] && [[ -f "${SSH_KEY}.pub" ]]; then
    PLAN_OPERATOR_SSH_PUBLIC_KEY_FILE="${SSH_KEY}.pub"
  fi
}

resolve_deploy_public_key() {
  local out_var="$1"
  local operator_ssh_public_key_value="$2"
  local deploy_ssh_public_key_value="$3"

  if [[ -n "$deploy_ssh_public_key_value" ]]; then
    printf -v "$out_var" '%s' "$deploy_ssh_public_key_value"
    return
  fi

  if [[ "$PLAN_SHARED_KEY" == "true" ]]; then
    warn_shared_key
    printf -v "$out_var" '%s' "$operator_ssh_public_key_value"
    return
  fi

  err "No SSH public key source found for deploy user."
  err "Provide --deploy-ssh-public-key-file, or pass --shared-key to reuse the operator key."
  exit 1
}

resolve_ssh_key_plan() {
  local operator_out_var="$1"
  local deploy_out_var="$2"
  local require_operator_key="$3"
  local root_keys_path="${4:-}"
  local operator_key_value=""
  local deploy_key_value=""

  read_public_key_file operator_key_value "$PLAN_OPERATOR_SSH_PUBLIC_KEY_FILE"
  read_public_key_file deploy_key_value "$PLAN_DEPLOY_SSH_PUBLIC_KEY_FILE"
  resolve_deploy_public_key deploy_key_value "$operator_key_value" "$deploy_key_value"

  if [[ "$require_operator_key" == "true" && -z "$operator_key_value" && ! -s "$root_keys_path" ]]; then
    err "No SSH public key source found for operator user."
    err "Provide --operator-ssh-public-key-file or --ssh-public-key-file, or create $root_keys_path first."
    err "This protects against locking yourself out when password auth is disabled."
    exit 1
  fi

  printf -v "$operator_out_var" '%s' "$operator_key_value"
  printf -v "$deploy_out_var" '%s' "$deploy_key_value"
}

tailscale_auth_mode() {
  if [[ "$TAILSCALE" != "true" ]]; then
    printf '%s' "disabled"
  elif [[ -n "$TAILSCALE_AUTH_KEY" ]]; then
    printf '%s' "auth-key"
  else
    printf '%s' "manual"
  fi
}

cloudflare_service_mode() {
  if [[ "$CLOUDFLARE_TUNNEL" != "true" ]]; then
    printf '%s' "disabled"
  elif [[ -n "$CLOUDFLARE_API_TOKEN" ]]; then
    printf '%s' "api"
  elif [[ -n "$CLOUDFLARE_TUNNEL_TOKEN" ]]; then
    printf '%s' "token"
  elif [[ -n "$CLOUDFLARE_TUNNEL_CONFIG" ]]; then
    printf '%s' "config"
  else
    printf '%s' "manual"
  fi
}

resolve_install_plan() {
  auto_detect_mode
  validate_mode

  if [[ "$MODE" == "remote" ]]; then
    confirm_or_prompt TARGET_HOST "Target VPS host (IP or DNS name)"
    confirm_or_prompt BOOTSTRAP_USER "Bootstrap SSH user" "root"
    if [[ -n "$SSH_KEY" && ! -f "$SSH_KEY" ]]; then
      err "SSH key file not found: $SSH_KEY"
      exit 1
    fi
  fi

  validate_tailscale_options
  validate_cloudflare_tunnel_options
  validate_install_options
  resolve_public_key_paths

  PLAN_MODE="$MODE"
  PLAN_TARGET_HOST="$TARGET_HOST"
  PLAN_BOOTSTRAP_USER="$BOOTSTRAP_USER"
  PLAN_SSH_KEY="$SSH_KEY"
  PLAN_OPERATOR_USER="$OPERATOR_USER"
  PLAN_DEPLOY_USER="$DEPLOY_USER"
  PLAN_TIMEZONE="$TIMEZONE"
  PLAN_LOCALE="$LOCALE"
  PLAN_TAILSCALE="$TAILSCALE"
  PLAN_TAILSCALE_AUTH_KEY="$TAILSCALE_AUTH_KEY"
  PLAN_TAILSCALE_HOSTNAME="$TAILSCALE_HOSTNAME"
  PLAN_TAILSCALE_AUTH_MODE="$(tailscale_auth_mode)"
  PLAN_CLOUDFLARE_TUNNEL="$CLOUDFLARE_TUNNEL"
  PLAN_CLOUDFLARE_API_TOKEN="$CLOUDFLARE_API_TOKEN"
  PLAN_CLOUDFLARE_ACCOUNT_ID="$CLOUDFLARE_ACCOUNT_ID"
  PLAN_CLOUDFLARE_TUNNEL_TOKEN="$CLOUDFLARE_TUNNEL_TOKEN"
  PLAN_CLOUDFLARE_TUNNEL_CONFIG="$CLOUDFLARE_TUNNEL_CONFIG"
  PLAN_CLOUDFLARE_SERVICE_MODE="$(cloudflare_service_mode)"
  PLAN_INSTALL_DOCKER="$INSTALL_DOCKER"
  PLAN_INSTALL_LITESTREAM="$INSTALL_LITESTREAM"
  PLAN_CHECK_MODE="$CHECK_MODE"
  PLAN_SHARED_KEY="$SHARED_KEY"
}

write_extra_vars_file() {
  local file_path="$1"
  local operator_ssh_public_key_value="$2"
  local deploy_ssh_public_key_value="$3"
  local escaped_tailscale_auth_key="${PLAN_TAILSCALE_AUTH_KEY//\'/\'\"\'\"\'}"
  local escaped_tailscale_hostname="${PLAN_TAILSCALE_HOSTNAME//\'/\'\"\'\"\'}"
  local escaped_cloudflare_api_token="${PLAN_CLOUDFLARE_API_TOKEN//\'/\'\"\'\"\'}"
  local escaped_cloudflare_account_id="${PLAN_CLOUDFLARE_ACCOUNT_ID//\'/\'\"\'\"\'}"
  local escaped_cloudflare_tunnel_token="${PLAN_CLOUDFLARE_TUNNEL_TOKEN//\'/\'\"\'\"\'}"
  local escaped_cloudflare_tunnel_config="${PLAN_CLOUDFLARE_TUNNEL_CONFIG//\'/\'\"\'\"\'}"

  {
    printf 'simple_vps_operator_user: "%s"\n' "$PLAN_OPERATOR_USER"
    printf 'simple_vps_deploy_user: "%s"\n' "$PLAN_DEPLOY_USER"
    printf 'simple_vps_admin_user: "%s"\n' "$PLAN_OPERATOR_USER"
    printf 'simple_vps_allow_shared_ssh_key: %s\n' "$PLAN_SHARED_KEY"
    printf 'simple_vps_timezone: "%s"\n' "$PLAN_TIMEZONE"
    printf 'simple_vps_locale: "%s"\n' "$PLAN_LOCALE"
    printf 'security_enable_tailscale: %s\n' "$PLAN_TAILSCALE"
    printf "simple_vps_tailscale_auth_key: '%s'\n" "$escaped_tailscale_auth_key"
    printf "simple_vps_tailscale_hostname: '%s'\n" "$escaped_tailscale_hostname"
    printf 'simple_vps_enable_cloudflare_tunnel: %s\n' "$PLAN_CLOUDFLARE_TUNNEL"
    printf "simple_vps_cloudflare_api_token: '%s'\n" "$escaped_cloudflare_api_token"
    printf "simple_vps_cloudflare_account_id: '%s'\n" "$escaped_cloudflare_account_id"
    printf "simple_vps_cloudflare_tunnel_token: '%s'\n" "$escaped_cloudflare_tunnel_token"
    printf "simple_vps_cloudflare_tunnel_config_path: '%s'\n" "$escaped_cloudflare_tunnel_config"
    printf 'simple_vps_install_docker: %s\n' "$PLAN_INSTALL_DOCKER"
    printf 'simple_vps_install_litestream: %s\n' "$PLAN_INSTALL_LITESTREAM"

    if [[ -n "$operator_ssh_public_key_value" ]]; then
      local escaped_key="${operator_ssh_public_key_value//\'/\'\"\'\"\'}"
      printf 'simple_vps_operator_ssh_public_keys:\n'
      printf "  - '%s'\n" "$escaped_key"
    else
      printf 'simple_vps_operator_ssh_public_keys: []\n'
    fi

    if [[ -n "$deploy_ssh_public_key_value" ]]; then
      local escaped_deploy_key="${deploy_ssh_public_key_value//\'/\'\"\'\"\'}"
      printf 'simple_vps_deploy_ssh_public_keys:\n'
      printf "  - '%s'\n" "$escaped_deploy_key"
    else
      printf 'simple_vps_deploy_ssh_public_keys: []\n'
    fi
  } > "$file_path"
}

preflight_ssh() {
  local user="$1"
  local host="$2"
  local key="$3"

  local cmd=(ssh -o BatchMode=yes -o ConnectTimeout=7)
  if [[ -n "$key" ]]; then
    cmd+=( -i "$key" )
  fi

  if ! "${cmd[@]}" "${user}@${host}" "echo connected" >/dev/null 2>&1; then
    err "SSH preflight failed for ${user}@${host}."
    if [[ -z "$key" ]]; then
      err "Remote mode expects SSH key-based auth (via ssh config/agent/default keys)."
      err "If you only have password credentials, SSH to the VPS first and use --mode local."
    fi
    err "Check host, credentials, and key access."
    exit 1
  fi
}

run_ansible_playbook() {
  local playbook="$1"
  shift
  local cmd=()

  cmd=(ansible-playbook "$@")
  if [[ "$PLAN_CHECK_MODE" == "true" ]]; then
    cmd+=(--check)
  fi
  if [[ ${#PASSTHROUGH_ARGS[@]} -gt 0 ]]; then
    cmd+=("${PASSTHROUGH_ARGS[@]}")
  fi
  cmd+=("$playbook")
  "${cmd[@]}"
}

dump_install_plan() {
  local operator_ssh_public_key_value=""
  local deploy_ssh_public_key_value=""
  local tmp_vars
  local require_operator_key="false"
  local root_keys_path="/root/.ssh/authorized_keys"

  if [[ "$PLAN_MODE" == "local" ]]; then
    require_operator_key="true"
  fi

  tmp_vars="$(mktemp)"
  chmod 600 "$tmp_vars"
  TMP_FILES+=("$tmp_vars")
  trap cleanup_tmp_files EXIT

  resolve_ssh_key_plan operator_ssh_public_key_value deploy_ssh_public_key_value "$require_operator_key" "$root_keys_path"
  write_extra_vars_file "$tmp_vars" "$operator_ssh_public_key_value" "$deploy_ssh_public_key_value"

  printf 'plan.mode=%s\n' "$PLAN_MODE"
  printf 'plan.target_host=%s\n' "$PLAN_TARGET_HOST"
  printf 'plan.bootstrap_user=%s\n' "$PLAN_BOOTSTRAP_USER"
  printf 'plan.operator_user=%s\n' "$PLAN_OPERATOR_USER"
  printf 'plan.deploy_user=%s\n' "$PLAN_DEPLOY_USER"
  printf 'plan.shared_key=%s\n' "$PLAN_SHARED_KEY"
  printf 'plan.tailscale=%s\n' "$PLAN_TAILSCALE"
  printf 'plan.tailscale_auth_mode=%s\n' "$PLAN_TAILSCALE_AUTH_MODE"
  printf 'plan.cloudflare_tunnel=%s\n' "$PLAN_CLOUDFLARE_TUNNEL"
  printf 'plan.cloudflare_service_mode=%s\n' "$PLAN_CLOUDFLARE_SERVICE_MODE"
  printf 'plan.docker=%s\n' "$PLAN_INSTALL_DOCKER"
  printf 'plan.litestream=%s\n' "$PLAN_INSTALL_LITESTREAM"
  printf 'plan.check_mode=%s\n' "$PLAN_CHECK_MODE"
  printf '%s\n' '--- extra-vars ---'
  cat "$tmp_vars"
}

run_remote() {
  ensure_ansible_local

  local operator_ssh_public_key_value=""
  local deploy_ssh_public_key_value=""
  resolve_ssh_key_plan operator_ssh_public_key_value deploy_ssh_public_key_value false

  info "Running in remote mode against ${PLAN_TARGET_HOST}"
  preflight_ssh "$PLAN_BOOTSTRAP_USER" "$PLAN_TARGET_HOST" "$PLAN_SSH_KEY"

  local tmp_inventory
  local tmp_vars
  tmp_inventory="$(mktemp)"
  tmp_vars="$(mktemp)"
  chmod 600 "$tmp_vars"

  TMP_FILES+=("$tmp_inventory" "$tmp_vars")
  trap cleanup_tmp_files EXIT

  cat > "$tmp_inventory" <<INVENTORY
[simple_vps]
simple_vps_host ansible_host=${PLAN_TARGET_HOST} ansible_python_interpreter=/usr/bin/python3
INVENTORY

  write_extra_vars_file "$tmp_vars" "$operator_ssh_public_key_value" "$deploy_ssh_public_key_value"

  local common_args=(
    -i "$tmp_inventory"
    -e "target=simple_vps"
    -e "@$tmp_vars"
  )

  local bootstrap_ssh_args=( -u "$PLAN_BOOTSTRAP_USER" )
  local apply_ssh_args=( -u "$PLAN_OPERATOR_USER" )
  if [[ -n "$PLAN_SSH_KEY" ]]; then
    bootstrap_ssh_args+=( --private-key "$PLAN_SSH_KEY" )
    apply_ssh_args+=( --private-key "$PLAN_SSH_KEY" )
  fi

  step "Phase 1/2: bootstrap"
  run_ansible_playbook "$SCRIPT_DIR/playbooks/vps-bootstrap.yml" \
    "${common_args[@]}" "${bootstrap_ssh_args[@]}"

  step "Phase 2/2: apply"
  if ! run_ansible_playbook "$SCRIPT_DIR/playbooks/vps-apply.yml" \
      "${common_args[@]}" "${apply_ssh_args[@]}"; then
    warn "Apply phase as '${PLAN_OPERATOR_USER}' failed; retrying as '${PLAN_BOOTSTRAP_USER}'."
    run_ansible_playbook "$SCRIPT_DIR/playbooks/vps-apply.yml" \
      "${common_args[@]}" "${bootstrap_ssh_args[@]}"
  fi
}

run_local() {
  if [[ "$(id -u)" -ne 0 ]]; then
    err "Local mode must run as root."
    exit 1
  fi

  ensure_ansible_inplace

  local operator_ssh_public_key_value=""
  local deploy_ssh_public_key_value=""
  local root_keys_path="/root/.ssh/authorized_keys"
  resolve_ssh_key_plan operator_ssh_public_key_value deploy_ssh_public_key_value true "$root_keys_path"

  info "Running in local mode on localhost"

  local tmp_inventory
  local tmp_vars
  tmp_inventory="$(mktemp)"
  tmp_vars="$(mktemp)"
  chmod 600 "$tmp_vars"

  TMP_FILES+=("$tmp_inventory" "$tmp_vars")
  trap cleanup_tmp_files EXIT

  cat > "$tmp_inventory" <<INVENTORY
[simple_vps]
simple_vps_local ansible_connection=local ansible_python_interpreter=/usr/bin/python3
INVENTORY

  write_extra_vars_file "$tmp_vars" "$operator_ssh_public_key_value" "$deploy_ssh_public_key_value"

  local common_args=(
    -i "$tmp_inventory"
    -e "target=simple_vps"
    -e "@$tmp_vars"
  )

  step "Phase 1/2: bootstrap"
  run_ansible_playbook "$SCRIPT_DIR/playbooks/vps-bootstrap.yml" \
    "${common_args[@]}"

  step "Phase 2/2: apply"
  run_ansible_playbook "$SCRIPT_DIR/playbooks/vps-apply.yml" \
    "${common_args[@]}"
}

main() {
  ORIGINAL_ARGC=$#
  parse_args "$@"
  setup_colors
  maybe_run_interactive_wizard
  resolve_install_plan

  if [[ "$INSTALLER_DUMP_PLAN" == "true" ]]; then
    dump_install_plan
    return
  fi

  prepare_ansible_env
  ensure_simple_vps_layout "$@"

  info "Simple VPS installer starting"
  info "Mode: $PLAN_MODE"
  info "Operator user: $PLAN_OPERATOR_USER"
  info "Deploy user: $PLAN_DEPLOY_USER"
  info "Timezone: $PLAN_TIMEZONE"
  info "Tailscale: $PLAN_TAILSCALE"
  if [[ "$PLAN_TAILSCALE" == "true" ]]; then
    info "Tailscale auth: $(present_or_missing "$PLAN_TAILSCALE_AUTH_KEY" "auth key provided" "manual login required")"
  fi
  info "Cloudflare Tunnel: $PLAN_CLOUDFLARE_TUNNEL"
  if [[ "$PLAN_CLOUDFLARE_TUNNEL" == "true" ]]; then
    if [[ -n "$PLAN_CLOUDFLARE_API_TOKEN" ]]; then
      info "Cloudflare API: token provided"
    elif [[ -n "$PLAN_CLOUDFLARE_TUNNEL_CONFIG" ]]; then
      info "Cloudflare Tunnel config: $PLAN_CLOUDFLARE_TUNNEL_CONFIG"
    else
      info "Cloudflare Tunnel auth: $(present_or_missing "$PLAN_CLOUDFLARE_TUNNEL_TOKEN" "token provided" "service not enabled")"
    fi
  fi
  info "Docker: $PLAN_INSTALL_DOCKER"
  info "Litestream: $PLAN_INSTALL_LITESTREAM"

  case "$PLAN_MODE" in
    remote)
      run_remote
      ;;
    local)
      run_local
      ;;
  esac

  info "Provisioning complete"
}

main "$@"

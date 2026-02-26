#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

CYCLES="${CYCLES:-3}"
PROFILE_FILE="${PROFILE_FILE:-profiles/test-loop.env}"
HOST_ENV_FILE=""
CONFIRM=false

usage() {
  cat <<USAGE
Usage: ./scripts/compat/repeat-scratch-cycles.sh [--cycles N] [--profile FILE] [--host-env FILE] --yes

Destructive loop:
  1) Uninstall k3s
  2) Install k3s
  3) Apply cluster layer
  4) Delete cluster layer
  5) Uninstall k3s

Safety:
  - Refuses Let’s Encrypt prod loops unless ALLOW_LETSENCRYPT_PROD_LOOP=true.
  - Requires explicit --yes.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cycles) CYCLES="$2"; shift 2 ;;
    --profile) PROFILE_FILE="$2"; shift 2 ;;
    --host-env) HOST_ENV_FILE="$2"; shift 2 ;;
    --yes) CONFIRM=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; usage; exit 1 ;;
  esac
done

die() { printf "[ERROR] %s\n" "$*" >&2; exit 1; }
log() { printf "[INFO] %s\n" "$*"; }

[[ "$CONFIRM" == "true" ]] || die "Refusing destructive test loop without --yes"
[[ "$CYCLES" =~ ^[0-9]+$ ]] || die "--cycles must be numeric"
[[ "$CYCLES" -ge 1 ]] || die "--cycles must be >= 1"
[[ -f "$PROFILE_FILE" ]] || die "Profile not found: $PROFILE_FILE"

# Load env contract for safety checks.
set -a
[[ -f "$ROOT_DIR/config.env" ]] && source "$ROOT_DIR/config.env"
if [[ "${PROFILE_EXCLUSIVE:-false}" == "false" ]]; then
  [[ -f "$ROOT_DIR/profiles/local.env" ]] && source "$ROOT_DIR/profiles/local.env"
  [[ -f "$ROOT_DIR/profiles/secrets.env" ]] && source "$ROOT_DIR/profiles/secrets.env"
fi
source "$PROFILE_FILE"
set +a

if [[ "${CLUSTER_ISSUER:-selfsigned}" == "letsencrypt" ]]; then
  le_env="${LETSENCRYPT_ENV:-staging}"
  le_create_prod="${LETSENCRYPT_CREATE_PROD_ISSUER:-true}"
  if [[ "$le_create_prod" != "true" && "$le_create_prod" != "false" ]]; then
    die "LETSENCRYPT_CREATE_PROD_ISSUER must be true|false (got: $le_create_prod)"
  fi
  if [[ "$le_env" == "prod" || "$le_env" == "production" || "$le_create_prod" == "true" ]]; then
    [[ "${ALLOW_LETSENCRYPT_PROD_LOOP:-false}" == "true" ]] || die \
      "This loop would hit Let’s Encrypt production (LETSENCRYPT_ENV=$le_env LETSENCRYPT_CREATE_PROD_ISSUER=$le_create_prod). Set ALLOW_LETSENCRYPT_PROD_LOOP=true to override."
  fi
fi

run_k3s_uninstall() {
  if [[ ! -x "/usr/local/bin/k3s-uninstall.sh" ]]; then
    log "k3s-uninstall.sh not found; skipping host uninstall"
    return 0
  fi
  log "Uninstalling k3s"
  if command -v sudo >/dev/null 2>&1; then
    sudo /usr/local/bin/k3s-uninstall.sh || true
  else
    /usr/local/bin/k3s-uninstall.sh || true
  fi
}

run_k3s_install() {
  local -a host_args=()
  [[ -n "$HOST_ENV_FILE" ]] && host_args+=(--host-env "$HOST_ENV_FILE")
  log "Installing k3s"
  ./scripts/manual_install_k3s_minimal.sh "${host_args[@]}"
}

for i in $(seq 1 "$CYCLES"); do
  log "=== Scratch cycle ${i}/${CYCLES} ==="
  run_k3s_uninstall
  run_k3s_install

  log "Applying platform with profile: $PROFILE_FILE"
  ./run.sh --profile "$PROFILE_FILE"

  log "Deleting platform with profile: $PROFILE_FILE"
  ./run.sh --profile "$PROFILE_FILE" --delete

  run_k3s_uninstall
done

log "Scratch loop complete (${CYCLES} cycle(s))"

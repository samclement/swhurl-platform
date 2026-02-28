#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

CYCLES="${CYCLES:-3}"
PROFILE_FILE="${PROFILE_FILE:-}"
CERT_MODE="${CERT_MODE:-staging}" # staging|selfsigned (ignored when --profile is provided)
HOST_ENV_FILE=""
CONFIRM=false
TMP_PROFILE=""

usage() {
  cat <<USAGE
Usage: ./scripts/compat/repeat-scratch-cycles.sh [--cycles N] [--cert-mode staging|selfsigned] [--profile FILE] [--host-env FILE] --yes

Destructive loop:
  1) Uninstall k3s
  2) Install k3s
  3) Apply cluster layer
  4) Delete cluster layer
  5) Uninstall k3s

Safety:
  - Refuses loops that would hit Let’s Encrypt production endpoints unless ALLOW_LETSENCRYPT_PROD_LOOP=true.
  - Requires explicit --yes.
Defaults:
  - If --profile is omitted, a temporary profile is generated from --cert-mode (default: staging).
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cycles) CYCLES="$2"; shift 2 ;;
    --cert-mode) CERT_MODE="$2"; shift 2 ;;
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

case "$CERT_MODE" in
  staging|selfsigned) ;;
  *) die "--cert-mode must be staging or selfsigned (got: ${CERT_MODE})" ;;
esac

if [[ -z "$PROFILE_FILE" ]]; then
  TMP_PROFILE="$(mktemp)"
  case "$CERT_MODE" in
    staging)
      cat >"$TMP_PROFILE" <<EOF
PLATFORM_CLUSTER_ISSUER=letsencrypt-staging
LETSENCRYPT_ENV=staging
LETSENCRYPT_PROD_SERVER=https://acme-staging-v02.api.letsencrypt.org/directory
EOF
      ;;
    selfsigned)
      cat >"$TMP_PROFILE" <<EOF
PLATFORM_CLUSTER_ISSUER=selfsigned
LETSENCRYPT_ENV=staging
LETSENCRYPT_STAGING_SERVER=https://acme-staging-v02.api.letsencrypt.org/directory
LETSENCRYPT_PROD_SERVER=https://acme-staging-v02.api.letsencrypt.org/directory
EOF
      ;;
  esac
  PROFILE_FILE="$TMP_PROFILE"
fi
[[ -f "$PROFILE_FILE" ]] || die "Profile not found: $PROFILE_FILE"
trap '[[ -n "$TMP_PROFILE" ]] && rm -f "$TMP_PROFILE"' EXIT

# Load env contract for safety checks.
set -a
[[ -f "$ROOT_DIR/config.env" ]] && source "$ROOT_DIR/config.env"
if [[ "${PROFILE_EXCLUSIVE:-false}" == "false" ]]; then
  [[ -f "$ROOT_DIR/profiles/local.env" ]] && source "$ROOT_DIR/profiles/local.env"
  [[ -f "$ROOT_DIR/profiles/secrets.env" ]] && source "$ROOT_DIR/profiles/secrets.env"
fi
source "$PROFILE_FILE"
set +a

prod_server="https://acme-v02.api.letsencrypt.org/directory"
staging_server="${LETSENCRYPT_STAGING_SERVER:-https://acme-staging-v02.api.letsencrypt.org/directory}"
configured_prod_server="${LETSENCRYPT_PROD_SERVER:-$prod_server}"
le_env="${LETSENCRYPT_ENV:-staging}"
case "$le_env" in
  prod|production) alias_server="$configured_prod_server" ;;
  *) alias_server="$staging_server" ;;
esac

if [[ "$configured_prod_server" == "$prod_server" || "$alias_server" == "$prod_server" ]]; then
  [[ "${ALLOW_LETSENCRYPT_PROD_LOOP:-false}" == "true" ]] || die \
    "This loop would hit Let’s Encrypt production (LETSENCRYPT_ENV=$le_env LETSENCRYPT_PROD_SERVER=$configured_prod_server). Set ALLOW_LETSENCRYPT_PROD_LOOP=true to override."
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
  ./host/run-host.sh --only 20_install_k3s.sh "${host_args[@]}"
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

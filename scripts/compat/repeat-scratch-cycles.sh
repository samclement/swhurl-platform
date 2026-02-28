#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

CYCLES="${CYCLES:-3}"
PROFILE_FILE="${PROFILE_FILE:-}"
CERT_MODE="${CERT_MODE:-staging}" # staging|prod
HOST_ENV_FILE=""
CONFIRM=false

usage() {
  cat <<USAGE
Usage: ./scripts/compat/repeat-scratch-cycles.sh [--cycles N] [--cert-mode staging|prod] [--profile FILE] [--host-env FILE] --yes

Destructive loop:
  1) Uninstall k3s
  2) Install k3s
  3) Apply cluster layer
  4) Delete cluster layer
  5) Uninstall k3s

Safety:
  - Refuses loops in production cert mode unless ALLOW_LETSENCRYPT_PROD_LOOP=true.
  - Requires explicit --yes.
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
  staging|prod) ;;
  *) die "--cert-mode must be staging or prod (got: ${CERT_MODE})" ;;
esac

if [[ "$CERT_MODE" == "prod" ]]; then
  [[ "${ALLOW_LETSENCRYPT_PROD_LOOP:-false}" == "true" ]] || die \
    "This loop is set to production cert mode. Set ALLOW_LETSENCRYPT_PROD_LOOP=true to override."
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

  log "Setting platform cert mode: ${CERT_MODE}"
  ./scripts/bootstrap/set-flux-path-modes.sh --platform-cert-env "$CERT_MODE"

  log "Applying platform"
  if [[ -n "$PROFILE_FILE" ]]; then
    [[ -f "$PROFILE_FILE" ]] || die "Profile not found: $PROFILE_FILE"
    ./run.sh --profile "$PROFILE_FILE"
  else
    ./run.sh
  fi

  log "Deleting platform"
  if [[ -n "$PROFILE_FILE" ]]; then
    ./run.sh --profile "$PROFILE_FILE" --delete
  else
    ./run.sh --delete
  fi

  run_k3s_uninstall
done

log "Scratch loop complete (${CYCLES} cycle(s))"

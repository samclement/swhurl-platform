#!/usr/bin/env bash
set -Eeuo pipefail

# Common helpers for platform scripts

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Shared verification/teardown contract.
# shellcheck disable=SC1091
source "$SCRIPT_DIR/00_verify_contract_lib.sh"

# Load config and profile and export for child process consumption.
set -a
if [[ -f "$ROOT_DIR/config.env" ]]; then
  # shellcheck disable=SC1090
  source "$ROOT_DIR/config.env"
fi

# Profile layering:
# - By default: config.env -> profiles/local.env -> profiles/secrets.env -> PROFILE_FILE (highest precedence)
# - Opt out (standalone profile): PROFILE_EXCLUSIVE=true uses only config.env -> PROFILE_FILE
PROFILE_EXCLUSIVE="${PROFILE_EXCLUSIVE:-false}"
if [[ "$PROFILE_EXCLUSIVE" != "true" && "$PROFILE_EXCLUSIVE" != "false" ]]; then
  die "PROFILE_EXCLUSIVE must be true or false (got: $PROFILE_EXCLUSIVE)"
fi

if [[ "$PROFILE_EXCLUSIVE" == "false" && -f "$ROOT_DIR/profiles/local.env" ]]; then
  # shellcheck disable=SC1090
  source "$ROOT_DIR/profiles/local.env"
fi
if [[ "$PROFILE_EXCLUSIVE" == "false" && -f "$ROOT_DIR/profiles/secrets.env" ]]; then
  # shellcheck disable=SC1090
  source "$ROOT_DIR/profiles/secrets.env"
fi
if [[ -n "${PROFILE_FILE:-}" && -f "$PROFILE_FILE" ]]; then
  # shellcheck disable=SC1090
  source "$PROFILE_FILE"
fi
set +a

log_info() { printf "[INFO] %s\n" "$*"; }
log_warn() { printf "[WARN] %s\n" "$*"; }
log_error() { printf "[ERROR] %s\n" "$*" >&2; }
die() { log_error "$*"; exit 1; }

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "Missing required command: $1"
}

ensure_context() {
  need_cmd kubectl
  # Robust reachability check that works across kubectl versions
  kubectl get --raw=/version >/dev/null 2>&1 || die "kubectl cannot reach a cluster; ensure kubeconfig is set"
}

wait_deploy() {
  local ns="$1" name="$2" timeout="${3:-${TIMEOUT_SECS:-300}}"
  kubectl -n "$ns" rollout status deploy/"$name" --timeout="${timeout}s"
}

wait_ds() {
  local ns="$1" name="$2" timeout="${3:-${TIMEOUT_SECS:-300}}"
  kubectl -n "$ns" rollout status ds/"$name" --timeout="${timeout}s"
}

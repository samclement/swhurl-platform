#!/usr/bin/env bash
set -Eeuo pipefail

# Common helpers for platform scripts

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Load config and profile. Helmfile templates use env-vars via `env "FOO"`,
# so we need to export loaded values, not just set shell variables.
set -a
if [[ -f "$ROOT_DIR/config.env" ]]; then
  # shellcheck disable=SC1090
  source "$ROOT_DIR/config.env"
fi
if [[ -n "${PROFILE_FILE:-}" && -f "$PROFILE_FILE" ]]; then
  # shellcheck disable=SC1090
  source "$PROFILE_FILE"
elif [[ -f "$ROOT_DIR/profiles/secrets.env" ]]; then
  # shellcheck disable=SC1090
  source "$ROOT_DIR/profiles/secrets.env"
elif [[ -f "$ROOT_DIR/profiles/local.env" ]]; then
  # shellcheck disable=SC1090
  source "$ROOT_DIR/profiles/local.env"
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

kubectl_ns() {
  local ns="$1"
  kubectl get ns "$ns" >/dev/null 2>&1 || kubectl create namespace "$ns" >/dev/null
}

wait_deploy() {
  local ns="$1" name="$2" timeout="${3:-${TIMEOUT_SECS:-300}}"
  kubectl -n "$ns" rollout status deploy/"$name" --timeout="${timeout}s"
}

wait_ds() {
  local ns="$1" name="$2" timeout="${3:-${TIMEOUT_SECS:-300}}"
  kubectl -n "$ns" rollout status ds/"$name" --timeout="${timeout}s"
}

wait_webhook_cabundle() {
  local name="$1" timeout="${2:-${TIMEOUT_SECS:-300}}"
  local start now ca
  start=$(date +%s)
  while true; do
    if kubectl get validatingwebhookconfiguration "$name" >/dev/null 2>&1; then
      ca=$(kubectl get validatingwebhookconfiguration "$name" -o jsonpath='{.webhooks[0].clientConfig.caBundle}' 2>/dev/null || true)
      if [[ -n "$ca" ]]; then
        log_info "Webhook '${name}' CA bundle populated"
        return 0
      fi
    fi
    now=$(date +%s)
    if (( now - start >= timeout )); then
      return 1
    fi
    sleep 2
  done
}

helm_upsert() {
  local release="$1" chart="$2" ns="$3"; shift 3
  kubectl_ns "$ns"
  helm upgrade --install "$release" "$chart" --namespace "$ns" "$@"
}

helmfile_cmd() {
  need_cmd helmfile
  local hf_file="${HELMFILE_FILE:-$ROOT_DIR/helmfile.yaml.gotmpl}"
  local hf_env="${HELMFILE_ENV:-default}"
  helmfile -f "$hf_file" -e "$hf_env" "$@"
}

sync_release() {
  local release="$1"
  helmfile_cmd -l app="$release" sync
}

destroy_release() {
  local release="$1"
  helmfile_cmd -l app="$release" destroy
}

label_managed() {
  local ns="$1" kind="$2" name="$3"
  kubectl -n "$ns" label "$kind" "$name" platform.swhurl.io/managed=true --overwrite >/dev/null 2>&1 || true
}

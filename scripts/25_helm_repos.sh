#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

need_cmd helm

add_repo() {
  local name="$1" url="$2" required="${3:-true}"
  local tries=0 max_tries="${HELM_REPO_RETRIES:-3}"
  while true; do
    if helm repo add "$name" "$url" >/dev/null 2>&1; then
      return 0
    fi
    tries=$((tries + 1))
    if (( tries >= max_tries )); then
      if [[ "$required" == "true" ]]; then
        die "Failed to add Helm repo '${name}' (${url}) after ${max_tries} attempts"
      fi
      log_warn "Failed to add optional Helm repo '${name}' (${url}); continuing"
      return 0
    fi
    sleep 2
  done
}

# Only add repos needed for enabled features so transient repo outages don't
# block unrelated installs.
add_repo jetstack https://charts.jetstack.io true
add_repo ingress-nginx https://kubernetes.github.io/ingress-nginx true

if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  add_repo oauth2-proxy https://oauth2-proxy.github.io/manifests true
fi
if [[ "${FEAT_CILIUM:-true}" == "true" ]]; then
  add_repo cilium https://helm.cilium.io/ true
fi
if [[ "${FEAT_CLICKSTACK:-true}" == "true" ]]; then
  add_repo clickstack https://clickhouse.github.io/ClickStack-helm-charts true
fi
if [[ "${FEAT_OTEL_K8S:-true}" == "true" ]]; then
  add_repo open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts true
fi
if [[ "${FEAT_MINIO:-true}" == "true" ]]; then
  add_repo minio https://charts.min.io/ true
fi

helm repo update >/dev/null 2>&1 || true
log_info "Helm repositories added/updated"

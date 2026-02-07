#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_MESH_ISTIO:-false}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_MESH_ISTIO=false; skipping istio"
  exit 0
fi

if ! command -v istioctl >/dev/null 2>&1; then
  log_warn "istioctl not installed; skipping"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling istio (minimal profile)"
  istioctl uninstall --purge -y || true
  kubectl delete ns istio-system --ignore-not-found
  exit 0
fi

log_info "Installing Istio minimal profile"
istioctl install -y --set profile=minimal
kubectl -n istio-system get pods

log_info "Istio installed"


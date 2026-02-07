#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling cert-manager"
  helm uninstall cert-manager -n cert-manager || true
  # Optionally remove CRDs (commented out by default)
  # kubectl delete crd -l app.kubernetes.io/name=cert-manager || true
  exit 0
fi

helm_upsert cert-manager jetstack/cert-manager cert-manager \
  --set installCRDs=true

kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager --timeout=${TIMEOUT_SECS:-300}s
kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager-webhook --timeout=${TIMEOUT_SECS:-300}s
kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager-cainjector --timeout=${TIMEOUT_SECS:-300}s

log_info "cert-manager installed"


#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

log_info "Smoke tests: nodes ready"
kubectl get nodes -o wide

log_info "Smoke tests: cert-manager pods"
kubectl -n cert-manager get pods

log_info "Smoke tests: ingress controller"
kubectl -n ingress get svc,deploy

if [[ "${FEAT_OBS:-true}" == "true" ]]; then
  log_info "Smoke tests: monitoring stack"
  kubectl -n observability get pods
fi

if [[ "${FEAT_MINIO:-true}" == "true" ]]; then
  log_info "Smoke tests: minio"
  kubectl -n storage get pods,svc,ingress
fi

log_info "Smoke tests finished"


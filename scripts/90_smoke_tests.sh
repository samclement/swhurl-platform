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

# Inbound connectivity test to Ingress via fixed NodePort 30313
if command -v curl >/dev/null 2>&1; then
  HOST="hello.${BASE_DOMAIN}"
  log_info "Smoke tests: HTTPS NodePort 30313 -> ${HOST}"
  set +e
  CODE=$(curl -sk -o /dev/null -w "%{http_code}" -H "Host: ${HOST}" https://127.0.0.1:30313/)
  set -e
  log_info "NodePort 30313 responded with HTTP ${CODE}"
else
  log_warn "curl not found; skipping NodePort connectivity test"
fi

if [[ "${FEAT_OBS:-true}" == "true" ]]; then
  log_info "Smoke tests: monitoring stack"
  kubectl -n observability get pods
fi

if [[ "${FEAT_MINIO:-true}" == "true" ]]; then
  log_info "Smoke tests: minio"
  kubectl -n storage get pods,svc,ingress
fi

log_info "Smoke tests finished"

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

# Optionally disable chart's startup API check job (defaults to false) to
# avoid Helm post-install timeouts on slower or freshly booted clusters.
STARTUP_APICHECK="${CM_STARTUP_API_CHECK:-false}"
helm_upsert cert-manager jetstack/cert-manager cert-manager \
  --set installCRDs=true \
  --set startupapicheck.enabled=${STARTUP_APICHECK}

kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager --timeout=${TIMEOUT_SECS:-300}s
kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager-webhook --timeout=${TIMEOUT_SECS:-300}s
kubectl -n cert-manager wait --for=condition=Available deploy/cert-manager-cainjector --timeout=${TIMEOUT_SECS:-300}s

# Ensure webhook CA bundle is injected before proceeding to issuer creation
if ! wait_webhook_cabundle cert-manager-webhook "${TIMEOUT_SECS:-300}"; then
  log_warn "Webhook CA bundle not ready; restarting webhook/cainjector and retrying"
  kubectl -n cert-manager rollout restart deploy/cert-manager-webhook deploy/cert-manager-cainjector >/dev/null 2>&1 || true
  kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=${TIMEOUT_SECS:-300}s
  kubectl -n cert-manager rollout status deploy/cert-manager-cainjector --timeout=${TIMEOUT_SECS:-300}s
  if ! wait_webhook_cabundle cert-manager-webhook "${TIMEOUT_SECS:-300}"; then
    die "cert-manager webhook CA bundle still not ready; retry later or inspect cert-manager-webhook"
  fi
fi

log_info "cert-manager installed"

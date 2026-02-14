#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling cert-manager"
  if helm -n cert-manager status cert-manager >/dev/null 2>&1; then
    helm uninstall cert-manager -n cert-manager || true
  else
    log_info "cert-manager release not present; skipping helm uninstall"
  fi
  if [[ "${CM_DELETE_CRDS:-true}" == "true" ]]; then
    crds="$(kubectl get crd -o name 2>/dev/null | rg 'cert-manager\.io|acme\.cert-manager\.io' || true)"
    if [[ -n "$crds" ]]; then
      log_info "Deleting cert-manager CRDs"
      # shellcheck disable=SC2086
      kubectl delete $crds --ignore-not-found || true
    fi
  fi
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

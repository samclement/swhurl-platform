#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

if [[ "$DELETE" == true ]]; then
  log_info "Delete-time runtime input cleanup moved to scripts/99_execute_teardown.sh; nothing to do"
  exit 0
fi

ensure_context

log_warn "scripts/29_prepare_platform_runtime_inputs.sh is a manual compatibility bridge; default ./run.sh apply does not invoke it."

# oauth2-proxy secret (required by oauth2-proxy Helm release)
if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  [[ -n "${OIDC_CLIENT_ID:-}" ]] || die "OIDC_CLIENT_ID is required when FEAT_OAUTH2_PROXY=true"
  [[ -n "${OIDC_CLIENT_SECRET:-}" ]] || die "OIDC_CLIENT_SECRET is required when FEAT_OAUTH2_PROXY=true"

  kubectl_ns ingress

  log_info "Ensuring ingress/Secret oauth2-proxy-secret (OIDC client + cookie secret)"

  # oauth2-proxy expects a cookie secret that is exactly 16, 24, or 32 bytes.
  # Create-once semantics:
  # - If OAUTH_COOKIE_SECRET is set: enforce it and update the Secret.
  # - Else: do not mutate an existing Secret (avoids auth outages on rerun).
  GEN_COOKIE_SECRET() { hexdump -v -e '/1 "%02X"' -n 16 /dev/urandom; }
  if kubectl -n ingress get secret oauth2-proxy-secret >/dev/null 2>&1; then
    if [[ -n "${OAUTH_COOKIE_SECRET:-}" ]]; then
      kubectl -n ingress create secret generic oauth2-proxy-secret \
        --from-literal=client-id="${OIDC_CLIENT_ID}" \
        --from-literal=client-secret="${OIDC_CLIENT_SECRET}" \
        --from-literal=cookie-secret="${OAUTH_COOKIE_SECRET}" \
        --dry-run=client -o yaml | kubectl apply -f -
    else
      log_info "oauth2-proxy-secret already exists and OAUTH_COOKIE_SECRET is unset; leaving cookie secret unchanged"
    fi
  else
    COOKIE_SECRET_VAL="${OAUTH_COOKIE_SECRET:-$(GEN_COOKIE_SECRET)}"
    kubectl -n ingress create secret generic oauth2-proxy-secret \
      --from-literal=client-id="${OIDC_CLIENT_ID}" \
      --from-literal=client-secret="${OIDC_CLIENT_SECRET}" \
      --from-literal=cookie-secret="${COOKIE_SECRET_VAL}" \
      --dry-run=client -o yaml | kubectl apply -f -
  fi

  label_managed ingress secret oauth2-proxy-secret

  # Verify secret exists and cookie-secret length is valid (16/24/32)
  for i in {1..10}; do
    if kubectl -n ingress get secret oauth2-proxy-secret >/dev/null 2>&1; then
      LEN=$(kubectl -n ingress get secret oauth2-proxy-secret -o jsonpath='{.data.cookie-secret}' | base64 -d | wc -c | tr -d '[:space:]')
      if [[ "$LEN" == "16" || "$LEN" == "24" || "$LEN" == "32" ]]; then
        break
      fi
    fi
    sleep 1
  done
  LEN=$(kubectl -n ingress get secret oauth2-proxy-secret -o jsonpath='{.data.cookie-secret}' | base64 -d | wc -c | tr -d '[:space:]' || echo 0)
  if [[ "$LEN" != "16" && "$LEN" != "24" && "$LEN" != "32" ]]; then
    die "oauth2-proxy-secret not created or invalid cookie-secret length ($LEN)"
  fi
fi

# Kubernetes OTel collectors config (required by otel-k8s Helm releases)
if [[ "${FEAT_OTEL_K8S:-true}" == "true" ]]; then
  kubectl_ns logging

  CLICKSTACK_KEY="${CLICKSTACK_API_KEY:-}"
  [[ -n "$CLICKSTACK_KEY" ]] || die "CLICKSTACK_API_KEY is required when FEAT_OTEL_K8S=true"

  log_info "Ensuring logging/Secret hyperdx-secret (HYPERDX_API_KEY from CLICKSTACK_API_KEY)"
  kubectl -n logging create secret generic hyperdx-secret \
    --from-literal=HYPERDX_API_KEY="$CLICKSTACK_KEY" \
    --dry-run=client -o yaml | kubectl apply -f -

  label_managed logging secret hyperdx-secret
fi

log_info "Platform config resources ensured"

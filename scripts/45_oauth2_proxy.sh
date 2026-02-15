#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_OAUTH2_PROXY:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_OAUTH2_PROXY=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling oauth2-proxy"
  destroy_release oauth2-proxy >/dev/null 2>&1 || true
  kubectl -n ingress delete secret oauth2-proxy-secret --ignore-not-found >/dev/null 2>&1 || true
  exit 0
fi

[[ -n "${OIDC_ISSUER:-}" ]] || die "OIDC_ISSUER is required for oauth2-proxy"
[[ -n "${OIDC_CLIENT_ID:-}" ]] || die "OIDC_CLIENT_ID is required for oauth2-proxy"
[[ -n "${OIDC_CLIENT_SECRET:-}" ]] || die "OIDC_CLIENT_SECRET is required for oauth2-proxy"

kubectl_ns ingress
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

REDIRECT_URL="${OAUTH_REDIRECT_URL:-https://${OAUTH_HOST}/oauth2/callback}"
export OAUTH_REDIRECT_URL="$REDIRECT_URL"
sync_release oauth2-proxy

wait_deploy ingress oauth2-proxy
# If the Secret was updated explicitly, the deploy will roll anyway; no forced restart here.
log_info "oauth2-proxy installed"

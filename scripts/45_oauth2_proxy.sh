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
  helm uninstall oauth2-proxy -n ingress || true
  kubectl -n ingress delete secret oauth2-proxy-secret --ignore-not-found
  exit 0
fi

if [[ -z "${OIDC_CLIENT_ID:-}" || -z "${OIDC_CLIENT_SECRET:-}" ]]; then
  log_warn "OIDC_CLIENT_ID/SECRET not set; oauth2-proxy will not authenticate. Set in config.env"
fi

kubectl_ns ingress
# oauth2-proxy expects a cookie secret that is exactly 16, 24, or 32 bytes.
# Generate a 32-character ASCII hex secret safely (no pipefail issues) if not provided.
GEN_COOKIE_SECRET() { hexdump -v -e '/1 "%02X"' -n 16 /dev/urandom; }
COOKIE_SECRET_VAL="${OAUTH_COOKIE_SECRET:-$(GEN_COOKIE_SECRET)}"
kubectl -n ingress create secret generic oauth2-proxy-secret \
  --from-literal=client-id="${OIDC_CLIENT_ID:-unset}" \
  --from-literal=client-secret="${OIDC_CLIENT_SECRET:-unset}" \
  --from-literal=cookie-secret="${COOKIE_SECRET_VAL}" \
  --dry-run=client -o yaml | kubectl apply -f -

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

PARENT_DOMAIN=".${BASE_DOMAIN}"
helm_upsert oauth2-proxy oauth2-proxy/oauth2-proxy ingress \
  --set config.existingSecret=oauth2-proxy-secret \
  --set extraArgs.provider=oidc \
  --set extraArgs.oidc-issuer-url="${OIDC_ISSUER:-https://example.com}" \
  --set extraArgs.redirect-url="${REDIRECT_URL}" \
  --set extraArgs.email-domain="*" \
  --set extraArgs.standard-logging=true \
  --set extraArgs.standard-logging-format=json \
  --set extraArgs.request-logging=true \
  --set extraArgs.request-logging-format=json \
  --set extraArgs.auth-logging=true \
  --set extraArgs.auth-logging-format=json \
  --set extraArgs.silence-ping-logging=true \
  --set extraArgs.cookie-domain="${PARENT_DOMAIN}" \
  --set extraArgs.whitelist-domain="${PARENT_DOMAIN}" \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.annotations."cert-manager\.io/cluster-issuer"=${CLUSTER_ISSUER} \
  --set ingress.hosts[0]="${OAUTH_HOST}" \
  --set ingress.tls[0].hosts[0]="${OAUTH_HOST}" \
  --set ingress.tls[0].secretName=oauth2-proxy-tls

wait_deploy ingress oauth2-proxy
# Ensure pods pick up any updated secret values (cookie-secret changes)
kubectl -n ingress rollout restart deploy/oauth2-proxy >/dev/null 2>&1 || true
wait_deploy ingress oauth2-proxy
log_info "oauth2-proxy installed"

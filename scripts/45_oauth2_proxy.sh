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
kubectl -n ingress create secret generic oauth2-proxy-secret \
  --from-literal=client-id="${OIDC_CLIENT_ID:-unset}" \
  --from-literal=client-secret="${OIDC_CLIENT_SECRET:-unset}" \
  --from-literal=cookie-secret="${OAUTH_COOKIE_SECRET:-$(openssl rand -base64 32 2>/dev/null || echo dummysecret)}" \
  --dry-run=client -o yaml | kubectl apply -f -

helm_upsert oauth2-proxy oauth2-proxy/oauth2-proxy ingress \
  --set config.existingSecret=oauth2-proxy-secret \
  --set extraArgs.provider=oidc \
  --set extraArgs.oidc-issuer-url="${OIDC_ISSUER:-https://example.com}" \
  --set extraArgs.redirect-url="https://${OAUTH_HOST}/oauth2/callback" \
  --set extraArgs.email-domain="*" \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.annotations."cert-manager\.io/cluster-issuer"=${CLUSTER_ISSUER} \
  --set ingress.hosts[0].host="${OAUTH_HOST}" \
  --set ingress.hosts[0].paths[0].path="/" \
  --set ingress.tls[0].hosts[0]="${OAUTH_HOST}" \
  --set ingress.tls[0].secretName=oauth2-proxy-tls

wait_deploy ingress oauth2-proxy
log_info "oauth2-proxy installed"


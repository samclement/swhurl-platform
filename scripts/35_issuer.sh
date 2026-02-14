#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

ISSUER_NAME="${CLUSTER_ISSUER:-selfsigned}"
LE_ENV="${LETSENCRYPT_ENV:-staging}"
case "$LE_ENV" in
  staging|prod|production) ;;
  *) die "LETSENCRYPT_ENV must be one of: staging, prod, production (got: $LE_ENV)" ;;
esac
[[ "$LE_ENV" == "production" ]] && LE_ENV="prod"

render_le_issuer() {
  local name="$1" server="$2" key_secret="$3"
  cat <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: ${name}
spec:
  acme:
    email: ${ACME_EMAIL}
    server: ${server}
    privateKeySecretRef:
      name: ${key_secret}
    solvers:
    - http01:
        ingress:
          class: nginx
EOF
}

if [[ "$DELETE" == true ]]; then
  log_info "Deleting ClusterIssuer '${ISSUER_NAME}' if present"
  if kubectl api-resources --api-group=cert-manager.io -o name 2>/dev/null | rg -q '(^|[.])clusterissuers([.]|$)'; then
    kubectl delete clusterissuer "$ISSUER_NAME" --ignore-not-found || true
    if [[ "$ISSUER_NAME" == "letsencrypt" ]]; then
      kubectl delete clusterissuer letsencrypt-staging letsencrypt-prod --ignore-not-found || true
    fi
  else
    log_info "clusterissuers.cert-manager.io not present; skipping"
  fi
  exit 0
fi

case "$ISSUER_NAME" in
  selfsigned)
    kubectl api-resources --api-group=cert-manager.io -o name 2>/dev/null | rg -q '(^|[.])clusterissuers([.]|$)' \
      || die "cert-manager CRDs are not present (clusterissuers.cert-manager.io missing). Install cert-manager first."
    kubectl apply -k "$SCRIPT_DIR/../infra/manifests/issuers/selfsigned"
    ;;
  letsencrypt)
    kubectl api-resources --api-group=cert-manager.io -o name 2>/dev/null | rg -q '(^|[.])clusterissuers([.]|$)' \
      || die "cert-manager CRDs are not present (clusterissuers.cert-manager.io missing). Install cert-manager first."
    if [[ -z "${ACME_EMAIL:-}" ]]; then
      die "ACME_EMAIL is empty. Set it in profiles/secrets.env or via --profile before creating the 'letsencrypt' ClusterIssuer."
    fi
    render_le_issuer \
      "letsencrypt-staging" \
      "https://acme-staging-v02.api.letsencrypt.org/directory" \
      "acme-account-key-staging" | kubectl apply -f -
    render_le_issuer \
      "letsencrypt-prod" \
      "https://acme-v02.api.letsencrypt.org/directory" \
      "acme-account-key-prod" | kubectl apply -f -

    if [[ "$LE_ENV" == "staging" ]]; then
      render_le_issuer \
        "letsencrypt" \
        "https://acme-staging-v02.api.letsencrypt.org/directory" \
        "acme-account-key" | kubectl apply -f -
      log_info "ClusterIssuer 'letsencrypt' set to staging"
    else
      render_le_issuer \
        "letsencrypt" \
        "https://acme-v02.api.letsencrypt.org/directory" \
        "acme-account-key" | kubectl apply -f -
      log_info "ClusterIssuer 'letsencrypt' set to production"
    fi
    ;;
  *)
    die "Unknown CLUSTER_ISSUER: $ISSUER_NAME"
    ;;
esac

log_info "ClusterIssuer '$ISSUER_NAME' ensured"

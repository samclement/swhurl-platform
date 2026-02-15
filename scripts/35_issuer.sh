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

has_clusterissuers_api() {
  # Primary: discovery (fast). Fallback: CRD presence (handles transient discovery hiccups).
  if kubectl api-resources --api-group=cert-manager.io -o name 2>/dev/null | rg -q '(^|[.])clusterissuers([.]|$)'; then
    return 0
  fi
  kubectl get crd clusterissuers.cert-manager.io >/dev/null 2>&1
}

if [[ "$DELETE" == true ]]; then
  log_info "Deleting ClusterIssuer '${ISSUER_NAME}' if present"
  if has_clusterissuers_api; then
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
    has_clusterissuers_api || die "cert-manager CRDs are not present (clusterissuers.cert-manager.io missing). Install cert-manager first."
    kubectl apply -k "$SCRIPT_DIR/../infra/manifests/issuers/selfsigned"
    ;;
  letsencrypt)
    has_clusterissuers_api || die "cert-manager CRDs are not present (clusterissuers.cert-manager.io missing). Install cert-manager first."
    if [[ -z "${ACME_EMAIL:-}" ]]; then
      die "ACME_EMAIL is empty. Set it in profiles/secrets.env or via --profile before creating the 'letsencrypt' ClusterIssuer."
    fi
    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT
    export ACME_EMAIL
    for d in letsencrypt-staging letsencrypt-prod; do
      kubectl kustomize "$SCRIPT_DIR/../infra/manifests/issuers/${d}" | envsubst '${ACME_EMAIL}' | kubectl apply -f -
    done
    if [[ "$LE_ENV" == "staging" ]]; then
      kubectl kustomize "$SCRIPT_DIR/../infra/manifests/issuers/letsencrypt-alias-staging" | envsubst '${ACME_EMAIL}' | kubectl apply -f -
      log_info "ClusterIssuer 'letsencrypt' set to staging"
    else
      kubectl kustomize "$SCRIPT_DIR/../infra/manifests/issuers/letsencrypt-alias-prod" | envsubst '${ACME_EMAIL}' | kubectl apply -f -
      log_info "ClusterIssuer 'letsencrypt' set to production"
    fi
    ;;
  *)
    die "Unknown CLUSTER_ISSUER: $ISSUER_NAME"
    ;;
esac

log_info "ClusterIssuer '$ISSUER_NAME' ensured"

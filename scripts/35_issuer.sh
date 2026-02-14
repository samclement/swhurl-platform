#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

ISSUER_NAME="${CLUSTER_ISSUER:-selfsigned}"

if [[ "$DELETE" == true ]]; then
  log_info "Deleting ClusterIssuer '${ISSUER_NAME}' if present"
  if kubectl api-resources --api-group=cert-manager.io -o name 2>/dev/null | rg -q '^clusterissuers$'; then
    kubectl delete clusterissuer "$ISSUER_NAME" --ignore-not-found || true
  else
    log_info "clusterissuers.cert-manager.io not present; skipping"
  fi
  exit 0
fi

case "$ISSUER_NAME" in
  selfsigned)
    kubectl apply -k "$SCRIPT_DIR/../infra/manifests/issuers/selfsigned"
    ;;
  letsencrypt)
    if [[ -z "${ACME_EMAIL:-}" ]]; then
      die "ACME_EMAIL is empty. Set it in profiles/secrets.env or via --profile before creating the 'letsencrypt' ClusterIssuer."
    fi
    TMPDIR=$(mktemp -d)
    trap 'rm -rf "$TMPDIR"' EXIT
    cp -r "$SCRIPT_DIR/../infra/manifests/issuers/letsencrypt" "$TMPDIR/issuer"
    ( export ACME_EMAIL; envsubst < "$TMPDIR/issuer/issuer.yaml" > "$TMPDIR/issuer/issuer.rendered.yaml" )
    mv "$TMPDIR/issuer/issuer.rendered.yaml" "$TMPDIR/issuer/issuer.yaml"
    kubectl apply -k "$TMPDIR/issuer"
    ;;
  *)
    die "Unknown CLUSTER_ISSUER: $ISSUER_NAME"
    ;;
esac

log_info "ClusterIssuer '$ISSUER_NAME' ensured"

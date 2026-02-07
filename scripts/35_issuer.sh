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
  kubectl delete clusterissuer "$ISSUER_NAME" --ignore-not-found
  exit 0
fi

case "$ISSUER_NAME" in
  selfsigned)
    cat <<EOF | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: selfsigned
spec:
  selfSigned: {}
EOF
    ;;
  letsencrypt)
    cat <<EOF | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    email: ${ACME_EMAIL}
    server: https://acme-v02.api.letsencrypt.org/directory
    privateKeySecretRef:
      name: acme-account-key
    solvers:
    - http01:
        ingress:
          class: nginx
EOF
    ;;
  *)
    die "Unknown CLUSTER_ISSUER: $ISSUER_NAME"
    ;;
esac

log_info "ClusterIssuer '$ISSUER_NAME' ensured"


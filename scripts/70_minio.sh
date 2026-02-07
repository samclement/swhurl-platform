#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_MINIO:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_MINIO=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling minio"
  helm uninstall minio -n storage || true
  kubectl -n storage delete secret minio-creds --ignore-not-found
  exit 0
fi

kubectl_ns storage
kubectl -n storage create secret generic minio-creds \
  --from-literal=rootUser="${MINIO_ROOT_USER}" \
  --from-literal=rootPassword="${MINIO_ROOT_PASSWORD}" \
  --dry-run=client -o yaml | kubectl apply -f -

helm_upsert minio minio/minio storage \
  --set mode=standalone \
  --set resources.requests.memory=512Mi \
  --set replicas=1 \
  --set persistence.enabled=true \
  --set persistence.size=20Gi \
  --set existingSecret=minio-creds \
  --set ingress.enabled=true \
  --set ingress.ingressClassName=nginx \
  --set ingress.annotations."cert-manager\.io/cluster-issuer"=${CLUSTER_ISSUER} \
  --set ingress.hosts[0].host="${MINIO_HOST}" \
  --set ingress.hosts[0].paths[0].path="/" \
  --set ingress.tls[0].hosts[0]="${MINIO_HOST}" \
  --set ingress.tls[0].secretName=minio-tls \
  --set consoleIngress.enabled=true \
  --set consoleIngress.ingressClassName=nginx \
  --set consoleIngress.annotations."cert-manager\.io/cluster-issuer"=${CLUSTER_ISSUER} \
  --set consoleIngress.hosts[0]="${MINIO_CONSOLE_HOST}" \
  --set consoleIngress.tls[0].hosts[0]="${MINIO_CONSOLE_HOST}" \
  --set consoleIngress.tls[0].secretName=minio-console-tls

log_info "minio installed"


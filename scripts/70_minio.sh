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
  destroy_release minio >/dev/null 2>&1 || true
  kubectl -n storage delete secret minio-creds --ignore-not-found >/dev/null 2>&1 || true
  exit 0
fi

kubectl_ns storage
kubectl -n storage create secret generic minio-creds \
  --from-literal=rootUser="${MINIO_ROOT_USER}" \
  --from-literal=rootPassword="${MINIO_ROOT_PASSWORD}" \
  --dry-run=client -o yaml | kubectl apply -f -

sync_release minio

log_info "minio installed"

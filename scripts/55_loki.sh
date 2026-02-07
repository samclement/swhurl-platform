#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_LOKI:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_LOKI=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling loki"
  helm uninstall loki -n observability || true
  exit 0
fi

helm_upsert loki grafana/loki observability \
  --set loki.auth_enabled=false \
  --set loki.commonConfig.replication_factor=1 \
  --set deploymentMode=SingleBinary \
  --set singleBinary.replicas=1 \
  --set loki.storage.type=filesystem \
  --set loki.useTestSchema=true \
  --set write.replicas=0 \
  --set read.replicas=0 \
  --set backend.replicas=0

log_info "loki installed"

# Wait for single-binary statefulset to be ready and gateway to be up
kubectl -n observability rollout status statefulset/loki --timeout=${TIMEOUT_SECS:-300}s || true
kubectl -n observability rollout status deploy/loki-gateway --timeout=${TIMEOUT_SECS:-300}s || true

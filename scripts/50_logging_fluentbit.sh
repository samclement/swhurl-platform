#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_LOGGING:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_LOGGING=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling fluent-bit"
  helm uninstall fluent-bit -n logging || true
  exit 0
fi

LOKI_URL="http://loki.observability.svc.cluster.local:3100"
helm_upsert fluent-bit fluent/fluent-bit logging \
  --set tolerations[0].operator=Exists \
  --set backend.type=loki \
  --set backend.loki.host="${LOKI_URL}"

wait_ds logging fluent-bit
log_info "fluent-bit installed"


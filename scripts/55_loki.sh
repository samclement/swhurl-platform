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
  --set loki.auth_enabled=false

log_info "loki installed"


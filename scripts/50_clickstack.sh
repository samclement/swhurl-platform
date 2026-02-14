#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_CLICKSTACK:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_CLICKSTACK=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling clickstack and legacy observability releases"
  destroy_release clickstack >/dev/null 2>&1 || true
  helm uninstall fluent-bit -n logging >/dev/null 2>&1 || true
  helm uninstall loki -n observability >/dev/null 2>&1 || true
  helm uninstall monitoring -n observability >/dev/null 2>&1 || true
  exit 0
fi

kubectl_ns observability
CLICKSTACK_HOST="${CLICKSTACK_HOST:-clickstack.${BASE_DOMAIN}}"
CLICKSTACK_API_KEY="${CLICKSTACK_API_KEY:-}"
[[ -n "$CLICKSTACK_API_KEY" ]] || die "CLICKSTACK_API_KEY is required for scripts/50_clickstack.sh"

if [[ "${CLICKSTACK_CLEAN_LEGACY:-true}" == "true" ]]; then
  helm uninstall fluent-bit -n logging >/dev/null 2>&1 || true
  helm uninstall loki -n observability >/dev/null 2>&1 || true
  helm uninstall monitoring -n observability >/dev/null 2>&1 || true
fi

sync_release clickstack

wait_deploy observability clickstack-app
wait_deploy observability clickstack-otel-collector
if kubectl -n observability get deploy clickstack-clickhouse >/dev/null 2>&1; then
  wait_deploy observability clickstack-clickhouse
fi

log_info "clickstack installed at https://${CLICKSTACK_HOST}"
log_warn "ClickStack keys may be generated/rotated at first startup and may not match configured values."
log_warn "After first login to HyperDX, copy the current ingestion key from the UI and set CLICKSTACK_INGESTION_KEY in your profile."
log_info "Then run: ./scripts/51_otel_k8s.sh"

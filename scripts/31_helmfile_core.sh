#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Destroying core platform Helm releases (phase=core)"
  helmfile_cmd -l phase=core destroy >/dev/null 2>&1 || true
  exit 0
fi

log_info "Syncing core platform Helm releases (phase=core)"
helmfile_cmd -l phase=core sync

# Issuer creation immediately after install relies on webhook CA injection, which is
# performed asynchronously by cert-manager-cainjector.
if ! wait_webhook_cabundle cert-manager-webhook "${TIMEOUT_SECS:-300}"; then
  log_warn "Webhook CA bundle not ready; restarting webhook/cainjector and retrying"
  kubectl -n cert-manager rollout restart deploy/cert-manager-webhook deploy/cert-manager-cainjector >/dev/null 2>&1 || true
  kubectl -n cert-manager rollout status deploy/cert-manager-webhook --timeout=${TIMEOUT_SECS:-300}s
  kubectl -n cert-manager rollout status deploy/cert-manager-cainjector --timeout=${TIMEOUT_SECS:-300}s
  if ! wait_webhook_cabundle cert-manager-webhook "${TIMEOUT_SECS:-300}"; then
    die "cert-manager webhook CA bundle still not ready; retry later or inspect cert-manager-webhook/cainjector"
  fi
fi

log_info "Core Helm releases synced"


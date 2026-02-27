#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do
  case "$arg" in
    --delete) DELETE=true ;;
    *) die "Unknown argument: $arg" ;;
  esac
done

ensure_context
need_cmd flux

if [[ "$DELETE" == "true" ]]; then
  log_info "Deleting Flux stack kustomizations"
  kubectl -n flux-system delete kustomization homelab-flux-stack --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n flux-system delete kustomization homelab-flux-sources --ignore-not-found >/dev/null 2>&1 || true

  # Wait for HelmRelease resources to be removed while controllers are still present.
  timeout_secs="${TIMEOUT_SECS:-300}"
  start="$(date +%s)"
  while true; do
    hr_count="$(kubectl get helmreleases.helm.toolkit.fluxcd.io -A --no-headers 2>/dev/null | wc -l | tr -d '[:space:]')"
    if [[ "$hr_count" == "0" ]]; then
      log_info "Flux-managed HelmRelease resources removed"
      break
    fi

    now="$(date +%s)"
    if (( now - start >= timeout_secs )); then
      die "Timed out waiting for HelmRelease resources to be deleted (${hr_count} remaining)"
    fi
    sleep 5
  done
  exit 0
fi

log_info "Reconciling Flux source and stack"
flux reconcile source git swhurl-platform -n flux-system --timeout=20m
flux reconcile kustomization homelab-flux-sources -n flux-system --with-source --timeout=20m
flux reconcile kustomization homelab-flux-stack -n flux-system --with-source --timeout=20m
log_info "Flux reconciliation complete"

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

if [[ "$DELETE" == "true" ]]; then
  log_info "Deleting Flux stack kustomizations (stack-only teardown)"
  kubectl -n flux-system delete kustomization homelab-flux-stack --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n flux-system delete kustomization homelab-flux-sources --ignore-not-found >/dev/null 2>&1 || true
  log_info "Requested deletion of homelab-flux-stack and homelab-flux-sources"
  log_info "Flux controllers and cluster services remain installed"
  exit 0
fi

need_cmd flux
if ! kubectl get crd kustomizations.kustomize.toolkit.fluxcd.io >/dev/null 2>&1; then
  die "Flux CRDs are not installed. Install Flux manually first (see README), then rerun."
fi
if ! kubectl -n flux-system get kustomization homelab-flux-stack >/dev/null 2>&1; then
  die "Flux bootstrap manifests are not applied. Run: make flux-bootstrap"
fi

log_info "Reconciling Flux source and stack"
flux reconcile source git swhurl-platform -n flux-system --timeout=20m
flux reconcile kustomization homelab-flux-sources -n flux-system --with-source --timeout=20m
flux reconcile kustomization homelab-flux-stack -n flux-system --with-source --timeout=20m
log_info "Flux reconciliation complete"

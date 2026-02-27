#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" != true ]]; then
  log_info "cert-manager lifecycle is Flux-managed; skipping in apply mode"
  exit 0
fi

log_info "Uninstalling cert-manager"
helm -n cert-manager uninstall cert-manager >/dev/null 2>&1 || true

# If cert-manager controllers are already gone, ACME resources can stay
# stuck on finalizers and block CRD deletion. Clear and delete instances first.
for r in \
  certificates.cert-manager.io \
  certificaterequests.cert-manager.io \
  orders.acme.cert-manager.io \
  challenges.acme.cert-manager.io \
  issuers.cert-manager.io \
  clusterissuers.cert-manager.io
 do
  mapfile -t objs < <(kubectl get "$r" -A -o name 2>/dev/null || true)
  for obj in "${objs[@]}"; do
    [[ -z "$obj" ]] && continue
    kubectl patch "$obj" --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
    kubectl delete "$obj" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  done
 done

if [[ "${CM_DELETE_CRDS:-true}" == "true" ]]; then
  crds="$(kubectl get crd -o name 2>/dev/null | rg 'cert-manager\.io|acme\.cert-manager\.io' || true)"
  if [[ -n "$crds" ]]; then
    log_info "Deleting cert-manager CRDs"
    # shellcheck disable=SC2086
    kubectl delete $crds --ignore-not-found --wait=false || true
  fi
fi

#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_CILIUM:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_CILIUM=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling cilium"
  destroy_release cilium >/dev/null 2>&1 || true
  log_info "Attempting force cleanup of labeled cilium resources"
  kubectl -n kube-system delete ds cilium cilium-envoy --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n kube-system delete deploy cilium-operator hubble-relay hubble-ui --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n kube-system delete svc cilium-envoy hubble-peer hubble-relay hubble-ui --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n kube-system delete deploy,ds,svc,cm,secret,sa,role,rolebinding \
    -l app.kubernetes.io/part-of=cilium --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete clusterrole,clusterrolebinding \
    -l app.kubernetes.io/part-of=cilium --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete ciliumidentity,ciliumendpoint,ciliumnode,ciliumnetworkpolicy,ciliumclusterwidenetworkpolicy,ciliumcidrgroup,ciliuml2announcementpolicy,ciliumloadbalancerippool,ciliumnodeconfig,ciliumpodippool \
    --all --ignore-not-found >/dev/null 2>&1 || true
  if [[ "${CILIUM_DELETE_CRDS:-true}" == "true" ]]; then
    crds="$(kubectl get crd -o name 2>/dev/null | rg '\.cilium\.io$' || true)"
    if [[ -n "$crds" ]]; then
      log_info "Deleting Cilium CRDs"
      # shellcheck disable=SC2086
      kubectl delete $crds --ignore-not-found || true
    fi
  fi
  kubectl -n kube-system wait --for=delete pod -l app.kubernetes.io/part-of=cilium --timeout=60s >/dev/null 2>&1 || true
  leftover_pods="$(kubectl -n kube-system get pod -l app.kubernetes.io/part-of=cilium -o name 2>/dev/null || true)"
  if [[ -n "$leftover_pods" ]]; then
    log_warn "Force deleting stuck Cilium pods"
    # shellcheck disable=SC2086
    kubectl -n kube-system delete $leftover_pods --force --grace-period=0 --ignore-not-found >/dev/null 2>&1 || true
    kubectl -n kube-system wait --for=delete pod -l app.kubernetes.io/part-of=cilium --timeout=30s >/dev/null 2>&1 || true
  fi
  exit 0
fi

if [[ "${CILIUM_SKIP_FLANNEL_CHECK:-false}" != "true" ]]; then
  flannel_backend=$(kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.annotations.flannel\.alpha\.coreos\.com/backend-type}{"\n"}{end}' | grep -v '^$' | head -n 1 || true)
  if [[ -n "$flannel_backend" ]]; then
    log_error "Detected flannel backend on nodes (${flannel_backend})."
    log_error "Cilium requires k3s with flannel disabled: --flannel-backend=none --disable-network-policy"
    log_error "Reinstall k3s, then rerun this step. Set CILIUM_SKIP_FLANNEL_CHECK=true to override."
    exit 1
  fi
fi

sync_release cilium

wait_ds kube-system cilium
wait_deploy kube-system cilium-operator

if kubectl -n kube-system get deploy hubble-relay >/dev/null 2>&1; then
  wait_deploy kube-system hubble-relay
fi
if kubectl -n kube-system get deploy hubble-ui >/dev/null 2>&1; then
  wait_deploy kube-system hubble-ui
fi
log_info "cilium installed"

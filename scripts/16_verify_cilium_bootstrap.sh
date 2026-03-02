#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

if [[ "${FEAT_CILIUM:-true}" != "true" ]]; then
  log_info "FEAT_CILIUM=false; skipping Cilium bootstrap check"
  exit 0
fi

if ! kubectl -n kube-system get ds cilium >/dev/null 2>&1; then
  die "Cilium daemonset is not present. Bootstrap Cilium first: make cilium-bootstrap"
fi

if ! kubectl -n kube-system get deploy cilium-operator >/dev/null 2>&1; then
  die "cilium-operator deployment is not present. Bootstrap Cilium first: make cilium-bootstrap"
fi

if ! kubectl get crd ciliumnetworkpolicies.cilium.io >/dev/null 2>&1; then
  die "Cilium CRDs are not present. Bootstrap Cilium first: make cilium-bootstrap"
fi

log_info "Cilium bootstrap verified"


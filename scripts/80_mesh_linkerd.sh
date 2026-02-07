#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_MESH_LINKERD:-false}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_MESH_LINKERD=false; skipping linkerd"
  exit 0
fi

if ! command -v linkerd >/dev/null 2>&1; then
  log_warn "linkerd CLI not installed; skipping"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling linkerd"
  linkerd uninstall | kubectl delete -f - || true
  exit 0
fi

log_info "Installing linkerd control plane"
linkerd check --pre || true
linkerd install | kubectl apply -f -
linkerd check || true

log_info "Linkerd installed"


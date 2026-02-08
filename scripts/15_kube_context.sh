#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

CTX="$(kubectl config current-context)"

if kubectl config get-contexts "$CTX" >/dev/null 2>&1; then
  log_info "Using kubectl context: $CTX"
  kubectl config use-context "$CTX" >/dev/null
else
  log_warn "Context $CTX not found; keeping current context"
fi

ensure_context
log_info "Kube context verified"

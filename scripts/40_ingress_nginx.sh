#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling ingress-nginx"
  destroy_release ingress-nginx >/dev/null 2>&1 || true
  exit 0
fi

sync_release ingress-nginx

wait_deploy ingress ingress-nginx-controller
log_info "ingress-nginx installed"

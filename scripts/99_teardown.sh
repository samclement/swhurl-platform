#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

log_info "Final teardown: k3s uninstall is not automatic."
log_info "Manual: sudo /usr/local/bin/k3s-uninstall.sh (server)"
if [[ "${K3S_UNINSTALL:-false}" == "true" ]]; then
  if command -v sudo >/dev/null 2>&1 && [[ -x "/usr/local/bin/k3s-uninstall.sh" ]]; then
    log_info "Running k3s-uninstall.sh via sudo (K3S_UNINSTALL=true)"
    sudo /usr/local/bin/k3s-uninstall.sh || true
  else
    log_warn "sudo or /usr/local/bin/k3s-uninstall.sh not available; skipping"
  fi
fi

log_info "Teardown complete"

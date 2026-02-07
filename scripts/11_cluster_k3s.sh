#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

# Lightweight handler for k3s provider. We do NOT install k3s automatically
# (requires root). This script validates access and prints clear next steps.

if [[ "${K8S_PROVIDER:-kind}" != "k3s" ]]; then
  log_info "K8S_PROVIDER is '${K8S_PROVIDER:-}', skipping k3s checks"
  exit 0
fi

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

if [[ "$DELETE" == true ]]; then
  log_info "k3s teardown not automated here. To uninstall: 'sudo /usr/local/bin/k3s-uninstall.sh' (server)"
  log_info "Set K3S_UNINSTALL=true to attempt running the uninstall script with sudo."
  if [[ "${K3S_UNINSTALL:-false}" == "true" ]]; then
    if command -v sudo >/dev/null 2>&1 && [[ -x "/usr/local/bin/k3s-uninstall.sh" ]]; then
      log_info "Running k3s-uninstall.sh via sudo"
      sudo /usr/local/bin/k3s-uninstall.sh || true
    else
      log_warn "sudo or /usr/local/bin/k3s-uninstall.sh not available; skipping"
    fi
  fi
  exit 0
fi

# Try to reach the cluster. If this fails, provide kubeconfig help.
if kubectl get --raw=/version >/dev/null 2>&1; then
  log_info "k3s cluster reachable via current kubectl context"
else
  log_warn "kubectl cannot reach a cluster. If k3s is installed on this host:"
  log_warn "  export KUBECONFIG=/etc/rancher/k3s/k3s.yaml && kubectl config use-context default"
  log_warn "Or copy the kubeconfig to ~/.kube/config with appropriate permissions."
  exit 1
fi

log_info "k3s provider ready"


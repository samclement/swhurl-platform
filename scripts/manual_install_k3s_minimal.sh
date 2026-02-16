#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

# Minimal bootstrap:
# 1) Install k3s with Traefik disabled and flannel/network-policy disabled
# 2) Configure kubeconfig and wait for node registration
#
# Cilium is installed separately by scripts/26_manage_cilium_lifecycle.sh.

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

if [[ "$DELETE" == true ]]; then
  log_info "k3s teardown is optional. To uninstall: 'sudo /usr/local/bin/k3s-uninstall.sh' (server)"
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

need_cmd curl
need_cmd kubectl
need_cmd sudo

K3S_VERSION="${K3S_VERSION:-}"
WAIT_SECS="${WAIT_SECS:-900}"
log_info "Bootstrap timeout set to ${WAIT_SECS}s"

if systemctl is-active --quiet k3s; then
  log_info "k3s already active; skipping install"
else
  log_info "Installing k3s (traefik disabled, flannel disabled)"
  if [[ -n "$K3S_VERSION" ]]; then
    curl -sfL https://get.k3s.io | sudo env \
      INSTALL_K3S_VERSION="$K3S_VERSION" \
      INSTALL_K3S_EXEC="--disable traefik --flannel-backend=none --disable-network-policy" \
      sh -
  else
    curl -sfL https://get.k3s.io | sudo env \
      INSTALL_K3S_EXEC="--disable traefik --flannel-backend=none --disable-network-policy" \
      sh -
  fi
fi

log_info "Configuring kubeconfig"
mkdir -p "$HOME/.kube"
sudo cp /etc/rancher/k3s/k3s.yaml "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config"
chmod 600 "$HOME/.kube/config"
export KUBECONFIG="$HOME/.kube/config"

log_info "Waiting for node registration"
for _ in $(seq 1 60); do
  if kubectl get nodes --no-headers 2>/dev/null | grep -q .; then
    break
  fi
  sleep 2
done
if ! kubectl get nodes --no-headers 2>/dev/null | grep -q .; then
  die "No nodes registered in cluster after waiting"
fi

# Do not wait for Ready here: with flannel disabled this depends on Cilium.
log_info "k3s installed without flannel/traefik. Next step: scripts/26_manage_cilium_lifecycle.sh"
log_info "Verify kubeconfig with: scripts/15_verify_cluster_access.sh"

#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/00_common.sh"

host_k3s_install_exec() {
  local mode="${K3S_INGRESS_MODE:-traefik}"
  case "$mode" in
    traefik)
      printf '%s' "--flannel-backend=none --disable-network-policy"
      ;;
    none)
      printf '%s' "--disable traefik --flannel-backend=none --disable-network-policy"
      ;;
    *)
      host_die "K3S_INGRESS_MODE must be traefik|none (got: $mode)"
      ;;
  esac
}

host_k3s_apply() {
  host_need_cmd curl
  host_need_cmd kubectl

  local exec_flags
  exec_flags="$(host_k3s_install_exec)"

  if systemctl is-active --quiet k3s; then
    host_log_info "k3s already active; skipping install"
  else
    host_log_info "Installing k3s (K3S_INGRESS_MODE=${K3S_INGRESS_MODE:-traefik})"
    if [[ -n "${K3S_VERSION:-}" ]]; then
      curl -sfL https://get.k3s.io | host_sudo env INSTALL_K3S_VERSION="$K3S_VERSION" INSTALL_K3S_EXEC="$exec_flags" sh -
    else
      curl -sfL https://get.k3s.io | host_sudo env INSTALL_K3S_EXEC="$exec_flags" sh -
    fi
  fi

  local kubeconfig_dest="${K3S_KUBECONFIG_DEST:-$HOME/.kube/config}"
  host_log_info "Configuring kubeconfig at ${kubeconfig_dest}"
  mkdir -p "$(dirname "$kubeconfig_dest")"
  host_sudo cp /etc/rancher/k3s/k3s.yaml "$kubeconfig_dest"
  host_sudo chown "$(id -u):$(id -g)" "$kubeconfig_dest"
  chmod 600 "$kubeconfig_dest"
  export KUBECONFIG="$kubeconfig_dest"

  host_log_info "Waiting for node registration"
  local i
  for i in $(seq 1 60); do
    if kubectl get nodes --no-headers 2>/dev/null | grep -q .; then
      break
    fi
    sleep 2
  done
  kubectl get nodes --no-headers 2>/dev/null | grep -q . || host_die "No nodes registered in cluster after waiting"

  host_log_info "k3s installed. Next: run cluster layer (Cilium + platform sync)"
}

host_k3s_delete() {
  host_log_info "k3s teardown is optional"
  if [[ "${K3S_UNINSTALL:-false}" != "true" ]]; then
    host_log_info "Set K3S_UNINSTALL=true to run /usr/local/bin/k3s-uninstall.sh"
    return 0
  fi

  if [[ -x "/usr/local/bin/k3s-uninstall.sh" ]]; then
    host_log_info "Running k3s uninstall"
    host_sudo /usr/local/bin/k3s-uninstall.sh || true
  else
    host_log_warn "k3s uninstall script not found; skipping"
  fi
}

#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "[ERROR] Missing required command: $1" >&2; exit 1; }
}

need_cmd kubectl

ensure_flux_cli() {
  if command -v flux >/dev/null 2>&1; then
    return 0
  fi
  if [[ "${AUTO_INSTALL_FLUX:-true}" != "true" ]]; then
    echo "[ERROR] Missing required command: flux (set AUTO_INSTALL_FLUX=true to auto-install)" >&2
    exit 1
  fi

  need_cmd curl
  need_cmd bash
  local bindir="${FLUX_INSTALL_DIR:-$HOME/.local/bin}"
  mkdir -p "$bindir"
  export PATH="$bindir:$PATH"
  echo "[INFO] Installing Flux CLI to $bindir"
  curl -fsSL https://fluxcd.io/install.sh | env BINDIR="$bindir" bash
  need_cmd flux
}

cilium_ready() {
  local desired ready
  desired="$(kubectl -n kube-system get ds cilium -o jsonpath='{.status.desiredNumberScheduled}' 2>/dev/null || true)"
  ready="$(kubectl -n kube-system get ds cilium -o jsonpath='{.status.numberReady}' 2>/dev/null || true)"
  [[ -n "$desired" && -n "$ready" && "$desired" != "0" && "$desired" == "$ready" ]]
}

ensure_cni() {
  if cilium_ready; then
    echo "[INFO] Cilium networking already ready"
    return 0
  fi
  if [[ "${FLUX_BOOTSTRAP_AUTO_CNI:-true}" != "true" ]]; then
    echo "[ERROR] No ready CNI detected and FLUX_BOOTSTRAP_AUTO_CNI=false." >&2
    echo "[ERROR] Install Cilium first (for example scripts/26_manage_cilium_lifecycle.sh), then retry." >&2
    exit 1
  fi
  echo "[INFO] No ready CNI detected; bootstrapping Cilium before Flux install"
  "$ROOT_DIR/scripts/25_prepare_helm_repositories.sh"
  "$ROOT_DIR/scripts/20_reconcile_platform_namespaces.sh"
  "$ROOT_DIR/scripts/26_manage_cilium_lifecycle.sh"
  if ! cilium_ready; then
    echo "[ERROR] Cilium did not become ready; aborting Flux bootstrap" >&2
    exit 1
  fi
}

if ! kubectl get --raw=/version >/dev/null 2>&1; then
  echo "[ERROR] kubectl cannot reach a cluster" >&2
  exit 1
fi

if [[ "$DELETE" == true ]]; then
  if ! command -v flux >/dev/null 2>&1; then
    echo "[WARN] flux command not found; skipping 'flux uninstall'"
    exit 0
  fi
  echo "[INFO] Uninstalling Flux controllers"
  flux uninstall --silent || true
  exit 0
fi

ensure_flux_cli
ensure_cni

echo "[INFO] Checking Flux prerequisites"
flux check --pre

echo "[INFO] Installing Flux controllers"
flux install --namespace flux-system

echo "[INFO] Applying bootstrap manifests from cluster/flux"
kubectl apply -k "$ROOT_DIR/cluster/flux"

echo "[INFO] Flux bootstrap complete"

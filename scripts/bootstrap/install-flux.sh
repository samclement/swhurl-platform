#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || { echo "[ERROR] Missing required command: $1" >&2; exit 1; }
}

need_cmd kubectl
need_cmd flux

if ! kubectl get --raw=/version >/dev/null 2>&1; then
  echo "[ERROR] kubectl cannot reach a cluster" >&2
  exit 1
fi

if [[ "$DELETE" == true ]]; then
  echo "[INFO] Uninstalling Flux controllers"
  flux uninstall --silent || true
  exit 0
fi

echo "[INFO] Checking Flux prerequisites"
flux check --pre

echo "[INFO] Installing Flux controllers"
flux install --namespace flux-system

echo "[INFO] Applying bootstrap manifests from cluster/flux"
kubectl apply -k "$ROOT_DIR/cluster/flux"

echo "[INFO] Flux bootstrap complete"

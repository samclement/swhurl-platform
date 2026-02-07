#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

OUT_DIR="${1:-.}"
mkdir -p "$OUT_DIR"

log_info "Cluster info"
kubectl cluster-info || true
kubectl get ns

log_info "Events (last 1h)"
kubectl get events --all-namespaces --sort-by=.lastTimestamp --field-selector=type!=Normal -A | tail -n 200 || true

log_info "Installed releases"
helm list -A || true


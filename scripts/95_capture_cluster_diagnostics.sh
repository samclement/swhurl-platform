#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

timestamp="$(date +%Y%m%d-%H%M%S)"
OUT_DIR="${1:-./artifacts/cluster-diagnostics-${timestamp}}"
mkdir -p "$OUT_DIR"

log_info "Cluster info"
kubectl cluster-info >"$OUT_DIR/cluster-info.txt" 2>&1 || true
kubectl get ns >"$OUT_DIR/namespaces.txt" 2>&1 || true

log_info "Events (last 1h)"
kubectl get events --all-namespaces --sort-by=.lastTimestamp --field-selector=type!=Normal -A | tail -n 200 >"$OUT_DIR/non-normal-events.txt" 2>&1 || true

log_info "Installed releases"
helm list -A >"$OUT_DIR/helm-releases.txt" 2>&1 || true

log_info "Diagnostics written to $OUT_DIR"

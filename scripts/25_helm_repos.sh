#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

need_cmd helm

helm repo add jetstack https://charts.jetstack.io >/dev/null 2>&1 || true
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx >/dev/null 2>&1 || true
helm repo add oauth2-proxy https://oauth2-proxy.github.io/manifests >/dev/null 2>&1 || true
helm repo add cilium https://helm.cilium.io/ >/dev/null 2>&1 || true
helm repo add clickstack https://clickhouse.github.io/ClickStack-helm-charts >/dev/null 2>&1 || true
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts >/dev/null 2>&1 || true
helm repo add minio https://charts.min.io/ >/dev/null 2>&1 || true
helm repo update

log_info "Helm repositories added/updated"

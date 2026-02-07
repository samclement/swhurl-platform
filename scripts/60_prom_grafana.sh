#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_OBS:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_OBS=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling kube-prometheus-stack"
  helm uninstall monitoring -n observability || true
  exit 0
fi

helm_upsert monitoring prometheus-community/kube-prometheus-stack observability \
  --set grafana.ingress.enabled=true \
  --set grafana.ingress.ingressClassName=nginx \
  --set grafana.ingress.annotations."cert-manager\.io/cluster-issuer"=${CLUSTER_ISSUER} \
  --set grafana.ingress.hosts[0]="${GRAFANA_HOST}" \
  --set grafana.ingress.tls[0].hosts[0]="${GRAFANA_HOST}" \
  --set grafana.ingress.tls[0].secretName=grafana-tls

wait_deploy observability monitoring-grafana
log_info "kube-prometheus-stack installed"


#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_OTEL_K8S:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_OTEL_K8S=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling Kubernetes OTel collectors"
  destroy_release otel-k8s-cluster >/dev/null 2>&1 || true
  destroy_release otel-k8s-daemonset >/dev/null 2>&1 || true
  kubectl -n logging delete secret hyperdx-secret --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n logging delete configmap otel-config-vars --ignore-not-found >/dev/null 2>&1 || true
  exit 0
fi

if ! helm status clickstack -n observability >/dev/null 2>&1; then
  die "ClickStack release not found in 'observability'. Install it first with ./scripts/50_clickstack.sh"
fi

kubectl_ns logging

OTLP_ENDPOINT="${CLICKSTACK_OTEL_ENDPOINT:-http://clickstack-otel-collector.observability.svc.cluster.local:4318}"
INGESTION_KEY="${CLICKSTACK_INGESTION_KEY:-}"
if [[ -z "$INGESTION_KEY" ]]; then
  die "CLICKSTACK_INGESTION_KEY is required. Get the current ingestion key from HyperDX UI (API Keys), set it in profiles/secrets.env, then rerun ./scripts/51_otel_k8s.sh"
fi

kubectl -n logging create secret generic hyperdx-secret \
  --from-literal=HYPERDX_API_KEY="$INGESTION_KEY" \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl -n logging create configmap otel-config-vars \
  --from-literal=HYPERDX_OTLP_ENDPOINT="$OTLP_ENDPOINT" \
  --dry-run=client -o yaml | kubectl apply -f -

label_managed logging secret hyperdx-secret
label_managed logging configmap otel-config-vars

sync_release otel-k8s-daemonset
sync_release otel-k8s-cluster

ds_name="$(kubectl -n logging get ds -l app.kubernetes.io/instance=otel-k8s-daemonset -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
deploy_name="$(kubectl -n logging get deploy -l app.kubernetes.io/instance=otel-k8s-cluster -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
if [[ -n "$ds_name" ]]; then
  kubectl -n logging rollout restart "ds/${ds_name}" >/dev/null 2>&1 || true
  wait_ds logging "$ds_name"
fi
if [[ -n "$deploy_name" ]]; then
  kubectl -n logging rollout restart "deploy/${deploy_name}" >/dev/null 2>&1 || true
  wait_deploy logging "$deploy_name"
fi

log_info "Kubernetes OTel collectors installed (endpoint: ${OTLP_ENDPOINT}, key source: CLICKSTACK_INGESTION_KEY)"
log_info "If exporters show 401, the ClickStack ingestion key changed. Update CLICKSTACK_INGESTION_KEY from HyperDX UI and rerun this script."

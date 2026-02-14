#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context
need_cmd helm

if [[ "${FEAT_OTEL_K8S:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_OTEL_K8S=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling Kubernetes OTel collectors"
  helm uninstall otel-k8s-daemonset -n logging || true
  helm uninstall otel-k8s-cluster -n logging || true
  kubectl -n logging delete secret hyperdx-secret --ignore-not-found
  kubectl -n logging delete configmap otel-config-vars --ignore-not-found
  exit 0
fi

if ! helm status clickstack -n observability >/dev/null 2>&1; then
  die "ClickStack release not found in 'observability'. Install it first with ./scripts/50_clickstack.sh"
fi

kubectl_ns logging

OTLP_ENDPOINT="${CLICKSTACK_OTEL_ENDPOINT:-http://clickstack-otel-collector.observability.svc.cluster.local:4318}"
INGESTION_KEY_SOURCE="CLICKSTACK_INGESTION_KEY"
INGESTION_KEY="${CLICKSTACK_INGESTION_KEY:-}"
if [[ -z "$INGESTION_KEY" ]]; then
  # TODO(swhurl-platform): Query the HyperDX ingestion key via ClickStack API instead of scraping effective.yaml.
  # Prefer the collector's active bearer token because this is what it currently validates.
  INGESTION_KEY="$(
    kubectl -n observability exec deploy/clickstack-otel-collector -- sh -lc \
      "sed -n '40,60p' /etc/otel/supervisor-data/effective.yaml | sed -n 's/^[[:space:]]*-[[:space:]]*//p' | head -n1" \
      2>/dev/null || true
  )"
  INGESTION_KEY_SOURCE="clickstack-otel-collector effective config"
fi
if [[ -z "$INGESTION_KEY" ]]; then
  INGESTION_KEY="$(kubectl -n observability get secret clickstack-app-secrets -o jsonpath='{.data.api-key}' 2>/dev/null | base64 -d || true)"
  INGESTION_KEY_SOURCE="observability/clickstack-app-secrets"
fi
[[ -n "$INGESTION_KEY" ]] || die "No ingestion key found. Set CLICKSTACK_INGESTION_KEY (preferred) or ensure clickstack-otel-collector is running."

kubectl -n logging create secret generic hyperdx-secret \
  --from-literal=HYPERDX_API_KEY="$INGESTION_KEY" \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl -n logging create configmap otel-config-vars \
  --from-literal=HYPERDX_OTLP_ENDPOINT="$OTLP_ENDPOINT" \
  --dry-run=client -o yaml | kubectl apply -f -

helm_upsert otel-k8s-daemonset open-telemetry/opentelemetry-collector logging \
  --reset-values \
  -f "$SCRIPT_DIR/../infra/values/otel-k8s-daemonset.yaml"

helm_upsert otel-k8s-cluster open-telemetry/opentelemetry-collector logging \
  --reset-values \
  -f "$SCRIPT_DIR/../infra/values/otel-k8s-deployment.yaml"

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

log_info "Kubernetes OTel collectors installed (endpoint: ${OTLP_ENDPOINT}, key source: ${INGESTION_KEY_SOURCE})"

#!/usr/bin/env bash
set +x
set -Eeuo pipefail

command -v kubectl >/dev/null 2>&1 || { echo "[ERROR] Missing required command: kubectl" >&2; exit 1; }
command -v flux >/dev/null 2>&1 || { echo "[ERROR] Missing required command: flux" >&2; exit 1; }
command -v base64 >/dev/null 2>&1 || { echo "[ERROR] Missing required command: base64" >&2; exit 1; }
kubectl get --raw=/version >/dev/null 2>&1 || { echo "[ERROR] kubectl cannot reach a cluster; ensure kubeconfig is set" >&2; exit 1; }

fail=0
say() { printf "\n== %s ==\n" "$1"; }
ok() { printf "[OK] %s\n" "$1"; }
bad() { printf "[BAD] %s\n" "$1"; fail=1; }

say "Flux Kustomizations"
if ! flux_kustomizations="$(flux get kustomizations -n flux-system --no-header)"; then
  bad "could not read Flux kustomizations"
elif [[ -z "$flux_kustomizations" ]]; then
  bad "no Flux kustomizations found in flux-system"
else
  while read -r name _revision _suspended ready _message; do
    if [[ "$ready" == "True" ]]; then
      ok "$name"
    else
      bad "$name is not Ready"
    fi
  done <<< "$flux_kustomizations"
fi

say "Runtime Secrets"
hyperdx_key="$(kubectl -n logging get secret hyperdx-secret -o jsonpath='{.data.HYPERDX_API_KEY}' 2>/dev/null || true)"
if [[ -n "$hyperdx_key" ]]; then
  ok "logging/hyperdx-secret.HYPERDX_API_KEY present"
else
  bad "logging/hyperdx-secret.HYPERDX_API_KEY is empty (run: make runtime-inputs-refresh-otel)"
fi

say "Ingestion Key Sync"
if ! mongo_key="$(kubectl -n observability exec deploy/clickstack-mongodb -- \
  mongosh hyperdx --quiet --eval 'const keys = db.teams.distinct("apiKey").filter(k => typeof k === "string" && k.length > 0); if (keys.length !== 1) quit(2); print(keys[0]);' 2>/dev/null)" || [[ -z "$mongo_key" ]]; then
  bad "cannot read a unique ClickStack team ingestion key; check MongoDB availability and team configuration"
elif ! hyperdx_plain="$(printf '%s' "$hyperdx_key" | base64 --decode 2>/dev/null)" || [[ -z "$hyperdx_plain" ]]; then
  bad "logging/hyperdx-secret.HYPERDX_API_KEY is empty or invalid base64"
elif [[ "$mongo_key" == "$hyperdx_plain" && "$hyperdx_key" == "$(printf '%s' "$mongo_key" | base64 --wrap=0)" ]]; then
  ok "ingestion Secret bytes match ClickStack; verify collector logs and fresh telemetry after a restart"
else
  bad "HYPERDX_API_KEY does not match the ClickStack ingestion key"
  printf "       Fix: update HYPERDX_API_KEY in platform-services/otel/base/secret-hyperdx.sops.yaml\n"
  printf "            using exactly one base64 layer in data; commit+push, then run: make runtime-inputs-refresh-otel\n"
fi
unset mongo_key hyperdx_plain hyperdx_key

say "Ingress"
if kubectl -n kube-system get deploy traefik -o jsonpath='{.spec.template.spec.containers[0].args}' 2>/dev/null \
    | grep -q 'entryPoints.web.http.redirections.entryPoint.scheme=https'; then
  ok "Traefik redirects HTTP to HTTPS"
else
  bad "Traefik does not redirect HTTP to HTTPS (plain-HTTP sign-in fails with 403); check helmchartconfig-traefik.yaml"
fi

say "Retention"
clickhouse() { kubectl -n observability exec deploy/clickstack-clickhouse -- clickhouse-client -q "$1" 2>/dev/null; }
if ! telemetry="$(clickhouse "SELECT countIf(position(engine_full, 'toIntervalDay(30)') > 0), count() FROM system.tables WHERE database = 'default' AND engine LIKE '%MergeTree' FORMAT TSV")"; then
  bad "could not read ClickHouse telemetry table TTLs"
elif read -r with_ttl total <<< "$telemetry" && (( total > 0 && with_ttl == total )); then
  ok "all $total telemetry tables expire after 30 days"
else
  bad "telemetry tables without a 30-day TTL ($telemetry with/total); the collector image default may have changed"
fi
system_logs="query_log','metric_log','asynchronous_metric_log','crash_log','processors_profile_log','part_log','trace_log','query_thread_log','query_views_log','opentelemetry_span_log"
if ! untimed="$(clickhouse "SELECT count() FROM system.tables WHERE database = 'system' AND name IN ('$system_logs') AND position(engine_full, 'TTL ') = 0 FORMAT TSV")"; then
  bad "could not read ClickHouse system log TTLs"
elif [[ "$untimed" == "0" ]]; then
  ok "ClickHouse system logs expire after 7 days"
else
  bad "$untimed ClickHouse system log table(s) have no TTL; restart clickstack-clickhouse after config changes"
fi
mongo_pv="$(kubectl -n observability get pvc clickstack-mongodb -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)"
if [[ -n "$mongo_pv" && "$(kubectl get pv "$mongo_pv" -o jsonpath='{.spec.persistentVolumeReclaimPolicy}' 2>/dev/null)" == "Retain" ]]; then
  ok "ClickStack MongoDB PV reclaim policy is Retain"
else
  bad "ClickStack MongoDB PV is not Retain; see docs/runbook.md#recovery"
fi
if [[ "$(kubectl -n observability get pvc clickstack-mongodb -o jsonpath='{.metadata.annotations.helm\.sh/resource-policy}' 2>/dev/null)" == "keep" ]]; then
  ok "ClickStack MongoDB PVC survives Helm uninstall"
else
  bad "ClickStack MongoDB PVC lacks helm.sh/resource-policy=keep"
fi

(( fail )) && exit 1
printf "\nValidation passed.\n"

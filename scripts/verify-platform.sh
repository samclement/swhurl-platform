#!/usr/bin/env bash
set -Eeuo pipefail

command -v kubectl >/dev/null 2>&1 || { echo "[ERROR] Missing required command: kubectl" >&2; exit 1; }
command -v flux >/dev/null 2>&1 || { echo "[ERROR] Missing required command: flux" >&2; exit 1; }
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
mongo_key="$(kubectl -n observability exec deploy/clickstack-mongodb -- \
  mongosh hyperdx --quiet --eval "db.teams.findOne({},{apiKey:1,_id:0}).apiKey" 2>/dev/null || true)"
hyperdx_plain="$(kubectl -n logging get secret hyperdx-secret \
  -o jsonpath='{.data.HYPERDX_API_KEY}' 2>/dev/null | base64 -d | base64 -d 2>/dev/null || true)"
if [[ -z "$mongo_key" ]]; then
  bad "could not read MongoDB hyperdx.teams.apiKey (is clickstack-mongodb running?)"
elif [[ -z "$hyperdx_plain" ]]; then
  bad "logging/hyperdx-secret.HYPERDX_API_KEY not found — cannot verify sync"
elif [[ "$mongo_key" == "$hyperdx_plain" ]]; then
  ok "HYPERDX_API_KEY matches MongoDB teams.apiKey — OTel collectors can authenticate to ClickStack"
else
  bad "HYPERDX_API_KEY does not match MongoDB teams.apiKey — OTel collectors cannot authenticate to ClickStack"
  printf "       MongoDB key: %s\n" "$mongo_key"
  printf "       HYPERDX_API_KEY: %s\n" "$hyperdx_plain"
  printf "       Fix: update HYPERDX_API_KEY in platform-services/otel/base/secret-hyperdx.sops.yaml\n"
  printf "            to the MongoDB value, commit+push, then run: make runtime-inputs-refresh-otel\n"
fi

(( fail )) && exit 1
printf "\nValidation passed.\n"

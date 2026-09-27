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

(( fail )) && exit 1
printf "\nValidation passed.\n"

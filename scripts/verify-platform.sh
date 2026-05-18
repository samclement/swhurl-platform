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

say "Token Alignment"
src="$(kubectl -n flux-system get secret platform-runtime-inputs -o jsonpath='{.data.CLICKSTACK_INGESTION_KEY}' 2>/dev/null || true)"
dst="$(kubectl -n logging get secret hyperdx-secret -o jsonpath='{.data.HYPERDX_API_KEY}' 2>/dev/null || true)"
if [[ -n "$src" && "$src" == "$dst" ]]; then
  ok "otel ingestion key aligned"
elif [[ -z "$src" ]]; then
  bad "platform-runtime-inputs.CLICKSTACK_INGESTION_KEY is empty"
else
  bad "otel token mismatch (run: make runtime-inputs-refresh-otel)"
fi

(( fail )) && exit 1
printf "\nValidation passed.\n"

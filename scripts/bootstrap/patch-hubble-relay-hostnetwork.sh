#!/usr/bin/env bash
set -Eeuo pipefail

ns="kube-system"
name="hubble-relay"

if ! kubectl -n "$ns" get deploy "$name" >/dev/null 2>&1; then
  echo "[INFO] ${ns}/${name} not found; skipping hostNetwork patch"
  exit 0
fi

current_host_network="$(kubectl -n "$ns" get deploy "$name" -o jsonpath='{.spec.template.spec.hostNetwork}' 2>/dev/null || true)"
current_dns_policy="$(kubectl -n "$ns" get deploy "$name" -o jsonpath='{.spec.template.spec.dnsPolicy}' 2>/dev/null || true)"

if [[ "$current_host_network" == "true" && "$current_dns_policy" == "ClusterFirstWithHostNet" ]]; then
  echo "[INFO] ${ns}/${name} already configured with hostNetwork=true"
  exit 0
fi

echo "[INFO] Patching ${ns}/${name} with hostNetwork=true"
kubectl -n "$ns" patch deploy "$name" --type=merge -p \
  '{"spec":{"template":{"spec":{"hostNetwork":true,"dnsPolicy":"ClusterFirstWithHostNet"}}}}'
kubectl -n "$ns" rollout status deploy/"$name" --timeout=10m

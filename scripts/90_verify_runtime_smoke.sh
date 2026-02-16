#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

fail=0
bad() { log_error "$1"; fail=1; }
ok() { log_info "$1"; }

log_info "Smoke tests: node readiness"
kubectl get nodes -o wide
total_nodes="$(kubectl get nodes --no-headers 2>/dev/null | wc -l | tr -d '[:space:]')"
ready_nodes="$(kubectl get nodes --no-headers 2>/dev/null | awk '$2 ~ /Ready/ {c++} END {print c+0}')"
if [[ "$total_nodes" == "0" ]]; then
  bad "No nodes found in cluster"
elif [[ "$ready_nodes" != "$total_nodes" ]]; then
  bad "Not all nodes are Ready (${ready_nodes}/${total_nodes})"
else
  ok "All nodes Ready (${ready_nodes}/${total_nodes})"
fi

log_info "Smoke tests: ingress NodePort wiring"
if kubectl -n ingress get svc ingress-nginx-controller >/dev/null 2>&1; then
  https_np="$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.ports[?(@.name=="https")].nodePort}')"
  if [[ "$https_np" == "30313" ]]; then
    ok "ingress-nginx HTTPS NodePort is 30313"
  else
    bad "ingress-nginx HTTPS NodePort mismatch (expected 30313, got ${https_np:-<empty>})"
  fi
else
  bad "ingress-nginx service not found"
fi

# End-to-end reachability test through ingress-nginx NodePort.
if command -v curl >/dev/null 2>&1; then
  host="hello.${BASE_DOMAIN}"
  log_info "Smoke tests: HTTPS NodePort 30313 -> Host: ${host}"
  set +e
  code="$(curl -sk -o /dev/null -w "%{http_code}" -H "Host: ${host}" https://127.0.0.1:30313/)"
  set -e
  if [[ "$code" =~ ^[234][0-9][0-9]$ ]]; then
    ok "Ingress HTTPS smoke check returned HTTP ${code}"
  else
    bad "Ingress HTTPS smoke check returned HTTP ${code:-<empty>}"
  fi
else
  log_warn "curl not found; skipping ingress HTTPS smoke check"
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

log_info "Smoke tests passed"

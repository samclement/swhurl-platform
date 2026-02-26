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

if [[ "${INGRESS_PROVIDER:-nginx}" == "nginx" ]]; then
  log_info "Smoke tests: ingress NodePort wiring"
  if kubectl -n ingress get svc ingress-nginx-controller >/dev/null 2>&1; then
    svc_type="$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.type}')"
    https_np="$(kubectl -n ingress get svc ingress-nginx-controller -o jsonpath='{.spec.ports[?(@.name=="https")].nodePort}')"
    if [[ "$svc_type" != "$VERIFY_INGRESS_SERVICE_TYPE" ]]; then
      bad "ingress-nginx service.type mismatch (expected ${VERIFY_INGRESS_SERVICE_TYPE}, got ${svc_type:-<empty>})"
    elif [[ "$https_np" == "$VERIFY_INGRESS_NODEPORT_HTTPS" ]]; then
      ok "ingress-nginx HTTPS NodePort is ${VERIFY_INGRESS_NODEPORT_HTTPS}"
    else
      bad "ingress-nginx HTTPS NodePort mismatch (expected ${VERIFY_INGRESS_NODEPORT_HTTPS}, got ${https_np:-<empty>})"
    fi
  else
    bad "ingress-nginx service not found"
  fi

  # End-to-end reachability test through ingress-nginx NodePort.
  if command -v curl >/dev/null 2>&1; then
    host="${APP_HOST:-${VERIFY_SAMPLE_INGRESS_HOST_PREFIX}.${BASE_DOMAIN}}"
    log_info "Smoke tests: HTTPS NodePort ${VERIFY_INGRESS_NODEPORT_HTTPS} -> Host: ${host}"
    set +e
    code="$(curl -sk -o /dev/null -w "%{http_code}" -H "Host: ${host}" "https://127.0.0.1:${VERIFY_INGRESS_NODEPORT_HTTPS}/")"
    set -e
    if [[ "$code" =~ ^[234][0-9][0-9]$ ]]; then
      ok "Ingress HTTPS smoke check returned HTTP ${code}"
    else
      bad "Ingress HTTPS smoke check returned HTTP ${code:-<empty>}"
    fi
  else
    log_warn "curl not found; skipping ingress HTTPS smoke check"
  fi
else
  log_info "INGRESS_PROVIDER=${INGRESS_PROVIDER:-nginx}; skipping ingress-nginx NodePort smoke checks"
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

log_info "Smoke tests passed"

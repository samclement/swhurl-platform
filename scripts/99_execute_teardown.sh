#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do
  case "$arg" in
    --delete) DELETE=true ;;
    *) die "Unknown argument: $arg" ;;
  esac
done

if [[ "$DELETE" != true ]]; then
  die "scripts/99_execute_teardown.sh is delete-only"
fi

ensure_context

managed_namespaces=("${PLATFORM_MANAGED_NAMESPACES[@]}")
NAMESPACE_DELETE_TIMEOUT_SECS="${NAMESPACE_DELETE_TIMEOUT_SECS:-180}"
DELETE_SCOPE="${DELETE_SCOPE:-managed}" # managed | dedicated-cluster

case "$DELETE_SCOPE" in
  managed|dedicated-cluster) ;;
  *) die "DELETE_SCOPE must be one of: managed, dedicated-cluster (got: ${DELETE_SCOPE})" ;;
esac

if [[ "$DELETE_SCOPE" == "dedicated-cluster" ]]; then
  log_warn "DELETE_SCOPE=dedicated-cluster: sweeping secrets cluster-wide (unsafe on shared clusters)"
  secret_rows="$(kubectl get secret -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"/"}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
  while IFS= read -r row; do
    [[ -z "$row" ]] && continue
    ns="${row%%/*}"
    name="${row#*/}"
    if is_allowed_k3s_secret_for_teardown "$ns" "$name"; then
      continue
    fi
    kubectl -n "$ns" delete secret "$name" --ignore-not-found >/dev/null 2>&1 || true
  done <<< "$secret_rows"
else
  log_info "Sweeping platform-managed secrets/configmaps in managed namespaces only (DELETE_SCOPE=managed)"
  for ns in "${managed_namespaces[@]}"; do
    kubectl get ns "$ns" >/dev/null 2>&1 || continue
    kubectl -n "$ns" delete secret -l platform.swhurl.io/managed=true --ignore-not-found >/dev/null 2>&1 || true
    kubectl -n "$ns" delete configmap -l platform.swhurl.io/managed=true --ignore-not-found >/dev/null 2>&1 || true
  done

  # Runtime-input source/target cleanup.
  kubectl -n flux-system delete secret platform-runtime-inputs --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n ingress delete secret oauth2-proxy-secret --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n logging delete secret hyperdx-secret --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n logging delete configmap otel-config-vars --ignore-not-found >/dev/null 2>&1 || true
  kubectl -n storage delete secret minio-creds --ignore-not-found >/dev/null 2>&1 || true
fi

log_info "Deleting managed namespaces: ${managed_namespaces[*]}"
for ns in "${managed_namespaces[@]}"; do
  kubectl delete ns "$ns" --ignore-not-found >/dev/null 2>&1 || true
done

log_info "Waiting for managed namespaces to terminate (${NAMESPACE_DELETE_TIMEOUT_SECS}s)"
for ns in "${managed_namespaces[@]}"; do
  kubectl wait --for=delete ns/"$ns" --timeout="${NAMESPACE_DELETE_TIMEOUT_SECS}s" >/dev/null 2>&1 || true
done

leftover_pvcs=()
for ns in "${managed_namespaces[@]}"; do
  rows="$(kubectl -n "$ns" get pvc -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)"
  while IFS= read -r pvc; do
    [[ -z "$pvc" ]] && continue
    leftover_pvcs+=("${ns}/${pvc}")
  done <<< "$rows"
done

ns_left=()
for ns in "${managed_namespaces[@]}"; do
  if kubectl get ns "$ns" >/dev/null 2>&1; then
    ns_left+=("$ns")
  fi
done

if [[ "${#leftover_pvcs[@]}" -gt 0 ]]; then
  log_error "PVCs still present after teardown wait: ${leftover_pvcs[*]}"
fi
if [[ "${#ns_left[@]}" -gt 0 ]]; then
  log_error "Namespaces still present after wait: ${ns_left[*]}"
fi

if [[ "${#leftover_pvcs[@]}" -gt 0 || "${#ns_left[@]}" -gt 0 ]]; then
  die "Refusing to continue delete while non-k3s workloads are still terminating. Resolve stuck PVC/namespace teardown before deleting Cilium."
fi

log_info "Deleting platform CRDs (cert-manager/acme/cilium)"
crds="$(kubectl get crd -o name 2>/dev/null | rg "$PLATFORM_CRD_NAME_REGEX" || true)"
if [[ -n "$crds" ]]; then
  # shellcheck disable=SC2086
  kubectl delete $crds --ignore-not-found >/dev/null 2>&1 || true
fi

log_info "Final teardown: k3s uninstall is not automatic."
log_info "Manual: sudo /usr/local/bin/k3s-uninstall.sh (server)"
if [[ "${K3S_UNINSTALL:-false}" == "true" ]]; then
  if command -v sudo >/dev/null 2>&1 && [[ -x "/usr/local/bin/k3s-uninstall.sh" ]]; then
    log_info "Running k3s-uninstall.sh via sudo (K3S_UNINSTALL=true)"
    sudo /usr/local/bin/k3s-uninstall.sh || true
  else
    log_warn "sudo or /usr/local/bin/k3s-uninstall.sh not available; skipping"
  fi
fi

log_info "Teardown complete"

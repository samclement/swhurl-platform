#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

if [[ "$DELETE" != true ]]; then
  log_info "Delete-clean verification only applies in --delete mode; skipping"
  exit 0
fi

ensure_context

fail=0
bad() { printf "[BAD] %s\n" "$1"; fail=1; }
ok() { printf "[OK] %s\n" "$1"; }
DELETE_SCOPE="${DELETE_SCOPE:-managed}" # managed | dedicated-cluster

case "$DELETE_SCOPE" in
  managed|dedicated-cluster) ;;
  *) bad "DELETE_SCOPE must be one of: managed, dedicated-cluster (got: ${DELETE_SCOPE})" ;;
esac

# 1) No Helm releases should remain.
if [[ "$(helm list -A -q | wc -l | tr -d '[:space:]')" == "0" ]]; then
  ok "No Helm releases remain"
else
  bad "Helm releases still present"
  helm list -A || true
fi

# 2) Managed namespaces should be gone.
managed_namespaces=(apps cert-manager cilium-secrets ingress logging observability platform-system storage)
ns_left=()
for ns in "${managed_namespaces[@]}"; do
  if kubectl get ns "$ns" >/dev/null 2>&1; then
    ns_left+=("$ns")
  fi
done
if [[ "${#ns_left[@]}" -eq 0 ]]; then
  ok "Managed namespaces removed"
else
  bad "Managed namespaces still present: ${ns_left[*]}"
fi

# 3) Cilium/cert-manager CRDs should be gone.
crd_patterns='cert-manager\.io|acme\.cert-manager\.io|\.cilium\.io$'
if kubectl get crd -o name | rg -q "$crd_patterns"; then
  bad "Platform CRDs still present"
  kubectl get crd -o name | rg "$crd_patterns" || true
else
  ok "Platform CRDs removed"
fi

# 4) Non-k3s-native secrets should be gone.
secret_rows=$(kubectl get secret -A -o jsonpath='{range .items[*]}{.metadata.namespace}{"/"}{.metadata.name}{"\n"}{end}' 2>/dev/null || true)
non_native=()
while IFS= read -r row; do
  [[ -z "$row" ]] && continue
  ns="${row%%/*}"
  name="${row#*/}"

  if [[ "$DELETE_SCOPE" == "managed" ]]; then
    # Only enforce cleanup expectations for platform-managed secrets in managed namespaces.
    case "$ns" in
      apps|cert-manager|cilium-secrets|ingress|logging|observability|platform-system|storage) ;;
      *) continue ;;
    esac
    if ! kubectl -n "$ns" get secret "$name" -o jsonpath='{.metadata.labels.platform\.swhurl\.io/managed}' 2>/dev/null | rg -q '^true$'; then
      continue
    fi
  else
    # dedicated-cluster: enforce cluster-wide cleanup (unsafe on shared clusters).
    if [[ "$ns" == "kube-system" ]]; then
      if [[ "$name" == "k3s-serving" || "$name" == *.node-password.k3s || "$name" == bootstrap-token-* ]]; then
        continue
      fi
    fi
  fi

  # everything else is treated as leftover
  non_native+=("$row")
done <<< "$secret_rows"

if [[ "${#non_native[@]}" -eq 0 ]]; then
  ok "No platform-managed secrets remain (scope: ${DELETE_SCOPE})"
else
  bad "Non-k3s-native secrets still present: ${non_native[*]}"
fi

# 5) No Cilium/Hubble resources should remain in kube-system.
kubectl -n kube-system wait --for=delete pod -l app.kubernetes.io/part-of=cilium --timeout=60s >/dev/null 2>&1 || true
cilium_left="$(kubectl -n kube-system get all -l app.kubernetes.io/part-of=cilium -o name 2>/dev/null || true)"
if [[ -z "$cilium_left" ]]; then
  ok "No Cilium resources remain in kube-system"
else
  bad "Cilium resources still present in kube-system"
  printf "%s\n" "$cilium_left"
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

ok "Delete-clean verification passed"

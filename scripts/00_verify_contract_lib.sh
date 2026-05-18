#!/usr/bin/env bash

# Helper library (not a runnable phase step).
# Sourced by scripts/00_lib.sh to provide shared verification/teardown contracts.

if [[ "${VERIFY_CONTRACT_LOADED:-}" == "1" ]]; then
  return 0 2>/dev/null || exit 0
fi
readonly VERIFY_CONTRACT_LOADED="1"

# Shared verification and teardown expectations.

# Runtime non-secret vars required by active composition.
readonly -a VERIFY_REQUIRED_PLATFORM_VARS=(
  OAUTH_HOST
  CLICKSTACK_HOST
)

readonly -a VERIFY_PLATFORM_EFFECTIVE_NON_SECRET_VARS=(
  OAUTH_HOST
  CLICKSTACK_HOST
)

# Ingress runtime verification contract (k3s packaged traefik).
readonly VERIFY_INGRESS_NODEPORT_HTTP="31514"
readonly VERIFY_INGRESS_NODEPORT_HTTPS="30313"
readonly VERIFY_SAMPLE_INGRESS_HOST_PREFIX="staging-hello"

# Config input contract.
readonly -a VERIFY_REQUIRED_BASE_VARS=(BASE_DOMAIN)
readonly VERIFY_REQUIRED_TIMEOUT_VAR="TIMEOUT_SECS"
readonly -a VERIFY_BASE_EFFECTIVE_NON_SECRET_VARS=(
  BASE_DOMAIN
)

verify_expected_letsencrypt_server() {
  case "${1:-staging}" in
    prod) printf 'https://acme-v02.api.letsencrypt.org/directory' ;;
    *) printf 'https://acme-staging-v02.api.letsencrypt.org/directory' ;;
  esac
}

verify_required_runtime_vars() {
  printf '%s\n' "${VERIFY_REQUIRED_PLATFORM_VARS[@]}" MINIO_HOST MINIO_CONSOLE_HOST
}

verify_effective_runtime_non_secret_vars() {
  printf '%s\n' "${VERIFY_BASE_EFFECTIVE_NON_SECRET_VARS[@]}" "${VERIFY_PLATFORM_EFFECTIVE_NON_SECRET_VARS[@]}" MINIO_HOST MINIO_CONSOLE_HOST
}

# ── Reusable kubectl verification helpers ──

verify_say() { printf "\n== %s ==\n" "$1"; }
verify_ok() { printf "[OK] %s\n" "$1"; }
verify_warn() { printf "[WARN] %s\n" "$1"; }
verify_mismatch() { printf "[MISMATCH] %s\n" "$1"; VERIFY_FAIL=1; }

verify_add_suggest() {
  local s="$1"
  for e in "${VERIFY_SUGGEST[@]:-}"; do
    [[ "$e" == "$s" ]] && return 0
  done
  VERIFY_SUGGEST+=("$s")
}

check_eq() {
  local label="$1" expected="$2" actual="$3" suggest="$4"
  if [[ "$expected" == "$actual" ]]; then
    verify_ok "$label: $actual"
  else
    verify_mismatch "$label: expected=$expected actual=$actual"
    [[ -n "$suggest" ]] && verify_add_suggest "$suggest"
  fi
}

ingress_class() {
  local namespace="$1" name="$2" class=""
  class="$(kubectl -n "$namespace" get ingress "$name" -o jsonpath='{.spec.ingressClassName}' 2>/dev/null || true)"
  if [[ -z "$class" ]]; then
    class="$(kubectl -n "$namespace" get ingress "$name" -o jsonpath='{.metadata.annotations.kubernetes\.io/ingress\.class}' 2>/dev/null || true)"
  fi
  printf '%s' "$class"
}

check_flux_kustomization_path() {
  local name="$1" expected_path="$2"
  local path=""
  if kubectl -n flux-system get kustomization "$name" >/dev/null 2>&1; then
    path="$(kubectl -n flux-system get kustomization "$name" -o jsonpath='{.spec.path}')"
    if [[ "$path" != "$expected_path" ]]; then
      verify_warn "${name} path '$path' is unexpected (expected ${expected_path})"
    fi
  else
    verify_warn "${name} kustomization not found"
  fi
  printf '%s' "$path"
}

check_cluster_resource_present() {
  local kind="$1" name="$2" present_msg="$3" missing_msg="$4" suggest="${5:-}"
  if kubectl get "$kind" "$name" >/dev/null 2>&1; then
    verify_ok "$present_msg"
  else
    verify_mismatch "$missing_msg"
    [[ -n "$suggest" ]] && verify_add_suggest "$suggest"
  fi
}

check_namespaced_resource_present() {
  local kind="$1" namespace="$2" name="$3" present_msg="$4" missing_msg="$5" suggest="${6:-}"
  if kubectl -n "$namespace" get "$kind" "$name" >/dev/null 2>&1; then
    verify_ok "$present_msg"
  else
    verify_mismatch "$missing_msg"
    [[ -n "$suggest" ]] && verify_add_suggest "$suggest"
  fi
}

check_namespaced_selector_present() {
  local kind="$1" namespace="$2" selector="$3" present_msg="$4" missing_msg="$5" suggest="${6:-}"
  local names
  names="$(kubectl -n "$namespace" get "$kind" -l "$selector" -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || true)"
  if [[ -n "$names" ]]; then
    verify_ok "$present_msg"
  else
    verify_mismatch "$missing_msg"
    [[ -n "$suggest" ]] && verify_add_suggest "$suggest"
  fi
}

check_service_nodeport() {
  local namespace="$1" service="$2" port_name="$3" expected_nodeport="$4" suggest="${5:-}"
  local actual_nodeport

  if ! kubectl -n "$namespace" get svc "$service" >/dev/null 2>&1; then
    verify_mismatch "${namespace}/${service} service not found"
    [[ -n "$suggest" ]] && verify_add_suggest "$suggest"
    return
  fi

  actual_nodeport="$(kubectl -n "$namespace" get svc "$service" -o jsonpath="{.spec.ports[?(@.name==\"${port_name}\")].nodePort}" 2>/dev/null || true)"
  if [[ -z "$actual_nodeport" ]]; then
    verify_mismatch "${namespace}/${service} port '${port_name}' nodePort is empty"
    [[ -n "$suggest" ]] && verify_add_suggest "$suggest"
    return
  fi

  check_eq "${service}.nodePort.${port_name}" "$expected_nodeport" "$actual_nodeport" "$suggest"
}

check_ingress_contract() {
  local namespace="$1" name="$2" label_prefix="$3" expected_host="$4" expected_issuer="$5" expected_class="$6"
  local suggest_host="$7" suggest_issuer="$8" suggest_class="$9"

  if kubectl -n "$namespace" get ingress "$name" >/dev/null 2>&1; then
    local actual_host actual_issuer actual_class
    actual_host="$(kubectl -n "$namespace" get ingress "$name" -o jsonpath='{.spec.rules[0].host}')"
    actual_issuer="$(kubectl -n "$namespace" get ingress "$name" -o jsonpath='{.metadata.annotations.cert-manager\.io/cluster-issuer}')"
    actual_class="$(ingress_class "$namespace" "$name")"
    [[ -n "$expected_host" ]] && check_eq "${label_prefix}.host" "$expected_host" "$actual_host" "$suggest_host"
    [[ -n "$expected_issuer" ]] && check_eq "${label_prefix}.issuer" "$expected_issuer" "$actual_issuer" "$suggest_issuer"
    [[ -n "$expected_class" ]] && check_eq "${label_prefix}.class" "$expected_class" "$actual_class" "$suggest_class"
  else
    verify_mismatch "${name} ingress not found in namespace ${namespace}"
    [[ -n "$suggest_host" ]] && verify_add_suggest "$suggest_host"
  fi
}

check_certificate_contract() {
  local namespace="$1" name="$2" label_prefix="$3" expected_host="$4" expected_issuer="$5" suggest="$6"

  if kubectl -n "$namespace" get certificate "$name" >/dev/null 2>&1; then
    local actual_cert_host actual_cert_issuer
    actual_cert_host="$(kubectl -n "$namespace" get certificate "$name" -o jsonpath='{.spec.dnsNames[0]}')"
    actual_cert_issuer="$(kubectl -n "$namespace" get certificate "$name" -o jsonpath='{.spec.issuerRef.name}')"
    check_eq "${label_prefix}.host" "$expected_host" "$actual_cert_host" "$suggest"
    check_eq "${label_prefix}.issuer" "$expected_issuer" "$actual_cert_issuer" "$suggest"
  else
    verify_mismatch "${name} certificate not found in namespace ${namespace}"
    [[ -n "$suggest" ]] && verify_add_suggest "$suggest"
  fi
}

read_secret_data() {
  local namespace="$1" name="$2" key="$3"
  kubectl -n "$namespace" get secret "$name" -o jsonpath="{.data.${key}}" 2>/dev/null \
    | base64 --decode 2>/dev/null || true
}

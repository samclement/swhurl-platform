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

# Ingress runtime verification contract.
readonly VERIFY_INGRESS_SERVICE_TYPE="NodePort"
readonly VERIFY_INGRESS_NODEPORT_HTTP="31514"
readonly VERIFY_INGRESS_NODEPORT_HTTPS="30313"
readonly VERIFY_SAMPLE_INGRESS_HOST_PREFIX="staging-hello"

# Teardown/delete-clean contract.
readonly -a PLATFORM_MANAGED_NAMESPACES=(apps-staging apps-prod cert-manager ingress logging observability platform-system storage)
readonly PLATFORM_CRD_NAME_REGEX='cert-manager\.io|acme\.cert-manager\.io'

# k3s-native secrets allowed during teardown verification.
readonly -a VERIFY_K3S_ALLOWED_SECRETS=(
  "k3s-serving"
  "*.node-password.k3s"
  "bootstrap-token-*"
)

# Config input contract.
readonly -a VERIFY_REQUIRED_BASE_VARS=(BASE_DOMAIN)
readonly VERIFY_REQUIRED_TIMEOUT_VAR="TIMEOUT_SECS"
readonly -a VERIFY_ALLOWED_INGRESS_PROVIDERS=(nginx traefik)
readonly -a VERIFY_ALLOWED_OBJECT_STORAGE_PROVIDERS=(minio ceph)
readonly -a VERIFY_BASE_EFFECTIVE_NON_SECRET_VARS=(
  BASE_DOMAIN
  INGRESS_PROVIDER
  OBJECT_STORAGE_PROVIDER
)

name_matches_any_pattern() {
  local value="$1"; shift
  local pattern
  for pattern in "$@"; do
    [[ "$value" == $pattern ]] && return 0
  done
  return 1
}

is_platform_managed_namespace() {
  local ns="$1"
  local item
  for item in "${PLATFORM_MANAGED_NAMESPACES[@]}"; do
    [[ "$item" == "$ns" ]] && return 0
  done
  return 1
}

is_allowed_k3s_secret_for_teardown() {
  local ns="$1" name="$2"
  [[ "$ns" == "kube-system" ]] || return 1
  name_matches_any_pattern "$name" "${VERIFY_K3S_ALLOWED_SECRETS[@]}"
}

is_allowed_k3s_secret_for_verify() {
  local ns="$1" name="$2"
  [[ "$ns" == "kube-system" ]] || return 1
  name_matches_any_pattern "$name" "${VERIFY_K3S_ALLOWED_SECRETS[@]}"
}

is_allowed_ingress_provider() {
  local value="$1"
  name_matches_any_pattern "$value" "${VERIFY_ALLOWED_INGRESS_PROVIDERS[@]}"
}

is_allowed_object_storage_provider() {
  local value="$1"
  name_matches_any_pattern "$value" "${VERIFY_ALLOWED_OBJECT_STORAGE_PROVIDERS[@]}"
}

verify_expected_letsencrypt_server() {
  local server_type="${1:-staging}"
  local staging_server="https://acme-staging-v02.api.letsencrypt.org/directory"
  local prod_server="https://acme-v02.api.letsencrypt.org/directory"

  case "$server_type" in
    staging) printf '%s' "$staging_server" ;;
    prod) printf '%s' "$prod_server" ;;
    *)
      printf '%s' "$staging_server"
      ;;
  esac
}

verify_required_runtime_vars() {
  local -A seen=()
  local var

  for var in "${VERIFY_REQUIRED_PLATFORM_VARS[@]}"; do
    [[ -n "${seen[$var]+x}" ]] && continue
    seen["$var"]=1
    printf '%s\n' "$var"
  done

  if [[ "${OBJECT_STORAGE_PROVIDER:-minio}" == "minio" ]]; then
    for var in MINIO_HOST MINIO_CONSOLE_HOST; do
      [[ -n "${seen[$var]+x}" ]] && continue
      seen["$var"]=1
      printf '%s\n' "$var"
    done
  fi
}

verify_effective_runtime_non_secret_vars() {
  local -A seen=()
  local var

  for var in "${VERIFY_BASE_EFFECTIVE_NON_SECRET_VARS[@]}"; do
    [[ -n "${seen[$var]+x}" ]] && continue
    seen["$var"]=1
    printf '%s\n' "$var"
  done

  for var in "${VERIFY_PLATFORM_EFFECTIVE_NON_SECRET_VARS[@]}"; do
    [[ -n "${seen[$var]+x}" ]] && continue
    seen["$var"]=1
    printf '%s\n' "$var"
  done

  if [[ "${OBJECT_STORAGE_PROVIDER:-minio}" == "minio" ]]; then
    for var in MINIO_HOST MINIO_CONSOLE_HOST; do
      [[ -n "${seen[$var]+x}" ]] && continue
      seen["$var"]=1
      printf '%s\n' "$var"
    done
  fi
}

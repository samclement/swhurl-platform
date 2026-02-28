#!/usr/bin/env bash

# Helper library (not a runnable phase step).
# Sourced by scripts/00_lib.sh to provide shared verification/teardown contracts.
# Depends on scripts/00_feature_registry_lib.sh.

if [[ -z "${FEATURE_REGISTRY_LOADED:-}" ]]; then
  _VERIFY_SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  # shellcheck disable=SC1091
  source "$_VERIFY_SCRIPT_DIR/00_feature_registry_lib.sh"
  unset _VERIFY_SCRIPT_DIR
fi

if [[ "${VERIFY_CONTRACT_LOADED:-}" == "1" ]]; then
  return 0 2>/dev/null || exit 0
fi
readonly VERIFY_CONTRACT_LOADED="1"

# Shared verification and teardown expectations.
# Feature-specific metadata is sourced from scripts/00_feature_registry_lib.sh.

# Ingress runtime verification contract.
readonly VERIFY_INGRESS_SERVICE_TYPE="NodePort"
readonly VERIFY_INGRESS_NODEPORT_HTTP="31514"
readonly VERIFY_INGRESS_NODEPORT_HTTPS="30313"
readonly VERIFY_SAMPLE_INGRESS_HOST_PREFIX="staging-hello"

# Teardown/delete-clean contract.
readonly -a PLATFORM_MANAGED_NAMESPACES=(apps-staging apps-prod cert-manager ingress logging observability platform-system storage)
readonly PLATFORM_CRD_NAME_REGEX='cert-manager\.io|acme\.cert-manager\.io|\.cilium\.io$'

# During teardown (before Cilium delete), keep Cilium helm release metadata.
readonly -a VERIFY_K3S_ALLOWED_SECRETS_PRE_CILIUM=(
  "k3s-serving"
  "*.node-password.k3s"
  "bootstrap-token-*"
  "sh.helm.release.v1.cilium.*"
)

# After full delete, Cilium release metadata should also be gone.
readonly -a VERIFY_K3S_ALLOWED_SECRETS_POST_CILIUM=(
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
  name_matches_any_pattern "$name" "${VERIFY_K3S_ALLOWED_SECRETS_PRE_CILIUM[@]}"
}

is_allowed_k3s_secret_for_verify() {
  local ns="$1" name="$2"
  [[ "$ns" == "kube-system" ]] || return 1
  name_matches_any_pattern "$name" "${VERIFY_K3S_ALLOWED_SECRETS_POST_CILIUM[@]}"
}

is_allowed_ingress_provider() {
  local value="$1"
  name_matches_any_pattern "$value" "${VERIFY_ALLOWED_INGRESS_PROVIDERS[@]}"
}

is_allowed_object_storage_provider() {
  local value="$1"
  name_matches_any_pattern "$value" "${VERIFY_ALLOWED_OBJECT_STORAGE_PROVIDERS[@]}"
}

verify_oauth_auth_url() {
  local oauth_host="$1"
  printf 'https://%s/oauth2/auth' "$oauth_host"
}

verify_oauth_auth_signin() {
  local oauth_host="$1"
  printf 'https://%s/oauth2/start?rd=$scheme://$host$request_uri' "$oauth_host"
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

verify_required_vars_for_enabled_features() {
  local -A seen=()
  local key var
  for key in "${FEATURE_KEYS[@]}"; do
    feature_is_enabled "$key" || continue
    if [[ "$key" == "minio" && "${OBJECT_STORAGE_PROVIDER:-minio}" != "minio" ]]; then
      continue
    fi
    while IFS= read -r var; do
      [[ -n "$var" ]] || continue
      [[ -n "${seen[$var]+x}" ]] && continue
      seen["$var"]=1
      printf '%s\n' "$var"
    done < <(feature_required_vars "$key")
  done
}

verify_effective_non_secret_vars() {
  local -A seen=()
  local key var

  for var in "${VERIFY_BASE_EFFECTIVE_NON_SECRET_VARS[@]}"; do
    [[ -n "${seen[$var]+x}" ]] && continue
    seen["$var"]=1
    printf '%s\n' "$var"
  done

  for key in "${FEATURE_KEYS[@]}"; do
    feature_is_enabled "$key" || continue
    if [[ "$key" == "minio" && "${OBJECT_STORAGE_PROVIDER:-minio}" != "minio" ]]; then
      continue
    fi
    while IFS= read -r var; do
      [[ -n "$var" ]] || continue
      [[ -n "${seen[$var]+x}" ]] && continue
      seen["$var"]=1
      printf '%s\n' "$var"
    done < <(feature_effective_non_secret_vars "$key")
  done
}

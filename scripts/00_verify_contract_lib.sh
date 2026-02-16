#!/usr/bin/env bash

# Helper library (not a runnable phase step).
# Sourced by scripts/00_lib.sh to provide shared verification/teardown contracts.

if [[ "${VERIFY_CONTRACT_LOADED:-}" == "1" ]]; then
  return 0 2>/dev/null || exit 0
fi
readonly VERIFY_CONTRACT_LOADED="1"

# Single source of truth for verification and teardown expectations.

# Ingress runtime verification contract.
readonly VERIFY_INGRESS_SERVICE_TYPE="NodePort"
readonly VERIFY_INGRESS_NODEPORT_HTTP="31514"
readonly VERIFY_INGRESS_NODEPORT_HTTPS="30313"
readonly VERIFY_SAMPLE_INGRESS_HOST_PREFIX="hello"

# Helmfile drift ignore contract.
readonly -a VERIFY_HELMFILE_IGNORED_RESOURCE_HEADERS=(
  "kube-system, cilium-ca, Secret (v1) has changed:"
  "kube-system, hubble-relay-client-certs, Secret (v1) has changed:"
  "kube-system, hubble-server-certs, Secret (v1) has changed:"
)

# Teardown/delete-clean contract.
readonly -a PLATFORM_MANAGED_NAMESPACES=(apps cert-manager ingress logging observability platform-system storage)
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
readonly -a VERIFY_REQUIRED_BASE_VARS=(BASE_DOMAIN CLUSTER_ISSUER)
readonly VERIFY_REQUIRED_TIMEOUT_VAR="TIMEOUT_SECS"
readonly -a VERIFY_ALLOWED_LETSENCRYPT_ENVS=(staging prod production)
readonly -a VERIFY_REQUIRED_OAUTH_VARS=(OAUTH_HOST OIDC_ISSUER OIDC_CLIENT_ID OIDC_CLIENT_SECRET)
readonly -a VERIFY_REQUIRED_CILIUM_VARS=(HUBBLE_HOST)
readonly -a VERIFY_REQUIRED_CLICKSTACK_VARS=(CLICKSTACK_HOST CLICKSTACK_API_KEY)
readonly -a VERIFY_REQUIRED_OTEL_VARS=(CLICKSTACK_OTEL_ENDPOINT CLICKSTACK_INGESTION_KEY)
readonly -a VERIFY_REQUIRED_MINIO_VARS=(MINIO_HOST MINIO_CONSOLE_HOST MINIO_ROOT_PASSWORD)
readonly -a VERIFY_EFFECTIVE_NON_SECRET_VARS=(
  BASE_DOMAIN
  CLUSTER_ISSUER
  LETSENCRYPT_ENV
  OAUTH_HOST
  HUBBLE_HOST
  CLICKSTACK_HOST
  CLICKSTACK_OTEL_ENDPOINT
  MINIO_HOST
  MINIO_CONSOLE_HOST
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

is_allowed_letsencrypt_env() {
  local value="$1"
  name_matches_any_pattern "$value" "${VERIFY_ALLOWED_LETSENCRYPT_ENVS[@]}"
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
  local le_env="${1:-staging}"
  case "$le_env" in
    prod|production) printf '%s' "https://acme-v02.api.letsencrypt.org/directory" ;;
    *) printf '%s' "https://acme-staging-v02.api.letsencrypt.org/directory" ;;
  esac
}

verify_expected_releases() {
  local -a expected=(
    "kube-system/platform-namespaces"
    "cert-manager/cert-manager"
    "kube-system/platform-issuers"
    "ingress/ingress-nginx"
  )
  [[ "${FEAT_CILIUM:-true}" == "true" ]] && expected+=("kube-system/cilium")
  [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]] && expected+=("ingress/oauth2-proxy")
  [[ "${FEAT_CLICKSTACK:-true}" == "true" ]] && expected+=("observability/clickstack")
  [[ "${FEAT_OTEL_K8S:-true}" == "true" ]] && expected+=("logging/otel-k8s-daemonset" "logging/otel-k8s-cluster")
  [[ "${FEAT_MINIO:-true}" == "true" ]] && expected+=("storage/minio")
  printf '%s\n' "${expected[@]}"
}

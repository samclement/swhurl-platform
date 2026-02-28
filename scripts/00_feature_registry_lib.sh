#!/usr/bin/env bash

# Helper library (not a runnable phase step).
# Canonical feature registry for flags, required vars, and expected releases.

if [[ "${FEATURE_REGISTRY_LOADED:-}" == "1" ]]; then
  return 0 2>/dev/null || exit 0
fi
readonly FEATURE_REGISTRY_LOADED="1"

readonly -a FEATURE_KEYS=(
  cilium
  oauth2_proxy
  clickstack
  otel_k8s
  minio
)

readonly -A FEATURE_FLAGS=(
  [cilium]="FEAT_CILIUM"
  [oauth2_proxy]="FEAT_OAUTH2_PROXY"
  [clickstack]="FEAT_CLICKSTACK"
  [otel_k8s]="FEAT_OTEL_K8S"
  [minio]="FEAT_MINIO"
)

readonly -A FEATURE_REQUIRED_VARS=(
  [cilium]="HUBBLE_HOST"
  [oauth2_proxy]="OAUTH_HOST OIDC_CLIENT_ID OIDC_CLIENT_SECRET OAUTH_COOKIE_SECRET"
  [clickstack]="CLICKSTACK_HOST CLICKSTACK_API_KEY"
  [otel_k8s]="CLICKSTACK_API_KEY"
  [minio]="MINIO_HOST MINIO_CONSOLE_HOST MINIO_ROOT_PASSWORD"
)

readonly -A FEATURE_EFFECTIVE_NON_SECRET_VARS=(
  [cilium]="HUBBLE_HOST"
  [oauth2_proxy]="OAUTH_HOST"
  [clickstack]="CLICKSTACK_HOST"
  [otel_k8s]="CLICKSTACK_OTEL_ENDPOINT"
  [minio]="MINIO_HOST MINIO_CONSOLE_HOST"
)

readonly -A FEATURE_EXPECTED_RELEASES=(
  [cilium]="kube-system/cilium"
  [oauth2_proxy]="ingress/oauth2-proxy"
  [clickstack]="observability/clickstack"
  [otel_k8s]="logging/otel-k8s-daemonset logging/otel-k8s-cluster"
  [minio]="storage/minio"
)

feature_registry_flags() {
  local key
  for key in "${FEATURE_KEYS[@]}"; do
    printf '%s\n' "${FEATURE_FLAGS[$key]}"
  done
}

feature_flag_var() {
  local key="$1"
  printf '%s' "${FEATURE_FLAGS[$key]:-}"
}

feature_is_enabled() {
  local key="$1"
  local flag
  flag="$(feature_flag_var "$key")"
  [[ -n "$flag" ]] || return 1
  [[ "${!flag:-true}" == "true" ]]
}

feature_required_vars() {
  local key="$1"
  local vars="${FEATURE_REQUIRED_VARS[$key]:-}"
  local v
  for v in $vars; do
    printf '%s\n' "$v"
  done
}

feature_effective_non_secret_vars() {
  local key="$1"
  local vars="${FEATURE_EFFECTIVE_NON_SECRET_VARS[$key]:-}"
  local v
  for v in $vars; do
    printf '%s\n' "$v"
  done
}

feature_expected_releases() {
  local key="$1"
  local releases="${FEATURE_EXPECTED_RELEASES[$key]:-}"
  local r
  for r in $releases; do
    printf '%s\n' "$r"
  done
}

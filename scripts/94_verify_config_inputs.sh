#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Config contract check is apply-only; skipping in delete mode"
  exit 0
fi

ok(){ printf "[OK] %s\n" "$1"; }
bad(){ printf "[BAD] %s\n" "$1"; fail=1; }
warn(){ printf "[WARN] %s\n" "$1"; }
need(){ local k="$1"; local v="${!k:-}"; [[ -n "$v" ]] && ok "$k is set" || bad "$k is set"; }

fail=0
printf "== Config Contract ==\n"
for key in "${VERIFY_REQUIRED_BASE_VARS[@]}"; do
  need "$key"
done
[[ "${!VERIFY_REQUIRED_TIMEOUT_VAR:-}" =~ ^[0-9]+$ ]] && ok "${VERIFY_REQUIRED_TIMEOUT_VAR} is numeric" || bad "${VERIFY_REQUIRED_TIMEOUT_VAR} is numeric"

if is_allowed_letsencrypt_env "${LETSENCRYPT_ENV:-staging}"; then
  ok "LETSENCRYPT_ENV is valid"
else
  bad "LETSENCRYPT_ENV must be staging|prod|production"
fi

platform_issuer="${PLATFORM_CLUSTER_ISSUER:-${CLUSTER_ISSUER:-letsencrypt-staging}}"
if is_allowed_cluster_issuer "$platform_issuer"; then
  ok "PLATFORM_CLUSTER_ISSUER is valid (${platform_issuer})"
else
  bad "PLATFORM_CLUSTER_ISSUER must be one of: selfsigned, letsencrypt, letsencrypt-staging, letsencrypt-prod"
fi

app_issuer="${APP_CLUSTER_ISSUER:-$platform_issuer}"
if is_allowed_cluster_issuer "$app_issuer"; then
  ok "APP_CLUSTER_ISSUER is valid (${app_issuer})"
else
  bad "APP_CLUSTER_ISSUER must be one of: selfsigned, letsencrypt, letsencrypt-staging, letsencrypt-prod"
fi

app_namespace="${APP_NAMESPACE:-apps-staging}"
if [[ "$app_namespace" == "apps-staging" || "$app_namespace" == "apps-prod" ]]; then
  ok "APP_NAMESPACE is valid (${app_namespace})"
else
  bad "APP_NAMESPACE must be apps-staging or apps-prod (got: ${app_namespace})"
fi

if [[ -n "${INGRESS_PROVIDER:-}" ]]; then
  if is_allowed_ingress_provider "${INGRESS_PROVIDER}"; then
    ok "INGRESS_PROVIDER is valid"
  else
    bad "INGRESS_PROVIDER must be nginx|traefik"
  fi
fi

if [[ -n "${OBJECT_STORAGE_PROVIDER:-}" ]]; then
  if is_allowed_object_storage_provider "${OBJECT_STORAGE_PROVIDER}"; then
    ok "OBJECT_STORAGE_PROVIDER is valid"
  else
    bad "OBJECT_STORAGE_PROVIDER must be minio|ceph"
  fi
fi

if [[ "${FEAT_OTEL_K8S:-true}" == "true" && -z "${CLICKSTACK_INGESTION_KEY:-}" ]]; then
  warn "CLICKSTACK_INGESTION_KEY is unset; OTel exporters will fall back to CLICKSTACK_API_KEY until you set it from ClickStack UI"
fi

printf "\n== Feature Contracts ==\n"
while IFS= read -r key; do
  [[ -n "$key" ]] || continue
  need "$key"
done < <(verify_required_vars_for_enabled_features)

printf "\n== Effective (non-secret) ==\n"
while IFS= read -r key; do
  [[ -n "$key" ]] || continue
  printf "%s=%s\n" "$key" "${!key:-}"
done < <(verify_effective_non_secret_vars)

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Config contract verification passed"

#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

DELETE=false
for arg in "$@"; do
  case "$arg" in
    --delete) DELETE=true ;;
    *) die "Unknown argument: $arg" ;;
  esac
done
if [[ "$DELETE" == true ]]; then
  die "scripts/94_verify_config_inputs.sh is apply-only"
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

read_flux_path() {
  local file="$1"
  awk '/^[[:space:]]*path:[[:space:]]*/ { print $2; exit }' "$REPO_ROOT/$file"
}

infra_flux_path="$(read_flux_path "clusters/home/infrastructure.yaml")"
case "$infra_flux_path" in
  ./infrastructure/overlays/home)
    ok "infrastructure Flux path is valid (${infra_flux_path})"
    ;;
  *)
    bad "infrastructure Flux path must be ./infrastructure/overlays/home (got: ${infra_flux_path:-<empty>})"
    ;;
esac

platform_flux_path="$(read_flux_path "clusters/home/platform.yaml")"
case "$platform_flux_path" in
  ./platform-services/overlays/home)
    ok "platform Flux path is valid (${platform_flux_path})"
    ;;
  *)
    bad "platform Flux path must be ./platform-services/overlays/home (got: ${platform_flux_path:-<empty>})"
    ;;
esac

read_platform_setting() {
  local key="$1"
  local file="$REPO_ROOT/clusters/home/flux-system/sources/configmap-platform-settings.yaml"
  [[ -f "$file" ]] || { printf ''; return 0; }
  awk -v k="$key" '$1 == k ":" { print $2; exit }' "$file" | tr -d '"'
}

platform_cert_issuer="$(read_platform_setting CERT_ISSUER)"
case "$platform_cert_issuer" in
  letsencrypt-staging|letsencrypt-prod)
    ok "platform-settings CERT_ISSUER is valid (${platform_cert_issuer})"
    ;;
  *)
    bad "platform-settings CERT_ISSUER must be letsencrypt-staging|letsencrypt-prod (got: ${platform_cert_issuer:-<empty>})"
    ;;
esac

tenants_flux_path="$(read_flux_path "clusters/home/tenants.yaml")"
case "$tenants_flux_path" in
  ./tenants/app-envs)
    ok "tenants Flux path is valid (${tenants_flux_path})"
    ;;
  *)
    bad "tenants Flux path must be ./tenants/app-envs (got: ${tenants_flux_path:-<empty>})"
    ;;
esac

app_example_flux_path="$(read_flux_path "clusters/home/app-example.yaml")"
case "$app_example_flux_path" in
  ./tenants/apps/example)
    ok "app-example Flux path is valid (${app_example_flux_path})"
    ;;
  *)
    bad "app-example Flux path must be ./tenants/apps/example (got: ${app_example_flux_path:-<empty>})"
    ;;
esac

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

#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

[[ "${1:-}" == "--delete" ]] && die "scripts/94_verify_config_inputs.sh is apply-only"

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
  awk '/^[[:space:]]*path:[[:space:]]*/ { print $2; exit }' "$REPO_ROOT/$1"
}

check_flux_path() {
  local name="$1" file="$2" expected="$3"
  local actual; actual="$(read_flux_path "$file")"
  [[ "$actual" == "$expected" ]] && ok "$name Flux path is valid ($actual)" || bad "$name Flux path must be $expected (got: ${actual:-<empty>})"
}

check_flux_path "infrastructure" "clusters/home/infrastructure.yaml" "./infrastructure/overlays/home"
check_flux_path "platform" "clusters/home/platform.yaml" "./platform-services/overlays/home"
check_flux_path "tenants" "clusters/home/tenants.yaml" "./tenants/app-envs"
check_flux_path "app-example" "clusters/home/app-example.yaml" "./tenants/apps/example"

read_platform_setting() {
  local file="$REPO_ROOT/clusters/home/flux-system/sources/configmap-platform-settings.yaml"
  [[ -f "$file" ]] || { printf ''; return 0; }
  awk -v k="$1" '$1 == k ":" { print $2; exit }' "$file" | tr -d '"'
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

runtime_inputs_sops_file="$REPO_ROOT/clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml"
if [[ -f "$runtime_inputs_sops_file" ]]; then
  ok "runtime-inputs SOPS secret exists (${runtime_inputs_sops_file#$REPO_ROOT/})"
  if rg -q '^sops:' "$runtime_inputs_sops_file"; then
    ok "runtime-inputs secret is SOPS-encrypted"
  else
    bad "runtime-inputs secret must be SOPS-encrypted (missing top-level sops metadata)"
  fi

  for secret_key in SHARED_OIDC_CLIENT_ID SHARED_OIDC_CLIENT_SECRET OAUTH_COOKIE_SECRET OAUTH_HOST CLICKSTACK_API_KEY CLICKSTACK_INGESTION_KEY; do
    if rg -q "^[[:space:]]+${secret_key}:" "$runtime_inputs_sops_file"; then
      ok "runtime-inputs key present: ${secret_key}"
    else
      bad "runtime-inputs key missing: ${secret_key}"
    fi
  done
else
  bad "runtime-inputs SOPS secret exists (${runtime_inputs_sops_file#$REPO_ROOT/})"
fi

printf "\n== Runtime Contracts ==\n"
while IFS= read -r key; do
  [[ -n "$key" ]] || continue
  need "$key"
done < <(verify_required_runtime_vars)

printf "\n== Effective (non-secret) ==\n"
while IFS= read -r key; do
  [[ -n "$key" ]] || continue
  printf "%s=%s\n" "$key" "${!key:-}"
done < <(verify_effective_runtime_non_secret_vars)

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Config contract verification passed"

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
need(){ local k="$1"; local v="${!k:-}"; [[ -n "$v" ]] && ok "$k is set" || bad "$k is set"; }

fail=0
printf "== Config Contract ==\n"
for key in "${VERIFY_REQUIRED_BASE_VARS[@]}"; do
  need "$key"
done
[[ "${!VERIFY_REQUIRED_TIMEOUT_VAR:-}" =~ ^[0-9]+$ ]] && ok "${VERIFY_REQUIRED_TIMEOUT_VAR} is numeric" || bad "${VERIFY_REQUIRED_TIMEOUT_VAR} is numeric"

if [[ "${CLUSTER_ISSUER:-}" == "letsencrypt" ]]; then
  need ACME_EMAIL
  if is_allowed_letsencrypt_env "${LETSENCRYPT_ENV:-staging}"; then
    ok "LETSENCRYPT_ENV is valid"
  else
    bad "LETSENCRYPT_ENV must be staging|prod|production"
  fi
fi

printf "\n== Feature Contracts ==\n"
if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  for key in "${VERIFY_REQUIRED_OAUTH_VARS[@]}"; do
    need "$key"
  done
fi
if [[ "${FEAT_CILIUM:-true}" == "true" ]]; then
  for key in "${VERIFY_REQUIRED_CILIUM_VARS[@]}"; do
    need "$key"
  done
fi
if [[ "${FEAT_CLICKSTACK:-true}" == "true" ]]; then
  for key in "${VERIFY_REQUIRED_CLICKSTACK_VARS[@]}"; do
    need "$key"
  done
fi
if [[ "${FEAT_OTEL_K8S:-true}" == "true" ]]; then
  for key in "${VERIFY_REQUIRED_OTEL_VARS[@]}"; do
    need "$key"
  done
fi
if [[ "${FEAT_MINIO:-true}" == "true" ]]; then
  for key in "${VERIFY_REQUIRED_MINIO_VARS[@]}"; do
    need "$key"
  done
fi

printf "\n== Effective (non-secret) ==\n"
for key in "${VERIFY_EFFECTIVE_NON_SECRET_VARS[@]}"; do
  printf "%s=%s\n" "$key" "${!key:-}"
done

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Config contract verification passed"

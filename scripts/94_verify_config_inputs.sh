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
need BASE_DOMAIN
need CLUSTER_ISSUER
[[ "${TIMEOUT_SECS:-}" =~ ^[0-9]+$ ]] && ok "TIMEOUT_SECS is numeric" || bad "TIMEOUT_SECS is numeric"

if [[ "${CLUSTER_ISSUER:-}" == "letsencrypt" ]]; then
  need ACME_EMAIL
  if [[ "${LETSENCRYPT_ENV:-staging}" =~ ^(staging|prod|production)$ ]]; then
    ok "LETSENCRYPT_ENV is valid"
  else
    bad "LETSENCRYPT_ENV must be staging|prod|production"
  fi
fi

printf "\n== Feature Contracts ==\n"
if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  need OAUTH_HOST
  need OIDC_ISSUER
  need OIDC_CLIENT_ID
  need OIDC_CLIENT_SECRET
fi
if [[ "${FEAT_CILIUM:-true}" == "true" ]]; then
  need HUBBLE_HOST
fi
if [[ "${FEAT_CLICKSTACK:-true}" == "true" ]]; then
  need CLICKSTACK_HOST
  need CLICKSTACK_API_KEY
fi
if [[ "${FEAT_OTEL_K8S:-true}" == "true" ]]; then
  need CLICKSTACK_OTEL_ENDPOINT
  need CLICKSTACK_INGESTION_KEY
fi
if [[ "${FEAT_MINIO:-true}" == "true" ]]; then
  need MINIO_HOST
  need MINIO_CONSOLE_HOST
  need MINIO_ROOT_PASSWORD
fi

printf "\n== Effective (non-secret) ==\n"
printf "BASE_DOMAIN=%s\n" "${BASE_DOMAIN:-}"
printf "CLUSTER_ISSUER=%s\n" "${CLUSTER_ISSUER:-}"
printf "LETSENCRYPT_ENV=%s\n" "${LETSENCRYPT_ENV:-}"
printf "OAUTH_HOST=%s\n" "${OAUTH_HOST:-}"
printf "HUBBLE_HOST=%s\n" "${HUBBLE_HOST:-}"
printf "CLICKSTACK_HOST=%s\n" "${CLICKSTACK_HOST:-}"
printf "CLICKSTACK_OTEL_ENDPOINT=%s\n" "${CLICKSTACK_OTEL_ENDPOINT:-}"
printf "MINIO_HOST=%s\n" "${MINIO_HOST:-}"
printf "MINIO_CONSOLE_HOST=%s\n" "${MINIO_CONSOLE_HOST:-}"

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Config contract verification passed"

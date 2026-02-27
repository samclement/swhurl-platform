#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/../00_lib.sh"

DELETE=false
for arg in "$@"; do
  case "$arg" in
    --delete) DELETE=true ;;
    *)
      log_error "Unknown argument: $arg"
      exit 1
      ;;
  esac
done

need_cmd kubectl
ensure_context

if [[ "$DELETE" == true ]]; then
  kubectl -n flux-system delete secret platform-runtime-inputs --ignore-not-found
  log_info "Deleted flux-system/platform-runtime-inputs"
  exit 0
fi

require_non_empty() {
  local name="$1"
  local val="$2"
  [[ -n "$val" ]] || die "Missing required variable: $name (set it in profiles/secrets.env or your selected profile)"
}

oidc_client_id="${OIDC_CLIENT_ID:-}"
oidc_client_secret="${OIDC_CLIENT_SECRET:-}"
oauth_cookie_secret="${OAUTH_COOKIE_SECRET:-}"
acme_email="${ACME_EMAIL:-}"
clickstack_api_key="${CLICKSTACK_API_KEY:-}"
clickstack_ingestion_key="${CLICKSTACK_INGESTION_KEY:-}"
platform_cluster_issuer="${PLATFORM_CLUSTER_ISSUER:-${CLUSTER_ISSUER:-letsencrypt-staging}}"
app_cluster_issuer="${APP_CLUSTER_ISSUER:-$platform_cluster_issuer}"
app_namespace="${APP_NAMESPACE:-apps-staging}"
app_host="${APP_HOST:-staging.hello.${BASE_DOMAIN:-}}"
letsencrypt_env="${LETSENCRYPT_ENV:-staging}"
default_letsencrypt_staging_server="https://acme-staging-v02.api.letsencrypt.org/directory"
default_letsencrypt_prod_server="https://acme-v02.api.letsencrypt.org/directory"
letsencrypt_staging_server="${LETSENCRYPT_STAGING_SERVER:-$default_letsencrypt_staging_server}"
letsencrypt_prod_server="${LETSENCRYPT_PROD_SERVER:-$default_letsencrypt_prod_server}"

if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  require_non_empty "OIDC_CLIENT_ID" "$oidc_client_id"
  require_non_empty "OIDC_CLIENT_SECRET" "$oidc_client_secret"
  require_non_empty "OAUTH_COOKIE_SECRET" "$oauth_cookie_secret"
  case "${#oauth_cookie_secret}" in
    16|24|32) ;;
    *) die "OAUTH_COOKIE_SECRET must be exactly 16, 24, or 32 characters" ;;
  esac
fi

require_non_empty "ACME_EMAIL" "$acme_email"

if ! is_allowed_cluster_issuer "$platform_cluster_issuer"; then
  die "PLATFORM_CLUSTER_ISSUER must be one of: selfsigned, letsencrypt, letsencrypt-staging, letsencrypt-prod"
fi
if ! is_allowed_cluster_issuer "$app_cluster_issuer"; then
  die "APP_CLUSTER_ISSUER must be one of: selfsigned, letsencrypt, letsencrypt-staging, letsencrypt-prod"
fi
if [[ "$app_namespace" != "apps-staging" && "$app_namespace" != "apps-prod" ]]; then
  die "APP_NAMESPACE must be apps-staging or apps-prod (got: ${app_namespace})"
fi
require_non_empty "APP_HOST" "$app_host"

if ! is_allowed_letsencrypt_env "$letsencrypt_env"; then
  die "LETSENCRYPT_ENV must be staging|prod|production"
fi
if [[ "$letsencrypt_env" == "production" ]]; then
  letsencrypt_env="prod"
fi

letsencrypt_alias_server="$letsencrypt_staging_server"
if [[ "$letsencrypt_env" == "prod" ]]; then
  letsencrypt_alias_server="$letsencrypt_prod_server"
fi

if [[ "${FEAT_CLICKSTACK:-true}" == "true" || "${FEAT_OTEL_K8S:-true}" == "true" ]]; then
  require_non_empty "CLICKSTACK_API_KEY" "$clickstack_api_key"
fi

if [[ "${FEAT_OTEL_K8S:-true}" == "true" && -z "$clickstack_ingestion_key" ]]; then
  clickstack_ingestion_key="$clickstack_api_key"
  log_warn "CLICKSTACK_INGESTION_KEY is not set; defaulting to CLICKSTACK_API_KEY for OTel exporters"
  log_warn "After first ClickStack login, set CLICKSTACK_INGESTION_KEY in profiles/secrets.env and rerun: make runtime-inputs-sync"
fi

kubectl create secret generic platform-runtime-inputs \
  -n flux-system \
  --from-literal=OIDC_CLIENT_ID="$oidc_client_id" \
  --from-literal=OIDC_CLIENT_SECRET="$oidc_client_secret" \
  --from-literal=OAUTH_COOKIE_SECRET="$oauth_cookie_secret" \
  --from-literal=ACME_EMAIL="$acme_email" \
  --from-literal=PLATFORM_CLUSTER_ISSUER="$platform_cluster_issuer" \
  --from-literal=APP_CLUSTER_ISSUER="$app_cluster_issuer" \
  --from-literal=APP_NAMESPACE="$app_namespace" \
  --from-literal=APP_HOST="$app_host" \
  --from-literal=LETSENCRYPT_ENV="$letsencrypt_env" \
  --from-literal=LETSENCRYPT_STAGING_SERVER="$letsencrypt_staging_server" \
  --from-literal=LETSENCRYPT_PROD_SERVER="$letsencrypt_prod_server" \
  --from-literal=LETSENCRYPT_ALIAS_SERVER="$letsencrypt_alias_server" \
  --from-literal=CLICKSTACK_API_KEY="$clickstack_api_key" \
  --from-literal=CLICKSTACK_INGESTION_KEY="$clickstack_ingestion_key" \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl -n flux-system annotate secret platform-runtime-inputs kustomize.toolkit.fluxcd.io/prune=disabled --overwrite >/dev/null
kubectl -n flux-system label secret platform-runtime-inputs platform.swhurl.io/managed=true --overwrite >/dev/null
log_info "Synchronized flux-system/platform-runtime-inputs from local env/profile"

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

legacy_oidc_client_id="${OIDC_CLIENT_ID:-}"
legacy_oidc_client_secret="${OIDC_CLIENT_SECRET:-}"
hello_oidc_client_id="${HELLO_OIDC_CLIENT_ID:-${legacy_oidc_client_id}}"
hello_oidc_client_secret="${HELLO_OIDC_CLIENT_SECRET:-${legacy_oidc_client_secret}}"
oauth_cookie_secret="${OAUTH_COOKIE_SECRET:-}"
clickstack_api_key="${CLICKSTACK_API_KEY:-}"
clickstack_ingestion_key="${CLICKSTACK_INGESTION_KEY:-}"

if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  require_non_empty "HELLO_OIDC_CLIENT_ID" "$hello_oidc_client_id"
  require_non_empty "HELLO_OIDC_CLIENT_SECRET" "$hello_oidc_client_secret"
  require_non_empty "OAUTH_COOKIE_SECRET" "$oauth_cookie_secret"
  case "${#oauth_cookie_secret}" in
    16|24|32) ;;
    *) die "OAUTH_COOKIE_SECRET must be exactly 16, 24, or 32 characters" ;;
  esac
  if [[ -n "$legacy_oidc_client_id" || -n "$legacy_oidc_client_secret" ]]; then
    log_warn "OIDC_CLIENT_ID/OIDC_CLIENT_SECRET are deprecated; prefer HELLO_OIDC_*"
  fi
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
  --from-literal=HELLO_OIDC_CLIENT_ID="$hello_oidc_client_id" \
  --from-literal=HELLO_OIDC_CLIENT_SECRET="$hello_oidc_client_secret" \
  --from-literal=OAUTH_COOKIE_SECRET="$oauth_cookie_secret" \
  --from-literal=CLICKSTACK_API_KEY="$clickstack_api_key" \
  --from-literal=CLICKSTACK_INGESTION_KEY="$clickstack_ingestion_key" \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl -n flux-system annotate secret platform-runtime-inputs kustomize.toolkit.fluxcd.io/prune=disabled --overwrite >/dev/null
kubectl -n flux-system label secret platform-runtime-inputs platform.swhurl.com/managed=true --overwrite >/dev/null
log_info "Synchronized flux-system/platform-runtime-inputs from local env/profile"

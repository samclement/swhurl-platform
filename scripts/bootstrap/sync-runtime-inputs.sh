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
clickstack_api_key="${CLICKSTACK_API_KEY:-}"
clickstack_bootstrap_email="${CLICKSTACK_BOOTSTRAP_EMAIL:-}"
clickstack_bootstrap_password="${CLICKSTACK_BOOTSTRAP_PASSWORD:-}"

if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  require_non_empty "OIDC_CLIENT_ID" "$oidc_client_id"
  require_non_empty "OIDC_CLIENT_SECRET" "$oidc_client_secret"
  require_non_empty "OAUTH_COOKIE_SECRET" "$oauth_cookie_secret"
  case "${#oauth_cookie_secret}" in
    16|24|32) ;;
    *) die "OAUTH_COOKIE_SECRET must be exactly 16, 24, or 32 characters" ;;
  esac
fi

if [[ "${FEAT_CLICKSTACK:-true}" == "true" || "${FEAT_OTEL_K8S:-true}" == "true" ]]; then
  require_non_empty "CLICKSTACK_API_KEY" "$clickstack_api_key"
fi

if [[ "${FEAT_CLICKSTACK:-true}" == "true" ]]; then
  require_non_empty "CLICKSTACK_BOOTSTRAP_EMAIL" "$clickstack_bootstrap_email"
  require_non_empty "CLICKSTACK_BOOTSTRAP_PASSWORD" "$clickstack_bootstrap_password"

  if (( ${#clickstack_bootstrap_password} < 12 )) \
    || ! [[ "$clickstack_bootstrap_password" =~ [A-Z] ]] \
    || ! [[ "$clickstack_bootstrap_password" =~ [a-z] ]] \
    || ! [[ "$clickstack_bootstrap_password" =~ [0-9] ]] \
    || ! [[ "$clickstack_bootstrap_password" =~ [^A-Za-z0-9] ]]; then
    die "CLICKSTACK_BOOTSTRAP_PASSWORD must be >=12 chars and include upper/lower/digit/special"
  fi
fi

kubectl create secret generic platform-runtime-inputs \
  -n flux-system \
  --from-literal=OIDC_CLIENT_ID="$oidc_client_id" \
  --from-literal=OIDC_CLIENT_SECRET="$oidc_client_secret" \
  --from-literal=OAUTH_COOKIE_SECRET="$oauth_cookie_secret" \
  --from-literal=CLICKSTACK_API_KEY="$clickstack_api_key" \
  --from-literal=CLICKSTACK_BOOTSTRAP_EMAIL="$clickstack_bootstrap_email" \
  --from-literal=CLICKSTACK_BOOTSTRAP_PASSWORD="$clickstack_bootstrap_password" \
  --dry-run=client -o yaml | kubectl apply -f -

kubectl -n flux-system annotate secret platform-runtime-inputs kustomize.toolkit.fluxcd.io/prune=disabled --overwrite >/dev/null
kubectl -n flux-system label secret platform-runtime-inputs platform.swhurl.io/managed=true --overwrite >/dev/null
log_info "Synchronized flux-system/platform-runtime-inputs from local env/profile"

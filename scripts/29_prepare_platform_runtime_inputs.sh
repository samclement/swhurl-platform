#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

if [[ "$DELETE" == true ]]; then
  log_info "Delete-time runtime input cleanup moved to scripts/99_execute_teardown.sh; nothing to do"
  exit 0
fi

ensure_context

log_warn "scripts/29_prepare_platform_runtime_inputs.sh is a manual compatibility bridge; default ./run.sh apply does not invoke it."
log_info "Syncing flux-system/Secret platform-runtime-inputs for Flux post-build substitution"

GEN_COOKIE_SECRET() { hexdump -v -e '/1 "%02X"' -n 16 /dev/urandom; }

# oauth2-proxy cookie secret create-once semantics:
# - If OAUTH_COOKIE_SECRET is set: enforce it.
# - Else: reuse the currently synced flux-source value.
# - Else: generate a fresh 16-byte value.
cookie_secret="${OAUTH_COOKIE_SECRET:-}"
if [[ -z "$cookie_secret" ]]; then
  cookie_secret="$(kubectl -n flux-system get secret platform-runtime-inputs -o jsonpath='{.data.OAUTH_COOKIE_SECRET}' 2>/dev/null | base64 -d || true)"
fi
if [[ -z "$cookie_secret" ]]; then
  cookie_secret="$(GEN_COOKIE_SECRET)"
fi

cookie_secret_len="$(printf '%s' "$cookie_secret" | wc -c | tr -d '[:space:]')"
if [[ "$cookie_secret_len" != "16" && "$cookie_secret_len" != "24" && "$cookie_secret_len" != "32" ]]; then
  die "OAUTH_COOKIE_SECRET must be exactly 16, 24, or 32 bytes (got: ${cookie_secret_len})"
fi

if [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]]; then
  [[ -n "${OIDC_CLIENT_ID:-}" ]] || die "OIDC_CLIENT_ID is required when FEAT_OAUTH2_PROXY=true"
  [[ -n "${OIDC_CLIENT_SECRET:-}" ]] || die "OIDC_CLIENT_SECRET is required when FEAT_OAUTH2_PROXY=true"
fi

if [[ "${FEAT_OTEL_K8S:-true}" == "true" ]]; then
  [[ -n "${CLICKSTACK_API_KEY:-}" ]] || die "CLICKSTACK_API_KEY is required when FEAT_OTEL_K8S=true"
fi

kubectl_ns flux-system

args=(
  kubectl -n flux-system create secret generic platform-runtime-inputs
  --from-literal=OIDC_CLIENT_ID="${OIDC_CLIENT_ID:-}"
  --from-literal=OIDC_CLIENT_SECRET="${OIDC_CLIENT_SECRET:-}"
  --from-literal=OAUTH_COOKIE_SECRET="${cookie_secret}"
  --from-literal=CLICKSTACK_API_KEY="${CLICKSTACK_API_KEY:-}"
  --dry-run=client -o yaml
)

"${args[@]}" | kubectl apply -f -
label_managed flux-system secret platform-runtime-inputs

log_info "Flux runtime input source secret ensured: flux-system/platform-runtime-inputs"

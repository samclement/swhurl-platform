#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_CLICKSTACK:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_CLICKSTACK=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling clickstack and legacy observability releases"
  helm uninstall clickstack -n observability || true
  helm uninstall fluent-bit -n logging || true
  helm uninstall loki -n observability || true
  helm uninstall monitoring -n observability || true
  exit 0
fi

kubectl_ns observability
CLICKSTACK_HOST="${CLICKSTACK_HOST:-clickstack.${BASE_DOMAIN}}"
CLICKSTACK_API_KEY="${CLICKSTACK_API_KEY:-$(cat /proc/sys/kernel/random/uuid)}"

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT
cp "$SCRIPT_DIR/../infra/values/clickstack.yaml" "$TMPDIR/values.yaml"
(
  export CLICKSTACK_HOST CLICKSTACK_API_KEY CLUSTER_ISSUER
  envsubst < "$TMPDIR/values.yaml" > "$TMPDIR/values.rendered.yaml"
)

values_args=(-f "$TMPDIR/values.rendered.yaml")
if [[ "${FEAT_OAUTH2_PROXY:-false}" == "true" ]]; then
  if [[ -n "${OAUTH_HOST:-}" ]]; then
    cp "$SCRIPT_DIR/../infra/values/clickstack-oauth.yaml" "$TMPDIR/values.oauth.yaml"
    (
      DOLLAR='$'
      export OAUTH_HOST DOLLAR
      envsubst < "$TMPDIR/values.oauth.yaml" > "$TMPDIR/values.oauth.rendered.yaml"
    )
    values_args+=(-f "$TMPDIR/values.oauth.rendered.yaml")
  else
    log_warn "FEAT_OAUTH2_PROXY=true but OAUTH_HOST is empty; skipping ClickStack OAuth ingress annotations"
  fi
fi

if [[ "${CLICKSTACK_CLEAN_LEGACY:-true}" == "true" ]]; then
  helm uninstall fluent-bit -n logging >/dev/null 2>&1 || true
  helm uninstall loki -n observability >/dev/null 2>&1 || true
  helm uninstall monitoring -n observability >/dev/null 2>&1 || true
fi

helm_upsert clickstack clickstack/clickstack observability \
  --reset-values \
  "${values_args[@]}"

wait_deploy observability clickstack-app
wait_deploy observability clickstack-otel-collector
if kubectl -n observability get deploy clickstack-clickhouse >/dev/null 2>&1; then
  wait_deploy observability clickstack-clickhouse
fi
log_info "clickstack installed at https://${CLICKSTACK_HOST}"

#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_LOGGING:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_LOGGING=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling fluent-bit"
  helm uninstall fluent-bit -n logging || true
  exit 0
fi

LOKI_URL="${LOKI_URL:-http://loki.observability.svc.cluster.local:3100}"

# Render a values overlay that switches outputs to Loki
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT
cp "$SCRIPT_DIR/../values/fluent-bit-loki.yaml" "$TMPDIR/values.yaml"
(
  export LOKI_URL CLUSTER_NAME
  envsubst < "$TMPDIR/values.yaml" > "$TMPDIR/values.rendered.yaml"
)

helm_upsert fluent-bit fluent/fluent-bit logging \
  --reset-values \
  -f "$TMPDIR/values.rendered.yaml" \
  --set tolerations[0].operator=Exists

wait_ds logging fluent-bit
log_info "fluent-bit installed"

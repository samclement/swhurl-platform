#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "${FEAT_CILIUM:-true}" != "true" && "$DELETE" != true ]]; then
  log_info "FEAT_CILIUM=false; skipping install"
  exit 0
fi

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling cilium"
  helm uninstall cilium -n kube-system || true
  exit 0
fi

if [[ "${CILIUM_SKIP_FLANNEL_CHECK:-false}" != "true" ]]; then
  flannel_backend=$(kubectl get nodes -o jsonpath='{range .items[*]}{.metadata.annotations.flannel\.alpha\.coreos\.com/backend-type}{"\n"}{end}' | grep -v '^$' | head -n 1 || true)
  if [[ -n "$flannel_backend" ]]; then
    log_error "Detected flannel backend on nodes (${flannel_backend})."
    log_error "Cilium requires k3s with flannel disabled: --flannel-backend=none --disable-network-policy"
    log_error "Reinstall k3s, then rerun this step. Set CILIUM_SKIP_FLANNEL_CHECK=true to override."
    exit 1
  fi
fi

VALUES_FILE="$SCRIPT_DIR/../infra/values/cilium.yaml"
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT
cp "$VALUES_FILE" "$TMPDIR/values.yaml"
(
  DOLLAR='$'
  export CLUSTER_ISSUER OAUTH_HOST HUBBLE_HOST DOLLAR
  envsubst < "$TMPDIR/values.yaml" > "$TMPDIR/values.rendered.yaml"
)

helm_upsert cilium cilium/cilium kube-system -f "$TMPDIR/values.rendered.yaml"

wait_ds kube-system cilium
wait_deploy kube-system cilium-operator

if kubectl -n kube-system get deploy hubble-relay >/dev/null 2>&1; then
  wait_deploy kube-system hubble-relay
fi
if kubectl -n kube-system get deploy hubble-ui >/dev/null 2>&1; then
  wait_deploy kube-system hubble-ui
fi
log_info "cilium installed"

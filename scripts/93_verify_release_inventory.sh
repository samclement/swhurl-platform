#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Release inventory check is apply-only; skipping in delete mode"
  exit 0
fi

ensure_context
need_cmd helm

say() { printf "\n== %s ==\n" "$1"; }
ok() { printf "[OK] %s\n" "$1"; }
bad() { printf "[BAD] %s\n" "$1"; fail=1; }

fail=0
say "Required Releases"

expected=(
  "kube-system/platform-namespaces"
  "cert-manager/cert-manager"
  "kube-system/platform-issuers"
  "ingress/ingress-nginx"
)
[[ "${FEAT_CILIUM:-true}" == "true" ]] && expected+=("kube-system/cilium")
[[ "${FEAT_OAUTH2_PROXY:-true}" == "true" ]] && expected+=("ingress/oauth2-proxy")
[[ "${FEAT_CLICKSTACK:-true}" == "true" ]] && expected+=("observability/clickstack")
[[ "${FEAT_OTEL_K8S:-true}" == "true" ]] && expected+=("logging/otel-k8s-daemonset" "logging/otel-k8s-cluster")
[[ "${FEAT_MINIO:-true}" == "true" ]] && expected+=("storage/minio")

actual="$(helm list -A --no-headers 2>/dev/null | awk '{print $2"/"$1}')"
for item in "${expected[@]}"; do
  if grep -qx "$item" <<< "$actual"; then
    ok "$item"
  else
    bad "$item"
  fi
done

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Release inventory verification passed"

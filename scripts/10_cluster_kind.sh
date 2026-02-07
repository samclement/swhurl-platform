#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

if [[ "${K8S_PROVIDER:-kind}" != "kind" ]]; then
  log_info "K8S_PROVIDER is '${K8S_PROVIDER:-}', skipping kind cluster creation"
  exit 0
fi

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

export KIND_EXPERIMENTAL_PROVIDER="${KIND_EXPERIMENTAL_PROVIDER:-podman}"

if [[ "$DELETE" == true ]]; then
  log_info "Deleting kind cluster '${CLUSTER_NAME}'"
  kind delete cluster --name "${CLUSTER_NAME}" || true
  exit 0
fi

if kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  log_info "Kind cluster '${CLUSTER_NAME}' already exists"
  exit 0
fi

log_info "Creating kind cluster '${CLUSTER_NAME}'"
if [[ -f "${KIND_CONFIG}" ]]; then
  kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_CONFIG}"
else
  kind create cluster --name "${CLUSTER_NAME}"
fi

kubectl cluster-info --context "kind-${CLUSTER_NAME}" >/dev/null 2>&1 || die "Failed to verify kind context"
log_info "Kind cluster is ready"


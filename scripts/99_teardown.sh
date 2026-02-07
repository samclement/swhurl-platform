#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

log_info "Final teardown: deleting cluster '${CLUSTER_NAME}'"

case "${K8S_PROVIDER:-kind}" in
  kind)
    kind delete cluster --name "${CLUSTER_NAME}" || true
    ;;
  *)
    log_warn "No teardown implemented for provider ${K8S_PROVIDER}"
    ;;
esac

log_info "Teardown complete"


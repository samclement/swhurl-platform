#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

for ns in platform-system ingress cert-manager logging observability storage; do
  kubectl_ns "$ns"
done

log_info "Namespaces ensured"


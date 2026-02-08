#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

kubectl apply -f "$SCRIPT_DIR/../infra/manifests/namespaces.yaml"
log_info "Namespaces applied"

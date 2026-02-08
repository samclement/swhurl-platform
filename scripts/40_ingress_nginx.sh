#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling ingress-nginx"
  helm uninstall ingress-nginx -n ingress || true
  exit 0
fi

helm_upsert ingress-nginx ingress-nginx/ingress-nginx ingress \
  -f "$SCRIPT_DIR/../infra/values/ingress-nginx-logging.yaml"

wait_deploy ingress ingress-nginx-controller
log_info "ingress-nginx installed"

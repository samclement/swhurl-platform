#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

log_info "Checking prerequisites"

# Required
need_cmd kubectl
need_cmd helm
need_cmd curl

case "${K8S_PROVIDER:-kind}" in
  kind)
    need_cmd kind
    ;;
esac

# Optional
command -v podman >/dev/null 2>&1 || log_warn "Podman not found (required for kind+podman provider on macOS/Linux)"
command -v jq >/dev/null 2>&1 || log_warn "jq not found (optional)"
command -v yq >/dev/null 2>&1 || log_warn "yq not found (optional)"
command -v sops >/dev/null 2>&1 || log_warn "sops not found (optional for secrets)"
command -v age >/dev/null 2>&1 || log_warn "age not found (optional for secrets)"

log_info "All checks passed (or warned)."


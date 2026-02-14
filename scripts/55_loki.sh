#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Uninstalling legacy Loki release (deprecated step)"
  helm uninstall loki -n observability || true
  exit 0
fi

log_info "Step 55 is deprecated. Loki is replaced by ClickStack via scripts/50_clickstack.sh."

#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

ensure_context

if [[ "$DELETE" == true ]]; then
  log_info "Destroying platform Helm releases (phase=platform)"
  helmfile_cmd -l phase=platform destroy >/dev/null 2>&1 || true
  exit 0
fi

log_info "Syncing platform Helm releases (phase=platform)"
helmfile_cmd -l phase=platform sync
log_info "Platform Helm releases synced"


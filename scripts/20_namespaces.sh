#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Namespaces are managed by Helmfile; skipping in delete mode"
  exit 0
fi

need_cmd helmfile

# Namespaces are managed declaratively via a local Helm chart so the platform can
# rely on them existing before applying secrets/config.
helmfile_cmd -l component=platform-namespaces sync
log_info "Namespaces ensured (helmfile: component=platform-namespaces)"

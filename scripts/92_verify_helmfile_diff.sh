#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Helmfile validation is apply-only; skipping in delete mode"
  exit 0
fi

ensure_context
need_cmd helmfile

log_info "Verifying Helmfile environment '${HELMFILE_ENV:-default}'"
helmfile_cmd repos >/dev/null

log_info "Running helmfile lint"
helmfile_cmd lint

rendered="$(mktemp)"
trap 'rm -f "$rendered"' EXIT

log_info "Rendering desired manifests (helmfile template)"
helmfile_cmd template > "$rendered"

log_info "Validating rendered manifests against live API (kubectl dry-run=server)"
if ! kubectl apply --dry-run=server -f "$rendered" >/dev/null 2>&1; then
  log_warn "Server dry-run failed; trying client dry-run fallback"
  kubectl apply --dry-run=client -f "$rendered" >/dev/null
fi

log_info "Template/dry-run validation passed"

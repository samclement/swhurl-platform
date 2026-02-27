#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  # Namespace deletion is intentionally owned by scripts/99_execute_teardown.sh.
  log_info "Namespace reconcile is apply-only; skipping in delete mode"
  exit 0
fi

kubectl apply -k "$ROOT_DIR/cluster/base/namespaces" >/dev/null
log_info "Namespaces ensured (cluster/base/namespaces)"

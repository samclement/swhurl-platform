#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

ensure_context

DELETE=false
for arg in "$@"; do
  case "$arg" in
    --delete) DELETE=true ;;
    *) die "Unknown argument: $arg" ;;
  esac
done
if [[ "$DELETE" == true ]]; then
  die "scripts/20_reconcile_platform_namespaces.sh is apply-only (namespace deletion is owned by scripts/99_execute_teardown.sh)"
fi

kubectl apply -k "$ROOT_DIR/infrastructure/namespaces" >/dev/null
log_info "Namespaces ensured (infrastructure/namespaces)"

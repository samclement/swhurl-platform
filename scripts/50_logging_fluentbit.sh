#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

log_warn "scripts/50_logging_fluentbit.sh is deprecated; delegating to scripts/50_clickstack.sh"
exec "$SCRIPT_DIR/50_clickstack.sh" "$@"

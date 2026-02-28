#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/../lib/00_common.sh"

if host_has_flag "--delete" "$@"; then
  host_log_info "Host bootstrap delete is a no-op"
  exit 0
fi

host_log_info "Host bootstrap is a no-op (dependencies are documented in README.md)"

#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/../lib/10_packages_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

if [[ "$DELETE" == true ]]; then
  host_log_info "Host bootstrap delete is a no-op"
  exit 0
fi

host_ensure_base_packages
host_verify_host_commands
host_log_info "Host bootstrap complete"

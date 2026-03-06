#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/../lib/20_dynamic_dns_lib.sh"

if host_has_flag "--delete" "$@"; then
  host_dynamic_dns_delete
else
  host_dynamic_dns_apply
fi

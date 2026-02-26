#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/00_common.sh"

host_dynamic_dns_script() {
  local root
  root="${HOST_REPO_ROOT:-$(host_repo_root_from_lib)}"
  printf '%s/scripts/manual_configure_route53_dns_updater.sh' "$root"
}

host_dynamic_dns_apply() {
  local script
  script="$(host_dynamic_dns_script)"
  [[ -x "$script" ]] || host_die "Missing executable DNS script: $script"
  host_log_info "Applying dynamic DNS systemd units via legacy script"
  "$script"
}

host_dynamic_dns_delete() {
  local script
  script="$(host_dynamic_dns_script)"
  [[ -x "$script" ]] || host_die "Missing executable DNS script: $script"
  host_log_info "Deleting dynamic DNS systemd units via legacy script"
  "$script" --delete
}

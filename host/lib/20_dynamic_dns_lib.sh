#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/00_common.sh"

readonly HOST_DDNS_SERVICE="aws-dns-updater.service"
readonly HOST_DDNS_TIMER="aws-dns-updater.timer"
readonly HOST_DDNS_SERVICE_PATH="/etc/systemd/system/${HOST_DDNS_SERVICE}"
readonly HOST_DDNS_TIMER_PATH="/etc/systemd/system/${HOST_DDNS_TIMER}"
readonly HOST_DDNS_MANAGED_MARKER="Managed template for host dynamic DNS updater"

host_dynamic_dns_is_supported_host() {
  if [[ "$(uname -s || true)" != "Linux" ]]; then
    host_log_info "Non-Linux host detected; skipping dynamic DNS task"
    return 1
  fi
  if ! command -v systemctl >/dev/null 2>&1; then
    host_log_info "systemd not available; skipping dynamic DNS task"
    return 1
  fi
  return 0
}

host_dynamic_dns_user_home() {
  local run_user="$1"
  local home
  home="$(getent passwd "$run_user" 2>/dev/null | cut -d: -f6 || true)"
  if [[ -z "$home" ]]; then
    home="$HOME"
  fi
  [[ -n "$home" ]] || host_die "Could not determine home directory for ${run_user}"
  printf '%s' "$home"
}

host_dynamic_dns_write_unit_if_changed() {
  local path="$1" content="$2"
  if [[ -f "$path" ]] && cmp -s <(printf "%s" "$content") "$path"; then
    host_log_info "$(basename "$path") already up-to-date"
    return 1
  fi

  if [[ -f "$path" ]]; then
    host_log_info "Updating $(basename "$path")"
  else
    host_log_info "Creating $(basename "$path")"
  fi
  printf "%s" "$content" | host_sudo tee "$path" >/dev/null
  return 0
}

host_dynamic_dns_apply() {
  host_dynamic_dns_is_supported_host || return 0

  local root run_user run_home helper_source helper_target service_template timer_template
  root="${HOST_REPO_ROOT:-$(host_repo_root_from_lib)}"
  run_user="${SUDO_USER:-$(id -un)}"
  run_home="$(host_dynamic_dns_user_home "$run_user")"
  helper_source="${root}/scripts/aws-dns-updater.sh"
  helper_target="${run_home}/.local/scripts/aws-dns-updater.sh"
  service_template="${root}/host/templates/systemd/dynamic-dns.service.tmpl"
  timer_template="${root}/host/templates/systemd/dynamic-dns.timer.tmpl"
  [[ -f "$helper_source" ]] || host_die "Helper script not found: $helper_source"
  [[ -f "$service_template" ]] || host_die "Missing service template: $service_template"
  [[ -f "$timer_template" ]] || host_die "Missing timer template: $timer_template"

  host_sudo mkdir -p "$(dirname "$helper_target")"
  if ! cmp -s "$helper_source" "$helper_target" 2>/dev/null; then
    host_log_info "Installing dynamic DNS helper to $helper_target"
    host_sudo install -m 0755 "$helper_source" "$helper_target"
    host_sudo chown "$run_user:$run_user" "$helper_target"
  else
    host_log_info "Dynamic DNS helper already up-to-date: $helper_target"
  fi

  local service_content timer_content
  service_content="$(
    sed \
      -e "s|__EXEC_START__|/bin/bash ${helper_target}|g" \
      -e "s|__RUN_USER__|${run_user}|g" \
      "$service_template"
  )"
  timer_content="$(cat "$timer_template")"

  local unit_changed=0
  if host_dynamic_dns_write_unit_if_changed "$HOST_DDNS_SERVICE_PATH" "$service_content"; then
    unit_changed=1
  fi
  if host_dynamic_dns_write_unit_if_changed "$HOST_DDNS_TIMER_PATH" "$timer_content"; then
    unit_changed=1
  fi

  if (( unit_changed == 1 )); then
    host_log_info "Reloading systemd units"
    host_sudo systemctl daemon-reload
  fi

  host_sudo systemctl enable "$HOST_DDNS_SERVICE" >/dev/null || true
  host_sudo systemctl enable "$HOST_DDNS_TIMER" >/dev/null || true

  if (( unit_changed == 1 )); then
    host_sudo systemctl restart "$HOST_DDNS_SERVICE" || true
    host_sudo systemctl restart "$HOST_DDNS_TIMER" || true
  else
    host_sudo systemctl start "$HOST_DDNS_SERVICE" || true
    host_sudo systemctl start "$HOST_DDNS_TIMER" || true
  fi

  host_log_info "Configured dynamic DNS updater for *.homelab.swhurl.com"
}

host_dynamic_dns_delete() {
  host_dynamic_dns_is_supported_host || return 0

  if [[ ! -f "$HOST_DDNS_SERVICE_PATH" ]] || ! grep -q "$HOST_DDNS_MANAGED_MARKER" "$HOST_DDNS_SERVICE_PATH"; then
    host_log_info "Dynamic DNS units not recognized as host-managed; nothing to delete"
    return 0
  fi

  host_log_info "Deleting host-managed dynamic DNS units"
  host_sudo systemctl stop "$HOST_DDNS_TIMER" "$HOST_DDNS_SERVICE" || true
  host_sudo systemctl disable "$HOST_DDNS_TIMER" "$HOST_DDNS_SERVICE" >/dev/null || true
  host_sudo rm -f "$HOST_DDNS_SERVICE_PATH" "$HOST_DDNS_TIMER_PATH" || true
  host_sudo systemctl daemon-reload || true
}

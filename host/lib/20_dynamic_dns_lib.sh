#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/00_common.sh"

host_dynamic_dns_is_supported_host() {
  local os
  os="$(uname -s || true)"
  if [[ "$os" != "Linux" ]]; then
    host_log_info "Non-Linux host detected ($os); skipping dynamic DNS task"
    return 1
  fi
  if ! command -v systemctl >/dev/null 2>&1; then
    host_log_info "systemd not available; skipping dynamic DNS task"
    return 1
  fi
  return 0
}

host_dynamic_dns_service_name() { printf '%s' "aws-dns-updater.service"; }
host_dynamic_dns_timer_name() { printf '%s' "aws-dns-updater.timer"; }
host_dynamic_dns_service_path() { printf '%s' "/etc/systemd/system/$(host_dynamic_dns_service_name)"; }
host_dynamic_dns_timer_path() { printf '%s' "/etc/systemd/system/$(host_dynamic_dns_timer_name)"; }

host_dynamic_dns_run_user() {
  printf '%s' "${SUDO_USER:-$(id -un)}"
}

host_dynamic_dns_run_home() {
  local run_user="$1" home
  home="$(getent passwd "$run_user" | cut -d: -f6 || true)"
  if [[ -z "$home" ]]; then
    # Fallback to current HOME if getent is unavailable.
    home="$HOME"
  fi
  [[ -n "$home" ]] || host_die "Could not determine home directory for ${run_user}"
  printf '%s' "$home"
}

host_dynamic_dns_helper_source() {
  local root
  root="${HOST_REPO_ROOT:-$(host_repo_root_from_lib)}"
  printf '%s/scripts/aws-dns-updater.sh' "$root"
}

host_dynamic_dns_helper_target() {
  local run_home="$1"
  printf '%s/.local/scripts/aws-dns-updater.sh' "$run_home"
}

host_dynamic_dns_template_path() {
  local file="$1" root
  root="${HOST_REPO_ROOT:-$(host_repo_root_from_lib)}"
  printf '%s/host/templates/systemd/%s' "$root" "$file"
}

host_dynamic_dns_subdomains() {
  local raw="${SWHURL_SUBDOMAINS:-}"

  if [[ -z "$raw" ]]; then
    if [[ -n "${SWHURL_SUBDOMAIN:-}" ]]; then
      raw="$SWHURL_SUBDOMAIN"
    elif [[ -n "${BASE_DOMAIN:-}" && "$BASE_DOMAIN" =~ \.swhurl\.com$ ]]; then
      local base
      base="${BASE_DOMAIN%.swhurl.com}"
      base="${base%.}"
      raw="$base oauth.$base staging.hello.$base prod.hello.$base clickstack.$base hubble.$base minio.$base minio-console.$base"
      host_log_warn "SWHURL_SUBDOMAINS not set; derived defaults: $raw"
    else
      raw="homelab"
      host_log_warn "SWHURL_SUBDOMAINS not set; defaulting to '$raw'"
    fi
  fi

  raw="${raw//,/ }"
  # shellcheck disable=SC2206
  local arr=($raw)
  local s
  for s in "${arr[@]}"; do
    [[ -n "$s" ]] && printf '%s\n' "$s"
  done
}

host_dynamic_dns_install_helper() {
  local source="$1" target="$2" run_user="$3"
  [[ -f "$source" ]] || host_die "Helper script not found: $source"

  host_sudo mkdir -p "$(dirname "$target")"
  if ! cmp -s "$source" "$target" 2>/dev/null; then
    host_log_info "Installing dynamic DNS helper to $target"
    host_sudo install -m 0755 "$source" "$target"
    host_sudo chown "$run_user:$run_user" "$target"
  else
    host_log_info "Dynamic DNS helper already up-to-date: $target"
  fi
}

host_dynamic_dns_render_service_content() {
  local template="$1" exec_start="$2" run_user="$3"
  sed \
    -e "s|__EXEC_START__|${exec_start}|g" \
    -e "s|__RUN_USER__|${run_user}|g" \
    "$template"
}

host_dynamic_dns_render_timer_content() {
  local template="$1"
  cat "$template"
}

host_dynamic_dns_write_unit_if_changed() {
  local path="$1" content="$2"
  local changed=false
  if [[ -f "$path" ]]; then
    if ! diff -q <(printf "%s" "$content") "$path" >/dev/null 2>&1; then
      host_log_info "Updating $(basename "$path")"
      printf "%s" "$content" | host_sudo tee "$path" >/dev/null
      changed=true
    else
      host_log_info "$(basename "$path") already up-to-date"
    fi
  else
    host_log_info "Creating $(basename "$path")"
    printf "%s" "$content" | host_sudo tee "$path" >/dev/null
    changed=true
  fi

  [[ "$changed" == "true" ]]
}

host_dynamic_dns_apply() {
  host_dynamic_dns_is_supported_host || return 0

  local run_user run_home helper_source helper_target service_template timer_template
  run_user="$(host_dynamic_dns_run_user)"
  run_home="$(host_dynamic_dns_run_home "$run_user")"
  helper_source="$(host_dynamic_dns_helper_source)"
  helper_target="$(host_dynamic_dns_helper_target "$run_home")"
  service_template="$(host_dynamic_dns_template_path dynamic-dns.service.tmpl)"
  timer_template="$(host_dynamic_dns_template_path dynamic-dns.timer.tmpl)"

  host_dynamic_dns_install_helper "$helper_source" "$helper_target" "$run_user"

  local -a subdomains=()
  while IFS= read -r sub; do
    [[ -n "$sub" ]] && subdomains+=("$sub")
  done < <(host_dynamic_dns_subdomains)
  [[ "${#subdomains[@]}" -gt 0 ]] || host_die "No subdomains provided or derived"

  local exec_start="/bin/bash ${helper_target}"
  local s
  for s in "${subdomains[@]}"; do
    exec_start+=" ${s}"
  done

  local service_content timer_content
  service_content="$(host_dynamic_dns_render_service_content "$service_template" "$exec_start" "$run_user")"
  timer_content="$(host_dynamic_dns_render_timer_content "$timer_template")"

  local unit_changed=0
  if host_dynamic_dns_write_unit_if_changed "$(host_dynamic_dns_service_path)" "$service_content"; then
    unit_changed=1
  fi
  if host_dynamic_dns_write_unit_if_changed "$(host_dynamic_dns_timer_path)" "$timer_content"; then
    unit_changed=1
  fi

  if (( unit_changed == 1 )); then
    host_log_info "Reloading systemd units"
    host_sudo systemctl daemon-reload
  fi

  local service timer
  service="$(host_dynamic_dns_service_name)"
  timer="$(host_dynamic_dns_timer_name)"

  host_sudo systemctl enable "$service" >/dev/null || true
  host_sudo systemctl enable "$timer" >/dev/null || true

  if (( unit_changed == 1 )); then
    host_sudo systemctl restart "$service" || true
    host_sudo systemctl restart "$timer" || true
  else
    host_sudo systemctl start "$service" || true
    host_sudo systemctl start "$timer" || true
  fi

  host_log_info "Configured dynamic DNS subdomains: ${subdomains[*]} (under swhurl.com)"
}

host_dynamic_dns_delete() {
  host_dynamic_dns_is_supported_host || return 0

  local service_path timer_path service timer
  service_path="$(host_dynamic_dns_service_path)"
  timer_path="$(host_dynamic_dns_timer_path)"
  service="$(host_dynamic_dns_service_name)"
  timer="$(host_dynamic_dns_timer_name)"

  local managed=false
  if [[ -f "$service_path" ]] && grep -q "Managed template for host dynamic DNS updater" "$service_path"; then
    managed=true
  fi

  if [[ "$managed" != "true" ]]; then
    host_log_info "Dynamic DNS units not recognized as host-managed; nothing to delete"
    return 0
  fi

  host_log_info "Deleting host-managed dynamic DNS units"
  host_sudo systemctl stop "$timer" "$service" || true
  host_sudo systemctl disable "$timer" "$service" >/dev/null || true
  host_sudo rm -f "$service_path" "$timer_path" || true
  host_sudo systemctl daemon-reload || true
}

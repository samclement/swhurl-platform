# Shared helpers for the host installers (dynamic-dns.sh, backup-timer.sh).
# Sourced, not run. Both install system units under /etc/systemd/system that
# run as the invoking user, and log to the system journal (journalctl -u <unit>).

host_log_info() { printf "[HOST][INFO] %s\n" "$*"; }
host_log_error() { printf "[HOST][ERROR] %s\n" "$*" >&2; }
host_die() { host_log_error "$*"; exit 1; }

host_need_cmd() {
  command -v "$1" >/dev/null 2>&1 || host_die "Missing required command: $1"
}

host_sudo() {
  if [[ "${EUID:-$(id -u)}" -eq 0 ]]; then
    "$@"
  else
    host_need_cmd sudo
    sudo "$@"
  fi
}

# True on Linux with systemd; otherwise explains and returns 1.
host_has_systemd() {
  if [[ "$(uname -s || true)" != "Linux" ]]; then
    host_log_info "Non-Linux host detected; skipping"
    return 1
  fi
  if ! command -v systemctl >/dev/null 2>&1; then
    host_log_info "systemd not available; skipping"
    return 1
  fi
  return 0
}

# The user a unit runs as: the one who ran sudo, else the current user.
host_run_user() { printf '%s' "${SUDO_USER:-$(id -un)}"; }

host_user_home() {
  local run_user="$1" home
  home="$(getent passwd "$run_user" 2>/dev/null | cut -d: -f6 || true)"
  [[ -n "$home" ]] || home="$HOME"
  [[ -n "$home" ]] || host_die "Could not determine home directory for ${run_user}"
  printf '%s' "$home"
}

# Write content to a root-owned path; returns 1 (no write) when unchanged.
host_write_if_changed() {
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

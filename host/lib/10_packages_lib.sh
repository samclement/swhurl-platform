#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/00_common.sh"

host_detect_package_manager() {
  local requested="${HOST_PACKAGE_MANAGER:-auto}"
  if [[ "$requested" != "auto" ]]; then
    printf '%s' "$requested"
    return 0
  fi

  if command -v apt-get >/dev/null 2>&1; then
    printf 'apt'
  elif command -v dnf >/dev/null 2>&1; then
    printf 'dnf'
  elif command -v pacman >/dev/null 2>&1; then
    printf 'pacman'
  elif command -v zypper >/dev/null 2>&1; then
    printf 'zypper'
  else
    printf ''
  fi
}

host_default_packages_for_manager() {
  local mgr="$1"
  case "$mgr" in
    apt) printf '%s\n' curl jq ripgrep gettext-base util-linux ;;
    dnf) printf '%s\n' curl jq ripgrep gettext util-linux ;;
    pacman) printf '%s\n' curl jq ripgrep gettext util-linux ;;
    zypper) printf '%s\n' curl jq ripgrep gettext-tools util-linux ;;
    *) return 1 ;;
  esac
}

host_install_packages() {
  local mgr="$1"; shift
  local -a pkgs=("$@")
  [[ "${#pkgs[@]}" -gt 0 ]] || return 0

  host_log_info "Installing host packages via ${mgr}: ${pkgs[*]}"

  case "$mgr" in
    apt)
      host_sudo apt-get update -y
      host_sudo apt-get install -y "${pkgs[@]}"
      ;;
    dnf)
      host_sudo dnf install -y "${pkgs[@]}"
      ;;
    pacman)
      host_sudo pacman -Sy --noconfirm "${pkgs[@]}"
      ;;
    zypper)
      host_sudo zypper --non-interactive install "${pkgs[@]}"
      ;;
    *)
      host_die "Unsupported package manager: ${mgr}"
      ;;
  esac
}

host_ensure_base_packages() {
  if [[ "${HOST_INSTALL_PACKAGES:-true}" != "true" ]]; then
    host_log_warn "HOST_INSTALL_PACKAGES=false; skipping package installation"
    return 0
  fi

  local mgr
  mgr="$(host_detect_package_manager)"
  [[ -n "$mgr" ]] || host_die "Could not detect package manager; set HOST_PACKAGE_MANAGER explicitly"

  local -a pkgs=()
  if [[ -n "${HOST_BASE_PACKAGES:-}" ]]; then
    # shellcheck disable=SC2206
    pkgs=(${HOST_BASE_PACKAGES})
  else
    while IFS= read -r p; do
      [[ -n "$p" ]] && pkgs+=("$p")
    done < <(host_default_packages_for_manager "$mgr")
  fi

  host_install_packages "$mgr" "${pkgs[@]}"
}

host_verify_host_commands() {
  local required="${HOST_REQUIRED_COMMANDS:-curl jq rg envsubst base64 hexdump}"
  local cmd
  for cmd in $required; do
    host_need_cmd "$cmd"
  done

  # These are managed by separate steps in many environments. Warn only.
  for cmd in kubectl helm flux; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
      host_log_warn "${cmd} not found (expected for cluster lifecycle operations)"
    fi
  done
}

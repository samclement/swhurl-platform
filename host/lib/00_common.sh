#!/usr/bin/env bash

if [[ "${HOST_COMMON_LIB_LOADED:-}" == "1" ]]; then
  return 0 2>/dev/null || exit 0
fi
readonly HOST_COMMON_LIB_LOADED="1"

host_log_info() { printf "[HOST][INFO] %s\n" "$*"; }
host_log_warn() { printf "[HOST][WARN] %s\n" "$*"; }
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

host_repo_root_from_lib() {
  local lib_dir
  lib_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  (cd "$lib_dir/../.." && pwd)
}

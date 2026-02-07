#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

OS=$(uname -s || true)
if [[ "$OS" == "Darwin" ]]; then
  if ! command -v podman >/dev/null 2>&1; then
    log_warn "Podman not installed; skipping podman machine setup"
    exit 0
  fi
  if ! podman machine list | grep -q "^default"; then
    log_info "Initializing podman machine"
    podman machine init --cpus 4 --memory 6144 --disk-size 40 || true
  fi
  log_info "Starting podman machine"
  podman machine start || true
else
  if command -v systemctl >/dev/null 2>&1 && command -v podman >/dev/null 2>&1; then
    log_info "Enabling podman.socket for Docker-compatible API"
    systemctl --user enable --now podman.socket || true
  else
    log_warn "Podman or systemd not available; skipping socket setup"
  fi
fi

log_info "Podman setup complete"


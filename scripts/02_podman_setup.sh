#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

if [[ "${K8S_PROVIDER:-kind}" != "kind" ]]; then
  log_info "K8S_PROVIDER is '${K8S_PROVIDER:-}', skipping Podman setup"
  exit 0
fi

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
    if systemctl --user enable --now podman.socket >/dev/null 2>&1; then
      :
    else
      log_warn "podman.socket unit not available or failed to start; attempting podman machine fallback"
      if podman machine --help >/dev/null 2>&1; then
        if ! podman machine list | grep -q "^default"; then
          log_info "Initializing podman machine"
          podman machine init --cpus 4 --memory 6144 --disk-size 40 || true
        fi
        log_info "Starting podman machine"
        podman machine start || true
      else
        log_warn "Podman machine not available. Ensure rootless deps (uidmap, slirp4netns) and containers config exist, or install containers-common."
      fi
    fi
  else
    log_warn "Podman or systemd not available; skipping socket setup"
  fi
fi

log_info "Podman setup complete"

#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

need_cmd helm

add_repo() {
  local name="$1" url="$2" required="${3:-true}"
  local tries=0 max_tries="${HELM_REPO_RETRIES:-3}"
  while true; do
    # --force-update makes reruns idempotent (updates URL if repo already exists).
    if helm repo add "$name" "$url" --force-update >/dev/null 2>&1; then
      return 0
    fi
    tries=$((tries + 1))
    if (( tries >= max_tries )); then
      if [[ "$required" == "true" ]]; then
        die "Failed to add Helm repo '${name}' (${url}) after ${max_tries} attempts"
      fi
      log_warn "Failed to add optional Helm repo '${name}' (${url}); continuing"
      return 0
    fi
    sleep 2
  done
}

# Current usage: pre-Flux Cilium bootstrap.
add_repo cilium https://helm.cilium.io/ true

helm repo update >/dev/null 2>&1 || true
log_info "Helm repositories added/updated (cilium)"

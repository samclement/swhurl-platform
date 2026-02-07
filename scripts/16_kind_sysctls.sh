#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done

# No-op on delete
[[ "$DELETE" == true ]] && exit 0

ensure_context

# Skip entirely unless provider is kind
if [[ "${K8S_PROVIDER:-kind}" != "kind" ]]; then
  log_info "Provider is '${K8S_PROVIDER:-}', skipping kind sysctl tuning"
  exit 0
fi

# Allow opting out entirely
if [[ "${KIND_TUNE_INOTIFY:-true}" != "true" ]]; then
  log_info "Skipping kind sysctl tuning (KIND_TUNE_INOTIFY=false)"
  exit 0
fi

# Determine container engine for kind nodes
ENGINE=""
if command -v podman >/dev/null 2>&1; then
  ENGINE=podman
elif command -v docker >/dev/null 2>&1; then
  ENGINE=docker
else
  log_warn "Neither podman nor docker found; skipping node sysctl tuning"
  exit 0
fi

# Rootless Podman cannot change kernel sysctls inside containers due to user namespaces.
# Detect and skip with guidance to avoid misleading failures or sudo requirements.
if [[ "$ENGINE" == "podman" ]]; then
  if podman info --format '{{.Host.Security.Rootless}}' 2>/dev/null | grep -qi true; then
    log_warn "Rootless Podman detected; skipping inotify tuning on kind nodes."
    log_warn "Use Docker or Podman machine (macOS) / rootful VM, or set KIND_TUNE_INOTIFY=false to silence this step."
    exit 0
  fi
fi

NODES=()
if command -v kind >/dev/null 2>&1; then
  if [[ -n "${CLUSTER_NAME:-}" ]]; then
    mapfile -t NODES < <(kind get nodes --name "${CLUSTER_NAME}" 2>/dev/null || kind get nodes)
  else
    mapfile -t NODES < <(kind get nodes)
  fi
fi

if [[ ${#NODES[@]} -eq 0 ]]; then
  log_warn "No kind nodes found; skipping sysctl tuning"
  exit 0
fi

log_info "Tuning inotify limits inside kind nodes: ${NODES[*]}"
for n in "${NODES[@]}"; do
  # Try sysctl, fallback to writing /proc directly.
  $ENGINE exec "$n" sh -lc '
    set -e
    val_w=1048576
    val_i=1024
    if command -v sysctl >/dev/null 2>&1; then
      sysctl -w fs.inotify.max_user_watches=$val_w >/dev/null 2>&1 || true
      sysctl -w fs.inotify.max_user_instances=$val_i >/dev/null 2>&1 || true
    fi
    echo $val_w > /proc/sys/fs/inotify/max_user_watches 2>/dev/null || true
    echo $val_i > /proc/sys/fs/inotify/max_user_instances 2>/dev/null || true
    printf "node=%s max_user_watches=$(cat /proc/sys/fs/inotify/max_user_watches 2>/dev/null) max_user_instances=$(cat /proc/sys/fs/inotify/max_user_instances 2>/dev/null)\n" "$(hostname)"
  ' || log_warn "Failed to tune inotify on node $n"
done

# Nudge Prometheus pod if present to re-init quickly
PROM_POD=$(kubectl -n observability get pod -l app.kubernetes.io/name=prometheus -o name 2>/dev/null | head -n1 || true)
if [[ -n "$PROM_POD" ]]; then
  log_info "Restarting $PROM_POD to pick up new limits"
  kubectl -n observability delete "$PROM_POD" --ignore-not-found || true
fi

log_info "Kind node sysctl tuning complete"

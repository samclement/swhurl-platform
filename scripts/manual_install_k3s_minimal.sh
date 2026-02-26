#!/usr/bin/env bash
set -Eeuo pipefail

# Legacy compatibility entrypoint.
# Host-layer k3s lifecycle now lives under host/tasks and is orchestrated by host/run-host.sh.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOST_RUNNER="$ROOT_DIR/host/run-host.sh"

if [[ ! -x "$HOST_RUNNER" ]]; then
  echo "[ERROR] Host runner not found/executable: $HOST_RUNNER" >&2
  exit 1
fi

exec "$HOST_RUNNER" --only 20_install_k3s.sh "$@"

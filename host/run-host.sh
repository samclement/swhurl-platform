#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOST_DIR="$ROOT_DIR/host"

DELETE_MODE=false
DRY_RUN=false
ONLY_FILTER=""
HOST_ENV_FILE=""

usage() {
  cat <<USAGE
Usage: ./host/run-host.sh [--host-env FILE] [--only N[,N...]] [--dry-run] [--delete]

Options:
  --host-env FILE  Load host-specific env overrides (highest precedence)
  --only LIST      Comma-separated task numbers or basenames to run
  --dry-run        Print host plan without executing
  --delete         Run host delete plan (reverse order)
USAGE
}

task_path() { printf "%s/tasks/%s\n" "$HOST_DIR" "$1"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --delete) DELETE_MODE=true; shift ;;
    --dry-run) DRY_RUN=true; shift ;;
    --only) ONLY_FILTER="$2"; shift 2 ;;
    --host-env) HOST_ENV_FILE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; usage; exit 1 ;;
  esac
done

set -a
[[ -f "$ROOT_DIR/config.env" ]] && source "$ROOT_DIR/config.env"
[[ -f "$HOST_DIR/config/homelab.env" ]] && source "$HOST_DIR/config/homelab.env"
[[ -f "$HOST_DIR/config/host.env" ]] && source "$HOST_DIR/config/host.env"
if [[ -n "$HOST_ENV_FILE" ]]; then
  [[ -f "$HOST_ENV_FILE" ]] || { echo "Missing --host-env file: $HOST_ENV_FILE" >&2; exit 1; }
  source "$HOST_ENV_FILE"
fi
set +a

export HOST_REPO_ROOT="$ROOT_DIR"

APPLY_STEPS=(
  "$(task_path 00_bootstrap_host.sh)"
  "$(task_path 10_dynamic_dns.sh)"
  "$(task_path 20_install_k3s.sh)"
)
DELETE_STEPS=(
  "$(task_path 20_install_k3s.sh)"
  "$(task_path 10_dynamic_dns.sh)"
)

if [[ "$DELETE_MODE" == true ]]; then
  ALL_STEPS=("${DELETE_STEPS[@]}")
else
  ALL_STEPS=("${APPLY_STEPS[@]}")
fi

SELECTED_STEPS=()
if [[ -z "$ONLY_FILTER" ]]; then
  SELECTED_STEPS=("${ALL_STEPS[@]}")
else
  IFS=',' read -r -a FILTER_TOKENS <<< "$ONLY_FILTER"
  for step in "${ALL_STEPS[@]}"; do
    base="$(basename "$step")"
    num="${base%%_*}"
    for token in "${FILTER_TOKENS[@]}"; do
      if [[ "$token" == "$num" || "$token" == "$base" ]]; then
        SELECTED_STEPS+=("$step")
        break
      fi
    done
  done
fi

echo "Host Plan:"
if [[ "$DELETE_MODE" == true ]]; then
  echo "  - delete (reverse order)"
else
  echo "  - apply"
fi
for step in "${SELECTED_STEPS[@]}"; do
  base="$(basename "$step")"
  if [[ "$DELETE_MODE" == true ]]; then
    echo "  - ${base} (delete)"
  else
    echo "  - ${base}"
  fi
done

if [[ "$DRY_RUN" == true ]]; then
  echo "Host dry run: exiting without executing."
  exit 0
fi

for step in "${SELECTED_STEPS[@]}"; do
  base="$(basename "$step")"
  if [[ ! -x "$step" ]]; then
    echo "[host][skip] $base (not executable or missing)"
    continue
  fi

  if [[ "$DELETE_MODE" == true ]]; then
    echo "[host][run] $base --delete"
    "$step" --delete
  else
    echo "[host][run] $base"
    "$step"
  fi
done

echo "Host run complete."

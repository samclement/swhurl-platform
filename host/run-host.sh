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

task_path() { printf "%s/tasks/%s\n" "$HOST_DIR" "$1"; }

filter_steps() {
  local -n in_arr=$1 out_arr=$2
  local only="$3"
  if [[ -z "$only" ]]; then
    out_arr=("${in_arr[@]}")
    return
  fi
  IFS=',' read -r -a tokens <<< "$only"
  out_arr=()
  local s base num t
  for s in "${in_arr[@]}"; do
    base="$(basename "$s")"
    num="${base%%_*}"
    for t in "${tokens[@]}"; do
      if [[ "$t" == "$num" || "$t" == "$base" ]]; then
        out_arr+=("$s")
        break
      fi
    done
  done
}

build_apply_plan() {
  local -n out=$1
  out=(
    "$(task_path 00_bootstrap_host.sh)"
    "$(task_path 10_dynamic_dns.sh)"
    "$(task_path 20_install_k3s.sh)"
  )
}

build_delete_plan() {
  local -n out=$1
  out=(
    "$(task_path 20_install_k3s.sh)"
    "$(task_path 10_dynamic_dns.sh)"
  )
}

print_plan() {
  local -n steps=$1
  echo "Host Plan:"
  if [[ "$DELETE_MODE" == true ]]; then
    echo "  - delete (reverse order)"
  else
    echo "  - apply"
  fi
  local s base
  for s in "${steps[@]}"; do
    base="$(basename "$s")"
    if [[ "$DELETE_MODE" == true ]]; then
      echo "  - ${base} (delete)"
    else
      echo "  - ${base}"
    fi
  done
}

run_step() {
  local s="$1"
  local base="$(basename "$s")"
  if [[ ! -x "$s" ]]; then
    echo "[host][skip] $base (not executable or missing)"
    return 0
  fi

  if [[ "$DELETE_MODE" == true ]]; then
    echo "[host][run] $base --delete"
    "$s" --delete
  else
    echo "[host][run] $base"
    "$s"
  fi
}

if [[ "$DELETE_MODE" == true ]]; then
  build_delete_plan ALL_STEPS
else
  build_apply_plan ALL_STEPS
fi

filter_steps ALL_STEPS SELECTED_STEPS "$ONLY_FILTER"
print_plan SELECTED_STEPS

if [[ "$DRY_RUN" == true ]]; then
  echo "Host dry run: exiting without executing."
  exit 0
fi

for s in "${SELECTED_STEPS[@]}"; do
  run_step "$s"
done

echo "Host run complete."

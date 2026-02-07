#!/usr/bin/env bash
set -Eeuo pipefail

# Orchestrator for local platform setup scripts.
# - Loads config/env and optional profile
# - Supports ONLY filter (comma-separated step numbers or filenames)
# - Supports --delete to run teardown in reverse
# - Skips missing scripts gracefully

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

DELETE_MODE=false
DRY_RUN=false
ONLY_FILTER=""
PROFILE_FILE=""

usage() {
  cat <<USAGE
Usage: ./run.sh [--profile FILE] [--only N[,N...]] [--dry-run] [--delete]

Options:
  --profile FILE  Load additional env vars (overrides config.env)
  --only LIST     Comma-separated step numbers or script basenames to run
  --dry-run       Print plan without executing
  --delete        Run teardown (reverse order), passing --delete to steps

Env:
  ONLY            Same as --only (overridden by CLI flag)
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --delete) DELETE_MODE=true; shift ;;
    --dry-run) DRY_RUN=true; shift ;;
    --only) ONLY_FILTER="$2"; shift 2 ;;
    --profile) PROFILE_FILE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; usage; exit 1 ;;
  esac
done

# Load base config
if [[ -f "config.env" ]]; then
  # shellcheck disable=SC1091
  source "config.env"
fi

# Load profile if provided and export the path so child scripts can re-source
if [[ -n "$PROFILE_FILE" ]]; then
  export PROFILE_FILE
  # shellcheck disable=SC1090
  source "$PROFILE_FILE"
fi

# Allow ONLY from env if not given via flag
ONLY_FILTER="${ONLY_FILTER:-${ONLY:-}}"

# Collect steps from scripts directory
mapfile -t ALL_STEPS < <(ls -1 scripts/[0-9][0-9]_*.sh 2>/dev/null | sort)

if [[ ${#ALL_STEPS[@]} -eq 0 ]]; then
  echo "No scripts found under scripts/. Nothing to do."; exit 0
fi

filter_steps() {
  local -n in_arr=$1 out_arr=$2
  local only="$3"
  if [[ -z "$only" ]]; then
    out_arr=("${in_arr[@]}")
    return
  fi
  IFS=',' read -r -a tokens <<< "$only"
  out_arr=()
  for s in "${in_arr[@]}"; do
    base=$(basename "$s")
    num=${base%%_*}
    for t in "${tokens[@]}"; do
      if [[ "$t" == "$num" || "$t" == "$base" ]]; then
        out_arr+=("$s")
        break
      fi
    done
  done
}

filter_steps ALL_STEPS SELECTED_STEPS "$ONLY_FILTER"

# Reverse for delete mode
if [[ "$DELETE_MODE" == true ]]; then
  mapfile -t SELECTED_STEPS < <(printf '%s\n' "${SELECTED_STEPS[@]}" | tac)
fi

should_skip() {
  local step="$1"
  case "$(basename "$step")" in
    00_lib.sh)
      # helper library, never executed as a step
      return 0 ;;
    76_app_scaffold.sh)
      # scaffolder; run directly with args when needed
      return 0 ;;
    12_dns_register.sh)
      [[ "${FEAT_DNS_REGISTER:-true}" == "true" || "$DELETE_MODE" == true ]] || return 0 ;;
    45_oauth2_proxy.sh)
      [[ "${FEAT_OAUTH2_PROXY:-true}" == "true" || "$DELETE_MODE" == true ]] || return 0 ;;
    50_logging_fluentbit.sh)
      [[ "${FEAT_LOGGING:-true}" == "true" || "$DELETE_MODE" == true ]] || return 0 ;;
    55_loki.sh)
      [[ "${FEAT_LOKI:-true}" == "true" || "$DELETE_MODE" == true ]] || return 0 ;;
    60_prom_grafana.sh)
      [[ "${FEAT_OBS:-true}" == "true" || "$DELETE_MODE" == true ]] || return 0 ;;
    70_minio.sh)
      [[ "${FEAT_MINIO:-true}" == "true" || "$DELETE_MODE" == true ]] || return 0 ;;
    80_mesh_linkerd.sh)
      [[ "${FEAT_MESH_LINKERD:-false}" == "true" || "$DELETE_MODE" == true ]] || return 0 ;;
    81_mesh_istio.sh)
      [[ "${FEAT_MESH_ISTIO:-false}" == "true" || "$DELETE_MODE" == true ]] || return 0 ;;
  esac
  return 1
}

echo "Plan:"
for s in "${SELECTED_STEPS[@]}"; do
  printf "  - %s %s\n" "$(basename "$s")" "$([[ "$DELETE_MODE" == true ]] && echo "(delete)" || echo "")"
done

if [[ "$DRY_RUN" == true ]]; then
  echo "Dry run: exiting without executing."
  exit 0
fi

for s in "${SELECTED_STEPS[@]}"; do
  base=$(basename "$s")
  if should_skip "$s"; then
    echo "[skip] $base (feature disabled)"
    continue
  fi
  if [[ ! -x "$s" ]]; then
    echo "[skip] $base (not executable or missing)"
    continue
  fi
  if [[ "$DELETE_MODE" == true ]]; then
    echo "[run] $base --delete"
    "$s" --delete
  else
    echo "[run] $base"
    "$s"
  fi
done

echo "Done."

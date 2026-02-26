#!/usr/bin/env bash
set -Eeuo pipefail

# Phase-based orchestrator for platform scripts.
# - Explicit ordering and dependencies (no implicit script discovery)
# - --delete runs reverse phases with deterministic finalizers
# - --only can filter by step number (NN) or script basename

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

DELETE_MODE=false
DRY_RUN=false
ONLY_FILTER=""
PROFILE_FILE=""
WITH_HOST_LAYER="${RUN_HOST_LAYER:-false}"
HOST_ENV_FILE=""

usage() {
  cat <<USAGE
Usage: ./run.sh [--profile FILE] [--host-env FILE] [--only N[,N...]] [--dry-run] [--delete] [--with-host]

Options:
  --profile FILE  Load additional env vars (overrides config.env)
  --host-env FILE Load host-layer env vars for ./host/run-host.sh
  --only LIST     Comma-separated step numbers or script basenames to run
  --dry-run       Print plan without executing
  --delete        Run teardown (reverse order), passing --delete to steps
  --with-host     Include host layer orchestration via ./host/run-host.sh

Env:
  ONLY                 Same as --only (overridden by CLI flag)
  RUN_HOST_LAYER       If true, include host layer orchestration (default false)
  FEAT_VERIFY           If false, skip verification scripts in apply runs (default true)
  FEAT_VERIFY_DEEP      If true, run extra/diagnostic verification scripts (default false)

Manual prereqs:
  DNS registration is not part of the orchestrator plan. If you use .swhurl.com
  domains and want automatic Route53 updates, run: ./scripts/manual_configure_route53_dns_updater.sh
  Host bootstrap (k3s install) is also manual. See README.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --delete) DELETE_MODE=true; shift ;;
    --dry-run) DRY_RUN=true; shift ;;
    --only) ONLY_FILTER="$2"; shift 2 ;;
    --profile) PROFILE_FILE="$2"; shift 2 ;;
    --host-env) HOST_ENV_FILE="$2"; shift 2 ;;
    --with-host) WITH_HOST_LAYER=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown arg: $1" >&2; usage; exit 1 ;;
  esac
done

# Load env with the same layering semantics as scripts/00_lib.sh, so the plan
# is computed from the same effective configuration the scripts will use:
#   config.env -> profiles/local.env -> profiles/secrets.env -> --profile (highest precedence)
#
# Opt out (standalone profile): PROFILE_EXCLUSIVE=true uses only config.env -> --profile
PROFILE_EXCLUSIVE="${PROFILE_EXCLUSIVE:-false}"
if [[ "$PROFILE_EXCLUSIVE" != "true" && "$PROFILE_EXCLUSIVE" != "false" ]]; then
  echo "[ERROR] PROFILE_EXCLUSIVE must be true or false (got: $PROFILE_EXCLUSIVE)" >&2
  exit 1
fi
if [[ "$WITH_HOST_LAYER" != "true" && "$WITH_HOST_LAYER" != "false" ]]; then
  echo "[ERROR] RUN_HOST_LAYER/--with-host must resolve to true or false (got: $WITH_HOST_LAYER)" >&2
  exit 1
fi

set -a
if [[ -f "config.env" ]]; then
  # shellcheck disable=SC1091
  source "config.env"
fi
if [[ "$PROFILE_EXCLUSIVE" == "false" && -f "profiles/local.env" ]]; then
  # shellcheck disable=SC1091
  source "profiles/local.env"
fi
if [[ "$PROFILE_EXCLUSIVE" == "false" && -f "profiles/secrets.env" ]]; then
  # shellcheck disable=SC1091
  source "profiles/secrets.env"
fi
if [[ -n "$PROFILE_FILE" ]]; then
  export PROFILE_FILE
  # shellcheck disable=SC1090
  source "$PROFILE_FILE"
fi
set +a

# Allow ONLY from env if not given via flag.
ONLY_FILTER="${ONLY_FILTER:-${ONLY:-}}"

step_path() { printf "scripts/%s\n" "$1"; }

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
    local base num t
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

add_step() {
  local -n _arr="$1"
  local s="$2"
  [[ -f "$s" ]] || return 0
  _arr+=("$s")
}

add_step_if() {
  local arr_name="$1"
  local cond="$2" s="$3"
  [[ "$cond" == "true" ]] || return 0
  add_step "$arr_name" "$s"
}

FEAT_VERIFY="${FEAT_VERIFY:-true}"
FEAT_VERIFY_DEEP="${FEAT_VERIFY_DEEP:-false}"
if [[ "$FEAT_VERIFY" != "true" && "$FEAT_VERIFY" != "false" ]]; then
  echo "[ERROR] FEAT_VERIFY must be true or false (got: $FEAT_VERIFY)" >&2
  exit 1
fi
if [[ "$FEAT_VERIFY_DEEP" != "true" && "$FEAT_VERIFY_DEEP" != "false" ]]; then
  echo "[ERROR] FEAT_VERIFY_DEEP must be true or false (got: $FEAT_VERIFY_DEEP)" >&2
  exit 1
fi
if [[ "$FEAT_VERIFY" != "true" ]]; then
  FEAT_VERIFY_DEEP="false"
fi

build_apply_plan() {
  local -n out_arr=$1
  out_arr=()

  # 1) Prerequisites
  add_step out_arr "$(step_path 01_check_prereqs.sh)"

  # 2) Cluster Access (kubeconfig)
  add_step out_arr "$(step_path 15_verify_cluster_access.sh)"

  # 3) Environment & Config Contract
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 94_verify_config_inputs.sh)"

  # 4) Cluster Dependencies
  add_step out_arr "$(step_path 25_prepare_helm_repositories.sh)"
  add_step out_arr "$(step_path 20_reconcile_platform_namespaces.sh)"
  add_step_if out_arr "${FEAT_CILIUM:-true}" "$(step_path 26_manage_cilium_lifecycle.sh)"

  # 5) Platform Services
  add_step out_arr "$(step_path 31_sync_helmfile_phase_core.sh)"
  add_step out_arr "$(step_path 29_prepare_platform_runtime_inputs.sh)"
  add_step out_arr "$(step_path 36_sync_helmfile_phase_platform.sh)"
  # Service mesh scripts removed (Linkerd/Istio) to keep platform focused and reduce surface area.

  # 6) Test Application
  add_step out_arr "$(step_path 75_manage_sample_app_lifecycle.sh)"

  # 7) Verification
  # Core verification gates (default)
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 91_verify_platform_state.sh)"
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 92_verify_helmfile_drift.sh)"
  # Extra verification/diagnostics (opt-in)
  add_step_if out_arr "$FEAT_VERIFY_DEEP" "$(step_path 90_verify_runtime_smoke.sh)"
  add_step_if out_arr "$FEAT_VERIFY_DEEP" "$(step_path 93_verify_expected_releases.sh)"
  add_step_if out_arr "$FEAT_VERIFY_DEEP" "$(step_path 95_capture_cluster_diagnostics.sh)"
  add_step_if out_arr "$FEAT_VERIFY_DEEP" "$(step_path 96_verify_orchestrator_contract.sh)"
  add_step_if out_arr "$FEAT_VERIFY_DEEP" "$(step_path 97_verify_provider_matrix.sh)"
}

build_delete_plan() {
  local -n out_arr=$1
  out_arr=()

  # Preflight: require kube context (do not pass --delete).
  add_step out_arr "$(step_path 15_verify_cluster_access.sh)"

  # Reverse platform components (apps -> services -> finalizers).
  add_step out_arr "$(step_path 75_manage_sample_app_lifecycle.sh)"

  add_step out_arr "$(step_path 36_sync_helmfile_phase_platform.sh)"
  add_step out_arr "$(step_path 29_prepare_platform_runtime_inputs.sh)"
  add_step out_arr "$(step_path 31_sync_helmfile_phase_core.sh)"
  add_step out_arr "$(step_path 30_manage_cert_manager_cleanup.sh)"
  # Service mesh scripts removed (Linkerd/Istio) to keep platform focused and reduce surface area.

  # Remove the namespaces Helm release record (namespaces themselves are deleted in 99_execute_teardown.sh).
  add_step out_arr "$(step_path 20_reconcile_platform_namespaces.sh)"

  # Deterministic finalizers: teardown -> cilium -> verify.
  add_step out_arr "$(step_path 99_execute_teardown.sh)"
  add_step_if out_arr "${FEAT_CILIUM:-true}" "$(step_path 26_manage_cilium_lifecycle.sh)"
  add_step out_arr "$(step_path 98_verify_teardown_clean.sh)"
}

print_plan() {
  local -n steps=$1
  echo "Plan:"

  if [[ "$WITH_HOST_LAYER" == "true" ]]; then
    if [[ "$DELETE_MODE" == "true" ]]; then
      echo "  - Host layer enabled (runs after cluster delete steps)"
    else
      echo "  - Host layer enabled (runs before cluster apply steps)"
    fi
  fi

  # Phase headings are informational only; script order is the source of truth.
  if [[ "$DELETE_MODE" != true ]]; then
    echo "  - 1) Prerequisites & verify"
    echo "  - 2) Basic Kubernetes Cluster (kubeconfig)"
    echo "  - 3) Environment (profiles/secrets) & verification"
    echo "  - 4) Cluster deps (helm/cilium) & verification"
    echo "  - 5) Platform services & verification"
    echo "  - 6) Test application & verification"
    echo "  - 7) Cluster verification suite"
  else
    echo "  - Delete (reverse phases; cilium last)"
  fi

  for s in "${steps[@]}"; do
    local base
    base=$(basename "$s")
    if [[ "$DELETE_MODE" == true && "$base" != "15_verify_cluster_access.sh" ]]; then
      printf "  - %s (delete)\n" "$base"
    else
      printf "  - %s\n" "$base"
    fi
  done
}

run_host_layer() {
  [[ "$WITH_HOST_LAYER" == "true" ]] || return 0
  local host_runner="$ROOT_DIR/host/run-host.sh"
  [[ -x "$host_runner" ]] || { echo "[ERROR] Host layer requested but runner not found/executable: $host_runner" >&2; exit 1; }

  local -a host_args=()
  if [[ "$DELETE_MODE" == "true" ]]; then
    host_args+=(--delete)
  fi
  if [[ "$DRY_RUN" == "true" ]]; then
    host_args+=(--dry-run)
  fi
  if [[ -n "$HOST_ENV_FILE" ]]; then
    host_args+=(--host-env "$HOST_ENV_FILE")
  fi

  echo "[run] host/run-host.sh ${host_args[*]}"
  "$host_runner" "${host_args[@]}"
}

run_step() {
  local s="$1"
  local base
  base=$(basename "$s")

  if [[ ! -x "$s" ]]; then
    echo "[skip] $base (not executable or missing)"
    return 0
  fi

  # Feature gates for direct --only execution and readability.
  case "$base" in
    26_manage_cilium_lifecycle.sh)
      [[ "${FEAT_CILIUM:-true}" == "true" || "$DELETE_MODE" == true ]] || { echo "[skip] $base (FEAT_CILIUM=false)"; return 0; } ;;
    29_prepare_platform_runtime_inputs.sh)
      # Contains internal feature gates for individual resources.
      ;;
    31_sync_helmfile_phase_core.sh|36_sync_helmfile_phase_platform.sh)
      ;;
    91_verify_platform_state.sh|92_verify_helmfile_drift.sh|94_verify_config_inputs.sh)
      [[ "$DELETE_MODE" == false && "$FEAT_VERIFY" == "true" ]] || { echo "[skip] $base (FEAT_VERIFY=false or delete mode)"; return 0; } ;;
    90_verify_runtime_smoke.sh|93_verify_expected_releases.sh|95_capture_cluster_diagnostics.sh|96_verify_orchestrator_contract.sh)
      [[ "$DELETE_MODE" == false && "$FEAT_VERIFY_DEEP" == "true" ]] || { echo "[skip] $base (FEAT_VERIFY_DEEP=false or delete mode)"; return 0; } ;;
    98_verify_teardown_clean.sh|99_execute_teardown.sh)
      [[ "$DELETE_MODE" == true ]] || { echo "[skip] $base (delete-only)"; return 0; } ;;
  esac

  if [[ "$DELETE_MODE" == true ]]; then
    if [[ "$base" == "15_verify_cluster_access.sh" ]]; then
      echo "[run] $base"
      "$s"
    else
      echo "[run] $base --delete"
      "$s" --delete
    fi
  else
    echo "[run] $base"
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
  run_host_layer
  echo "Dry run: exiting without executing."
  exit 0
fi

if [[ "$WITH_HOST_LAYER" == "true" && "$DELETE_MODE" != "true" ]]; then
  run_host_layer
fi

for s in "${SELECTED_STEPS[@]}"; do
  run_step "$s"
done

if [[ "$WITH_HOST_LAYER" == "true" && "$DELETE_MODE" == "true" ]]; then
  run_host_layer
fi

echo "Done."

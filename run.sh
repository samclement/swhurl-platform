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

usage() {
  cat <<USAGE
Usage: ./run.sh [--profile FILE] [--only N[,N...]] [--dry-run] [--delete]

Options:
  --profile FILE  Load additional env vars (overrides config.env)
  --only LIST     Comma-separated step numbers or script basenames to run
  --dry-run       Print plan without executing
  --delete        Run teardown (reverse order), passing --delete to steps

Env:
  ONLY                 Same as --only (overridden by CLI flag)
  FEAT_BOOTSTRAP_K3S    If true, include scripts/10 + scripts/11 in apply runs (default false)
  FEAT_VERIFY           If false, skip verification scripts in apply runs (default true)
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

FEAT_BOOTSTRAP_K3S="${FEAT_BOOTSTRAP_K3S:-false}"
FEAT_VERIFY="${FEAT_VERIFY:-true}"

build_apply_plan() {
  local -n out_arr=$1
  out_arr=()

  # 1) Prerequisites
  add_step out_arr "$(step_path 01_check_prereqs.sh)"

  # 2) DNS & Network Reachability
  add_step_if out_arr "${FEAT_DNS_REGISTER:-true}" "$(step_path 12_dns_register.sh)"

  # 3) Cluster Access (kubeconfig)
  add_step out_arr "$(step_path 15_kube_context.sh)"
  add_step_if out_arr "$FEAT_BOOTSTRAP_K3S" "$(step_path 10_install_k3s_cilium_minimal.sh)"
  add_step_if out_arr "$FEAT_BOOTSTRAP_K3S" "$(step_path 11_cluster_k3s.sh)"

  # 4) Environment & Config Contract
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 94_verify_config_contract.sh)"

  # 5) Cluster Dependencies
  add_step out_arr "$(step_path 25_helm_repos.sh)"
  add_step out_arr "$(step_path 20_namespaces.sh)"
  add_step_if out_arr "${FEAT_CILIUM:-true}" "$(step_path 26_cilium.sh)"

  # 6) Platform Services
  add_step out_arr "$(step_path 31_helmfile_core.sh)"
  add_step out_arr "$(step_path 35_issuer.sh)"
  add_step out_arr "$(step_path 29_platform_config.sh)"
  add_step out_arr "$(step_path 36_helmfile_platform.sh)"
  # Service mesh scripts removed (Linkerd/Istio) to keep platform focused and reduce surface area.

  # 7) Test Application
  add_step out_arr "$(step_path 75_sample_app.sh)"

  # 8) Verification
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 90_smoke_tests.sh)"
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 91_validate_cluster.sh)"
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 92_verify_helmfile_diff.sh)"
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 93_verify_release_inventory.sh)"
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 95_dump_context.sh)"
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 95_verify_kustomize_builds.sh)"
  add_step_if out_arr "$FEAT_VERIFY" "$(step_path 96_verify_script_surface.sh)"
}

build_delete_plan() {
  local -n out_arr=$1
  out_arr=()

  # Preflight: require kube context (do not pass --delete).
  add_step out_arr "$(step_path 15_kube_context.sh)"

  # Reverse platform components (apps -> services -> finalizers).
  add_step out_arr "$(step_path 75_sample_app.sh)"

  add_step out_arr "$(step_path 36_helmfile_platform.sh)"
  add_step out_arr "$(step_path 29_platform_config.sh)"
  add_step out_arr "$(step_path 35_issuer.sh)"
  add_step out_arr "$(step_path 31_helmfile_core.sh)"
  add_step out_arr "$(step_path 30_cert_manager.sh)"
  # Service mesh scripts removed (Linkerd/Istio) to keep platform focused and reduce surface area.

  add_step_if out_arr "${FEAT_DNS_REGISTER:-true}" "$(step_path 12_dns_register.sh)"

  # Deterministic finalizers: teardown -> cilium -> verify.
  add_step out_arr "$(step_path 99_teardown.sh)"
  add_step_if out_arr "${FEAT_CILIUM:-true}" "$(step_path 26_cilium.sh)"
  add_step out_arr "$(step_path 98_verify_delete_clean.sh)"
}

print_plan() {
  local -n steps=$1
  echo "Plan:"

  # Phase headings are informational only; script order is the source of truth.
  if [[ "$DELETE_MODE" != true ]]; then
    echo "  - 1) Prerequisites & verify"
    echo "  - 2) DNS & Network Reachability"
    echo "  - 3) Basic Kubernetes Cluster (kubeconfig)"
    echo "  - 4) Environment (profiles/secrets) & verification"
    echo "  - 5) Cluster deps (helm/cilium) & verification"
    echo "  - 6) Platform services & verification"
    echo "  - 7) Test application & verification"
    echo "  - 8) Cluster verification suite"
  else
    echo "  - Delete (reverse phases; cilium last)"
  fi

  for s in "${steps[@]}"; do
    local base
    base=$(basename "$s")
    if [[ "$DELETE_MODE" == true && "$base" != "15_kube_context.sh" ]]; then
      printf "  - %s (delete)\n" "$base"
    else
      printf "  - %s\n" "$base"
    fi
  done
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
    10_install_k3s_cilium_minimal.sh|11_cluster_k3s.sh)
      [[ "$FEAT_BOOTSTRAP_K3S" == "true" ]] || { echo "[skip] $base (FEAT_BOOTSTRAP_K3S=false)"; return 0; } ;;
    12_dns_register.sh)
      [[ "${FEAT_DNS_REGISTER:-true}" == "true" || "$DELETE_MODE" == true ]] || { echo "[skip] $base (FEAT_DNS_REGISTER=false)"; return 0; } ;;
    26_cilium.sh)
      [[ "${FEAT_CILIUM:-true}" == "true" || "$DELETE_MODE" == true ]] || { echo "[skip] $base (FEAT_CILIUM=false)"; return 0; } ;;
    29_platform_config.sh)
      # Contains internal feature gates for individual resources.
      ;;
    31_helmfile_core.sh|36_helmfile_platform.sh)
      ;;
    90_smoke_tests.sh|91_validate_cluster.sh|92_verify_helmfile_diff.sh|93_verify_release_inventory.sh|94_verify_config_contract.sh|95_dump_context.sh|95_verify_kustomize_builds.sh|96_verify_script_surface.sh)
      [[ "$DELETE_MODE" == false && "$FEAT_VERIFY" == "true" ]] || { echo "[skip] $base (FEAT_VERIFY=false or delete mode)"; return 0; } ;;
    98_verify_delete_clean.sh|99_teardown.sh)
      [[ "$DELETE_MODE" == true ]] || { echo "[skip] $base (delete-only)"; return 0; } ;;
  esac

  if [[ "$DELETE_MODE" == true ]]; then
    if [[ "$base" == "15_kube_context.sh" ]]; then
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
  echo "Dry run: exiting without executing."
  exit 0
fi

for s in "${SELECTED_STEPS[@]}"; do
  run_step "$s"
done

echo "Done."

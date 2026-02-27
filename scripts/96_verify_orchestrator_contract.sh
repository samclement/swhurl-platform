#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$SCRIPT_DIR/00_lib.sh"

DELETE=false
for arg in "$@"; do [[ "$arg" == "--delete" ]] && DELETE=true; done
if [[ "$DELETE" == true ]]; then
  log_info "Orchestrator contract check is apply-only; skipping in delete mode"
  exit 0
fi

ok(){ printf "[OK] %s\n" "$1"; }
bad(){ printf "[BAD] %s\n" "$1"; fail=1; }
warn(){ printf "[WARN] %s\n" "$1"; }

fail=0
printf "== Orchestrator Contract Verification ==\n"

run="$SCRIPT_DIR/../run.sh"
root="$SCRIPT_DIR/.."
config_file="$root/config.env"
helmfile_file="$root/helmfile.yaml.gotmpl"
flux_stack_file="$root/cluster/overlays/homelab/flux/stack-kustomizations.yaml"

# Verify the supported default apply pipeline uses the Helmfile phase scripts.
for s in 31_sync_helmfile_phase_core.sh 36_sync_helmfile_phase_platform.sh; do
  p="$SCRIPT_DIR/$s"
  if [[ -f "$p" ]]; then
    ok "$s: present"
  else
    bad "$s: present"
  fi
done

if rg -q '31_sync_helmfile_phase_core\.sh' "$run" && rg -q '36_sync_helmfile_phase_platform\.sh' "$run"; then
  ok "run.sh: apply phase scripts wired into plan"
else
  bad "run.sh: apply phase scripts wired into plan"
fi

if [[ -f "$SCRIPT_DIR/29_prepare_platform_runtime_inputs.sh" ]]; then
  bad "29_prepare_platform_runtime_inputs.sh: removed (legacy bridge retired)"
else
  ok "29_prepare_platform_runtime_inputs.sh: removed (legacy bridge retired)"
fi

if rg -n 'build_apply_plan' -A50 "$run" | rg -q '29_prepare_platform_runtime_inputs\.sh'; then
  bad "run.sh: apply plan excludes 29_prepare_platform_runtime_inputs.sh"
else
  ok "run.sh: apply plan excludes 29_prepare_platform_runtime_inputs.sh"
fi

if rg -n 'build_delete_plan' -A50 "$run" | rg -q '29_prepare_platform_runtime_inputs\.sh'; then
  bad "run.sh: delete plan excludes 29_prepare_platform_runtime_inputs.sh"
else
  ok "run.sh: delete plan excludes 29_prepare_platform_runtime_inputs.sh"
fi

if [[ -d "$root/cluster/base/runtime-inputs" ]]; then
  ok "cluster/base/runtime-inputs: present"
else
  bad "cluster/base/runtime-inputs: present"
fi

runtime_inputs_kustomization_file="$root/cluster/base/runtime-inputs/kustomization.yaml"
runtime_inputs_flux_block="$(rg -n 'name: homelab-runtime-inputs' -A25 "$flux_stack_file" || true)"
if [[ -n "$runtime_inputs_flux_block" ]] \
  && printf '%s\n' "$runtime_inputs_flux_block" | rg -q 'path: ./cluster/base/runtime-inputs' \
  && printf '%s\n' "$runtime_inputs_flux_block" | rg -q 'postBuild:' \
  && printf '%s\n' "$runtime_inputs_flux_block" | rg -q 'substituteFrom:' \
  && printf '%s\n' "$runtime_inputs_flux_block" | rg -q 'name: platform-runtime-inputs' \
  && ! rg -q 'secret-platform-runtime-inputs\.yaml' "$runtime_inputs_kustomization_file" \
  && ! rg -q '^replacements:' "$runtime_inputs_kustomization_file"; then
  ok "Flux stack: runtime inputs use postBuild substitution from external platform-runtime-inputs secret"
else
  bad "Flux stack: runtime inputs use postBuild substitution from external platform-runtime-inputs secret"
fi

if [[ -x "$SCRIPT_DIR/bootstrap/sync-runtime-inputs.sh" ]] \
  && rg -q '^runtime-inputs-sync:' "$root/Makefile" \
  && rg -q 'sync-runtime-inputs\.sh' "$root/Makefile"; then
  ok "Runtime input sync command is available (scripts/bootstrap/sync-runtime-inputs.sh + Makefile target)"
else
  bad "Runtime input sync command is available (scripts/bootstrap/sync-runtime-inputs.sh + Makefile target)"
fi

if ! rg -q 'name: homelab-clickstack-bootstrap' "$flux_stack_file" \
  && rg -n 'name: homelab-otel' -A12 "$flux_stack_file" | rg -q 'name: homelab-clickstack'; then
  ok "Flux stack: clickstack bootstrap kustomization removed; otel depends directly on clickstack"
else
  bad "Flux stack: clickstack bootstrap kustomization removed; otel depends directly on clickstack"
fi

if rg -q 'helmfile_cmd -l phase=core (sync|destroy)' "$SCRIPT_DIR/31_sync_helmfile_phase_core.sh"; then
  ok "31_sync_helmfile_phase_core.sh: uses helmfile_cmd with phase=core label selection"
else
  bad "31_sync_helmfile_phase_core.sh: uses helmfile_cmd with phase=core label selection"
fi
if rg -q 'helmfile_cmd -l phase=platform (sync|destroy)' "$SCRIPT_DIR/36_sync_helmfile_phase_platform.sh"; then
  ok "36_sync_helmfile_phase_platform.sh: uses helmfile_cmd with phase=platform label selection"
else
  bad "36_sync_helmfile_phase_platform.sh: uses helmfile_cmd with phase=platform label selection"
fi

# Release-specific scripts should keep using shared Helmfile helpers if/when they exist.
for s in 26_manage_cilium_lifecycle.sh 30_manage_cert_manager_cleanup.sh; do
  p="$SCRIPT_DIR/$s"
  if rg -q 'sync_release ' "$p"; then
    ok "$s: sync_release path present"
  else
    bad "$s: sync_release path present"
  fi
  if rg -q 'destroy_release ' "$p"; then
    ok "$s: destroy_release path present"
  else
    bad "$s: destroy_release path present"
  fi
done

printf "\n== Feature Registry Contracts ==\n"

allowed_non_feature_flags=(FEAT_VERIFY FEAT_VERIFY_DEEP)
mapfile -t registry_flags < <(feature_registry_flags | rg -v '^$' | sort -u)
if [[ "${#registry_flags[@]}" -eq 0 ]]; then
  bad "feature registry exposes FEAT_* flags"
else
  ok "feature registry exposes FEAT_* flags"
fi

# Every FEAT_* in config/profiles should be represented by the registry
# unless it is an orchestration-only toggle.
config_sources=("$config_file")
for p in "$root"/profiles/*.env; do
  [[ -f "$p" ]] || continue
  config_sources+=("$p")
done

mapfile -t declared_feat_flags < <(
  awk -F= '/^[[:space:]]*FEAT_[A-Z0-9_]+[[:space:]]*=/{gsub(/[[:space:]]/,"",$1); print $1}' "${config_sources[@]}" | sort -u
)
for flag in "${declared_feat_flags[@]}"; do
  if name_matches_any_pattern "$flag" "${registry_flags[@]}"; then
    continue
  fi
  if name_matches_any_pattern "$flag" "${allowed_non_feature_flags[@]}"; then
    continue
  fi
  bad "${flag}: declared in config/profiles but missing from feature registry"
done
ok "All feature FEAT_* flags in config/profiles are covered by the registry"

# Ensure config.env carries defaults for each registered feature flag.
mapfile -t config_defaults < <(
  awk -F= '/^[[:space:]]*FEAT_[A-Z0-9_]+[[:space:]]*=/{gsub(/[[:space:]]/,"",$1); print $1}' "$config_file" | sort -u
)
for flag in "${registry_flags[@]}"; do
  if name_matches_any_pattern "$flag" "${config_defaults[@]}"; then
    ok "${flag}: default present in config.env"
  else
    bad "${flag}: missing default in config.env"
  fi
done

# Parse release refs from helmfile (<namespace>/<name>).
mapfile -t helmfile_releases < <(
  awk '
    /^[[:space:]]*-[[:space:]]name:[[:space:]]*/{
      name=$0
      sub(/^[[:space:]]*-[[:space:]]name:[[:space:]]*/,"",name)
      gsub(/"/,"",name)
      next
    }
    /^[[:space:]]*namespace:[[:space:]]*/{
      ns=$0
      sub(/^[[:space:]]*namespace:[[:space:]]*/,"",ns)
      gsub(/"/,"",ns)
      if(name!=""){print ns "/" name; name=""}
    }
  ' "$helmfile_file" | sort -u
)

for key in "${FEATURE_KEYS[@]}"; do
  mapfile -t expected_releases < <(feature_expected_releases "$key")
  if [[ "${#expected_releases[@]}" -eq 0 ]]; then
    bad "feature '${key}' has no expected release mapping"
  fi
  for rel in "${expected_releases[@]}"; do
    if printf '%s\n' "${helmfile_releases[@]}" | grep -qx "$rel"; then
      ok "feature '${key}' release mapped in helmfile: ${rel}"
    else
      bad "feature '${key}' release missing from helmfile: ${rel}"
    fi
  done
done

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Orchestrator contract verification passed"

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

fail=0
printf "== Orchestrator Contract Verification ==\n"

run="$SCRIPT_DIR/../run.sh"
root="$SCRIPT_DIR/.."
config_file="$root/config.env"
flux_home_dir="$root/clusters/home"
flux_infrastructure_file="$flux_home_dir/infrastructure.yaml"
platform_overlay_kustomization="$root/platform-services/overlays/home/kustomization.yaml"

for s in scripts/bootstrap/install-flux.sh scripts/bootstrap/sync-runtime-inputs.sh scripts/32_reconcile_flux_stack.sh; do
  if [[ -x "$root/$s" ]]; then
    ok "$s: present"
  else
    bad "$s: present"
  fi
done

if rg -q 'bootstrap/install-flux\.sh' "$run" \
  && rg -q 'bootstrap/sync-runtime-inputs\.sh' "$run" \
  && rg -q '32_reconcile_flux_stack\.sh' "$run"; then
  ok "run.sh: Flux apply steps wired into plan"
else
  bad "run.sh: Flux apply steps wired into plan"
fi

if rg -q '31_sync_helmfile_phase_core\.sh|36_sync_helmfile_phase_platform\.sh|92_verify_helmfile_drift\.sh|75_manage_sample_app_lifecycle\.sh' "$run"; then
  bad "run.sh: no Helmfile compatibility steps in plan"
else
  ok "run.sh: no Helmfile compatibility steps in plan"
fi

if [[ -e "$root/helmfile.yaml.gotmpl" ]]; then
  bad "helmfile.yaml.gotmpl: removed"
else
  ok "helmfile.yaml.gotmpl: removed"
fi

if [[ -d "$root/environments" ]]; then
  bad "environments/: removed"
else
  ok "environments/: removed"
fi

if [[ -d "$root/infrastructure/runtime-inputs" ]]; then
  ok "infrastructure/runtime-inputs: present"
else
  bad "infrastructure/runtime-inputs: present"
fi

runtime_inputs_kustomization_file="$root/infrastructure/runtime-inputs/kustomization.yaml"
runtime_inputs_flux_block="$(cat "$flux_infrastructure_file" 2>/dev/null || true)"
infrastructure_overlay_block="$(cat "$root/infrastructure/overlays/home/kustomization.yaml" 2>/dev/null || true)"
if [[ -n "$runtime_inputs_flux_block" ]] \
  && printf '%s\n' "$runtime_inputs_flux_block" | rg -q 'path: ./infrastructure/overlays/home' \
  && printf '%s\n' "$runtime_inputs_flux_block" | rg -q 'postBuild:' \
  && printf '%s\n' "$runtime_inputs_flux_block" | rg -q 'substituteFrom:' \
  && printf '%s\n' "$runtime_inputs_flux_block" | rg -q 'name: platform-runtime-inputs' \
  && printf '%s\n' "$infrastructure_overlay_block" | rg -q '../../runtime-inputs' \
  && ! rg -q 'secret-platform-runtime-inputs\.yaml' "$runtime_inputs_kustomization_file" \
  && ! rg -q '^replacements:' "$runtime_inputs_kustomization_file"; then
  ok "Infrastructure layer: runtime inputs use postBuild substitution from external platform-runtime-inputs secret"
else
  bad "Infrastructure layer: runtime inputs use postBuild substitution from external platform-runtime-inputs secret"
fi

if [[ -x "$SCRIPT_DIR/bootstrap/sync-runtime-inputs.sh" ]] \
  && rg -q '^runtime-inputs-sync:' "$root/Makefile" \
  && rg -q 'sync-runtime-inputs\.sh' "$root/Makefile"; then
  ok "Runtime input sync command is available (scripts/bootstrap/sync-runtime-inputs.sh + Makefile target)"
else
  bad "Runtime input sync command is available (scripts/bootstrap/sync-runtime-inputs.sh + Makefile target)"
fi

if ! rg -q 'homelab-clickstack-bootstrap' "$flux_home_dir"/*.yaml \
  && rg -q '../../clickstack/base' "$platform_overlay_kustomization" \
  && rg -q '../../otel/base' "$platform_overlay_kustomization"; then
  ok "Platform layer: clickstack + otel are modeled directly without bootstrap shim"
else
  bad "Platform layer: clickstack + otel are modeled directly without bootstrap shim"
fi

printf "\n== Feature Registry Contracts ==\n"

allowed_non_feature_flags=(FEAT_VERIFY FEAT_VERIFY_DEEP)
mapfile -t registry_flags < <(feature_registry_flags | rg -v '^$' | sort -u)
if [[ "${#registry_flags[@]}" -eq 0 ]]; then
  bad "feature registry exposes FEAT_* flags"
else
  ok "feature registry exposes FEAT_* flags"
fi

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

mapfile -t manifest_releases < <(
  find "$root/infrastructure" "$root/platform-services" "$root/tenants" -name '*.yaml' -type f -print0 \
    | xargs -0 awk '
      BEGIN {kind=""; name=""; ns=""}
      /^kind:[[:space:]]*HelmRelease[[:space:]]*$/ {kind="HelmRelease"; name=""; ns=""; next}
      kind=="HelmRelease" && /^[[:space:]]*name:[[:space:]]*/ && name=="" {
        line=$0; sub(/^[[:space:]]*name:[[:space:]]*/,"",line); gsub(/"/,"",line); name=line; next
      }
      kind=="HelmRelease" && /^[[:space:]]*namespace:[[:space:]]*/ && ns=="" {
        line=$0; sub(/^[[:space:]]*namespace:[[:space:]]*/,"",line); gsub(/"/,"",line); ns=line;
        if (name != "") { print ns "/" name; kind=""; name=""; ns="" }
      }
    ' | sort -u
)

for key in "${FEATURE_KEYS[@]}"; do
  mapfile -t expected_releases < <(feature_expected_releases "$key")
  if [[ "${#expected_releases[@]}" -eq 0 ]]; then
    bad "feature '${key}' has no expected release mapping"
  fi
  for rel in "${expected_releases[@]}"; do
    if printf '%s\n' "${manifest_releases[@]}" | grep -qx "$rel"; then
      ok "feature '${key}' release mapped in cluster manifests: ${rel}"
    else
      bad "feature '${key}' release missing from cluster manifests: ${rel}"
    fi
  done
done

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Orchestrator contract verification passed"

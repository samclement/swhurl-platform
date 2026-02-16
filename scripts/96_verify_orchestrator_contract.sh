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

# Verify the supported default pipeline uses the Helmfile phase scripts.
for s in 31_sync_helmfile_phase_core.sh 29_prepare_platform_runtime_inputs.sh 36_sync_helmfile_phase_platform.sh; do
  p="$SCRIPT_DIR/$s"
  if [[ -f "$p" ]]; then
    ok "$s: present"
  else
    bad "$s: present"
  fi
done

if rg -q '31_sync_helmfile_phase_core\.sh' "$run" && rg -q '29_prepare_platform_runtime_inputs\.sh' "$run" && rg -q '36_sync_helmfile_phase_platform\.sh' "$run"; then
  ok "run.sh: phase scripts wired into plan"
else
  bad "run.sh: phase scripts wired into plan"
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

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi

echo
ok "Orchestrator contract verification passed"

#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT_DIR"

if [[ "${FEAT_VERIFY:-true}" != "true" ]]; then
  echo "[compat] FEAT_VERIFY=false; skipping legacy verification suite"
  exit 0
fi

./scripts/94_verify_config_inputs.sh
./scripts/91_verify_platform_state.sh

if [[ "${FEAT_VERIFY_DEEP:-false}" == "true" ]]; then
  ./scripts/90_verify_runtime_smoke.sh
  ./scripts/93_verify_expected_releases.sh
  ./scripts/95_capture_cluster_diagnostics.sh
  ./scripts/96_verify_orchestrator_contract.sh
fi

echo "[compat] legacy verification complete"

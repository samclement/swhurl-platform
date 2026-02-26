# Swhurl Platform Walkthrough (Current)

Last updated: 2026-02-26

This file intentionally stays concise and points to the active source-of-truth docs while migration is in progress.

## Source Of Truth

- `README.md`: operator-facing usage and lifecycle commands.
- `docs/runbook.md`: phase-by-phase orchestration flow.
- `docs/contracts.md`: config, tooling, and delete contracts.
- `docs/target-tree-and-migration-checklist.md`: migration status and remaining Phase 6 items.

## Current Execution Model

1. Preferred path: Flux-managed cluster reconciliation from `cluster/`.
2. Compatibility path: `./run.sh` with explicit phase scripts.
3. Default apply plan uses:
   - `scripts/31_sync_helmfile_phase_core.sh`
   - `scripts/36_sync_helmfile_phase_platform.sh`
4. `scripts/29_prepare_platform_runtime_inputs.sh` is manual compatibility-only for runtime secret bridging and delete-time cleanup of legacy managed leftovers.

## Verification Flow

- Core checks (`FEAT_VERIFY=true`):
  - `scripts/94_verify_config_inputs.sh`
  - `scripts/91_verify_platform_state.sh`
  - `scripts/92_verify_helmfile_drift.sh`
- Deep checks (`FEAT_VERIFY_DEEP=true`):
  - `scripts/90_verify_runtime_smoke.sh`
  - `scripts/93_verify_expected_releases.sh`
  - `scripts/95_capture_cluster_diagnostics.sh`
  - `scripts/96_verify_orchestrator_contract.sh`
  - `scripts/97_verify_provider_matrix.sh`

## Regenerating A Full Executable Walkthrough

If you need a full executable transcript again, regenerate this document with Showboat from the current code state instead of patching historical outputs.

Suggested flow:
- run `uvx showboat init walkthrough.md "Swhurl Platform Walkthrough"`
- append notes and exec blocks incrementally
- run `uvx showboat verify walkthrough.md`

# Add Feature Checklist

Use this checklist when adding a new platform feature.

## 1) Declarative wiring (Flux)

- Add/update component manifests under `cluster/base/*` or `cluster/overlays/homelab/*`.
- Add/update Flux stack wiring in `cluster/overlays/homelab/flux/stack-kustomizations.yaml`.
- Keep `dependsOn` explicit.

## 2) Feature flags and config

- Add/verify feature flag default in `config.env` (`FEAT_*`).
- Update `scripts/00_feature_registry_lib.sh` required vars and expected release mapping.

## 3) Runtime inputs (if feature needs secrets)

- Add/update target manifests in `cluster/base/runtime-inputs/*`.
- Update `scripts/bootstrap/sync-runtime-inputs.sh` validation and secret projection values.

## 4) Verification updates

- `scripts/94_verify_config_inputs.sh`
- `scripts/91_verify_platform_state.sh`
- `scripts/93_verify_expected_releases.sh` (if inventory expectations change)
- `scripts/96_verify_orchestrator_contract.sh` (if orchestrator/contract changes)

## 5) Documentation

- `README.md`
- `docs/runbook.md`
- `AGENTS.md`

## 6) Validation before PR

- `bash -n scripts/*.sh host/**/*.sh`
- `./scripts/96_verify_orchestrator_contract.sh`
- `./scripts/02_print_plan.sh`
- Optional cluster-backed checks:
  - `./scripts/94_verify_config_inputs.sh`
  - `./scripts/93_verify_expected_releases.sh`
  - `./scripts/91_verify_platform_state.sh`

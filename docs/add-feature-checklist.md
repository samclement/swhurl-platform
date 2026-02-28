# Add Feature Checklist

Use this checklist when adding a new platform feature.

## 1) Declarative wiring (Flux)

- Add/update component manifests under `infrastructure/*`, `platform-services/*`, or `tenants/*`.
- Add/update Flux stack wiring in `clusters/home/{infrastructure,platform,tenants}.yaml` and layer overlay kustomizations.
- Keep `dependsOn` explicit.

## 2) Feature flags and config

- Add/verify feature flag default in `config.env` (`FEAT_*`).
- Update feature required vars in `scripts/00_verify_contract_lib.sh`.

## 3) Runtime inputs (if feature needs secrets)

- Add/update target manifests in `platform-services/runtime-inputs/*`.
- Update `scripts/bootstrap/sync-runtime-inputs.sh` validation and secret projection values.

## 4) Verification updates

- `scripts/94_verify_config_inputs.sh`
- `scripts/91_verify_platform_state.sh`

## 5) Documentation

- `README.md`
- `docs/runbook.md`
- `AGENTS.md`

## 6) Validation before PR

- `bash -n scripts/*.sh host/**/*.sh`
- `./run.sh --dry-run`
- Optional cluster-backed checks:
  - `./scripts/94_verify_config_inputs.sh`
  - `./scripts/91_verify_platform_state.sh`

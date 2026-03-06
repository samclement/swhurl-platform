# Add Feature Checklist

Use this checklist when adding a new platform feature.

## 1) Declarative wiring (Flux)

- Add/update component manifests under `infrastructure/*`, `platform-services/*`, or `tenants/*`.
- Add/update Flux stack wiring in `clusters/home/{infrastructure,platform,tenants}.yaml` and layer overlay kustomizations.
- Keep `dependsOn` explicit.

## 2) Config and contracts

- Default model: prefer declarative composition and runtime-input wiring over new `FEAT_*` switches.
- Keep non-secrets in `config.env` and secrets in `profiles/secrets.env`.
- Update required vars/contracts in `scripts/00_verify_contract_lib.sh`.
- If a new feature switch is absolutely necessary, document it in `docs/orchestration-api.md` and keep scope narrow (current default switch is `FEAT_VERIFY`).

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

- `bash -n scripts/*.sh scripts/bootstrap/*.sh host/run-host.sh host/tasks/*.sh host/lib/*.sh`
- `make install DRY_RUN=true`
- `make teardown DRY_RUN=true`
- Optional cluster-backed checks:
  - `./scripts/94_verify_config_inputs.sh`
  - `./scripts/91_verify_platform_state.sh`

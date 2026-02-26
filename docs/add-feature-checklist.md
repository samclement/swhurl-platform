# Add Feature Checklist (Keep It Simple)

Use this checklist when adding a new platform feature (for example Keycloak).

Goal: keep explicit scripts readable; avoid adding framework indirection unless there is repeated pain.

## 1) Declarative install wiring

- Add Helmfile release in `helmfile.yaml.gotmpl` with:
  - `installed:` feature gate
  - `phase` label (`core` or `platform`)
  - `component` label

## 2) Feature flags and values

- Add/verify feature flag default in `config.env` (`FEAT_*`).
- Add/verify environment mapping in `environments/common.yaml.gotmpl`.
- Add feature values file in `infra/values/` if needed.

## 3) Runtime input scripts (only if needed)

- If the feature requires pre-created Secrets/ConfigMaps, update:
  - `scripts/29_prepare_platform_runtime_inputs.sh` (manual compatibility bridge / delete helper)
- If the feature needs an extra chart repo, update:
  - `scripts/25_prepare_helm_repositories.sh`

Keep these scripts explicit and local. Prefer clear `if FEAT_*` blocks over meta-framework logic.

## 4) Verification updates

- Update required config checks via:
  - `scripts/00_feature_registry_lib.sh`
  - `scripts/94_verify_config_inputs.sh` (already registry-driven)
- Update expected release inventory via:
  - `scripts/00_feature_registry_lib.sh`
  - `scripts/93_verify_expected_releases.sh` (already registry-driven)
- Add runtime state checks in:
  - `scripts/91_verify_platform_state.sh`

## 5) Docs updates

- `README.md` feature list/flags
- `docs/runbook.md` phase behavior (if changed)
- `AGENTS.md` for repo learnings/gotchas

## 6) Verification commands before PR

- `bash -n scripts/*.sh`
- `./scripts/96_verify_orchestrator_contract.sh`
- `./scripts/02_print_plan.sh`
- Optional cluster-backed checks:
  - `./scripts/94_verify_config_inputs.sh`
  - `./scripts/93_verify_expected_releases.sh`
  - `./scripts/91_verify_platform_state.sh`

## Guardrails

- Do not add a new abstraction layer unless at least two concrete features are blocked by the same repeated maintenance problem.
- Prefer explicit file updates and clear comments over clever cross-file generation.

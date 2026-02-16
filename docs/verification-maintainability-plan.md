# Verification Framework Maintainability Plan (Keycloak-Driven)

Last updated: 2026-02-16

## Implementation Status

- Phase 1 started:
  - added `scripts/00_feature_registry_lib.sh`
  - `scripts/93_verify_expected_releases.sh` now resolves expected releases via registry-driven helpers
  - `scripts/94_verify_config_inputs.sh` now resolves feature required vars via registry-driven helpers
  - `scripts/91_verify_platform_state.sh` feature gates now use registry helpers (`feature_is_enabled`)
- Remaining Phase 1 work:
  - remove remaining feature metadata duplication outside verification (for example Helm repo and runtime-input script wiring), or explicitly scope those to later phases if intentional
- Phase 3 in progress:
  - `scripts/96_verify_orchestrator_contract.sh` now validates registry-to-config flag coverage, registry-to-Helmfile release mappings, and verify-script wiring in `run.sh`
  - `scripts/93_verify_expected_releases.sh` now checks missing expected releases and unexpected extras (with scope + allowlist controls)

## Goal

Make verification easy to maintain when adding a new platform feature (example: Keycloak), by reducing duplicate configuration, reducing required touchpoints, and enforcing consistency with automated checks.

## Context

Current verification is functional, but maintainer effort is high when introducing a feature.

As of this plan, key verification scripts are:

- `scripts/94_verify_config_inputs.sh` (config contract)
- `scripts/91_verify_platform_state.sh` (runtime state checks)
- `scripts/92_verify_helmfile_drift.sh` (drift gate)
- `scripts/93_verify_expected_releases.sh` (expected release inventory)
- `scripts/96_verify_orchestrator_contract.sh` (orchestrator/contract checks)
- `scripts/00_verify_contract_lib.sh` (shared verification constants/helpers)

Feature installation is Helmfile-driven (`phase=core`, `phase=platform`) with feature flags (`FEAT_*`) in `config.env` and environment mapping in `environments/common.yaml.gotmpl`.

## Why Keycloak Exposes the Problem

Adding Keycloak today likely requires manual updates in many places, including:

- `helmfile.yaml.gotmpl` (release definition)
- `scripts/25_helm_repos.sh` (repo setup)
- `config.env` (feature flag and variables)
- `environments/common.yaml.gotmpl` (feature mapping)
- `charts/platform-namespaces/values.yaml` (namespace, if needed)
- `scripts/00_verify_contract_lib.sh` (required vars, expected releases)
- `scripts/29_prepare_platform_runtime_inputs.sh` (secrets/config if needed)
- `scripts/91_verify_platform_state.sh` (runtime verification checks)
- `README.md` and `docs/runbook.md` (documentation updates)

This is the core maintainability issue: one feature requires synchronized edits across multiple unrelated files.

## Desired End State

Maintainer adds a new feature by editing one source-of-truth registry and one feature verification module, then runs one consistency check that validates wiring.

## High-Level Plan

### Phase 1: Single Feature Registry

Create a registry file (for example `scripts/feature_registry.sh` or `infra/contracts/features.yaml`) defining, per feature:

- feature flag name (`FEAT_*`)
- helmfile release(s)
- required env vars
- optional runtime inputs/secrets contract
- verification tier (`core` or `deep`)
- verify module entrypoint

Then make existing verification scripts read from this registry instead of hardcoded feature lists.

### Phase 2: Modular Runtime Verification

Refactor `scripts/91_verify_platform_state.sh` into a dispatcher + modules, for example:

- `scripts/91_verify_platform_state.sh` (dispatcher/orchestrator)
- `scripts/verify_components/oauth2_proxy.sh`
- `scripts/verify_components/clickstack.sh`
- `scripts/verify_components/minio.sh`
- `scripts/verify_components/keycloak.sh` (new feature case)

Goal: adding feature verification is additive, not invasive.

### Phase 3: Stronger Consistency Gates

Expand `scripts/96_verify_orchestrator_contract.sh` to fail when:

- a `FEAT_*` flag exists but has no registry entry
- a registry feature has no Helmfile release mapping
- a registry feature has no verify module
- verify modules exist but are not assigned to a tier
- tiered verify modules are not executed by `run.sh`

Also upgrade `scripts/93_verify_expected_releases.sh` to detect both:

- missing expected releases
- unexpected extra releases (with explicit allowlist support)

### Phase 4: Align Feature Gating Semantics

Resolve current mismatch risk between:

- script gating via `FEAT_*` environment values
- Helmfile `installed:` behavior via environment values files

Select one canonical source for feature enablement and enforce it across install + verification paths.

### Phase 5: CI Guardrails

Add CI workflow(s) to run on PRs:

- `bash -n` on scripts
- `shellcheck`
- `scripts/96_verify_orchestrator_contract.sh`
- `scripts/94_verify_config_inputs.sh` with representative profiles

Optional follow-up: lightweight mocked checks for registry/module integrity without requiring a cluster.

### Phase 6: Keycloak Pilot Implementation

Implement Keycloak using the new model as the proving case:

- add Keycloak feature in registry
- add Keycloak verify module
- verify repo/docs updates are derived from registry-driven flow
- confirm reduced maintainer touchpoints

## Issues To Resolve Within The Plan

Issue VER-001: Source-of-truth duplication across config, Helmfile, and verify scripts.
Resolution target: Phase 1.

Issue VER-002: Runtime verification monolith (`91`) causes high edit conflict and onboarding cost.
Resolution target: Phase 2.

Issue VER-003: Orchestrator contract checks are too narrow and can miss unwired verify scripts.
Resolution target: Phase 3.

Issue VER-004: Release inventory verification is one-way (missing-only) and does not catch unexpected residue.
Resolution target: Phase 3.

Issue VER-005: Feature gating may diverge between script env flags and Helmfile environment values.
Resolution target: Phase 4.

Issue VER-006: No automated PR guardrails for verification framework consistency.
Resolution target: Phase 5.

Issue VER-007: Lack of maintainer onboarding guidance for "add feature + add verification".
Resolution target: add/update docs in Phases 1-3, finalize in Phase 6.

Issue VER-008: Backward compatibility risk during migration from hardcoded lists to registry/model-based checks.
Resolution target: phased rollout with temporary compatibility mode in Phases 1-3.

Issue VER-009: Risk of false positives in strict release inventory checks (shared clusters / manual ops).
Resolution target: explicit allowlist and scope controls in Phase 3.

Issue VER-010: Secret-handling drift for runtime inputs created by scripts (`29_prepare_platform_runtime_inputs.sh`).
Resolution target: model runtime-input contracts in registry during Phases 1 and 6.

## Acceptance Criteria

- Adding a new platform feature requires at most:
  - one registry update
  - one feature verify module
  - one Helmfile release definition
- `96` fails on any missing wiring between feature flag, Helmfile, and verification module.
- `93` reports both missing and unexpected releases.
- Documentation includes a concise maintainer playbook for adding a feature and verification.
- Keycloak is added successfully using the new flow.

## Future Prompt Handoff Notes

If a future prompt asks to continue this work, start by:

- reading this file first (`docs/verification-maintainability-plan.md`)
- running `scripts/96_verify_orchestrator_contract.sh`
- identifying the next unresolved `VER-00X` issue

Primary migration sequence recommendation:

1. Implement Phase 1 registry.
2. Implement Phase 3 consistency checks against that registry.
3. Refactor Phase 2 modular verification.
4. Add CI (Phase 5).
5. Use Keycloak as migration proof (Phase 6).

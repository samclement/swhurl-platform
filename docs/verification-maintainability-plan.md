# Verification Framework Maintainability Plan (Keycloak-Driven)

Last updated: 2026-02-26

## Implementation Status

Current state (2026-02-26):

- Implemented:
  - `scripts/00_feature_registry_lib.sh` is the feature verification registry.
  - `scripts/93_verify_expected_releases.sh` is Flux-first (`VERIFY_INVENTORY_MODE=auto|flux|helm`) and falls back to Helm release inventory while preserving strict-extra controls.
  - `scripts/94_verify_config_inputs.sh` resolves feature-required vars via registry helpers.
  - `scripts/91_verify_platform_state.sh` gates feature checks via registry helpers (`feature_is_enabled`).
  - `scripts/96_verify_orchestrator_contract.sh` validates registry/config flag coverage and registry/Helmfile release mappings.
- Intentional simplification:
  - Keep `scripts/25_prepare_helm_repositories.sh` explicit and runtime-input wiring declarative in `infrastructure/runtime-inputs`.
  - Keep runtime verification mostly centralized in `scripts/91_verify_platform_state.sh` unless maintenance pain clearly justifies splitting.
  - Use `docs/add-feature-checklist.md` as the primary maintainer workflow.

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
- `scripts/25_prepare_helm_repositories.sh` (repo setup)
- `config.env` (feature flag and variables)
- `environments/common.yaml.gotmpl` (feature mapping)
- `infrastructure/namespaces/namespaces.yaml` (shared namespace manifests, if needed)
- `scripts/00_verify_contract_lib.sh` (required vars, expected releases)
- `infrastructure/runtime-inputs/*` (runtime secrets/config if needed)
- `scripts/91_verify_platform_state.sh` (runtime verification checks)
- `README.md` and `docs/runbook.md` (documentation updates)

This is the core maintainability issue: one feature requires synchronized edits across multiple unrelated files.

## Desired End State

Maintainer adds a new feature by editing one source-of-truth registry and one runtime verification touchpoint, then runs one consistency check that validates wiring.

## High-Level Plan

### Phase 1: Single Feature Registry

Use `scripts/00_feature_registry_lib.sh` as the source of truth defining, per feature:

- feature flag name (`FEAT_*`)
- helmfile release(s)
- required env vars
- optional runtime inputs/secrets contract
- verification tier (`core` or `deep`)
- runtime verification entrypoint (currently in `scripts/91_verify_platform_state.sh`)

Then make existing verification scripts read from this registry instead of hardcoded feature lists.

### Phase 2: Runtime Verification Modularity (Conditional)

Keep `scripts/91_verify_platform_state.sh` as a single verifier by default.

If it becomes materially hard to maintain, split it into a thin dispatcher plus per-feature modules (for example under `scripts/verify_components/`) as a scoped follow-up.

Goal: add feature verification with minimal invasive edits while avoiding abstraction overhead until needed.

### Phase 3: Stronger Consistency Gates

Expand `scripts/96_verify_orchestrator_contract.sh` to fail when:

- a `FEAT_*` flag exists but has no registry entry
- a registry feature has no Helmfile release mapping
- a registry feature has no runtime verification coverage
- runtime verification coverage is assigned to the wrong verification tier
- tiered verification scripts are not executed by `run.sh`

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
- add Keycloak runtime verification (in `scripts/91_verify_platform_state.sh`, or module form if Phase 2 is adopted)
- verify repo/docs updates are derived from registry-driven flow
- confirm reduced maintainer touchpoints

## Issues To Resolve Within The Plan

Issue VER-001: Source-of-truth duplication across config, Helmfile, and verify scripts.
Resolution target: Phase 1.

Issue VER-002: Runtime verification monolith (`91`) may cause edit conflict/onboarding cost as features grow.
Resolution target: Phase 2 (only if complexity threshold is crossed).

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

Issue VER-010: Secret-handling drift for runtime inputs.
Resolution target: keep runtime-input contracts declarative in `infrastructure/runtime-inputs` and verified by orchestrator contract checks.

## Acceptance Criteria

- Adding a new platform feature requires at most:
  - one registry update
  - one runtime verification update (single file or module, depending on Phase 2)
  - one Helmfile release definition
- `96` fails on any missing wiring between feature flag and Helmfile release mappings.
- `93` reports both missing and unexpected releases.
- Documentation includes a concise maintainer playbook for adding a feature and verification.
- Keycloak is added successfully using the new flow.

## Future Prompt Handoff Notes

If a future prompt asks to continue this work, start by:

- reading this file first (`docs/verification-maintainability-plan.md`)
- running `scripts/96_verify_orchestrator_contract.sh`
- identifying the next unresolved `VER-00X` issue

Primary migration sequence recommendation:

1. Keep Phase 1 registry as source of truth and maintain it.
2. Continue expanding Phase 3 consistency checks against that registry.
3. Add CI guardrails (Phase 5).
4. Re-evaluate Phase 2 modular verification only if maintainability pain is concrete.
5. Use Keycloak as migration proof (Phase 6).

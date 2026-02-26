# Homelab Intent and Design Direction

## Purpose

This repo should be the repeatable, maintainable control plane for a single-server (or small) homelab platform.
It should install host prerequisites, bootstrap Kubernetes, converge platform services declaratively, and deploy an example app that demonstrates platform integration.

Companion implementation plan:

- `docs/target-tree-and-migration-checklist.md`

## Intent

Primary goals:

1. Install host/package-manager dependencies required by this repo.
2. Configure dynamic DNS updates for homelab domains.
3. Install k3s.
4. Install platform components:
   - cert-manager
   - ClickStack
   - OTel collectors
   - oauth2-proxy
   - Cilium + Hubble
   - object storage (currently MinIO, planned move to Ceph-class option)
5. Install an example app that demonstrates ingress, TLS, and optional OIDC integration.
6. Keep operations repeatable and mostly declarative, with imperative logic limited to orchestration, adoption, and cleanup edge-cases.

Planned direction changes:

- Ingress: retire `ingress-nginx` as default and move to k3s default Traefik.
- Storage: retire MinIO as default and move to a Ceph-based option over time.

## Design Principles

1. Declarative first: Helmfile + charts/values define desired state.
2. Imperative only where necessary: bootstrap, input generation, lifecycle/finalizer cleanup.
3. Explicit orchestration: fixed phase plan in `run.sh`, no hidden script discovery.
4. Provider-oriented architecture: ingress/storage implementations should be swappable behind stable contracts.
5. Verification as contract: config checks, runtime state checks, drift checks, and teardown checks remain first-class.

## Current Fit and Gaps

Current strengths:

- Phase-based orchestration already exists and is readable (`run.sh`).
- Helmfile labels (`phase`, `component`) already support composable orchestration.
- Verification/teardown contracts are centralized (`00_verify_contract_lib.sh`).
- Feature registry model exists (`00_feature_registry_lib.sh`).

Current gaps against intent:

- Host dependency installation is checked but not managed (`01_check_prereqs.sh` verifies only).
- Ingress and object storage are modeled as fixed components, not provider families.
- k3s bootstrap script currently disables Traefik, which conflicts with the new desired default.
- Example app currently demonstrates basic ingress/OIDC/TLS, but not full platform integrations (telemetry/object storage usage).

## Recommended Improvements

1. Add host bootstrap phase for dependencies
- Add a new script (for example `scripts/10_prepare_host_dependencies.sh`) that installs required host packages idempotently.
- Keep distro-specific logic isolated (`apt`, `dnf`, etc.) in dedicated host Bash modules (for example under `host/lib/`).
- Keep `01_check_prereqs.sh` as validation gate even after install support is added.

2. Introduce provider switches for ingress and object storage
- Add `INGRESS_PROVIDER=traefik|nginx`.
- Add `OBJECT_STORAGE_PROVIDER=minio|ceph`.
- Keep stable consumer-facing env vars (`OAUTH_HOST`, `CLICKSTACK_HOST`, `MINIO_*` or generic storage host vars) while chart/release wiring changes by provider.

3. Rework Helmfile phases around capability domains
- Keep `phase=core|platform`, but make ingress/storage releases conditional by provider.
- Add provider-neutral wrapper scripts (for example `sync_ingress_provider`, `sync_storage_provider`) that select labels.

4. Align k3s bootstrap with desired ingress default
- Add explicit k3s mode variable (for example `K3S_INGRESS_MODE=traefik|none`).
- Default to Traefik mode for new installs.
- Keep `none` mode for advanced users who want external ingress choices.

5. Plan ingress migration with compatibility window
- Keep `ingress-nginx` as optional transitional provider.
- Add migration runbook for:
  - ingress class changes
  - auth annotation compatibility
  - cert-manager solver ingress class updates
- Validate equivalent routing and TLS behavior before switching defaults.

6. Plan storage migration with data movement strategy
- Introduce Ceph option as opt-in provider before defaulting it.
- Document data migration path from MinIO buckets to target storage.
- Keep existing app-facing S3 endpoint contract stable during transition where possible.

7. Strengthen example app as integration reference
- Extend sample app to show:
  - OIDC edge auth
  - TLS via cert-manager
  - telemetry export to platform OTel path
  - object storage interaction (S3-compatible API)

8. Add CI contract checks for maintainability
- Add CI targets for:
  - script lint (`shellcheck`)
  - Helmfile template + lint
  - verification scripts in dry/simulated modes
  - orchestrator contract check (`96_verify_orchestrator_contract.sh`)

9. Adopt ADR-style decisions for major migrations
- Record ingress and storage provider decisions in lightweight ADR docs.
- Tie each ADR to:
  - config contract changes
  - migration scripts/runbooks
  - rollback strategy

10. Improve ownership boundaries
- Keep this repo focused on platform lifecycle.
- Keep application examples minimal but production-like for integration patterns.
- For large host provisioning needs, prefer external configuration management and call it from this repo.

## Suggested Implementation Sequence

1. Add this intent as a tracked design target (this document).
2. Add host dependency install script and wire it into `run.sh` before prereq verification.
3. Add ingress provider abstraction with Traefik path, keep nginx optional.
4. Update k3s bootstrap defaults to align with Traefik-first design.
5. Add storage provider abstraction with Ceph opt-in path, keep MinIO optional.
6. Expand sample app to cover telemetry and storage integration.
7. Add CI checks for orchestration contracts and drift/verification scripts.

## Non-Goals (for now)

- Full multi-node production-grade cluster automation.
- Replacing all shell orchestration immediately.
- Solving every host OS/distribution package edge case in one pass.

## Success Criteria

1. A fresh host can be converged with one documented sequence and minimal manual edits.
2. Re-running apply is safe and converges without surprise drift.
3. Delete path remains deterministic and verifiable.
4. Ingress and storage providers can evolve without rewriting the entire orchestrator.
5. Example app remains a reliable reference for platform integration patterns.

# Homelab Intent and Design Direction

## Purpose

This repo should be the repeatable, maintainable control plane for a single-server (or small) homelab platform.
It should install host prerequisites, bootstrap Kubernetes, converge platform services declaratively, and deploy an example app that demonstrates platform integration.

Companion implementation plan:

- `docs/target-tree-and-migration-checklist.md`

## Intent

Primary goals:

1. Keep host dependency prerequisites explicit in README.
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

1. Declarative first: Flux-managed manifests and Kustomizations define desired state.
2. Imperative only where necessary: bootstrap, input generation, lifecycle/finalizer cleanup.
3. Explicit orchestration: fixed phase plan in `run.sh`, no hidden script discovery.
4. Provider-oriented architecture: ingress/storage implementations should be swappable behind stable contracts.
5. Verification as contract: config checks, runtime state checks, drift checks, and teardown checks remain first-class.

## Current Fit and Gaps

Current strengths:

- Phase-based orchestration already exists and is readable (`run.sh`).
- Helmfile labels (`phase`, `component`) already support composable orchestration.
- Verification/teardown contracts are centralized (`00_verify_contract_lib.sh`).
- Feature flag/required-var verification metadata is centralized (`00_verify_contract_lib.sh`).

Current gaps against intent:

- Host orchestration is still opt-in (not default in cluster-only runs).
- Ingress and object storage provider intent flags now exist; remaining gap is provider default/promotion policy over time (Traefik/Ceph direction).
- k3s bootstrap defaults align with Traefik-first (`K3S_INGRESS_MODE=traefik`), but cluster provisioning remains a manual prerequisite path.
- Example app currently demonstrates basic ingress/OIDC/TLS, but not full platform integrations (telemetry/object storage usage).

## Recommended Improvements

1. Tighten host bootstrap adoption
- Keep dependency requirements documented in README instead of maintaining a separate prereq-check step script.

2. Continue provider switch rollout for ingress and object storage
- Keep stable consumer-facing env vars (`OAUTH_HOST`, `CLICKSTACK_HOST`, `MINIO_*` or generic storage host vars) while chart/release wiring changes by provider.

3. Continue reducing legacy script surface area
- Keep `phase=core|platform` ownership explicit while GitOps overlays become the default operator path.
- Avoid adding new one-off wrapper scripts unless they materially reduce operational risk.

4. Keep k3s bootstrap behavior explicit
- Maintain `K3S_INGRESS_MODE=traefik|none` and document when `none` is required.
- Keep `traefik` as the default for new installs unless a migration demands otherwise.

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
  - verification scripts in dry/simulated modes
  - `./run.sh --dry-run`

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
2. Increase adoption of the host layer in day-to-day workflows (`host/run-host.sh`).
3. Finalize ingress provider migration policy (Traefik default timing + rollback criteria).
4. Finalize storage provider migration policy (Ceph default timing + data-migration checklist).
5. Expand sample app to cover telemetry and storage integration.
6. Add CI checks for orchestration contracts and drift/verification scripts.

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

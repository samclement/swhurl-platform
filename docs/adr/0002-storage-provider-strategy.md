# ADR 0002: Object Storage Provider Strategy

- Status: accepted
- Date: 2026-02-26

## Context

The platform currently runs MinIO and keeps a Ceph path available for future migration.
The repo needs a stable way to switch storage providers without changing higher-level
platform or tenant layering.

## Decision

Use composition-driven provider selection in:
- `infrastructure/overlays/home/kustomization.yaml`

Current default is MinIO (`../../storage/minio/base`).
Optional Ceph composition path is `../../storage/ceph/base`.

Keep `OBJECT_STORAGE_PROVIDER` in `config.env` as an operator intent hint for
verification and operational checks, not as the source of deployment truth.

## Consequences

- Storage provider state is explicit in Git composition.
- Flux remains the single reconciler for provider resources.
- Migration work can proceed incrementally behind a stable layer contract.
- Data migration remains an operational concern and must be handled by runbook.

## Follow-ups

1. Keep `docs/runbooks/migrate-minio-to-ceph.md` aligned with actual manifests.
2. Define/implement Ceph resources before switching default composition.

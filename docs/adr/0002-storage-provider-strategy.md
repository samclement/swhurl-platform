# ADR 0002: Object Storage Provider Strategy

- Status: accepted
- Date: 2026-02-26

## Context

The current platform deploys MinIO for object storage. Planned direction is to move to
a Ceph-class storage model. The repo needs a clear contract so this transition does not
require restructuring orchestration scripts.

## Decision

Adopt explicit storage provider intent via `OBJECT_STORAGE_PROVIDER` with allowed values:

- `minio`
- `ceph`

Use this as the contract for:

- Helmfile installation gating (MinIO installed only for `minio`)
- runtime input preparation (MinIO secrets only when `minio`)
- provider-aware verification checks
- migration runbook and overlay composition

## Consequences

- MinIO lifecycle becomes optional and can be disabled declaratively.
- Ceph migration can proceed incrementally without breaking non-storage platform layers.
- Data migration remains an operational responsibility; automation must not imply data
  safety. Runbook-driven export/import validation is required.

## Follow-ups

1. Implement Ceph provider resources in `infrastructure/storage/ceph/base` and switch composition in `infrastructure/overlays/home/kustomization.yaml`.
2. Keep `docs/runbooks/migrate-minio-to-ceph.md` aligned with actual migration tooling.

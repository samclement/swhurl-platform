# ADR 0002: Object Storage Provider Strategy

- Status: superseded 2026-09-28: MinIO was removed (it held no buckets) and no object storage runs. Choose a provider afresh when a service needs one.
- Date: 2026-02-26

## Context

The platform currently runs MinIO.
The repo needs a stable way to switch storage providers without changing higher-level
platform or tenant layering.

## Decision

Use composition-driven provider selection in:
- `clusters/home/infra.yaml` (one Flux unit per provider path; the `infrastructure/overlays/home` aggregator was removed in PR03)

Current default is MinIO (`homelab-minio` → `infrastructure/storage/minio/base`).
Legacy Ceph composition manifests are no longer retained in this repo.

Verification checks MinIO directly (hardcoded); there is no config-level provider switch.

## Consequences

- Storage provider state is explicit in Git composition.
- Flux remains the single reconciler for provider resources.
- Data migration remains an operational concern and must be handled by runbook.

## Follow-ups

1. Define/implement Ceph resources under active paths before switching default composition.

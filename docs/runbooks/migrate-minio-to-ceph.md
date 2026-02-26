# Runbook: Migrate Object Storage MinIO to Ceph

This runbook defines a controlled path from in-cluster MinIO to a Ceph-backed storage
provider model.

## Scope

- Transition provider intent from `OBJECT_STORAGE_PROVIDER=minio` to `ceph`.
- Preserve application data by explicit export/import.
- Keep repo automation deterministic while Ceph overlays are introduced.

## Preconditions

1. Current platform converges and MinIO is healthy:
   - `./run.sh`
   - `kubectl -n storage get pods`
2. Ceph target architecture is chosen (in-cluster operator vs external cluster).
3. Backup and restore path is tested for required buckets.

## Data Migration (Recommended Sequence)

1. Freeze writes for workloads using MinIO.
2. Export MinIO data.
3. Import data into Ceph S3 endpoint.
4. Validate object counts/checksums for critical buckets.
5. Update applications to Ceph credentials/endpoints.

Example export/import tooling can use `mc` or `rclone`; keep credentials out of git and
store secrets in `profiles/secrets.env` or external secret manager.

## Platform Switch Steps

1. Use the committed provider profile for Ceph intent
   (`profiles/provider-ceph.env`) instead of creating an ad-hoc profile file.

2. Reconcile platform with Ceph provider intent.

```bash
./run.sh --profile profiles/provider-ceph.env
```

3. Confirm MinIO is no longer managed by Helmfile.

```bash
helm list -n storage | rg minio || true
```

4. Validate platform/app integration against Ceph endpoint and credentials.

## Expected Behavior in This Repo

- MinIO release installs only when `OBJECT_STORAGE_PROVIDER=minio`.
- MinIO runtime secret preparation is skipped when provider is Ceph.
- MinIO verification checks are skipped when provider is Ceph.
- Ceph implementation is staged through `cluster/overlays/homelab/providers/storage-ceph`.

## Rollback

1. Restore provider intent to MinIO:

```bash
OBJECT_STORAGE_PROVIDER=minio ./run.sh
```

2. Re-verify platform:

```bash
./scripts/91_verify_platform_state.sh
./scripts/92_verify_helmfile_drift.sh
```

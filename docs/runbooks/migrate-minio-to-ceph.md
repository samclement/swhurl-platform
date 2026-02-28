# Runbook: Migrate Object Storage MinIO to Ceph

This runbook switches Flux stack storage ownership from `storage-minio` to `storage-ceph`.

## Preconditions

1. Current stack is healthy:

```bash
make flux-reconcile
kubectl -n storage get pods
```

2. Ceph target is prepared and tested.
3. Backup/restore path for buckets is validated.

## Data Migration

Recommended sequence:
1. Freeze app writes to MinIO-backed buckets.
2. Export MinIO data.
3. Import into Ceph S3 endpoint.
4. Validate object counts/checksums.
5. Update apps to Ceph credentials/endpoints.

## Platform Migration

1. Update shared infrastructure composition in `infrastructure/overlays/home/kustomization.yaml`:
- replace: `../../storage/minio/base`
- with: `../../storage/ceph/base`

2. Commit the path change.

3. Reconcile:

```bash
make flux-reconcile
```

4. Verify MinIO resources are gone and app integrations work with Ceph.

## Rollback

Revert the composition change in `infrastructure/overlays/home/kustomization.yaml`, then:

```bash
make flux-reconcile
```

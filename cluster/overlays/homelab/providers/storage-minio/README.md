# Storage Provider: MinIO

Compatibility overlay for the current in-cluster object storage path.

Current wiring targets `cluster/base/storage/minio`.
Use `cluster/overlays/homelab/platform/prod/storage-minio` as a promotion overlay to
switch MinIO ingress annotations/hosts to prod TLS intent.

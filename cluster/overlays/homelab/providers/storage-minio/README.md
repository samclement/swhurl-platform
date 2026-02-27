# Storage Provider: MinIO

Compatibility overlay for the current in-cluster object storage path.

Current wiring targets `cluster/base/storage/minio`.
MinIO ingress certificate issuer intent is controlled by `PLATFORM_CLUSTER_ISSUER`
via Flux post-build substitution (staging/prod), without changing this provider path.

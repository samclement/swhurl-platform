# Provider Overlays

This folder holds environment overlays for provider choices.

- Pick one ingress provider overlay.
- Pick one object storage provider overlay.
- Keep non-selected overlays out of the active `kustomization.yaml`.

These overlays are scaffolds during migration from the legacy Helmfile pipeline.

Current ingress compatibility scaffold:
- `ingress-nginx/helmrelease-ingress-nginx.yaml` (suspended by default)

Current storage compatibility scaffold:
- `storage-minio/kustomization.yaml` delegates to `../platform/staging/storage-minio`
  so provider selection stays separate from environment (staging/prod) value overlays.

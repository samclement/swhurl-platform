# Provider Overlays

This folder holds environment overlays for provider choices.

- Pick one ingress provider overlay.
- Pick one object storage provider overlay.
- Keep non-selected overlays out of the active `kustomization.yaml`.

Default homelab stack uses:
- `ingress-nginx`
- `storage-minio`

`ingress-traefik` and `storage-ceph` remain provider migration targets.

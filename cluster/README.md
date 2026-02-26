# Cluster Layer (GitOps)

This directory is the target declarative cluster layer for migration from script-driven orchestration.

Structure:

- `flux/`: Flux bootstrap manifests and source definitions.
- `base/`: provider-agnostic platform components (component-level scaffolds for `cert-manager`, `cert-manager/issuers`, `cilium`, `oauth2-proxy`, `clickstack`, `otel`, `storage/*`, `apps/example`).
- `overlays/homelab/`: environment composition and provider selection.
- `overlays/homelab/providers/`: ingress/storage provider overlay scaffolds (`ingress-traefik`, `ingress-nginx`, `storage-minio`, `storage-ceph`).
- `overlays/homelab/flux/`: Flux `Kustomization` dependency chain for phase ordering.

During migration, legacy orchestration in `run.sh` remains the source of truth for applies/deletes.
Use this tree as the build-out path for phased GitOps adoption.

Safety default:

- In `cluster/overlays/homelab/flux/stack-kustomizations.yaml`, only the namespaces layer is active by default.
- Remaining layers (`cilium`, `cert-manager`, `issuers`, `ingress`, `oauth2-proxy`, `clickstack`, `otel`, `storage`, `example-app`) start with `spec.suspend: true` until each migration phase is ready.

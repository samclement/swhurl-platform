# Cluster Layer (GitOps)

This directory is the active declarative cluster layer. Flux owns ordering and reconciliation.

Structure:

- `flux/`: Flux bootstrap manifests and source definitions.
- `base/`: component HelmRelease definitions (`cert-manager`, `cert-manager/issuers`, `cilium`, `oauth2-proxy`, `clickstack`, `otel`, `storage/*`, `apps/example`).
- `overlays/homelab/`: default homelab composition (nginx + minio + app staging overlay).
- `overlays/homelab/apps/`: staging/prod app overlays (`apps-staging` / `apps-prod`).
- `overlays/homelab/platform/`: optional platform promotion overlays (staging/prod TLS issuer intent).
- `overlays/homelab/providers/`: explicit ingress/storage provider overlays.
- `overlays/homelab/flux/`: Flux `Kustomization` dependency chain.

Operational note:

- Runtime input secrets (OIDC, ClickStack ingestion key) are still prepared by `scripts/29_prepare_platform_runtime_inputs.sh` as a compatibility bridge until a fully declarative secret manager flow is adopted.

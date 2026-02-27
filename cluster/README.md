# Cluster Layer (GitOps)

This directory is the active declarative cluster layer. Flux owns ordering and reconciliation.

Structure:

- `flux/`: Flux bootstrap manifests and source definitions.
- `base/`: component HelmRelease definitions (`cert-manager`, `cert-manager/issuers`, `cilium`, `metrics-server`, `oauth2-proxy`, `clickstack`, `otel`, `storage/*`, `apps/example`).
- `overlays/homelab/`: default homelab composition (nginx + minio + app staging overlay).
- `overlays/homelab/apps/`: staging/prod app overlays (`apps-staging` / `apps-prod`).
- `overlays/homelab/providers/`: explicit ingress/storage provider overlays.
- `overlays/homelab/flux/`: Flux `Kustomization` dependency chain.

Operational note:

- Runtime input source and targets are declarative under `cluster/base/runtime-inputs`.
- The source secret (`flux-system/platform-runtime-inputs`) is external; sync it from local env/profile with `make runtime-inputs-sync` (used for runtime targets and Flux substitutions including issuer controls `ACME_EMAIL`, `LETSENCRYPT_ENV`, `LETSENCRYPT_*_SERVER`, and `PLATFORM_CLUSTER_ISSUER`).
- Active provider/app overlay selection is declarative in `cluster/overlays/homelab/flux/stack-kustomizations.yaml` (`homelab-ingress.path`, `homelab-storage.path`, `homelab-example-app.path`).

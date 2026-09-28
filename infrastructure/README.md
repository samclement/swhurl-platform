# Infrastructure Layer

Shared cluster infrastructure for the homelab cluster.

- core controllers (`cert-manager`)
- certificate issuers (`cert-manager/issuers/*`)
- ingress/storage provider resources
- shared non-app namespaces (`namespaces`)

Note:
- Active defaults use k3s packaged networking (`flannel`), ingress (`traefik`), and metrics (`metrics-server`).
- k3s packaged `metrics-server` and `traefik` are expected to remain enabled.
- k3s-packaged Traefik is configured declaratively via `infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml` (NodePorts pinned to `31514`/`30313`).
- Legacy provider manifests are removed from this repo.

Each directory is reconciled by its own Flux unit, listed in `clusters/home/infrastructure.yaml`; `cluster-base` groups namespaces and storage classes.

Certificate issuer for infrastructure ingresses comes from `CERT_ISSUER` in the `flux-system/platform-settings` ConfigMap ([`configmap-platform-settings.yaml`](../clusters/home/flux-system/sources/configmap-platform-settings.yaml)), substituted by Flux. Overview: [services](../docs/services.md).

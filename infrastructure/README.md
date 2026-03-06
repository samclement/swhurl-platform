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
- Legacy provider manifests are removed from this repo; `infrastructure/overlays/home` is the active composition.

Composition entrypoint: `infrastructure/overlays/home/kustomization.yaml`.

Certificate issuer for infrastructure ingresses is substituted via Flux post-build from:
- `flux-system/configmap-platform-settings`
- key: `CERT_ISSUER` (`letsencrypt-staging|letsencrypt-prod`)

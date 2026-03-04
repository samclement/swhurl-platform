# Infrastructure Layer

Shared cluster infrastructure for the homelab cluster.

- core controllers (`cert-manager`)
- certificate issuers (`cert-manager/issuers/*`)
- ingress/storage provider resources
- shared non-app namespaces (`namespaces`)

Note:
- Active defaults use k3s packaged networking (`flannel`), ingress (`traefik`), and metrics (`metrics-server`).
- k3s packaged `metrics-server` and `traefik` are expected to remain enabled.
- Flux-managed `infrastructure/metrics-server/base` and `infrastructure/ingress-nginx/base` are retained as legacy/optional manifests and are not part of the active `home` composition.

Composition entrypoint: `infrastructure/overlays/home/kustomization.yaml`.

Certificate issuer for infrastructure ingresses is substituted via Flux post-build from:
- `flux-system/configmap-platform-settings`
- key: `CERT_ISSUER` (`letsencrypt-staging|letsencrypt-prod`)

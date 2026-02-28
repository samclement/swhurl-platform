# Infrastructure Layer

Shared cluster infrastructure for the homelab cluster.

- networking and core controllers (`cilium`, `metrics-server`, `cert-manager`)
- certificate issuers (`cert-manager/issuers/*`)
- ingress/storage provider resources
- runtime-input target secrets (`runtime-inputs`)
- shared non-app namespaces (`namespaces`)

Composition entrypoint: `infrastructure/overlays/home/kustomization.yaml`.

Certificate issuer overlays:
- Default: `infrastructure/overlays/home` (uses `letsencrypt-staging` for infrastructure ingresses).
- Production cert issuer: `infrastructure/overlays/home-letsencrypt-prod` (patches infrastructure ingresses to `letsencrypt-prod`).

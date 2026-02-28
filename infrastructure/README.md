# Infrastructure Layer

Shared cluster infrastructure for the homelab cluster.

- networking and core controllers (`cilium`, `metrics-server`, `cert-manager`)
- certificate issuers (`cert-manager/issuers/*`)
- ingress/storage provider resources
- runtime-input target secrets (`runtime-inputs`)
- shared non-app namespaces (`namespaces`)

Composition entrypoint: `infrastructure/overlays/home/kustomization.yaml`.

Certificate issuer for infrastructure ingresses is substituted via Flux post-build from:
- `flux-system/configmap-platform-settings`
- key: `CERT_ISSUER` (`letsencrypt-staging|letsencrypt-prod`)

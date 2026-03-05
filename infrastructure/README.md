# Infrastructure Layer

Shared cluster infrastructure for the homelab cluster.

- core controllers (`cert-manager`)
- certificate issuers (`cert-manager/issuers/*`)
- ingress/storage provider resources (`traefik`, `minio`)
- automated DNS (`external-dns`)
- shared non-app namespaces (`namespaces`)

## Core Components

- **Ingress:** k3s-packaged Traefik, configured via `infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml` (NodePorts pinned to `31514`/`30313`).
- **DNS:** ExternalDNS managing Route53 A-records based on Ingress resources.
- **Certs:** cert-manager with ClusterIssuers for Let's Encrypt (staging/prod).
- **Storage:** MinIO for object storage.

## Composition

- Infrastructure Entrypoint: `infrastructure/overlays/home/kustomization.yaml`.
- Issuer Entrypoint: `infrastructure/overlays/home-issuers/kustomization.yaml`.

Certificate issuer for infrastructure ingresses is substituted via Flux post-build from:
- `flux-system/platform-settings`
- key: `CERT_ISSUER` (`letsencrypt-staging|letsencrypt-prod`)

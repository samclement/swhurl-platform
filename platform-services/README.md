# Platform Services Layer

Shared platform services deployed once per cluster.

- `oauth2-proxy`
- `clickstack`
- `otel`

Composition entrypoint: `platform-services/overlays/home/kustomization.yaml`.

Certificate issuer overlays:
- Default: `platform-services/overlays/home` (uses `letsencrypt-staging` for oauth2-proxy and clickstack ingresses).
- Production cert issuer: `platform-services/overlays/home-letsencrypt-prod` (patches oauth2-proxy and clickstack to `letsencrypt-prod`).

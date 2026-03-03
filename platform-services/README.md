# Platform Services Layer

Shared platform services deployed once per cluster.

- `oauth2-proxy`
- `oauth2-proxy-keycloak-canary` (suspended by default)
- `keycloak` (rollout-safe skeleton, suspended by default)
- `clickstack`
- `otel`

Composition entrypoint: `platform-services/overlays/home/kustomization.yaml`.

Certificate issuer for platform-service ingresses is substituted via Flux post-build from:
- `flux-system/configmap-platform-settings`
- key: `CERT_ISSUER` (`letsencrypt-staging|letsencrypt-prod`)

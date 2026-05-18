# Platform Services Layer

Shared platform services deployed once per cluster.

- `oauth2-proxy`
- `clickstack`
- `otel`

Composition entrypoint: `platform-services/overlays/home/kustomization.yaml`.

Runtime Secrets are SOPS-encrypted final Kubernetes Secret manifests co-located with the service that consumes them:
- `oauth2-proxy/base/secret-oauth2-proxy-shared.sops.yaml`
- `clickstack/base/secret-clickstack-runtime-inputs.sops.yaml`
- `otel/base/secret-hyperdx.sops.yaml`

Certificate issuer for platform-service ingresses is substituted via Flux post-build from:
- `flux-system/configmap-platform-settings`
- key: `CERT_ISSUER` (`letsencrypt-staging|letsencrypt-prod`)

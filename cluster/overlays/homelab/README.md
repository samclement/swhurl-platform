# Homelab Overlay

Environment-specific composition layer:
- selects ingress/storage providers
- applies domain/TLS/value overrides
- supports staging/prod app overlay separation (`apps-staging`, `apps-prod`)
- supports optional platform overlay examples under `platform/prod` (default homelab flow uses `PLATFORM_CLUSTER_ISSUER` toggle)

Default `kustomization.yaml` composes:
- base platform components
- ingress-nginx provider overlay
- minio storage provider overlay
- staging app overlay

Use `providers/*` and `apps/*` overlays when switching providers or promoting environments.

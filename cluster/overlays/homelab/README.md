# Homelab Overlay

Environment-specific composition layer:
- selects ingress/storage providers
- applies domain/TLS/value overrides
- supports staging/prod app overlay separation (`apps-staging`, `apps-prod`)
- supports staging/prod platform overlay separation (`platform/staging`, `platform/prod`)

Default `kustomization.yaml` composes:
- base platform components
- ingress-nginx provider overlay
- minio storage provider overlay
- staging app overlay

Use `providers/*` and `apps/*` overlays when switching providers or promoting environments.

# Homelab Overlay

Environment-specific composition layer:
- selects ingress/storage providers
- applies domain/TLS/value overrides
- supports staging/prod app overlay separation (`apps-staging`, `apps-prod`)
- supports staging/prod platform overlay separation (`platform/staging`, `platform/prod`)

Use `providers/*` folders to toggle implementation choices without changing base components.
Select exactly one ingress overlay and one storage overlay when wiring this into an active
GitOps stack.

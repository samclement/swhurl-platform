# Base Component: Example App

Reference app layer for platform integration.

- Base resources are plain manifests (`Deployment`, `Service`, `Ingress`, `Certificate`).
- Base ingress auth is applied in overlays via shared `ingress` namespace Traefik middleware (`oauth-auth-shared`) from `platform-services/oauth2-proxy/base`.
- Base defaults to staging URL + staging issuer.
- Staging/prod overlays set URL/namespace, and both override certificate issuer to `letsencrypt-prod`.

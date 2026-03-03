# Base Component: Example App

Reference app layer for platform integration.

- Base resources are plain manifests (`Deployment`, `Service`, `Ingress`, `Certificate`).
- Base includes `CiliumNetworkPolicy` for `hello-web` to enable Hubble L7 HTTP visibility on port `80`.
- L7 observe policy uses `fromEntities: [cluster]` so ingress-controller traffic from other namespaces is allowed.
- Base ingress auth is applied in overlays via shared `ingress` namespace Traefik middleware (`oauth-auth`) from `platform-services/oauth2-proxy/base`.
- Base defaults to staging URL + staging issuer.
- Staging/prod overlays set URL/namespace, and both override certificate issuer to `letsencrypt-prod`.

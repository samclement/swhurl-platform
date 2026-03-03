# Base Component: Example App

Reference app layer for platform integration.

- Base resources are plain manifests (`Deployment`, `Service`, `Ingress`, `Certificate`).
- Base includes `CiliumNetworkPolicy` for `hello-web` to enable Hubble L7 HTTP visibility on port `80`.
- L7 observe policy uses `fromEntities: [cluster]` so ingress-controller traffic from other namespaces is allowed.
- Base includes a Traefik oauth2-proxy `forwardAuth` middleware (`oauth2-proxy.ingress.svc.cluster.local`).
- Base defaults to staging URL + staging issuer.
- Staging/prod overlays set URL/namespace, and both override certificate issuer to `letsencrypt-prod`.

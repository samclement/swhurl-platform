# Base Component: Example App

Reference app layer for platform integration.

- Base resources are plain manifests (`Deployment`, `Service`, `Ingress`, `Certificate`).
- Base defaults to staging URL + staging issuer.
- Tenant overlays select URL/issuer combinations by path (`tenants/apps/example/overlays/*`).

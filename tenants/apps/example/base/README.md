# Base Component: Example App

Reference app layer for platform integration.

- Base resources are plain manifests (`Deployment`, `Service`, `Ingress`, `Certificate`).
- Default stack points directly to this base path.
- `homelab-example-app` receives `${APP_NAMESPACE}`, `${APP_HOST}`, `${APP_CLUSTER_ISSUER}`, and `${OAUTH_HOST}` via Flux `postBuild.substituteFrom` from `flux-system/platform-runtime-inputs`.

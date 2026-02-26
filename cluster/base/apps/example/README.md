# Base Component: Example App

Populate with the reference application that demonstrates platform integration.
The flux stack entry `homelab-example-app` is suspended by default.

Current scaffold:

- `helmrelease-hello-web.yaml`: suspended Flux `HelmRelease` pointing at `./charts/apps-hello`.
  - Base namespace default is `apps-staging` with `tls.issuerName=letsencrypt-staging`.
  - Use `cluster/overlays/homelab/apps/staging` or `cluster/overlays/homelab/apps/prod`
    to express environment-specific namespace/issuer intent.

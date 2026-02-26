# Ingress Provider: NGINX

Compatibility overlay while clusters transition away from ingress-nginx.

Current scaffold:

- `helmrelease-ingress-nginx.yaml`: suspended Flux `HelmRelease` matching current
  ingress-nginx chart/version and logging defaults.

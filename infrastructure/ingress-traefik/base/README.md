# Ingress Provider: Traefik

Optional Flux-managed Traefik provider overlay.

Native k3s Traefik is the default ingress path in this repo; this folder is reserved for
targeted Flux ownership where needed.

Current usage:
- `helmchartconfig-traefik.yaml` declaratively configures the k3s-packaged Traefik chart.
- NodePorts are pinned to preserve edge-router compatibility:
  - HTTP `80 -> 31514`
  - HTTPS `443 -> 30313`

# Homelab Platform Overlays

Environment overlays for platform components managed by Flux.

- Base component manifests in `cluster/base/*` are the default path and carry staging
  issuer intent (`letsencrypt-staging`) for repeatable cycles.
- `prod/`: promotion overlays that patch platform ingress annotations/hosts to
  `letsencrypt-prod` once staging is stable.

These overlays patch component-level Flux `HelmRelease` resources from `cluster/base/*`
without changing base ownership boundaries.

# Homelab Platform Overlays

Environment overlays for platform components managed by Flux.

- Base component manifests in `cluster/base/*` are the default path and use
  `${PLATFORM_CLUSTER_ISSUER}` substitution (default config value is
  `letsencrypt-staging`).
- `prod/`: optional overlay examples that patch platform ingress annotations/hosts.
  Default homelab flow uses a single platform issuer toggle (`PLATFORM_CLUSTER_ISSUER`)
  without switching platform overlay paths.

These overlays patch component-level Flux `HelmRelease` resources from `cluster/base/*`
without changing base ownership boundaries.

Platform cert toggle workflow:
- Configure `PLATFORM_CLUSTER_ISSUER=letsencrypt-staging|letsencrypt-prod`.
- Run `make runtime-inputs-sync` then `make flux-reconcile`.
- See `docs/runbooks/promote-platform-certs-to-prod.md`.

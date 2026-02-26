# Homelab Platform Overlays

Environment overlays for platform components managed by Flux.

- `staging/`: default migration target for repeatable build/destroy and integration testing.
- `prod/`: promotion target once staging behavior is stable.

These overlays patch component-level Flux `HelmRelease` resources from `cluster/base/*`
without changing base ownership boundaries.

# Homelab App Overlays

Environment overlays for regular applications using platform capabilities.

- `staging/`: deploy app workloads to `apps-staging` and default to `letsencrypt-staging`.
- `prod/`: deploy app workloads to `apps-prod` and default to `letsencrypt-prod`.

These overlays are scaffolds during migration from the legacy Helmfile pipeline.

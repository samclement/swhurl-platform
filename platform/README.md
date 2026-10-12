# Platform services

Shared services that apps use or people visit, one directory and one Flux unit (`platform-<directory>`) each, defined in [`clusters/home/platform.yaml`](../clusters/home/platform.yaml) (`notify` in [`platform-notify.yaml`](../clusters/home/platform-notify.yaml)); the list is in [architecture](../docs/architecture.md#flux-units). One that needs a Secret keeps it encrypted beside it as `secret.sops.yaml`. What they do and how they are configured: [services](../docs/services.md).

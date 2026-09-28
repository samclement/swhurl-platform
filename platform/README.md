# Platform services

Shared services that apps use or people visit, one directory and one Flux unit (`platform-<directory>`) each, defined in [`clusters/home/platform.yaml`](../clusters/home/platform.yaml): `oauth2-proxy` (sign-in), `clickstack`, `otel`, `reloader`. Each keeps its encrypted Secret beside it as `secret.sops.yaml`. What they do and how they are configured: [services](../docs/services.md).

# Platform Services

Shared services, one Flux unit each (listed in `clusters/home/platform.yaml`): `oauth2-proxy` (sign-in), `clickstack`, `otel`, `reloader`. Each keeps its encrypted Secret beside it in `*/base/*.sops.yaml`. What they do and how they are configured: [services](../docs/services.md).

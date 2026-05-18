# Base Component: OTel Collectors

Active Flux-owned standalone OTel collector releases.

- Runtime ingestion key lives in `secret-hyperdx.sops.yaml`.
- The rendered Secret is `logging/hyperdx-secret`.
- OTel pods consume `HYPERDX_API_KEY` through `secretKeyRef` environment variables.
- After ClickStack first-login setup, copy the Ingestion API key from the ClickStack admin UI into this Secret and run `make runtime-inputs-refresh-otel`.
- Secret environment variables do not hot-reload; collector pods must restart after key rotation.


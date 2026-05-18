# Base Component: ClickStack

Active Flux-owned ClickStack release definition.

- Runtime bootstrap/app API key lives in `secret-clickstack-runtime-inputs.sops.yaml`.
- The HelmRelease passes `CLICKSTACK_API_KEY` to `hyperdx.apiKey`.
- The ClickStack chart renders that value into `observability/clickstack-app-secrets.api-key`.
- This key is distinct from the standalone OTel collector ingestion key after first-login setup.


# Base Component: ClickStack

Active Flux-owned ClickStack release definition.

- Runtime bootstrap key lives in `secret-clickstack-runtime-inputs.sops.yaml` as `CLICKSTACK_API_KEY`.
- The HelmRelease passes it to `hyperdx.apiKey`; the chart seeds MongoDB with this value as the team API key on a fresh install.
- The chart also renders it into `observability/clickstack-app-secrets.api-key` on every deploy.
- **`CLICKSTACK_API_KEY` is not the live ingestion key.** After first-login setup, MongoDB owns the ingestion key (`hyperdx.teams.apiKey`). It persists across redeployments as long as MongoDB data survives.
- The live ingestion key is what `HYPERDX_API_KEY` in `platform-services/otel/base/secret-hyperdx.sops.yaml` must match. See that README for the rotation procedure.

## When MongoDB data is lost (full reinstall)

On a fresh ClickStack deploy (MongoDB wiped), the new team's ingestion key will be seeded from `CLICKSTACK_API_KEY`. After first-login, retrieve the active ingestion key from the ClickStack UI or MongoDB and update `HYPERDX_API_KEY` accordingly — see `platform-services/otel/base/README.md`.


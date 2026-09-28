# OTel collectors

Two standalone collector releases in `logging` (a per-node DaemonSet and a cluster Deployment) that send telemetry to ClickStack. Overview and key relationships: [services](../../docs/services.md#clickstack-and-otel).

- `secret.sops.yaml` → `logging/hyperdx-secret.HYPERDX_API_KEY`, read at container start. It must equal the ClickStack team ingestion key held in MongoDB, **not** `CLICKSTACK_API_KEY`.
- Both collectors opt in to Reloader, so they restart when the Secret changes.
- The HelmReleases reference the key as `$${env:HYPERDX_API_KEY}`; the `platform-otel` unit's Flux substitution turns that into `${env:...}`.

## Rotate the ingestion key

1. Copy the team's ingestion key from the ClickStack UI (do not print it from MongoDB into logs).
2. `sops platform/otel/secret.sops.yaml` and set `data.HYPERDX_API_KEY` to the key base64-encoded **once** (`printf %s '<key>' | base64 -w0`).
3. `make check-secrets`, commit, push, `make runtime-inputs-sync`.
4. `make verify-platform` compares the live Secret with the team key by bytes; then check the collector logs have no HTTP 401 errors.

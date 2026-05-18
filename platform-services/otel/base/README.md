# Base Component: OTel Collectors

Active Flux-owned standalone OTel collector releases.

- Runtime ingestion key lives in `secret-hyperdx.sops.yaml` as `HYPERDX_API_KEY`.
- The rendered Secret is `logging/hyperdx-secret`; pods consume it via `secretKeyRef`.
- **`HYPERDX_API_KEY` must match the live MongoDB ingestion key** — `hyperdx.teams.apiKey` in the ClickStack MongoDB instance. This is NOT the same as `CLICKSTACK_API_KEY` (the Helm bootstrap key); see `platform-services/clickstack/base/README.md`.
- Secret environment variables do not hot-reload; collector pods must restart after key rotation.

## Verifying sync

```
make verify-platform
```

Compares `logging/hyperdx-secret.HYPERDX_API_KEY` against the live MongoDB `hyperdx.teams.apiKey`. A mismatch means telemetry is being silently dropped.

## Rotating the ingestion key

1. Get the current ingestion key from ClickStack (MongoDB is the source of truth):
   ```
   kubectl -n observability exec deploy/clickstack-mongodb -- \
     mongosh hyperdx --quiet --eval "db.teams.findOne({}, {apiKey:1, _id:0})"
   ```

2. Edit the SOPS secret and set `HYPERDX_API_KEY` to that value:
   ```
   SOPS_AGE_KEY_FILE=./age.agekey sops platform-services/otel/base/secret-hyperdx.sops.yaml
   ```

3. Commit and push.

4. Apply to the cluster and verify:
   ```
   make runtime-inputs-refresh-otel
   make verify-platform
   ```


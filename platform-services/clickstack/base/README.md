# Base Component: ClickStack

Active Flux-owned ClickStack release definition.

- Runtime bootstrap key lives in `secret-clickstack-runtime-inputs.sops.yaml` as `CLICKSTACK_API_KEY`.
- The HelmRelease passes it to `hyperdx.apiKey`; the chart seeds MongoDB with this value as the team API key on a fresh install.
- The chart also renders it into `observability/clickstack-app-secrets.api-key` on every deploy.
- **`CLICKSTACK_API_KEY` is not the live ingestion key.** After first-login setup, MongoDB owns the ingestion key (`hyperdx.teams.apiKey`). It persists across redeployments as long as MongoDB data survives.
- The live ingestion key is what `HYPERDX_API_KEY` in `platform-services/otel/base/secret-hyperdx.sops.yaml` must match; see the [OTel README](../../otel/base/README.md) to rotate it.
- Known issue: `CLICKSTACK_API_KEY` is stored base64-encoded twice ([services](../../../docs/services.md#clickstack-and-otel)).

## Retention

- Telemetry tables (`otel_*`, `hyperdx_sessions`) expire after 30 days. That TTL comes from the ClickStack collector image when it creates the tables, not from chart values; `make verify-platform` fails if it changes.
- ClickHouse's own diagnostic logs (`system.query_log`, `trace_log`, `metric_log`, ...) expire after 7 days via `configmap-clickhouse-system-log-ttl.yaml`, mounted into `config.d` by the HelmRelease `postRenderers` patch because the chart's `config.xml` is not configurable. ClickHouse reads it only at startup: restart `deploy/clickstack-clickhouse` after editing. When a table definition changes, ClickHouse renames the old table to `<name>_N` (without TTL); drop those once checked.
- `global.keepPVC: true` puts `helm.sh/resource-policy: keep` on all three PVCs, so a Helm uninstall leaves them.
- MongoDB is backed up with `make backup-mongodb`; see [backups and recovery](../../../docs/operations.md#backups-and-recovery).

## When MongoDB data is lost (full reinstall)

Restore the latest backup first (see [backups and recovery](../../../docs/operations.md#backups-and-recovery)). If none exists:

On a fresh ClickStack deploy (MongoDB wiped), the new team's ingestion key will be seeded from `CLICKSTACK_API_KEY`. After first-login, retrieve the active ingestion key from the ClickStack UI or MongoDB and update `HYPERDX_API_KEY` ([OTel README](../../otel/base/README.md)).


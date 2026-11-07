# ClickStack adapter fixtures

Listings in the shape HyperDX's external API (`/api/v2`, `@hyperdx/api` 2.39.1) answers them, with
the server's own fields the platform does not write (`fillNulls`, an empty `valueExpression`,
timestamps, `state`). Values are synthetic, not a live export.

- `sources.json`: a trace, a log and a metric source.
- `webhooks.json`: a reserved webhook and someone's Slack webhook, with `meta.total`.
- `dashboards.json`: an app dashboard and someone's board whose tile does not decode as the
  platform writes tiles (`x` is not a whole number, `config` has no `select`).
- `alerts.json`: an app alert and someone's saved-search alert with a fractional threshold.

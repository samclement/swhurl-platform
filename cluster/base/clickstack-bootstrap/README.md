# Base Component: clickstack-bootstrap

Flux-owned one-time ClickStack team bootstrap Job.

This runs only after the ClickStack HelmRelease is healthy (via Flux `dependsOn`)
and creates the initial team/user if `/installation` reports no team yet.

Inputs are projected by runtime-inputs into:

- `observability/clickstack-bootstrap-inputs`

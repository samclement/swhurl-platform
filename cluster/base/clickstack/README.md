# Base Component: clickstack

Active Flux-owned ClickStack (ClickHouse + HyperDX) release definition.

The one-time team bootstrap Job is managed separately in
`cluster/base/clickstack-bootstrap` so Flux can enforce ordering:
`clickstack -> clickstack-bootstrap -> otel`.

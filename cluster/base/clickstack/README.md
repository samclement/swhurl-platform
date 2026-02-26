# Base Component: clickstack

Target location for ClickStack (ClickHouse + HyperDX) base resources during GitOps
migration.

Current scaffold:

- `helmrelease-clickstack.yaml`: suspended Flux `HelmRelease` matching the current chart/version path.

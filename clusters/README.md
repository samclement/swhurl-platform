# Clusters

Flux entrypoint for the `home` cluster. Units and their dependencies: [architecture](../docs/architecture.md#flux-units).

- `home/flux-system/`: root units and sources, applied by `make flux-bootstrap` (not reconciled by Flux itself)
- `home/flux-system/sources/`: `GitRepository`, `HelmRepository` objects and `platform-settings`
- `home/infrastructure.yaml`, `home/platform.yaml`: one Flux unit per shared capability
- `home/app-<app>-<env>.yaml`: one Flux unit per app instance, written by `make app-new`
- `home/kustomization.yaml`: the list of active units

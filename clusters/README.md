# Clusters

Flux cluster entrypoints live here.

- `home/flux-system`: bootstrap manifests applied by `make flux-bootstrap`
- `home/flux-system/sources`: Flux source objects (`GitRepository`, `HelmRepository`)
- `home/infrastructure.yaml`: shared infrastructure layer
- `home/platform.yaml`: shared platform-services layer
- `home/tenants.yaml`: tenant/app-environment layer

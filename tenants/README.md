# Tenants

App instances live in `apps/<app>/<env>/`, one per app and environment, each with its own namespace (`<app>-<env>`) and Flux unit (`clusters/home/app-<app>-<env>.yaml`). Create them with `make app-new`; see [apps](../docs/apps.md).

Current instances: `apps/hello/staging` (`staging-hello.homelab.swhurl.com`) and `apps/hello/prod` (`hello.homelab.swhurl.com`).

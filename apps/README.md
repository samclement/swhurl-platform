# Apps

App instances live in `<app>/<env>/`, one per app and environment, each with its own namespace (`<app>-<env>`) and Flux unit (`clusters/home/app-<app>-<env>.yaml`). Create them with `make app-new`; see [apps](../docs/apps.md).

Current instances: `hello/staging` (`staging-hello.homelab.swhurl.com`) and `hello/prod` (`hello.homelab.swhurl.com`).

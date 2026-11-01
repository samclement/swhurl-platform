# Apps

The team registry lives here (`teams.yaml`). An instance is not a folder here any more: what it is lives in `v2-records/<app>/<env>/` and what that compiles to in `v2-apps/instances/<app>/<env>/`, beside its Flux unit `clusters/home/app-<app>-<env>.json`. Start a new app with `make app-repo` and the `make app-new` line it prints, then `swhurl-v2 migrate`; see [apps](../docs/apps.md#start-a-new-app).

The apps to read first: [the demo apps](../docs/apps.md#the-demo-apps).

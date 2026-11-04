# Apps

Nothing is kept here any more. The team registry is v2's, `v2-records/teams.json` ([teams](../docs/apps.md#teams-and-isolation)), and an instance is not a folder here: what it is lives in `v2-records/<app>/<env>/` and what that compiles to in `v2-apps/instances/<app>/<env>/`, beside its Flux unit `clusters/home/app-<app>-<env>.json`. Start a new app with `make app-repo`, which prints the `swhurl-v2 create` line that makes its staging instance; see [apps](../docs/apps.md#start-a-new-app).

The apps to read first: [the demo apps](../docs/apps.md#the-demo-apps).

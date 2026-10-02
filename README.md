# Swhurl Platform

GitOps source for a single-node k3s homelab. Flux reconciles everything in this repo onto the cluster: shared infrastructure (cert-manager, Traefik settings, storage), shared services (Google sign-in, ClickStack observability, OpenTelemetry collectors, Reloader) and app instances such as `hello.homelab.swhurl.com`. A signed-in web console at `console.homelab.swhurl.com` shows it all and turns changes into pull requests.

The cluster is live. What has been verified on it, and when, is in [current state](docs/current-state.md); what is left to build is in [the plan](docs/plan.md), section 0.

## Make a change

Every change goes through Git; Flux applies what is on `main` within seconds of a push. Other ways in (console and Renovate pull requests, the console's own deploys, the few operator commands that write to the cluster directly): [how changes reach the cluster](docs/architecture.md#how-changes-reach-the-cluster).

```bash
make check                       # offline checks, the same as CI
git commit -am "..." && git push
make flux-reconcile              # optional: waits until every unit is Ready at your commit (the push webhook already started it)
make verify-platform             # expect "Validation passed."
```

`make check` needs a few pinned tools; see [contributing](docs/contributing.md#validation). There is no whole-platform teardown or reinstall: `make destroy-data` is the only command that deletes data ([lifecycle](docs/operations.md#lifecycle)).

## Where to go next

| I want to… | Read |
| --- | --- |
| Start a new app: repository, code and staging, from the console or `make app-repo` | [Start a new app](docs/apps.md#start-a-new-app) |
| Deploy a new version, or put an app in production | [Deploy a new image](docs/apps.md#deploy-a-new-image), [add production](docs/apps.md#add-production) |
| Change, operate or remove an app (its whole life) | [Apps](docs/apps.md) |
| See apps and Flux units, or change them from a browser | [Console](docs/console.md) |
| Understand how the console deploys itself | [Deploy a new console](docs/console.md#deploy-a-new-console) |
| Operate, rotate a Secret, back up or troubleshoot | [Operations](docs/operations.md) |
| Understand or review Renovate chart update PRs | [Chart updates](docs/operations.md#chart-updates) |
| Look up a `make` target | [Commands](docs/commands.md) |
| Understand a shared service, its settings or Secrets | [Services](docs/services.md) |
| See how the pieces depend on and own each other, and how a commit becomes running pods | [Architecture](docs/architecture.md) |
| Build the platform on a bare host | [Bootstrap](docs/bootstrap.md) |
| Change the repo safely (tests, fixtures, docs) | [Contributing](docs/contributing.md) |

## Layout

| Path | Contents |
| --- | --- |
| `clusters/home/` | Flux entrypoint: sources, settings and one Flux unit per capability or app instance |
| `infra/` | Namespaces, storage classes, cert-manager, issuers, Traefik settings |
| `platform/` | oauth2-proxy (sign-in), ClickStack and its MongoDB/ClickHouse operators, OTel collectors, Reloader, the web console, with their encrypted Secrets |
| `apps/<app>/<env>/` | Generated app instances (each app's code lives in its own repository, made from a [stack template](docs/apps.md#stacks-and-features)) |
| `images/` | Container images built from this repo (the console) |
| `tools/swhurl/`, `host/`, `tests/` | Python operator tooling and the web console's code; host systemd timers for dynamic DNS and the daily MongoDB backup (bash); offline tests and fixtures |

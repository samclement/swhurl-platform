# Swhurl Platform

GitOps source for a single-node k3s homelab. Flux reconciles everything in this repo onto the cluster: shared infrastructure (cert-manager, Traefik settings, storage), shared services (Google sign-in, ClickStack observability, OpenTelemetry collectors, Reloader) and app instances such as `hello.homelab.swhurl.com`.

The cluster is live. What has been verified on it, and when, is in [current state](docs/operations/current-state.md). The [implementation plan](Swhurl-platform-implementation-plan.md) is paused; its section 0 lists what is left.

## Make a change

Every change goes through Git; Flux applies what is on `main`.

```bash
make validate-repo test-safety   # offline checks, the same as CI
git commit -am "..." && git push
make flux-reconcile              # apply now and wait, instead of waiting for Flux to poll Git (every minute)
make verify-platform             # expect "Validation passed."
```

`make validate-repo` needs a few pinned tools; see [contributing](docs/contributing.md#validation). There is no whole-platform teardown: `make teardown` and `make reinstall` refuse to run, and `make destroy-data` is the only command that deletes data ([lifecycle](docs/operations.md#lifecycle)).

## Where to go next

| I want to… | Read |
| --- | --- |
| Deploy an app or change one | [Apps](docs/apps.md) |
| Operate, rotate a Secret, back up or troubleshoot | [Operations](docs/operations.md) |
| Look up a `make` target | [Commands](docs/commands.md) |
| Understand a shared service, its settings or Secrets | [Services](docs/services.md) |
| See how the pieces depend on and own each other | [Architecture](docs/architecture.md) |
| Build the platform on a bare host | [Bootstrap](docs/bootstrap.md) |
| Change the repo safely (tests, fixtures, docs) | [Contributing](docs/contributing.md) |

## Layout

| Path | Contents |
| --- | --- |
| `clusters/home/` | Flux entrypoint: sources, settings and one Flux unit per capability or app instance |
| `infrastructure/` | Namespaces, storage classes, cert-manager, issuers, Traefik settings |
| `platform-services/` | oauth2-proxy (sign-in), ClickStack, OTel collectors, Reloader, with their encrypted Secrets |
| `tenants/apps/<app>/<env>/` | Generated app instances |
| `tools/swhurl/`, `scripts/`, `host/`, `tests/` | Python operator tooling, remaining bash scripts, host DNS updater, offline tests and fixtures |

# Tenants

## Overview

The tenant model is split into two layers:

- [`tenants/app-envs`](../tenants/app-envs): shared landing zones for tenant environments
- [`tenants/apps`](../tenants/apps): application manifests and overlays

The concepts and ownership contracts are defined in [architecture](architecture.md#concepts-and-boundaries). An application instance means one app in one environment; the current example still combines both instances in one Flux unit.

The active example app is reconciled separately through [`clusters/home/app-example.yaml`](../clusters/home/app-example.yaml), which points to [`tenants/apps/example`](../tenants/apps/example).

## Current Architecture

### Landing zones

Current tenant namespaces are:

- `apps-staging`
- `apps-prod`

They are defined in:

- [`tenants/app-envs/staging`](../tenants/app-envs/staging)
- [`tenants/app-envs/prod`](../tenants/app-envs/prod)

### Example application

The example app uses:

- base manifests in [`tenants/apps/example/base`](../tenants/apps/example/base)
- environment overlays in [`tenants/apps/example/overlays/staging`](../tenants/apps/example/overlays/staging) and [`tenants/apps/example/overlays/prod`](../tenants/apps/example/overlays/prod)

Current hosts:

- staging: `staging-hello.homelab.swhurl.com`
- prod: `hello.homelab.swhurl.com`

Shared auth is applied at the ingress layer through the Traefik middleware reference:

- `ingress-oauth-auth-shared@kubernetescrd`

## Getting Started

### Add a new landing zone

1. Add a namespace manifest under `tenants/app-envs/<env>`.
2. Add that path to the relevant tenant Kustomization if needed.
3. Reconcile with `make flux-reconcile`.

### Add a new app

New app instances use the generator and the pinned [bjw-s app-template](https://bjw-s-labs.github.io/helm-charts/docs/app-template/) chart (`5.2.1`, HelmRepository `bjw-s`). One instance is one app in one environment, with its own namespace (`<app>-<env>`) and Flux unit (`homelab-app-<app>-<env>`):

```bash
make app-new NAME=hello ARGS="--env staging --exposure authenticated-web \
  --host hello-next.homelab.swhurl.com --image docker.io/nginxinc/nginx-unprivileged:1.27-alpine \
  --uid 101 --health-path /"
make app-policy
git add tenants/apps/hello clusters/home && git commit -m "apps: add hello staging" && git push
make flux-reconcile
```

It writes `tenants/apps/<app>/<env>/` (Namespace, HelmRelease, Kustomization, optional encrypted Secret) and `clusters/home/app-<app>-<env>.yaml`, and registers the unit. Run `python3 scripts/app-new.py --help` for all options. The output is plain YAML; edit it afterwards like any manifest.

| Choice | Options and rules |
| --- | --- |
| `--kind` | `web` (Service + probes on `--health-path`, required) or `worker` (no Service, no route) |
| `--exposure` | `private` (no route; default), `authenticated-web` (sign-in middleware; host under `homelab.swhurl.com`), `public` (no sign-in; host must be **outside** `homelab.swhurl.com`, so the shared sign-in cookie never reaches it) |
| `--image` | `repo:tag`, `repo@sha256:…` or both; no `latest`; **prod requires a digest** |
| `--persistence SIZE` | Adds a claim on `local-path-retain`, kept on Helm uninstall; the namespace is marked never-prune |
| `--secret-keys A,B` | Adds a SOPS-encrypted Secret stub (values `REPLACE_ME`), injects it with `envFrom`, sets the app unit's decryption and opts the workload into Reloader (adding the namespace to Reloader's watch list). Edit real values with `sops tenants/apps/<app>/<env>/secret.sops.yaml` |

Every instance gets these defaults: non-root (`--uid`, default 65532), no service-account token, all capabilities dropped, read-only root filesystem with a writable `/tmp`, small CPU/memory requests and a memory limit. The generator refuses to overwrite an existing instance.

`make app-policy` renders every instance with Helm and checks the result: pinned images (digest in prod), non-root, no privilege escalation, resource requests/limits, no service-account token, no host access, exposure rules (private has no Ingress; routes under `homelab.swhurl.com` need sign-in; public stays outside it), TLS on every route, and a named storage class. A reviewed exception goes on the HelmRelease as `platform.swhurl.com/policy-exceptions: "rule-id=reason"`. CI runs it on every push.

`make app-template-test` deploys the three generated fixtures (`tests/fixtures/apps`: a worker, an authenticated web app with a Secret, a persistent prod app) through real Flux units, checks them, and removes them.

### Reuse shared edge authentication

For Traefik ingress auth, reference the shared middleware from the app ingress:

```yaml
traefik.ingress.kubernetes.io/router.middlewares: ingress-oauth-auth-shared@kubernetescrd
```

The example app overlays are the current reference implementation.

## Current Constraints

- The tenants landing-zone layer only creates namespaces. App deployment is handled by separate Flux Kustomizations.
- The example app predates the generator: it is raw manifests in the shared `apps-staging`/`apps-prod` namespaces, both in one Flux unit. PR05 migrates it; `make app-policy` covers generated instances only.
- There is no generic tenant contract document for quotas, network policies, or RBAC defaults.
- App hostnames are currently hardcoded in the example overlays, not derived from `config.env`.

## Caveats

- The example app base defaults to staging-oriented values, but both staging and prod overlays currently override the certificate issuer to `letsencrypt-prod`.
- Shared auth depends on the `ingress` namespace middleware created by the shared oauth2-proxy service. If that middleware is absent, tenant ingresses that reference it will fail.
- The current tenant model assumes one cluster with shared platform services and environment-specific namespaces rather than hard isolation between tenants.
- Additional tenant apps require explicit Flux wiring in `clusters/home`; adding manifests under `tenants/apps` alone does not deploy them.

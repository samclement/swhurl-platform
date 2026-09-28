# Tenants

## Overview

An **app instance** is one app in one environment. Each instance owns its namespace (`<app>-<env>`), an app-template HelmRelease, optional encrypted Secret, and its own Flux unit `homelab-app-<app>-<env>` (`clusters/home/app-<app>-<env>.yaml` → `tenants/apps/<app>/<env>`). Instances depend only on `homelab-cluster-base` and, when signed-in, `homelab-auth`, so one instance's failure never blocks another. Concepts and ownership: [architecture](architecture.md#concepts-and-boundaries).

## Current instances

| Instance | Host | Namespace | Image |
| --- | --- | --- | --- |
| `hello/staging` | `staging-hello.homelab.swhurl.com` | `hello-staging` | `nginxinc/nginx-unprivileged:1.27-alpine` pinned by digest |
| `hello/prod` | `hello.homelab.swhurl.com` | `hello-prod` | same digest |

Both are `authenticated-web` (shared sign-in) and serve the stock nginx page as UID 101 on port 8080. Staging and production currently differ only in namespace and host: same image digest, same issuer (`letsencrypt-prod`) and same sign-in. `staging` is a separate failure and rollout boundary, not a separate trust boundary.

## Operate an instance

```bash
make app-status APP=hello ENV=prod     # desired vs applied revision and digest, replicas, route, TLS, failing containers
make app-logs APP=hello ENV=prod       # FOLLOW=true to stream, TAIL=N lines
make app-reconcile APP=hello ENV=prod  # fetch Git and reconcile only this instance
make app-check APP=hello ENV=prod      # policy check for this instance, offline
```

`make install` stays the whole-platform bootstrap/verify path.

## Getting Started

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

- There are no per-instance quotas, NetworkPolicies or RBAC defaults yet; namespaces separate failures and ownership, not trust.
- Hostnames are literal in each instance's HelmRelease, not derived from `config.env`.
- Staging and production share the image digest by convention; promoting a new digest is a manual edit until PR06 (Renovate).

## Caveats

- `authenticated-web` depends on the `ingress/oauth-auth-shared` middleware from `homelab-auth`; its app units wait for that unit.
- Moving a host between instances causes a few seconds of Traefik's default certificate while cert-manager issues the new one.
- Adding files under `tenants/apps` alone deploys nothing: the instance's unit must be listed in `clusters/home/kustomization.yaml` (`make app-new` does this).

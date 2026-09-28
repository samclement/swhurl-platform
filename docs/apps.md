# Apps

An **app instance** is one app in one environment: namespace `<app>-<env>`, a HelmRelease of the pinned [bjw-s app-template](https://bjw-s-labs.github.io/helm-charts/docs/app-template/) chart (5.2.1), an optional encrypted Secret, and its own Flux unit `homelab-app-<app>-<env>`. Instances wait only for `homelab-cluster-base` and, when signed-in, `homelab-auth`; a broken instance never blocks another, and an observability outage never blocks an app.

## Current instances

| Instance | Host | Image |
| --- | --- | --- |
| `hello/staging` | `staging-hello.homelab.swhurl.com` | `nginxinc/nginx-unprivileged:1.27-alpine`, pinned by digest |
| `hello/prod` | `hello.homelab.swhurl.com` | same digest |

Both require sign-in and serve the stock nginx page as UID 101 on port 8080. Staging and production differ only in namespace and host: staging is a separate rollout and failure boundary, not a separate trust boundary.

## Add an app

```bash
make app-new NAME=weather-api ARGS="--env staging --image ghcr.io/me/weather-api:1.4.0 \
  --exposure authenticated-web --host weather.homelab.swhurl.com --health-path /ready \
  --secret-keys API_TOKEN,DB_URL"
sops tenants/apps/weather-api/staging/secret.sops.yaml     # replace the REPLACE_ME values
make app-policy secrets-check
git add tenants/apps/weather-api clusters/home platform-services/reloader
git commit -m "apps: add weather-api staging" && git push
make flux-reconcile && make app-status APP=weather-api ENV=staging
```

The generator ([`scripts/app-new.py`](../scripts/app-new.py), `--help` for all options) writes `tenants/apps/<app>/<env>/` and `clusters/home/app-<app>-<env>.yaml`, and registers the unit in `clusters/home/kustomization.yaml`. Files under `tenants/apps` deploy nothing until that registration exists. The output is plain YAML; edit it like any manifest afterwards.

| Option | Rules |
| --- | --- |
| `--kind` | `web` (Service and probes on `--health-path`, required) or `worker` (no Service, no route) |
| `--exposure` | `private` (no route, default); `authenticated-web` (sign-in, host under `homelab.swhurl.com`); `public` (no sign-in, host **outside** `homelab.swhurl.com` so the sign-in cookie never reaches it) |
| `--image` | `repo:tag`, `repo@sha256:…` or both; no `latest`; **production requires a digest** |
| `--persistence SIZE` | A claim on `local-path-retain`, kept on Helm uninstall; the namespace is never pruned |
| `--secret-keys A,B` | An encrypted Secret stub (`stringData`, values `REPLACE_ME`) injected with `envFrom`; sets the unit's decryption and adds the namespace to Reloader so changes restart the app |
| `--uid`, `--port`, `--cpu`, `--memory`, `--memory-limit`, `--issuer` | Defaults: 65532, 8080, `10m`, `32Mi`, `128Mi`, `letsencrypt-prod` |

Every instance runs non-root with no service-account token, all capabilities dropped and a read-only root filesystem with a writable `/tmp`. The generator refuses to overwrite an instance, expose a worker, put a public app in the sign-in cookie domain, or ship production without a digest. If a Secret stub cannot be encrypted, it is removed, so plaintext never reaches Git.

## The app policy

`make app-policy` renders every instance with Helm and checks the Kubernetes objects: pinned images (digest in production), non-root, no privilege escalation, CPU/memory requests and a memory limit, no service-account token, no host access, exposure (private has no Ingress; hosts under `homelab.swhurl.com` need sign-in; public hosts stay outside it), TLS on every host, and a named storage class. CI runs it on every push.

A reviewed exception goes on the HelmRelease, with a reason:

```yaml
metadata:
  annotations:
    platform.swhurl.com/policy-exceptions: "no-escalation=needs raw sockets for ICMP"
```

## Operate an instance

```bash
make app-status APP=hello ENV=prod     # desired vs running revision and digest, replicas, route, TLS, failing containers
make app-logs APP=hello ENV=prod       # FOLLOW=true, TAIL=N
make app-reconcile APP=hello ENV=prod
make app-check APP=hello ENV=prod      # policy, offline
```

Change an app by editing its files and pushing. Promote by copying the staging image digest into the production HelmRelease. Uninstall by removing its line from `clusters/home/kustomization.yaml` ([lifecycle](operations.md#lifecycle)). Secret values: [operations](operations.md#secrets).

## Moving a host between instances

Deploy the new instance on a temporary host and check it. Then, in one commit, remove the old route and put the host on the new instance. Expect a few seconds of Traefik's default certificate while cert-manager issues the new one.

## Limits

- No per-instance quotas, NetworkPolicies or RBAC: namespaces separate failures and ownership, not trust.
- Everything under `homelab.swhurl.com` shares the sign-in cookie.
- Promoting a digest is a manual edit; automated update PRs are planned work ([plan](../Swhurl-platform-implementation-plan.md) section 0, PR06).
- `nginx-unprivileged` listens on IPv4 only (its IPv6 script cannot edit the read-only config); use `127.0.0.1`, not `localhost`, inside the pod.

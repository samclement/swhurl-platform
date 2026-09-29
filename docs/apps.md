# Apps

An **app instance** is one app in one environment: namespace `<app>-<env>`, a HelmRelease of the pinned [bjw-s app-template](https://bjw-s-labs.github.io/helm-charts/docs/app-template/) chart (5.2.1), an optional encrypted Secret, and its own Flux unit `app-<app>-<env>`. Instances wait only for `infra-base` and, when signed-in, `platform-oauth2-proxy`; a broken instance never blocks another, and an observability outage never blocks an app.

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
sops apps/weather-api/staging/secret.sops.yaml     # replace the REPLACE_ME values
make check-apps check-secrets
git add apps/weather-api clusters/home platform/reloader
git commit -m "apps: add weather-api staging" && git push
make flux-reconcile && make app-status APP=weather-api ENV=staging
```

The console's New app form opens the same change as a PR ([console](console.md)). The generator ([`tools/swhurl/apps/new.py`](../tools/swhurl/apps/new.py); `make app-new NAME=x ARGS=--help` lists all options) renders what it wrote against the app policy before exiting (it warns and skips the check if Helm is missing; `--no-policy-check` skips it), and writes `apps/<app>/<env>/` and `clusters/home/app-<app>-<env>.yaml`, and registers the unit in `clusters/home/kustomization.yaml`. Files under `apps` deploy nothing until that registration exists. The output is plain YAML; edit it like any manifest afterwards.

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

`make check-apps` renders every instance with Helm and checks the Kubernetes objects: pinned images (digest in production), non-root, no privilege escalation, CPU/memory requests and a memory limit, no service-account token, no host access, exposure (private has no Ingress; hosts under `homelab.swhurl.com` need sign-in; public hosts stay outside it), TLS on every host, and a named storage class. It also compares the source manifests of an app's environments: they may differ only in namespace, hosts, image tag and digest, replicas, resources and issuer (encrypted Secrets are skipped). CI runs it on every push.

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

Change an app by editing its files and pushing, or with these Git-only commands (commit and push after each; the console offers the same as pull requests, [console](console.md)):

```bash
make app-promote APP=hello                                  # staging's image tag and digest into prod
make app-scale APP=hello ENV=prod ARGS="--replicas 2 --memory-limit 256Mi"
make app-remove APP=hello ENV=staging                       # files, unit, registration, Reloader entry
```

Each checks the result against the app policy. They edit only HelmReleases that are exactly as the generator writes them; a hand-edited one is refused (edit it yourself). Removing an instance with a retained volume leaves its namespace and claim on the cluster ([lifecycle](operations.md#lifecycle)). Secret values: [operations](operations.md#secrets).

## Telemetry

Container stdout and stderr reach ClickStack without any setup. For metrics and traces (and structured logs), use an OpenTelemetry SDK that sends OTLP to the collector on the app's own node: it runs with host networking, so the address is the node IP. Add this to the container in the HelmRelease, in every environment:

```yaml
            env:
              HOST_IP:
                valueFrom:
                  fieldRef:
                    fieldPath: status.hostIP
              OTEL_EXPORTER_OTLP_ENDPOINT: http://$(HOST_IP):4318   # gRPC: port 4317
              OTEL_SERVICE_NAME: weather-api
```

Apps need no key: the collector adds the ingestion key, the pod, namespace and deployment, and forwards to ClickStack ([services](services.md#clickstack-and-otel)). In HyperDX, filter on `ServiceName` or `k8s.namespace.name`. SDK auto-instrumentation and runtime metrics work unchanged. Telemetry sent in a pod's first second can lack the pod attributes, while the collector's pod lookup catches up. Nothing scrapes Prometheus `/metrics` endpoints. The generator does not write these lines.

## Moving a host between instances

Deploy the new instance on a temporary host and check it. Then, in one commit, remove the old route and put the host on the new instance. Expect a few seconds of Traefik's default certificate while cert-manager issues the new one.

## Limits

- No per-instance quotas, NetworkPolicies or RBAC: namespaces separate failures and ownership, not trust.
- Everything under `homelab.swhurl.com` shares the sign-in cookie.
- Image tags and digests are edited by hand (then `make app-promote` copies staging's into prod). Renovate opens PRs only for chart versions, including app-template ([chart updates](operations.md#chart-updates)); digest PRs are planned ([plan](plan.md) section 0, PR06).
- `nginx-unprivileged` listens on IPv4 only (its IPv6 script cannot edit the read-only config); use `127.0.0.1`, not `localhost`, inside the pod.

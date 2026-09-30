# Apps

An **app instance** is one app in one environment: namespace `<app>-<env>`, a HelmRelease of the pinned [bjw-s app-template](https://bjw-s-labs.github.io/helm-charts/docs/app-template/) chart (5.2.1), an optional encrypted Secret, and its own Flux unit `app-<app>-<env>`. Instances wait only for `infra-base` and, when signed-in, `platform-oauth2-proxy`; a broken instance never blocks another, and an observability outage never blocks an app.

## Current instances

| Instance | Host | Image |
| --- | --- | --- |
| `hello/staging` | `staging-hello.homelab.swhurl.com` | `nginxinc/nginx-unprivileged:1.27-alpine`, pinned by digest |
| `hello/prod` | `hello.homelab.swhurl.com` | same digest |
| `hello-ts/staging` | `staging-hello-ts.homelab.swhurl.com` | [`samclement/hello-ts`](https://github.com/samclement/hello-ts) from the template; deployed automatically on each push |
| `hello-ts/prod` | `hello-ts.homelab.swhurl.com` | changes only through a promote |

All require sign-in. `hello` serves the stock nginx page as UID 101 on port 8080; `hello-ts` is the template's TypeScript app, sending traces and metrics to ClickStack as `ServiceName` `hello-ts`. Staging and production differ only in namespace, host and image: staging is a separate rollout and failure boundary, not a separate trust boundary.

## Add an app

An app built from the [swhurl template](https://github.com/samclement/swhurl-app-template-typescript) needs only a name, its image and who can reach it; the preset fills in the rest:

```bash
make app-new NAME=weather-api ARGS="--preset swhurl-web --env staging \
  --image ghcr.io/samclement/weather-api:42-a1b2c3d@sha256:<digest> --secret-keys API_TOKEN,DB_URL"
sops apps/weather-api/staging/secret.sops.yaml     # replace the REPLACE_ME values
make check-apps check-secrets
git add apps/weather-api clusters/home platform/reloader
git commit -m "apps: add weather-api staging" && git push
make flux-reconcile && make app-status APP=weather-api ENV=staging   # flux-reconcile waits for the new unit
```

| Preset | Fills in | For |
| --- | --- | --- |
| `swhurl-web` | `--kind web --exposure authenticated-web --port 8080 --health-path /healthz --uid 65532 --otlp --auto-deploy`; host `staging-<name>.homelab.swhurl.com` (`<name>.homelab.swhurl.com` in prod) | A web app or API from the template |
| `swhurl-worker` | `--kind worker --exposure private --uid 65532 --otlp --auto-deploy` | A background worker from the template |

Any flag you give wins over the preset (for example `--exposure public --host weather.example.com`, or `--no-otlp`). The values are the template's conventions, kept in [`contract.py`](../tools/swhurl/apps/contract.py). Without a preset, give every option yourself:

```bash
make app-new NAME=hello ARGS="--env staging --image docker.io/nginxinc/nginx-unprivileged:1.27-alpine \
  --exposure authenticated-web --uid 101 --health-path /"
```

The console's New app form opens the same change as a PR ([console](console.md)): it starts on the web preset, showing only name, environment, image, exposure, host and Secret keys, with the preset's values under **Advanced**; **Other image** shows every option. The generator ([`tools/swhurl/apps/new.py`](../tools/swhurl/apps/new.py); `make app-new NAME=x ARGS=--help` lists all options) renders what it wrote against the app policy before exiting (it warns and skips the check if Helm is missing; `--no-policy-check` skips it), and writes `apps/<app>/<env>/` and `clusters/home/app-<app>-<env>.yaml`, and registers the unit in `clusters/home/kustomization.yaml`. Files under `apps` deploy nothing until that registration exists. The output is plain YAML; edit it like any manifest afterwards.

| Option | Rules |
| --- | --- |
| `--kind` | `web` (Service and probes on `--health-path`, required) or `worker` (no Service, no route) |
| `--exposure` | `private` (no route, default); `authenticated-web` (sign-in, host under `homelab.swhurl.com`); `public` (no sign-in, host **outside** `homelab.swhurl.com` so the sign-in cookie never reaches it) |
| `--image` | `repo:tag`, `repo@sha256:…` or both; no `latest`; **production requires a digest** |
| `--persistence SIZE` | A claim on `local-path-retain`, kept on Helm uninstall; the namespace is never pruned |
| `--preset` | `swhurl-web` or `swhurl-worker`: defaults for an app from the template (table above) |
| `--host` | Required for `public`; for `authenticated-web` it defaults to `staging-<name>.homelab.swhurl.com` (`<name>.homelab.swhurl.com` in prod) |
| `--auto-deploy` / `--no-auto-deploy` | Staging only: Flux deploys each newer image the app publishes; needs an image `REPO:<run>-<sha>@sha256:…` ([deploy a new image](#deploy-a-new-image)) |
| `--otlp` / `--no-otlp` | The app has an OpenTelemetry SDK: points it at the cluster collector ([telemetry](#telemetry)) |
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
make app-status APP=hello ENV=prod     # Git revision applied?, running image matches?, replicas, who can reach it, route, TLS, failures
make app-logs APP=hello ENV=prod       # FOLLOW=true, TAIL=N
make app-reconcile APP=hello ENV=prod
make app-check APP=hello ENV=prod      # policy, offline
```

Change an app by editing its files and pushing, or with these Git-only commands (commit and push after each; the console offers the same as pull requests, [console](console.md)):

```bash
make app-promote APP=hello                                  # staging's image tag and digest into prod
make app-scale APP=hello ENV=prod ARGS="--replicas 2 --memory-limit 256Mi"
make app-expose APP=hello ENV=staging ARGS="--exposure public --host hello.example.com"   # who can reach it
make app-remove APP=hello ENV=staging                       # files, unit, registration, Reloader entry
```

`app-expose` switches an instance between `private` (no route), `authenticated-web` (Google sign-in; it keeps a signed-in host or derives `staging-<name>.homelab.swhurl.com`) and `public` (no sign-in; `--host` outside `homelab.swhurl.com`, so the sign-in cookie never reaches it). It rewrites the route, the Namespace's exposure label and the unit's dependencies together, and refuses a route on a worker. Staging and production may differ (for example a signed-in staging preview of a public app); `make check-apps` then compares their other settings but not their routes.

Each checks the result against the app policy. They edit only HelmReleases that are exactly as the generator writes them; a hand-edited one is refused (edit it yourself). Removing an instance with a retained volume leaves its namespace and claim on the cluster ([lifecycle](operations.md#lifecycle)). Secret values: [operations](operations.md#secrets).

## Telemetry

What reaches ClickStack depends on whether the app has an OpenTelemetry SDK:

| Your app | Do | What reaches ClickStack |
| --- | --- | --- |
| No OpenTelemetry SDK | Nothing | stdout and stderr as logs, with pod, namespace and deployment attributes |
| An SDK that should report here | `--otlp` (the console's **Sends OpenTelemetry** box) | The logs above, plus traces, metrics and structured logs under `ServiceName` = the app name |
| An SDK that should not report here, or reports to its own backend | Leave `--otlp` off; set `OTEL_SDK_DISABLED=true` or its own endpoint | Logs only. An unconfigured SDK sends to `localhost:4318`, where nothing listens in the pod, and logs export errors |

`--otlp` writes the cluster default into the container, the same in every environment (the values come from [`contract.py`](../tools/swhurl/apps/contract.py)):

```yaml
            env:
              HOST_IP:
                valueFrom:
                  fieldRef:
                    fieldPath: status.hostIP                # the node IP
              OTEL_EXPORTER_OTLP_ENDPOINT: http://$(HOST_IP):4318
              OTEL_EXPORTER_OTLP_PROTOCOL: http/protobuf
              OTEL_SERVICE_NAME: weather-api                # the app name
```

The collector DaemonSet runs on every node with host networking, so it listens on the node IP: 4318 for OTLP over HTTP, 4317 for gRPC. It adds the ingestion key and the pod, namespace and deployment, and forwards to ClickStack ([services](services.md#clickstack-and-otel)).

**What must be true of the app** for `--otlp` to work:

- It has an OpenTelemetry SDK or auto-instrumentation.
- The SDK reads the standard `OTEL_*` environment variables; an endpoint hard-coded in the app overrides them.
- It exports OTLP over HTTP/protobuf. For gRPC, change the endpoint port to 4317 and the protocol to `grpc` by hand.
- It sends no key or auth headers; the collector adds them.

To add this to an existing instance, paste the block into each environment's HelmRelease. `make check-apps` fails (rule `otlp-host-ip`) if `$(HOST_IP)` is used without `HOST_IP` defined from `status.hostIP` before it; Kubernetes would otherwise pass the literal text to the SDK. In HyperDX, filter on `ServiceName` or `k8s.namespace.name` (which tells staging from production). Telemetry sent in a pod's first second can lack the pod attributes while the collector's pod lookup catches up. Nothing scrapes Prometheus `/metrics` endpoints.

## Deploy a new image

An instance runs the image named by `repository`, `tag` and `digest` in its HelmRelease values. Staging changes first; production gets the same digest only when you promote it.

**Apps from the template (automatic in staging).** An instance generated with `--preset` (or `--auto-deploy`) in staging is watched by Flux image automation. Push to the app repository's `main` and it reaches staging on its own:

```text
app push → its workflow publishes ghcr.io/<owner>/<app>:<run>-<sha>
  → image-reflector-controller sees the new tag (checks every minute)
  → ImagePolicy <app>-staging picks the highest <run> and its digest
  → image-automation-controller commits the new tag and digest to apps/<app>/staging as fluxcdbot
  → push webhook → Flux applies → helm-controller rolls the Deployment
```

The staging HelmRelease's `tag:` and `digest:` lines carry `# {"$imagepolicy": "flux-system:<app>-staging:tag"}` (and `:digest`) markers; they tell Flux which lines to rewrite. Keep them when editing by hand (`make app-scale` keeps them). `make verify-platform` shows each app's newest image under Image Automation. To stop automatic deploys for one app, remove the markers (and `image-automation.yaml` with its line in `kustomization.yaml`) in a commit; staging then keeps its current image until you change it by hand.

**Other apps (by hand).** Nothing watches them; change the pin yourself:

1. Find the new image's digest: the registry's page for that tag, the digest your image build printed, or `docker buildx imagetools inspect <repository>:<tag>` (the `Digest:` line; use the index digest for a multi-architecture image).
2. In `apps/<app>/staging/helmrelease.yaml`, set both lines under `controllers.main.containers.main.image`:

   ```yaml
   tag: 1.28-alpine
   digest: sha256:<new digest>
   ```

   Staging accepts a tag alone, but `make app-promote` refuses an image without a digest (production requires one), so set both.
3. `make app-check APP=<app> ENV=staging`, commit, push (or open a pull request and merge it). The [push webhook](services.md#push-webhook) has Flux fetch it within seconds; its unit applies the new values and helm-controller rolls the Deployment ([the full chain](architecture.md#how-changes-reach-the-cluster)).

**Then, for both:** `make app-status APP=<app> ENV=staging` compares the running image with Git by digest (Kubernetes records a running image as `repository@digest`, without the tag): `running: matches desired` once the new pod is Ready, or `different image` during a rollout or when it fails, and you promote the same image with `make app-promote APP=<app>` (commit, push) or **Promote to prod** in the console, which opens the pull request.

To roll back staging, revert the commit that changed the pin; for an automatic app, a newer image then replaces it again, so fix forward in the app, or remove the markers first. Chart versions are different: Renovate opens pull requests for app-template, and one merged PR updates every instance, staging and production together ([chart updates](operations.md#chart-updates)).

## Moving a host between instances

Deploy the new instance on a temporary host and check it. Then, in one commit, remove the old route and put the host on the new instance. Expect a few seconds of Traefik's default certificate while cert-manager issues the new one.

## Limits

- No per-instance quotas, NetworkPolicies or RBAC: namespaces separate failures and ownership, not trust.
- Everything under `homelab.swhurl.com` shares the sign-in cookie.
- Only staging updates automatically, and only for apps whose tags follow `<run>-<sha>` (the template's workflow); other apps' pins are edited by hand ([deploy a new image](#deploy-a-new-image)).
- `nginx-unprivileged` listens on IPv4 only (its IPv6 script cannot edit the read-only config); use `127.0.0.1`, not `localhost`, inside the pod.

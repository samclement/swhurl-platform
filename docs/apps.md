# Apps

An **app instance** is one app in one environment: namespace `<app>-<env>`, a HelmRelease of the pinned [bjw-s app-template](https://bjw-s-labs.github.io/helm-charts/docs/app-template/) chart (5.2.1), an optional encrypted Secret, and its own Flux unit `app-<app>-<env>`. Instances wait only for `infra-base` and, when signed-in, `platform-oauth2-proxy`; a broken instance never blocks another, and an observability outage never blocks an app.

An app's life, and where each step is described. Every change is a Git edit (by hand, a `make` command or a console pull request) that Flux applies ([how changes reach the cluster](architecture.md#how-changes-reach-the-cluster)):

| Stage | Console | Command | Section |
| --- | --- | --- | --- |
| Start a new app: code, repository and staging | **New app** | `make app-repo`, then `make app-new` | [Start a new app](#start-a-new-app) |
| Ship a new version | — | push to the app's repository | [Deploy a new image](#deploy-a-new-image) |
| Promote to production | **Promote to prod** | `make app-promote` | [Promote to production](#promote-to-production) |
| Add an image that already exists | **New app**, other tabs | `make app-new` | [Add an existing image](#add-an-existing-image) |
| Choose who can reach it | **Who can reach it** | `make app-expose` | [Who can reach it](#who-can-reach-it) |
| Give it secrets | — | `sops apps/<app>/<env>/secret.sops.yaml` | [Secrets](#secrets) |
| Keep dependencies current | — | Renovate pull requests in the app's repository | [Dependency updates](#dependency-updates-in-app-repositories) |
| Watch, resize, fix | the app page, **Scale**, **Reconcile** | `make app-status`, `app-logs`, `app-scale`, `app-reconcile` | [Operate an instance](#operate-an-instance) |
| Remove | **Uninstall** | `make app-remove` | [Remove an app](#remove-an-app) |

## Current instances

| Instance | Host | Image |
| --- | --- | --- |
| `hello/staging` | `staging-hello.homelab.swhurl.com` | `nginxinc/nginx-unprivileged:1.27-alpine`, pinned by digest |
| `hello/prod` | `hello.homelab.swhurl.com` | same digest |
| `hello-ts/staging` | `staging-hello-ts.homelab.swhurl.com` | [`samclement/hello-ts`](https://github.com/samclement/hello-ts); deployed automatically on each push |
| `hello-ts/prod` | `hello-ts.homelab.swhurl.com` | changes only through a promote |

All require sign-in. `hello` serves the stock nginx page as UID 101 on port 8080; `hello-ts` is a TypeScript app made before the templates used Copier, sending traces and metrics to ClickStack as `ServiceName` `hello-ts`. Staging is a separate rollout and failure boundary, not a separate trust boundary.

## Start a new app

From nothing to a running staging app in a few minutes (about three for TypeScript; six or seven for Kotlin, whose first build downloads its dependencies): the platform creates the app's GitHub repository from a template, waits for its first image and adds it to staging. Every push to the app's `main` then deploys to staging on its own.

**In the console:** **New app** → **New app and repository** (the first tab). Give a name, pick a stack and its features, choose who can reach it, and press **Create repository and open pull request**. The job page shows each step; when it finishes it links to a pull request here that adds `<name>/staging`. Merge it, and Flux deploys it within a minute ([what the job does, and when it stops](console.md#use-it)).

**From a terminal**, the same in two steps (the first needs your `gh` login and SSH access to GitHub):

```bash
make app-repo NAME=weather-api STACK=kotlin ANSWERS="kind=web database=sqlite"   # DRY_RUN=true shows the plan
# [OK] Created https://github.com/samclement/weather-api from samclement/swhurl-app-template-kotlin
# [OK] First image: ghcr.io/samclement/weather-api:1-a1b2c3d@sha256:…
#   make app-new NAME=weather-api ARGS="--from-repo samclement/weather-api --env staging --image ghcr.io/…"
make app-new NAME=weather-api ARGS="--from-repo samclement/weather-api --env staging --image ghcr.io/…"   # the printed line
git add apps/weather-api clusters/home && git commit -m "apps: add weather-api/staging" && git push
make app-status APP=weather-api ENV=staging    # expect "running: matches desired"
```

`make app-repo` refuses a name that already exists on GitHub, creates the repository **public** (the cluster pulls images without credentials), waits for its first build (checks, image, smoke test, publish) and reads the image digest from GHCR as the cluster will. If that build fails it stops with the run's link: the repository stays; fix the app, push, and run `make app-new` with the image that run publishes.

### Stacks and features

A **stack** is a [Copier](https://copier.readthedocs.io/) template repository for one language and framework:

| Stack | Template | Runs as |
| --- | --- | --- |
| `typescript` (default) | [`swhurl-app-template-typescript`](https://github.com/samclement/swhurl-app-template-typescript) | Node 24; starts in about a second, about 60 MB of memory; the platform's default resources |
| `kotlin` | [`swhurl-app-template-kotlin`](https://github.com/samclement/swhurl-app-template-kotlin) | Kotlin on Micronaut, Java 25, about 150 MB image; about 10 s to start, about 200 MB of memory. Its `swhurl.yaml` asks for 100m CPU, 192Mi (limit 384Mi) and two minutes to start |

**Features** are the template's own questions (its `copier.yml`, read from GitHub, so a stack's features need no platform code); both stacks ask:

| Feature | Choices | The app gets | The platform adds |
| --- | --- | --- | --- |
| `kind` | `web` (default), `worker` | An HTTP service answering `/healthz`, or a background process that works every `WORK_INTERVAL_MS` | A worker is private: no Service or route (the console hides **Who can reach it**) |
| `database` | `none` (default), `sqlite` | SQLite at `DATABASE_PATH` with migrations in `migrations/` applied at startup | A retained volume, one copy running at a time, nightly backups and `make restore-sqlite` ([operations](operations.md#backups-and-recovery)). Staging and production each have their own database; a promote copies the image, not the data |

Either way the app listens on 8080 (web), runs as UID 65532 writing only to `/tmp` (and `/data` with a database), sends traces and metrics over OpenTelemetry, logs JSON linked to its traces, and publishes `ghcr.io/<owner>/<app>:<run>-<sha>` from `main`. Nothing in the app's repository names the cluster: its [`swhurl.yaml`](#swhurlyaml) says what it needs, and this repo writes the manifests. Each template's README is the guide on the app's side. Template changes reach existing apps only through the shared workflow (`app.yml`) and Renovate preset; applying the rest (`copier update`) is [planned](plan.md) (section 8, phase 7).

### Add production

Production is created once, with the image staging runs, and changes only through a promote afterwards. Use the app's `swhurl.yaml` (`--from-repo`), so production gets the same resources, probes and database as staging:

```bash
make app-status APP=weather-api ENV=staging    # the Image line: REPO:TAG@sha256:…
make app-new NAME=weather-api ARGS="--from-repo samclement/weather-api --env prod --image <that image>"
git add apps/weather-api clusters/home && git commit -m "apps: add weather-api/prod" && git push
```

Production requires the digest and never deploys automatically. The console's preset tabs can create production too, but they carry the TypeScript template's defaults: for a Kotlin app, set **Memory request** 192Mi, **Memory limit** 384Mi and **CPU request** 100m under **Advanced**; the start-up allowance is not on the form, so use the command.

## Add an existing image

For an image that already exists: an app made elsewhere, a public image such as nginx, or a retry after a failed first build. Pick the source of the defaults:

| The app | Command | Console |
| --- | --- | --- |
| Has a `swhurl.yaml` in its repository | `make app-new NAME=<app> ARGS="--from-repo OWNER/REPO --env staging --image …"` | (not on the form; use the command) |
| Follows the TypeScript template's conventions | `--preset swhurl-web` or `--preset swhurl-worker` | **Web app** or **Worker** tab |
| Anything else | every option yourself (table below) | **Other image** tab, with **Advanced** open |

```bash
make app-new NAME=hello ARGS="--env staging --image docker.io/nginxinc/nginx-unprivileged:1.27-alpine \
  --exposure authenticated-web --uid 101 --health-path /"
make check-apps
git add apps/hello clusters/home && git commit -m "apps: add hello/staging" && git push
make flux-reconcile && make app-status APP=hello ENV=staging   # flux-reconcile waits for the new unit
```

`--from-repo OWNER/REPO[@REF]` reads `swhurl.yaml` from the default branch (or `REF`) through GitHub's API, with `GITHUB_TOKEN` if set (a private repository needs it); `--manifest PATH` reads a local copy. A flag you give wins over `swhurl.yaml` or a preset (for example `--exposure public --host weather.example.com`). With `--secret-keys`, set the values before pushing ([secrets](#secrets)) and also `git add platform/reloader`.

| Preset | Fills in |
| --- | --- |
| `swhurl-web` | `--kind web --exposure authenticated-web --port 8080 --health-path /healthz --uid 65532 --otlp --auto-deploy`; host `staging-<name>.homelab.swhurl.com` (`<name>.homelab.swhurl.com` in prod) |
| `swhurl-worker` | `--kind worker --exposure private --uid 65532 --otlp --auto-deploy` |

### What the generator writes

`make app-new` ([`new.py`](../tools/swhurl/apps/new.py); `make app-new NAME=x ARGS=--help` lists every option) writes `apps/<app>/<env>/` and `clusters/home/app-<app>-<env>.yaml`, registers the unit in `clusters/home/kustomization.yaml` (files under `apps` deploy nothing until then), and checks the result against [the app policy](#the-app-policy) before exiting (it warns and skips the check if Helm is missing; `--no-policy-check` skips it). The output is plain YAML; edit it like any manifest afterwards. The console's New app pull requests run this same command in a copy of `main`.

| Option | Rules |
| --- | --- |
| `--from-repo`, `--manifest`, `--preset` | Where the defaults come from (one of them, or none) |
| `--kind` | `web` (Service and probes on `--health-path`, required) or `worker` (no Service, no route) |
| `--image` | `repo:tag`, `repo@sha256:…` or both; no `latest`; **production requires a digest** |
| `--exposure`, `--host` | Who can reach it ([below](#who-can-reach-it)); default `private` |
| `--secret-keys A,B` | An encrypted Secret stub ([secrets](#secrets)) |
| `--otlp` / `--no-otlp` | The app has an OpenTelemetry SDK: points it at the cluster collector ([telemetry](#telemetry)) |
| `--auto-deploy` / `--no-auto-deploy` | Staging only: Flux deploys each newer image the app publishes; needs an image `REPO:<run>-<sha>@sha256:…` ([deploy a new image](#deploy-a-new-image)) |
| `--database sqlite` | A retained volume (`--database-size`, default 1Gi) at `/data`, with the database file `/data/app.db` passed to the app as `DATABASE_PATH`; backed up daily |
| `--startup-seconds N` | Web only: a startup probe gives the app up to N seconds (10-600) to first answer its health path before liveness checks begin; without it, three failed liveness checks (about 30 s) restart a slow starter |
| `--persistence SIZE` | A claim on `local-path-retain`, kept on Helm uninstall; the namespace is never pruned ([remove an app](#remove-an-app)). Any instance with a volume runs one replica and stops the old pod before starting the new one (policy rule `single-writer`) |
| `--uid`, `--port`, `--cpu`, `--memory`, `--memory-limit`, `--issuer` | Defaults: 65532, 8080, `10m`, `32Mi`, `128Mi`, `letsencrypt-prod` |

Every instance runs non-root with no service-account token, all capabilities dropped and a read-only root filesystem with a writable `/tmp`. The generator refuses to overwrite an instance, expose a worker, put a public app in the sign-in cookie domain, or ship production without a digest.

## swhurl.yaml

What an app needs from the platform, kept in the app's own repository (the templates write it from your feature answers) and read by `app-new --from-repo` and `--manifest`. Each field becomes an `app-new` default; flags on the command line still win. The name, environment, image and host belong to each instance and are never in the file. The schema is `manifest_defaults` in [`contract.py`](../tools/swhurl/apps/contract.py); unknown fields and other versions are refused.

```yaml
version: 1              # required; this platform reads version 1
stack: kotlin           # which template the app came from (informational)
kind: web               # required: web or worker            --kind
port: 8080              # web only (default 8080)            --port
healthPath: /healthz    # web only, required                 --health-path
startupSeconds: 120     # web only, optional (10-600)         --startup-seconds
uid: 65532              # default 65532                      --uid
telemetry: otlp         # otlp or none (default)             --otlp
autoDeploy: true        # staging follows new images         --auto-deploy
database: sqlite        # optional; databaseSize: 1Gi        --database, --database-size
secrets: [API_TOKEN]    # optional: variable names           --secret-keys
resources: {cpu: 100m, memory: 192Mi, memoryLimit: 384Mi}   # optional: --cpu, --memory, --memory-limit
exposure: authenticated-web   # optional; default: web apps authenticated-web, workers private
```

## Who can reach it

| Exposure | Route | Host |
| --- | --- | --- |
| `private` (the default) | None: reachable only inside the cluster | — |
| `authenticated-web` | Behind Google sign-in (the shared oauth2-proxy middleware) | Under `homelab.swhurl.com`; derived as `staging-<name>.homelab.swhurl.com` (`<name>.homelab.swhurl.com` in prod) unless given |
| `public` | No sign-in | `--host` **outside** `homelab.swhurl.com`, so the shared sign-in cookie never reaches it |

Set it with `--exposure` at creation, and change it later with `make app-expose APP=hello ENV=staging ARGS="--exposure public --host hello.example.com"` or the app page's **Who can reach it** form. `app-expose` rewrites the route, the Namespace's exposure label and the unit's dependencies together, keeps a signed-in host (or derives one), and refuses a route on a worker. Staging and production may differ, for example a signed-in staging preview of a public app. `make app-status` and the app page show the exposure read from the live routes.

## Secrets

`--secret-keys API_TOKEN,DB_URL` (the console's **Secret environment variables**) writes `apps/<app>/<env>/secret.sops.yaml`: a Secret with those keys set to `REPLACE_ME`, SOPS-encrypted before the command returns (if it cannot be encrypted, it is removed, so plaintext never reaches Git). The app receives each key as an environment variable (`envFrom`), the unit decrypts it with `flux-system/sops-age`, and the namespace is added to Reloader so a changed value restarts the app.

**Reserved names.** The platform sets some variables itself (`HOST_IP` and `OTEL_*` for telemetry, `DATABASE_PATH` for SQLite), and a container's own `env` silently wins over `envFrom`, so a secret with one of those names would never reach the app. `app-new`, `swhurl.yaml` and the console therefore refuse `HOST_IP`, `DATABASE_PATH` and any name starting `OTEL_`, `SWHURL_` (kept for future platform variables) or `KUBERNETES_` (the API server address in-cluster clients read). Each app's Secret lives in its own namespace, so two apps may use the same key names. The console's app page lists **Provided by the platform** (names and values) apart from **Your secrets** (the Secret's name; values are never read).

Set or change values in your own terminal with `sops apps/<app>/<env>/secret.sops.yaml` (for a console pull request, on its branch before merging), then `make check-secrets` (it fails on a `REPLACE_ME` left behind), commit and push. Rules and rotation: [operations](operations.md#secrets).

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

- It has an OpenTelemetry SDK or auto-instrumentation. A Node app that is an ES module must register OpenTelemetry's loader hook before anything is imported, or HTTP is not traced; the template does this in `src/instrumentation.ts`.
- The SDK reads the standard `OTEL_*` environment variables; an endpoint hard-coded in the app overrides them.
- It exports OTLP over HTTP/protobuf. For gRPC, change the endpoint port to 4317 and the protocol to `grpc` by hand.
- It sends no key or auth headers; the collector adds them.

To add this to an existing instance, paste the block into each environment's HelmRelease. `make check-apps` fails (rule `otlp-host-ip`) if `$(HOST_IP)` is used without `HOST_IP` defined from `status.hostIP` before it; Kubernetes would otherwise pass the literal text to the SDK.

**Logs linked to traces:** a JSON log line that carries `trace_id` and `span_id` (32 and 16 hex characters, at the top level as pino writes them, or under `mdc` as logback writes them with the Java agent) is attached to that trace, so a trace in HyperDX shows its logs. Both templates do this already.

**Health checks are not traced:** the collector drops the spans of requests whose user agent is `kube-probe/…`, so a health path that does more (a database query) leaves child spans without a parent; keep it cheap.

In HyperDX, filter on `ServiceName` or `k8s.namespace.name` (which tells staging from production). Telemetry sent in a pod's first second can lack the pod attributes while the collector's pod lookup catches up. Nothing scrapes Prometheus `/metrics` endpoints.

## Deploy a new image

An instance runs the image named by `repository`, `tag` and `digest` in its HelmRelease values. Staging changes first; production gets the same digest only when you [promote](#promote-to-production) it.

**Apps from a template (automatic in staging).** A staging instance with `autoDeploy: true` in its `swhurl.yaml` (both templates), `--preset` or `--auto-deploy` is watched by Flux image automation. Push to the app repository's `main` and it reaches staging on its own, in about two minutes:

```text
app push → its workflow checks, builds and publishes ghcr.io/<owner>/<app>:<run>-<sha>
  → image-reflector-controller sees the new tag (checks every minute)
  → ImagePolicy <app>-staging picks the highest <run> and its digest
  → image-automation-controller commits the new tag and digest to apps/<app>/staging as fluxcdbot
  → push webhook → Flux applies → helm-controller rolls the Deployment
```

The staging HelmRelease's `tag:` and `digest:` lines carry `# {"$imagepolicy": "flux-system:<app>-staging:tag"}` (and `:digest`) markers; they tell Flux which lines to rewrite. Keep them when editing by hand (`make app-scale` and `app-expose` keep them). `make verify-platform` shows each app's newest image under Image Automation. To stop automatic deploys for one app, remove the markers (and `image-automation.yaml` with its line in `kustomization.yaml`) in a commit; staging then keeps its current image until you change it by hand.

**Other apps (by hand).** Nothing watches them; change the pin yourself:

1. Find the new image's digest: the registry's page for that tag, the digest your image build printed, or `docker buildx imagetools inspect <repository>:<tag>` (the `Digest:` line; use the index digest for a multi-architecture image).
2. In `apps/<app>/staging/helmrelease.yaml`, set both lines under `controllers.main.containers.main.image`:

   ```yaml
   tag: 1.28-alpine
   digest: sha256:<new digest>
   ```

   Staging accepts a tag alone, but `make app-promote` refuses an image without a digest (production requires one), so set both.
3. `make app-check APP=<app> ENV=staging`, commit, push (or open a pull request and merge it). The [push webhook](services.md#push-webhook) has Flux fetch it within seconds; its unit applies the new values and helm-controller rolls the Deployment.

**Check it:** `make app-status APP=<app> ENV=staging` compares the running image with Git by digest: each pod's spec names the digest it was given, and a pull by digest guarantees that content (the node's own image ID can name another digest for the same image, when two builds published identical content). It reports `running: matches desired` once the new pod is Ready, or `different image` during a rollout or when it fails ([operate an instance](#operate-an-instance)).

**Roll back** staging by reverting the commit that changed the pin; for an automatic app, a newer image then replaces it again, so fix forward in the app, or remove the markers first. Chart versions are different: Renovate opens pull requests for app-template in this repository, and one merged pull request updates every instance, staging and production together ([chart updates](operations.md#chart-updates)).

## Dependency updates in app repositories

Apps from a template update their own dependencies. The Renovate GitHub App is installed for all repositories (a config file required), so a new app's repository is registered on Renovate's next run; its `renovate.json` extends its template's shared `renovate-preset.json` (TypeScript: npm packages, OpenTelemetry grouped; Kotlin: Gradle plugins and libraries, Kotlin and Micronaut grouped). Every pull request runs the app's checks from the template's shared `app.yml`: compile or type-check, tests, an image build, and a smoke test that starts the image as the cluster does, from the app's `swhurl.yaml` (details in each template's README).

| Situation | What validates an update | What deploys |
| --- | --- | --- |
| A template's own update pull request | The template's CI, which renders every combination of features and runs the app checks on each | Nothing: new apps start from the updated template |
| App with tests (both templates ship some) | All checks; minor, patch and digest updates merge themselves, majors wait | Each merge publishes an image that deploys to staging; promote to production |
| App without tests | Build and smoke test only; set `"automerge": false`, so you merge | Staging after your merge; check it before promoting |

Renovate merges only when it runs, so a passing update can wait hours; tick "run again" on the repository's Dependency Dashboard issue to hurry it. The repositories keep GitHub's **Allow auto-merge** off: with no branch protection, GitHub would merge without waiting for checks, while Renovate's own automerge waits for them. A version written inside a template's `*.jinja` file is invisible to Renovate; keep versions in plain files.

## Promote to production

Production never changes on its own. When staging runs what you want, copy its image to production:

```bash
make app-promote APP=<app>        # staging's tag and digest into prod, nothing else; FROM=/TO= override
git commit -am "apps: promote <app> to prod" && git push
make app-status APP=<app> ENV=prod
```

The console's **Promote to prod** (on the staging page) opens the same change as a pull request. Promote refuses an image without a digest, never copies the automatic-deploy markers (production has none), and checks the result against the app policy. The production instance must exist: create it once ([add production](#add-production)).

## Operate an instance

```bash
make app-status APP=hello ENV=prod     # Git revision applied?, running image matches?, replicas, who can reach it, route, TLS, failures
make app-logs APP=hello ENV=prod       # FOLLOW=true, TAIL=N, PREVIOUS=true (last crashed container)
make app-reconcile APP=hello ENV=prod  # fetch Git and reconcile this instance's unit now
make app-check APP=hello ENV=prod      # the app policy, offline
make app-scale APP=hello ENV=prod ARGS="--replicas 2 --memory-limit 256Mi"   # Git edit: commit and push
```

The console's app page shows what `make app-status` shows and offers **Reconcile** and **Scale** (as a pull request). The edit commands (`app-scale`, `app-expose`, `app-promote`, `app-remove`) change only files exactly as the generator wrote them and refuse a hand-edited one (edit it yourself); each checks its result against the app policy.

A failing instance also sends a push notification, and each automatic staging deploy announces itself ([alerts](services.md#alerts)). When a pod fails, `make app-status` and the app page name the usual cause and the fix: out of memory (raise `--memory-limit`), an image the node cannot pull (missing tag, or a private GHCR package), a missing Secret value, a crash loop (`make app-logs APP= ENV= PREVIOUS=true` shows the crashed container's output; usually the wrong port or a write outside `/tmp`), or a failing readiness check (the app must answer its health path on its port).

An instance fails fast: if its pods are not Ready within 3 minutes of a change (`FAIL_AFTER` in [`contract.py`](../tools/swhurl/apps/contract.py): the unit's `timeout` and the HelmRelease's `timeout`), its unit turns red and `make flux-reconcile` reports it; Helm then retries once and stops. `make app-status` lists failing containers. After fixing the cause, push, or run `make app-reconcile`. An app that genuinely needs longer to start (a large image, a slow first migration) can raise both timeouts in its own files.

## Remove an app

`make app-remove APP=<app> ENV=<env>` (or **Uninstall** in the console, as a pull request) deletes the instance's files, its unit file and registration and its Reloader entry; once pushed, Flux uninstalls it. An instance with `--persistence` keeps its namespace and claim on the cluster: deleting that data is a separate, explicit `make destroy-data` ([lifecycle](operations.md#lifecycle)). Removing staging also removes its automatic deploys; the app repository and its images are untouched.

## The app policy

`make check-apps` renders every instance with Helm and checks the Kubernetes objects: pinned images (digest in production), non-root, no privilege escalation, CPU/memory requests and a memory limit, no service-account token, no host access, exposure (private has no Ingress; hosts under `homelab.swhurl.com` need sign-in; public hosts stay outside it), TLS on every host, a named storage class, a single writer per volume (an instance that mounts a ReadWriteOnce claim runs one replica and stops the old pod before starting the new one), and `HOST_IP` defined before an OTLP endpoint uses it. It also compares the source manifests of an app's environments: they may differ only in namespace, hosts, image tag and digest, replicas, resources, issuer and exposure (when the environments' exposure differs, their routes are not compared; each is still checked on its own); encrypted Secrets and staging's `image-automation.yaml` are skipped. CI runs it on every push; the rules are listed in [`policy.py`](../tools/swhurl/apps/policy.py).

A reviewed exception goes on the HelmRelease, with a reason:

```yaml
metadata:
  annotations:
    platform.swhurl.com/policy-exceptions: "no-escalation=needs raw sockets for ICMP"
```

## Moving a host between instances

Deploy the new instance on a temporary host and check it. Then, in one commit, remove the old route and put the host on the new instance. Expect a few seconds of Traefik's default certificate while cert-manager issues the new one.

## Limits

- No per-instance quotas, NetworkPolicies or RBAC: namespaces separate failures and ownership, not trust.
- Everything under `homelab.swhurl.com` shares the sign-in cookie.
- Only staging updates automatically, and only for apps whose tags follow `<run>-<sha>` (the template's workflow); other apps' pins are edited by hand ([deploy a new image](#deploy-a-new-image)).
- App images must be public: the cluster has no registry pull credentials.
- `nginx-unprivileged` listens on IPv4 only (its IPv6 script cannot edit the read-only config); use `127.0.0.1`, not `localhost`, inside the pod.

# Apps

An **app instance** is one app in one environment: namespace `<app>-<env>`, a HelmRelease of the pinned [bjw-s app-template](https://bjw-s-labs.github.io/helm-charts/docs/app-template/) chart (5.2.1), an optional encrypted Secret, and its own Flux unit `app-<app>-<env>`. Instances wait only for `infra-base` and, when signed-in, `platform-oauth2-proxy`; a broken instance never blocks another, and an observability outage never blocks an app.

An app's life, and where each step is described. Every app comes from a stack template, and every change is a Git edit (by hand, a `make` command or a console pull request) that Flux applies ([how changes reach the cluster](architecture.md#how-changes-reach-the-cluster)):

| Stage | Console | Command | Section |
| --- | --- | --- | --- |
| Start a new app: code, repository and staging | **New app** | `make app-repo`, then `make app-new` | [Start a new app](#start-a-new-app) |
| Ship a new version | — | push to the app's repository | [Deploy a new image](#deploy-a-new-image) |
| Promote to production | **Promote to production** | `make app-promote` | [Promote to production](#promote-to-production) |
| Choose who can reach it | **Change access** | `make app-expose` | [Who can reach it](#who-can-reach-it) |
| Give it secrets | — | `sops apps/<app>/<env>/secret.sops.yaml` | [Secrets](#secrets) |
| Keep dependencies current | — | Renovate pull requests in the app's repository | [Dependency updates](#dependency-updates-in-app-repositories) |
| Watch, resize, fix | the app page, **Scale**, **Reconcile** | `make app-status`, `app-logs`, `app-scale`, `app-reconcile` | [Operate an instance](#operate-an-instance) |
| Remove | **Uninstall** | `make app-remove` | [Remove an app](#remove-an-app) |

## The demo apps

Four small apps, owned by team `demo`, show how apps on the platform work together. They run all the time, and a little traffic flows through them, so every page that describes an app (the console, a dashboard, a trace) has something real to show. Each repository's README says what it demonstrates and where in its code.

```mermaid
flowchart LR
  person(["you, signed in"]) -- "POST /buy" --> shop
  shopper["demo-shopper<br/>worker: buys every 30 s"] -- "call, as the app demo-shopper" --> shop["demo-shop<br/>web, signed-in"]
  shop -- "call, with a token only<br/>demo-orders accepts" --> orders["demo-orders<br/>web, SQLite"]
  orders -. "event demo-orders.created" .-> notifier["demo-notifier<br/>Kotlin worker"]
  notifier -- "an order of 10 or more" --> team(["team demo's<br/>notification channel"])
```

| App | Stack | Where | What it shows | Read about it |
| --- | --- | --- | --- | --- |
| [`demo-orders`](https://github.com/samclement/demo-orders) | TypeScript, web, SQLite | staging, prod | An API only other apps may use; its API and its event described in its own repository; a database the platform keeps and backs up | [Calling another app](#calling-another-app), [what an app offers](#what-an-app-offers), [backups](operations.md#backups-and-recovery) |
| [`demo-shop`](https://github.com/samclement/demo-shop) | TypeScript, web | staging | Calling another app with a platform-issued token; telling a signed-in person from an app | [Calling another app](#calling-another-app), [who can reach it](#who-can-reach-it) |
| [`demo-shopper`](https://github.com/samclement/demo-shopper) | TypeScript, worker | staging | A worker; an app calling another as itself; the steady traffic | [Stacks and features](#stacks-and-features) |
| [`demo-notifier`](https://github.com/samclement/demo-notifier) | Kotlin, worker | staging | Receiving another app's event; a message to the team; the second stack | [Events](#events), [notifications](#notifications) |

All web addresses require sign-in: `https://staging-demo-shop.homelab.swhurl.com`, `https://staging-demo-orders.homelab.swhurl.com` and `https://demo-orders.homelab.swhurl.com`. Staging is a separate rollout and failure boundary, not a separate trust boundary.

**See it work.** Each step is something you can open:

| To see | Do this |
| --- | --- |
| The apps and how they are connected | Console → **Apps/Services** → **Map**: two solid lines (calls) and one dashed (the event) |
| What each offers, and its routes or payload | Console → **Catalogue** → team `demo`; open `created` under `demo-orders` for its fields and an example |
| A purchase | Open `https://staging-demo-shop.homelab.swhurl.com/`: `latestOrders` shows what `demo-shopper` bought seconds ago |
| That only apps may place orders | Signed in, `POST https://staging-demo-orders.homelab.swhurl.com/orders` answers 401: you are a person, not `demo-shop` |
| One request through every app | Console → `demo-shop/staging` → **Logs and traces**: one trace runs `demo-shopper` → `demo-shop` → `demo-orders` → `demo-notifier` |
| Traffic and errors over time | Console → any demo app → **Dashboard** |
| A team notification | Signed in, `POST https://staging-demo-shop.homelab.swhurl.com/buy?item=tea&quantity=12`: ten or more makes `demo-notifier` tell the team's channel |
| A promotion | Console → `demo-shop/staging` → **Review promotion** (it needs `demo-orders` in production, which it has) |

**The traffic.** `demo-shopper` visits the shop every half minute: about four requests a minute through `demo-shop` and `demo-orders` and two events, with quantities of one to three so nobody is notified. `demo-orders` keeps the latest 1000 orders. To pause it: `make app-scale APP=demo-shopper ENV=staging ARGS="--replicas 0"`, commit and push.

**Use them as a pattern.** To call `demo-orders` or receive its event from your own app, use the Catalogue's **Add an app**; to start an app like one of them, choose the same stack and features in **New app** and copy what its README points at.

## Start a new app

From nothing to a running staging app in a few minutes (about two for TypeScript; a little longer for Kotlin, whose build also compiles and tests): the platform creates the app's GitHub repository from a template, waits for its first image and adds it to staging. Every push to the app's `main` then deploys to staging on its own.

**In the console:** **New app** → **Start a new app** (the default). Give a name, pick a stack and its features, choose its team, who can reach it and what it connects to (apps to call, events to receive and publish), and press **Create repository and open pull request**. The job page shows each step; when it finishes it links to a pull request here that adds `<name>/staging`. Merge it, and Flux deploys it within a minute ([what the job does, and when it stops](console.md#use-it)).

### What happens, and when

For example, starting `weather-api` from the TypeScript template creates `samclement/weather-api`, waits for its first published image, and proposes `weather-api/staging` in this platform repository. The timeline is:

```mermaid
sequenceDiagram
  actor Operator
  participant Console
  participant AppRepo as App GitHub repo
  participant Actions as App Container workflow
  participant Platform as swhurl-platform
  participant Validate as Validate workflow
  participant Flux
  participant Cluster
  participant CM as cert-manager
  participant Dash as dashboard sync

  Operator->>Console: Submit Start a new app
  Console->>AppRepo: Create public repo from selected stack
  Console->>AppRepo: Push rendered files to main
  AppRepo->>Actions: Push event starts first checks build smoke and publish run
  Actions-->>Console: Run succeeds and console reads published image digest
  Console->>Platform: Create console/new-weather-api-staging-<sha> branch and PR
  Platform->>Validate: PR event runs Validate
  Validate-->>Platform: Checks pass
  Platform->>Platform: Auto-merge gate verifies PR and fast-forwards main
  Platform->>Flux: GitHub push webhook for main (otherwise 1-minute source poll)
  Flux->>Cluster: Apply app unit and its manifests
  Cluster->>CM: Ingress appears with issuer annotation and TLS Secret name
  CM->>Cluster: HTTP-01 challenge, then certificate and TLS Secret
  Dash->>Cluster: Every minute list HelmRelease records created by app Flux units
  Dash->>Dash: Group matching records by app and environment
  Dash->>Dash: Create or update the App weather-api dashboard in HyperDX
```

The console job creates a **public** app repository because the cluster pulls its image without registry credentials. Its first commit starts the app repository's normal push workflow; this is a GitHub Actions push trigger, not a platform webhook. The console waits up to ten minutes for that workflow to finish, then reads the image digest and uses it in the platform change. If this first build fails or times out, the app repository remains and no platform PR is opened; fix and push the app, and once its workflow has published an image press **Finish creating this app** on the job's page ([jobs and restarts](console.md#jobs-and-restarts)), or add it yourself with the `make app-new … --from-repo` line shown [below](#start-a-new-app). The same button finishes a job that a console restart cut short.

The platform PR is opened only after that image exists. It runs the platform `Validate` pull-request workflow. A regular generated app PR is labeled for auto-merge and merges itself after validation and the trusted merge gate checks the current merge result. If the generated change includes an encrypted Secret stub, auto-merge is disabled because its values still need setting; set them on the PR branch and merge it yourself. Until merge, neither the namespace nor any app resources are created on the cluster. Platform PR branch pushes are filtered out by the Flux Receiver; the merge push to `main` is what invokes it. Without a successful webhook delivery, Flux's `GitRepository` still polls `main` every minute.

After the merge, Flux creates the app's dedicated `app-<name>-staging` unit from the newly registered unit file. It waits for `infra-base`, and also `platform-oauth2-proxy` for authenticated web exposure. The unit applies the namespace, HelmRelease and optional encrypted Secret. The chart creates the Deployment and Service for a web app; authenticated or public web exposure also creates an Ingress. Worker apps have no Service or Ingress. SQLite/database answers add a retained PVC and mount; secret answers add the SOPS Secret and register the namespace with Reloader. With automatic deploy enabled, the staging unit also creates this app's Flux `ImageRepository`, `ImagePolicy` and `ImageUpdateAutomation` in `flux-system`, so later published images can update the staging image pin in platform Git. These resources are app-scoped; the shared cluster services already exist.

**Certificates are requested only for a web app with a route** (`authenticated-web` or `public`). The generated Ingress names a TLS Secret and carries the selected ClusterIssuer annotation. The default is `letsencrypt-prod`, even for the staging environment; `letsencrypt-staging` and `selfsigned` can be selected. Once Flux/Helm has created the Ingress, cert-manager notices it and asynchronously performs the issuer's flow. For Let's Encrypt that is HTTP-01 through Traefik; on success cert-manager stores the certificate in the named Secret, which Traefik uses for HTTPS. The app Helm readiness does not wait for certificate issuance, so the app can become Ready while TLS is still pending. Private web apps and workers have no route and therefore request no certificate.

**The dashboard is also asynchronous.** The `console-dashboards` CronJob runs once a minute. It lists Kubernetes HelmRelease records, keeps the ones registered by an `app-<name>-<env>` Flux unit whose app name and namespace match the release, then groups staging and production records by app. As soon as Flux has created the staging HelmRelease, the job creates or updates `App: <name>` in HyperDX. It does not wait for the release or pods to become Ready. It uses ClickStack's API; no dashboard manifest or app-specific dashboard provisioning enters Git. A dashboard failure retries on the next scheduled run and does not block the app. Details and dashboard contents are in [Dashboards](#dashboards).

Once staging is deployed, later pushes to the app repository's `main` publish images. The app repository's [image webhook](services.md#image-webhook) tells Flux the moment the image is published; Flux image automation finds the new tag and commits its digest to this platform repository's `main`; the same webhook-to-reconcile path rolls out that image to staging. Production is not created by this flow.

**From a terminal**, the same in two steps (the first needs your `gh` login and SSH access to GitHub):

```bash
make app-repo NAME=weather-api STACK=kotlin ANSWERS="kind=web database=sqlite"   # DRY_RUN=true shows the plan
# [OK] Created https://github.com/samclement/weather-api from samclement/swhurl-app-template-kotlin
# [OK] First image: ghcr.io/samclement/weather-api:1-a1b2c3d@sha256:…
#   make app-new NAME=weather-api ARGS="--from-repo samclement/weather-api --env staging --team <team> --image ghcr.io/…"
make app-new NAME=weather-api ARGS="--from-repo samclement/weather-api --env staging --team payments --image ghcr.io/…"   # the printed line, with the team that owns it
git add apps/weather-api clusters/home && git commit -m "apps: add weather-api/staging" && git push
make app-status APP=weather-api ENV=staging    # expect "running: matches desired"
```

`make app-repo` refuses a name that already exists on GitHub, creates the repository **public** (the cluster pulls images without credentials), adds the [image webhook](services.md#image-webhook), waits for its first build (checks, image, smoke test, publish) and reads the image digest from GHCR as the cluster will. If that build fails it stops with the run's link: the repository stays; fix the app, push, and run the same `make app-new` line with the image that run publishes (the console's job prints the line too).

**Only apps made this way are supported.** In this repository `make app-new` refuses anything else: `--from-repo` must be `samclement/<name>`, the image must be `ghcr.io/samclement/<name>` (what that repository's workflow publishes) and its `swhurl.yaml` must set `autoDeploy: true`, as the stack templates do. There is no route for an image built elsewhere or a public image such as nginx: `make check-apps` (and so CI and the console's merge gate) fails an instance committed by hand that is not such an app ([the app policy](#the-app-policy)). Tests are the one exception: the throwaway live tests (`make live-test-*`) deploy nginx and BusyBox images from fixture manifests into labelled test namespaces and remove them afterwards.

### Stacks and features

A **stack** is a [Copier](https://copier.readthedocs.io/) template repository for one language and framework:

| Stack | Template | Runs as |
| --- | --- | --- |
| `typescript` (default) | [`swhurl-app-template-typescript`](https://github.com/samclement/swhurl-app-template-typescript) | Node 24; starts in about a second, about 60 MB of memory; the platform's default resources |
| `kotlin` | [`swhurl-app-template-kotlin`](https://github.com/samclement/swhurl-app-template-kotlin) | Kotlin on Micronaut, Java 25, about 150 MB image; about 10 s to start, about 200 MB of memory. Its `swhurl.yaml` asks for 100m CPU, 192Mi (limit 384Mi) and two minutes to start |

**Features** are the template's own questions (its `copier.yml`, read at the catalogue's pinned GitHub revision, so a stack's features need no platform code); both stacks ask:

| Feature | Choices | The app gets | The platform adds |
| --- | --- | --- | --- |
| `kind` | `web` (default), `worker` | An HTTP service answering `/healthz`, or a background process that works every `WORK_INTERVAL_MS` | A worker is private: no Service or route (the console hides **Change access**) |
| `database` | `none` (default), `sqlite` | SQLite at `DATABASE_PATH` with migrations in `migrations/` applied at startup | A retained volume, one copy running at a time, nightly backups and `make restore-sqlite` ([operations](operations.md#backups-and-recovery)). Staging and production each have their own database; a promote copies the image, not the data |
| `api` | `none` (default), `openapi` | Web only. An [OpenAPI](https://www.openapis.org) description generated from the app's routes, committed in its repository and served by the app: `openapi.json` (TypeScript, Hono with `@hono/zod-openapi`) or `src/main/resources/openapi.yaml` (Kotlin, Micronaut OpenAPI). A test fails while the file and the routes disagree | The link on the app page (`provides.api`), and the document kept beside each instance (`provides.apiFile`; [what an app offers](#what-an-app-offers)) |
| `events` | `none` (default), `asyncapi` | An [AsyncAPI 3](https://www.asyncapi.com) document, `asyncapi.yaml`, describing each event the app publishes (name, meaning, payload schema, example), with one example event (`visited` for web, `worked` for a worker). The helper refuses to publish an event that is not in the file or a payload that does not fit its schema | Its NATS key and stream at creation, and what it publishes from the document from then on ([what an app offers](#what-an-app-offers)). Every app can publish and subscribe without this feature: it adds the description |

Either way the app listens on 8080 (web), runs as UID 65532 writing only to `/tmp` (and `/data` with a database), sends traces and metrics over OpenTelemetry, logs JSON linked to its traces, and publishes `ghcr.io/<owner>/<app>:<run>-<sha>` from `main`. Nothing in the app's repository names the cluster: its [`swhurl.yaml`](#swhurlyaml) says what it needs, and this repo writes the manifests. Each template's README is the guide on the app's side. The catalogue pins reviewed template commits in `STACK_REVISIONS` in [`contract.py`](../tools/swhurl/apps/contract.py). Questions, rendering and contract checks all use that commit. Template releases have immutable version tags; Copier records the tag when it describes the pinned commit. Deliberate catalogue updates change the pin and run `make check-templates` before release. App workflow versions are pinned to release tags and bumped by Renovate. The shared Renovate preset keeps dependency rules consistent; [template updates](#template-updates) apply generated-file changes through reviewed PRs.

**How templates are tested.** Two halves, so neither grows with the other:

- **The template's own CI** (its Template workflow) renders combinations of its questions and, for each, type-checks, tests, builds the image and smoke-tests it. It builds the defaults, each non-default choice on its own and all non-defaults together for each kind (seven per stack), not every combination: that grows with the number of choices rather than doubling with each question. Each combination keeps its own image build cache. SQLite runtime checks apply migrations and write through the actual image, restart with the same data directory, then verify migration history and previous rows are preserved and new writes work. Database inspection uses a disposable copy including WAL files. Both stacks use the same conformance assertions; each retains its own compiler, tests and build.
- **This repo's `make check-templates`** (in `make check` and CI; needs network) clones each template, renders every combination with Copier and turns each `swhurl.yaml` into staging with `app-new --manifest` (a local file instead of `--from-repo`, writing to a scratch `--root`), then production through the promotion conversion; each must pass the [app policy](#the-app-policy) and the two must not drift. It builds nothing (32 combinations today), and catches a template declaring something the platform refuses before anyone creates an app from it.

### Template updates

Renovate's [Copier manager](https://docs.renovatebot.com/modules/manager/copier/) reads `.copier-answers.yml`, finds newer template version tags and runs Copier to update generated files with the saved answers. Template repositories publish immutable `v…` tags only after their combination checks pass. Copier updates always require manual review, even for minor releases: inspect app edits and resolve conflicts before merging. The installed Renovate GitHub App needs workflow-write permission when an update changes a workflow pin (it already writes workflow updates in the template repos). A CLI token without that scope cannot push such a branch; the ordinary reviewed SSH Git route can bootstrap it without credential changes. The app's Container workflow checks the PR and publishes an image only after merging to `main`, which deploys to staging. Promotion remains explicit.

Existing Copier apps whose answers record an untagged commit need one bootstrap update on a clean branch: `uvx copier@9.18.2 update --skip-answered --defaults --vcs-ref v0.1.0`. Review and commit the generated diff, then open a PR and wait for Container checks. Do not edit `_commit` manually. An app made before the templates used Copier has no answers file: render the template over it once (`copier copy --overwrite --vcs-ref <tag>` with its answers) and port its own code, after which Renovate proposes updates as for any other app.

Platform rendering remains exhaustive while cheap. On 3 October 2026 the eight combinations took 21.7 s with a cold uv dependency cache and 26.1 s warm on the operator host; each run still clones templates and fetches charts, so network variance dominates this pair. Representative warm GitHub template jobs took 42–75 s for TypeScript and 28–62 s for Kotlin, including SQLite restarts. An earlier cold Kotlin app build took 6 min 31 s from console form to PR ([evidence](current-state.md)). PRs and main run seven sets of answers per stack (listed in each Template workflow); there is no separate scheduled matrix. Platform rendering stays exhaustive. A third stack adds its catalogue repository and pin, template/build checks and runtime conformance evidence; it adds no language build dependencies here.

### Add production

Production is created by the first [promotion](#promote-to-production) from reviewed staging settings. The console and `make app-new` create staging only; `app-new --env prod` is refused.

### What the generator writes

`make app-new` ([`new.py`](../tools/swhurl/apps/new.py); `make app-new NAME=x ARGS=--help` lists every option) writes `apps/<app>/staging/` and `clusters/home/app-<app>-staging.yaml`, registers the unit in `clusters/home/kustomization.yaml` (files under `apps` deploy nothing until then), and checks the result against [the app policy](#the-app-policy) before exiting (a missing tool or failed render makes the command fail; `--no-policy-check` explicitly skips validation). The output is plain YAML; edit it like any manifest afterwards. The console's New app pull requests run this same command in a copy of `main`.

| Option | Rules |
| --- | --- |
| `--from-repo samclement/<name>[@REF]` | Required: reads `swhurl.yaml` from the app repository's default branch (or `REF`) through GitHub's API, with `GITHUB_TOKEN` if set; its fields become the defaults and a flag you give wins (for example `--exposure public --host weather.example.com`) |
| `--env` | `staging` (default); production is created by promotion |
| `--kind` | `web` (Service and probes on `--health-path`, required) or `worker` (no Service, no route) |
| `--image` | `ghcr.io/samclement/<name>:<run>-<sha>@sha256:…`, as `make app-repo` and the app's workflow print it |
| `--exposure`, `--host` | Who can reach it ([below](#who-can-reach-it)); default `private` |
| `--team` | Required (here or as `team:` in `swhurl.yaml`): the owning team, a name in `apps/teams.yaml` ([teams and isolation](#teams-and-isolation)) |
| `--secret-keys A,B` | An encrypted Secret stub ([secrets](#secrets)): set the values before pushing and also `git add platform/reloader` |
| `--otlp` / `--no-otlp` | The app has an OpenTelemetry SDK: points it at the cluster collector ([telemetry](#telemetry)) |
| `--database sqlite` | A retained volume (`--database-size`, default 1Gi) at `/data`, with the database file `/data/app.db` passed to the app as `DATABASE_PATH`; backed up daily |
| `--persistence SIZE` | A claim on `local-path-retain`, kept on Helm uninstall; the namespace is never pruned ([remove an app](#remove-an-app)). Any instance with a volume runs one replica and stops the old pod before starting the new one (policy rule `single-writer`) |
| `--uid`, `--port`, `--cpu`, `--memory`, `--memory-limit`, `--issuer` | Defaults: 65532, 8080, `10m`, `32Mi`, `128Mi`, `letsencrypt-prod` |

Every instance runs non-root with no service-account token, all capabilities dropped and a read-only root filesystem with a writable `/tmp`. Every web app gets the same start-up allowance: a startup probe gives it up to 120 s to first answer its health path before liveness checks begin. A fast app is Ready as soon as it answers; a JVM on a busy node needs most of it. The allowance is fixed rather than per app so that it stays a minute inside Helm's 3-minute wait (time to pull the image), and the templates' smoke tests wait just as long. The generator refuses to overwrite an instance, expose a worker, put a public app in the sign-in cookie domain, or ship production without a digest.

## swhurl.yaml

What an app needs from the platform, kept in the app's own repository (the templates write it from your feature answers) and read by `app-new --from-repo`. Each field becomes an `app-new` default; flags on the command line still win. The name, environment, image and host belong to each instance and are never in the file. The schema is `manifest_defaults` in [`contract.py`](../tools/swhurl/apps/contract.py); unknown fields and other versions are refused.

```yaml
version: 1              # required; this platform reads version 1
stack: kotlin           # which template the app came from (informational)
kind: web               # required: web or worker            --kind
port: 8080              # web only (default 8080)            --port
healthPath: /healthz    # web only, required                 --health-path
uid: 65532              # default 65532                      --uid
telemetry: otlp         # otlp or none (default)             --otlp
autoDeploy: true        # staging follows new images (required: app-new refuses false)
database: sqlite        # optional; databaseSize: 1Gi        --database, --database-size
secrets: [API_TOKEN]    # optional: variable names           --secret-keys
resources: {cpu: 100m, memory: 192Mi, memoryLimit: 384Mi}   # optional: --cpu, --memory, --memory-limit
exposure: authenticated-web   # optional; default: web apps authenticated-web, workers private
team: payments          # a name in apps/teams.yaml; required here or as --team (there is no default)
alerts: error-logs=20,errors=none   # optional: its own default-alert thresholds, or none   --alerts
calls: [orders]         # optional: apps this one calls; each must exist in staging   --calls
provides: {api: /openapi.json}   # optional, web only: where its API description is served   --api-path
#   also apiFile: openapi.json and eventsFile: asyncapi.yaml: where the repository keeps its OpenAPI and AsyncAPI documents (what an app offers)
events: {publish: [created], subscribe: [orders.created]}   # optional   --publish, --subscribe
```

## Who can reach it

| Exposure | Route | Host |
| --- | --- | --- |
| `private` (the default for `make app-new` and for a worker) | None; only apps that [declare a call](#calling-another-app) to it can connect | — |
| `authenticated-web` (the default for a web app made from a stack) | Behind Google sign-in (the shared oauth2-proxy middleware) | Under `homelab.swhurl.com`; derived as `staging-<name>.homelab.swhurl.com` (`<name>.homelab.swhurl.com` in prod) unless given |
| `public` | No sign-in | `--host` **outside** `homelab.swhurl.com`, so the shared sign-in cookie never reaches it |

Set it with `--exposure` at creation, and change it later with `make app-expose APP=demo-shop ENV=staging ARGS="--exposure public --host demo-shop.example.com"` or the app page's **Change access** form. `app-expose` rewrites the route, the Namespace's exposure label and the unit's dependencies together, keeps a signed-in host (or derives one), and refuses a route on a worker. Staging and production may differ, for example a signed-in staging preview of a public app. `make app-status` and the app page show the exposure read from the live routes.

## Teams and isolation

Every app belongs to one **team**: a name registered in [`apps/teams.yaml`](../apps/teams.yaml), written as the `platform.swhurl.com/team` label on each of the app's namespaces. There is no default team: name one at creation (`--team`, `team:` in `swhurl.yaml`, or the console's team choice, where none is preselected), or `make app-new` refuses and change it for every environment at once with `make app-team APP=demo-shop ARGS="--team <team>"`. A team is a name, not a set of people: it says who owns an app and lets you filter by owner; it grants nobody access.

Manage teams on the console's **Teams** page or with `make team-add`, `make team-edit` and `make team-remove`. The registry and the apps that name a team change together, so no app is left naming a team that is gone (`make check-apps` rule `team` would fail):

| Change | What keeps apps from being orphaned |
| --- | --- |
| Rename a team | The same change relabels every app that belongs to it |
| Remove a team that owns apps | Refused, unless the same change moves them to another team (`--move-apps-to`) |
| An app already naming an unregistered team | Listed first on the Teams page; register the team or move the app with `make app-team` |

Logs and traces already stored keep the team name they were written with; only new data carries a new name.

A team can name one [notification channel](services.md#notification-channels) (`make team-edit NAME=payments ARGS="--notify payments-phone"`): it gets a copy of the deployment and health messages of every app the team owns. Choosing a team for a new app is therefore also choosing where its messages go; the console's New app form names each team's channel.

Each instance carries two generated files beside its HelmRelease:

| File | What it does |
| --- | --- |
| `resourcequota.yaml` | Caps the namespace at 14 pods, 2 CPU and 2Gi of memory requested, and 4Gi of memory limits: the same for every instance. `make app-scale` refuses replicas or sizes that would not fit beside a backup pod and a certificate solver (rule `quota`) |
| `networkpolicy.yaml` | Decides who may connect to the app's pods: the Traefik pods on the app's port when the instance has a route, plus the apps that [declare a call](#calling-another-app) to it. Any other pod, in another namespace or the same one, is refused. `make app-expose` and `make app-calls` rewrite it |

The policy selects the app's own pods (`app.kubernetes.io/instance: <app>`), never every pod in the namespace: cert-manager's HTTP-01 solver runs there on port 8089 and must stay reachable, as must the SQLite backup pod. Outbound connections are not restricted. Health probes come from the node and are not affected.

## Calling another app

An app says which apps it calls, and the platform gives it their address and a credential, and tells each callee who may call it. For example, `demo-shop` calling `demo-orders`:

```
make app-calls APP=demo-shop ENV=staging ARGS="--add demo-orders"     # or calls: [demo-orders] in swhurl.yaml at creation
```

```mermaid
flowchart LR
  subgraph demo-shop-staging
    shop["demo-shop<br/>SWHURL_SERVICE_DEMO_ORDERS_URL<br/>token file for demo-orders"]
  end
  subgraph demo-orders-staging
    orders["demo-orders<br/>SWHURL_ALLOWED_CALLERS=demo-shop<br/>NetworkPolicy admits demo-shop"]
  end
  shop -- "Authorization: Bearer token" --> orders
```

One command changes both instances in one commit:

| Side | What is written | Why |
| --- | --- | --- |
| Caller | `SWHURL_SERVICE_DEMO_ORDERS_URL` (`http://demo-orders.demo-orders-$(SWHURL_ENV).svc.cluster.local:<port>`) | The callee in the caller's own environment: staging calls staging, production calls production |
| Caller | A token file at `/var/run/swhurl/tokens/demo-orders/token` | A service-account token Kubernetes issues for the audience `demo-orders` only, valid 10 minutes and replaced by the kubelet before it expires. The Kubernetes API itself refuses it, and so does any app other than `demo-orders` |
| Callee | `SWHURL_ALLOWED_CALLERS=demo-shop` | Who it accepts |
| Callee | Its NetworkPolicy admits `demo-shop`' pods | Anything undeclared is blocked before it reaches the app |

Every instance is also told who it is (`SWHURL_APP`, `SWHURL_ENV`), and every web app gets what it needs to check a token: `SWHURL_AUTH_ISSUER` and `SWHURL_AUTH_KEYS`, the cluster's public signing keys. Apps cannot fetch those keys themselves (the API server refuses anonymous requests and apps carry no API token), so they are kept in [`apps/signing-keys.json`](../apps/signing-keys.json); `make verify-platform` fails if they differ from the cluster's, and `make platform-signing-keys` refreshes them ([operations](operations.md#signing-keys)).

**What the app does.** The stack templates ship both halves; an app's own code only names the callee:

- **Calling:** `service("demo-orders")` reads the address and the token file (again on every call, since it is replaced) and sends `Authorization: Bearer <token>`.
- **Being called:** a request that carries a token must carry a good one, or it gets 401: signed by a key in `SWHURL_AUTH_KEYS`, issued by `SWHURL_AUTH_ISSUER` for the audience `SWHURL_APP`, unexpired, and belonging to `system:serviceaccount:<caller>-<SWHURL_ENV>:<caller>` for a caller in `SWHURL_ALLOWED_CALLERS`. A request without a token is a person: signed in when it carries oauth2-proxy's `x-auth-request-email`, anonymous otherwise (a public app). A route meant only for apps asks for an app caller and answers 401 to anyone else. Logs and spans carry `caller.type` (`app`, `user` or `anonymous`) and `caller.id`.

**Finding what exists.** The console's **Catalogue** lists every API and event of an environment with what its app says about it (its routes, or its payload's fields and an example), who uses it and what each app uses in turn, and a choice of app to use it from. The **Apps/Services** list shows every app with its team and connections, its **Map** view draws who calls whom, and each app page has a **Connect** part: its address, its connections one per row (the same columns for a call and an event, whichever app declared it), **Connect to an app** to pick from the web apps and events its environment has, what another app adds to call it, and a link to its API description if it declares one (`provides: {api: /openapi.json}`; shown, not checked). A refused change reads the same in the console as from `make`, because the console runs the same commands.

**Rules.** A callee must be a web app with an instance in the same environment. Calls are declared per environment, like replicas: declare them in staging, promote, then declare them in production. The first promotion of a caller is refused until every app it calls has a production instance, and it adds the caller to those apps' production instances; such a pull request touches other apps' files, so the console does not merge it by itself. `make app-remove` refuses an instance that others call. The variables above are the platform's: `make app-sync` rewrites them, and `make check-apps` fails when the two sides disagree (rules `calls`, `identity`, `call-tokens`).

## Events

An app can publish events and receive other apps' events without knowing who is on the other side. The broker is NATS JetStream ([services](services.md#nats)). For example, `demo-orders` publishing `created` and `demo-notifier` receiving it:

```
make app-events APP=demo-notifier ENV=staging ARGS="--add-subscribe demo-orders.created"   # or the Catalogue's Add an app
```

or, before the subscriber is created, in its `swhurl.yaml`:

```yaml
events:
  subscribe: [demo-orders.created]      # <app>.<event>
```

What `demo-orders` publishes is not declared on the platform at all: it [describes its events](#what-an-app-offers) in its own `asyncapi.yaml`, and the platform reads that. An app that does not describe them declares the list instead: `make app-events APP=<app> ENV=staging ARGS="--publish created,cancelled"`, or `events: {publish: [created, cancelled]}` in `swhurl.yaml`.

```mermaid
flowchart LR
  orders["demo-orders<br/>publish created"] -- "staging.demo-orders.created" --> stream[("stream staging-demo-orders<br/>7 days, 64Mi")]
  stream -- "consumer demo-notifier_created" --> notifier["demo-notifier<br/>subscribe demo-orders.created"]
```

An event is named `<app>.<event>`: only its app may publish it. `--publish` and `--subscribe` replace a whole list (`""` clears it); `--add-publish`, `--remove-publish`, `--add-subscribe` and `--remove-subscribe` change one name and keep the rest, as `make app-calls` does and as the console's Connect card runs them. What the command writes, in one commit:

| Where | What | Why |
| --- | --- | --- |
| The instance's HelmRelease | `SWHURL_EVENTS_PUBLISH`, `SWHURL_EVENTS_SUBSCRIBE`, `SWHURL_EVENTS_URL` and `SWHURL_EVENTS_NKEY` (the instance's public key) | What it declared, where NATS is, and who it is there |
| `events.sops.yaml` | `SWHURL_EVENTS_SEED`, the private half of that key | Created and encrypted the first time the instance declares events; never printed. Reloader restarts the app if it changes |
| `events.yaml` | A stream for a publisher; a durable consumer for each subscription | The stream controller creates them in NATS when the commit is applied |
| `platform/nats/users.yaml` | The instance's NATS user | A publisher may publish anything under its own name (`<env>.<app>.>`), so a new image can send a new event before the platform has read its description; it may fetch and acknowledge exactly the events it subscribes to, and read the description of the streams they are in (the Java client asks for it) |
| The instance's Flux unit | Waits for `platform-nats` | The manifests need the controller's resource types |

**What the app does.** The stack templates' `events` feature ships the helper: `events.publish("created", data)` and `events.subscribe("demo-orders.created", handler)`. An event travels as a [CloudEvents](https://cloudevents.io) JSON envelope with the trace context in its headers, so the subscriber's work joins the publisher's trace. A handler that throws gets the event again, up to five times. A new subscription starts from the oldest event the stream still holds, and a subscriber that was down receives what it missed when it returns.

**When it takes effect.** After the commit is applied, NATS learns the app's user within a minute or two (the time Kubernetes takes to refresh the server's mounted configuration). An app that starts sooner is refused at first; the helper keeps trying every five seconds and logs each try.

**Rules.** A subscription must name an event that an app of the same environment publishes; removing that event or its app is refused while someone subscribes. An app that [describes its events](#what-an-app-offers) publishes what its `asyncapi.yaml` says: `--publish` on it is overwritten the next time its documents are read. Staging and production are separate: an app's production instance has its own key and cannot read staging's events; subscriptions are declared with `make app-events ENV=prod` after promotion, and so is what it publishes unless the app describes its events, which promotion then carries. A stream keeps seven days or 64Mi, whichever comes first, and is not backed up: keep anything you must not lose in the app's own database. `make check-apps` fails when the files disagree with the declaration (rules `events`, `event-users`); `make app-sync` rewrites them.

## What an app offers

An app can describe what other apps may use: its HTTP API as an [OpenAPI](https://www.openapis.org) document and the events it publishes as an [AsyncAPI 3](https://www.asyncapi.com) document. Both live in the app's own repository, beside the code (the templates' `api` and `events` [features](#stacks-and-features) write them and keep them true), and its `swhurl.yaml` names them:

```yaml
provides:
  api: /openapi.json          # where the running app serves it
  apiFile: openapi.json       # where the repository keeps it
  eventsFile: asyncapi.yaml
```

The platform reads them for the image an instance runs, so staging and production each show what their own image offers:

```mermaid
flowchart LR
  push["push to the app's main"] --> image["image 8-b2c3d4e"]
  image -->|"Flux commits the new tag"| staging["apps/demo-orders/staging<br/>helmrelease.yaml"]
  staging -->|"Offers workflow:<br/>make app-offers --all staging"| read["reads swhurl.yaml and both documents<br/>from the repository at b2c3d4e"]
  read --> kept["apps/demo-orders/staging/offers/<br/>source.yaml, openapi.json, asyncapi.yaml"]
  read --> publishes["SWHURL_EVENTS_PUBLISH<br/>from asyncapi.yaml"]
  kept -->|"make app-promote"| prod["apps/demo-orders/prod/offers/"]
```

| What | Where | Why |
| --- | --- | --- |
| The documents, as the repository has them, and where they came from | `apps/<app>/<env>/offers/` (`source.yaml`: repository, commit, file names). Not Kubernetes resources: nothing is applied | The offline checks and the console read them without building or calling the app |
| Where the running app serves its API description | `SWHURL_API_PATH`, from `provides.api` | The console links it; an app that starts describing itself later gets it without `--api-path` |
| The commit they were read at | The annotation `platform.swhurl.com/offers` on the instance's HelmRelease | The console sees that the app describes itself; an annotation does not restart the app |
| The events the app publishes | `SWHURL_EVENTS_PUBLISH`, set from the channels `asyncapi.yaml` sends to (a channel's `address` is the event's name) | The AsyncAPI document is the declaration: nobody lists the events a second time. Everything that follows from the list is [as for any event](#events) |

**When it happens.** At creation (`make app-new --from-repo` reads them when `swhurl.yaml` names either file). After every new staging image: the [Offers workflow](../.github/workflows/offers.yml) runs on Flux's tag commit, reads every staging instance, checks the app policy and commits what changed to `main`. At promotion: `make app-promote` copies staging's documents to production and sets what production publishes, creating production's NATS key the first time; a promotion that changes only the documents, the commit and where the API description is served still merges itself, one that changes what the app publishes waits for review. By hand: `make app-offers APP=demo-orders ENV=staging`.

**A new event works at once.** The new image starts a minute or two before the workflow has read its document. NATS already lets the app publish anything under its own name and its stream keeps everything under that name, so the event is stored from the first publish; the helper in the app refuses anything its own `asyncapi.yaml` does not describe. Other apps can subscribe once the workflow's commit is applied. The exception is an app's first event ever (it has no key or stream yet): the templates' `events` feature ships an example event, so an app created with the feature has both from the start.

**What is refused.** A document that is not OpenAPI 3 or AsyncAPI 3, a file `swhurl.yaml` names but the commit lacks, an event name that is not a lowercase name, and a document that no longer sends an event another app of that environment still receives: the workflow fails for that app, which keeps what it had (the others are still read), and a promotion is refused. Remove the subscription first, or keep the event. `make check-apps` fails when what an instance publishes is not what its kept document describes (rule `offers`). Production is refused offers that were read for another image than staging runs.

**Where it shows.** The console's [Catalogue](console.md#use-it) lists what every app offers, described or not, and the Connect picker on an app page and in New app says what each choice is.

**Limits.** The repository is read anonymously, so it must be public (a private one needs `GITHUB_TOKEN`). The documents are checked for the shape the platform uses, not against the full specifications; the templates' own tests do more. An app that stops naming its documents loses `offers/` and keeps publishing what it published.

## Notifications

An app can send a message to its team's [notification channel](services.md#notification-channels) with one call. Nothing is declared: every instance is given the notify service's address (`SWHURL_NOTIFY_URL`) and a token file for it (`/var/run/swhurl/tokens/notify/token`, valid ten minutes, replaced by the kubelet, accepted by nothing else), and the stack templates ship the helper:

```ts
await notify("Order backlog", { message: "412 orders waiting", priority: "high" });   // TypeScript
```

```kotlin
platform.notify("Order backlog", message = "412 orders waiting", priority = "high")    // Kotlin
```

```mermaid
flowchart LR
  app["orders (staging)<br/>notify('Order backlog')"] -- "its own token" --> notify[Notify service]
  notify -- "team payments" --> channel[the team's channel]
```

| What | Rule |
| --- | --- |
| Where it goes | The channel of the team that owns the app. A team without a channel: the call succeeds and nothing is delivered |
| Who it is from | The app and environment of the token, never the request: the message arrives titled `orders/staging: Order backlog`, and an app cannot send as another |
| How many | 30 accepted messages an hour per instance; the next gets HTTP 429 |
| Failure | The helper returns false (and logs why) instead of throwing: a notification must not break the request that sent it. Outside the platform, where the address is not set, it does nothing |
| Also sent for you | [Default alerts](#alerts) on error logs and failed requests, with no code |
| Not for | The operator's platform topic, which gets the [deployment and health messages](services.md#notification-expectations) by itself, or an app's own end users |

The [request contract](services.md#notify-service) is the same one the platform's own messages use. The address and the token are the platform's: `make app-sync` rewrites them and `make check-apps` fails when they are missing (rules `identity`, `call-tokens`). `notify` cannot be an app's name.

## Secrets

`--secret-keys API_TOKEN,DB_URL` (the console's **Secret environment variables**) writes `apps/<app>/<env>/secret.sops.yaml`: a Secret with those keys set to `REPLACE_ME`, SOPS-encrypted before the command returns (if it cannot be encrypted, it is removed, so plaintext never reaches Git). The app receives each key as an environment variable (`envFrom`), the unit decrypts it with `flux-system/sops-age`, and the namespace is added to Reloader so a changed value restarts the app.

**Reserved names.** The platform sets some variables itself (`HOST_IP` and `OTEL_*` for telemetry, `DATABASE_PATH` for SQLite, `SWHURL_*` for [identity and calls](#calling-another-app)), and a container's own `env` silently wins over `envFrom`, so a secret with one of those names would never reach the app. `app-new`, `swhurl.yaml` and the console therefore refuse `HOST_IP`, `DATABASE_PATH` and any name starting `OTEL_`, `SWHURL_` (kept for future platform variables) or `KUBERNETES_` (the API server address in-cluster clients read). Each app's Secret lives in its own namespace, so two apps may use the same key names. The console's app page lists **Provided by the platform** (names and values) apart from **Your secrets** (the Secret's name; values are never read).

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

In HyperDX, filter on `ServiceName`, `k8s.namespace.name` (which tells staging from production) or `swhurl.team` (every app of one [team](#teams-and-isolation): the collector copies the namespace's team label onto logs, traces and metrics). Telemetry sent in a pod's first second can lack the pod attributes while the collector's pod lookup catches up. Nothing scrapes Prometheus `/metrics` endpoints.

## Deploy a new image

An instance runs the image named by `repository`, `tag` and `digest` in its HelmRelease values. Staging changes first; production gets the same digest only when you [promote](#promote-to-production) it.

**Staging is automatic.** Every staging instance is watched by Flux image automation. Push to the app repository's `main` and it reaches staging on its own, in about two minutes:

```text
app push → its workflow checks, builds and publishes ghcr.io/<owner>/<app>:<run>-<sha>
  → image webhook → image-reflector-controller scans the registry at once (hourly without the webhook)
  → ImagePolicy <app>-staging picks the highest <run> and its digest
  → the app-named image-automation-controller commits the new tag and digest to apps/<app>/staging as fluxcdbot
  → push webhook → Flux applies → helm-controller rolls the Deployment
```

The staging HelmRelease's `tag:` and `digest:` lines carry `# {"$imagepolicy": "flux-system:<app>-staging:tag"}` (and `:digest`) markers; they tell Flux which lines to rewrite. Keep them when editing by hand (`make app-scale` and `app-expose` keep them). `make verify-platform` shows each app's newest image under Image Automation and its webhook under Image Webhooks (`make app-hooks` adds a missing webhook; until then Flux finds new images within the hour).

**Check it:** `make app-status APP=<app> ENV=staging` compares the main controller/container's running image with Git by digest (sidecars and other controllers keep their own images): each pod's spec names the digest it was given, and a pull by digest guarantees that content (the node's own image ID can name another digest for the same image, when two builds published identical content). It reports `running: matches desired` once the new pod is Ready, or `different image` during a rollout or when it fails ([operate an instance](#operate-an-instance)).

**Roll back** staging by reverting the commit that changed the pin; the app's next published image replaces it again, so fix forward in the app. Chart versions are different: Renovate opens pull requests for app-template in this repository, and one merged pull request updates every instance, staging and production together ([chart updates](operations.md#chart-updates)).

## Dependency updates in app repositories

Apps from a template update their own dependencies. The Renovate GitHub App is installed for all repositories (a config file required), so a new app's repository is registered on Renovate's next run; its `renovate.json` extends its template's shared `renovate-preset.json` (TypeScript: npm packages, OpenTelemetry grouped; Kotlin: Gradle plugins and libraries, Kotlin and Micronaut grouped). Every pull request runs the app's checks from the template's shared `app.yml`: compile or type-check, tests, an image build, and a smoke test that starts the image as the cluster does, from the app's `swhurl.yaml` (details in each template's README).

| Situation | What validates an update | What deploys |
| --- | --- | --- |
| A template's own update pull request | The template's CI, which renders every combination of features and runs the app checks on each | Nothing: new apps start from the updated template |
| App with tests (both templates ship some) | All checks; minor, patch and digest updates merge themselves, majors wait | Each merge publishes an image that deploys to staging; promote to production |
| App without tests | Build and smoke test only; set `"automerge": false`, so you merge | Staging after your merge; check it before promoting |

Renovate merges only when it runs, so a passing update can wait hours; tick "run again" on the repository's Dependency Dashboard issue to hurry it. The repositories keep GitHub's **Allow auto-merge** off: with no branch protection, GitHub would merge without waiting for checks, while Renovate's own automerge waits for them. A version written inside a template's `*.jinja` file is invisible to Renovate; keep versions in plain files.

## Promote to production

Try staging, then run **in your own terminal**:

```bash
make app-promote APP=<app>
make check
git add apps/<app> clusters/home platform/reloader
git commit -m "apps: promote <app> to prod"
git push
make flux-reconcile
make app-status APP=<app> ENV=prod
```

`app-promote` always means staging → production. The CLI checks live staging readiness and that it runs the digest in this checkout. First promotion derives production from supported staging settings, generates and policy-checks it in scratch space, then writes the complete Git edit. Later promotions change only production's image tag/digest, preserving its settings and comments. Equal digests are the same image regardless of tag: no files change. Different repositories require a reviewed Git edit. An older known build is labelled a rollback; database migrations are not reversed.

First production supports the current app-template chart, one Deployment controller/container, a standard web or private worker, probes, resources, security settings, command/arguments, telemetry, the main HTTP service/route, platform-managed retained storage and the app Secret. Compatible handwritten settings are preserved. Extra controllers, containers, resources, chart value sources, external volume bindings, ambiguous anchors and staging-specific references are refused with an explanation. Later image edits need an unambiguous main image mapping and passing policy; they do not convert custom configuration.

Production gets independent, initially empty storage. Startup applies database migrations normally. Staging data, PV bindings and credential values are never copied. App Secret keys create new encrypted `REPLACE_ME` stubs; set production values with `sops apps/<app>/prod/secret.sops.yaml` and review the Reloader registration before merging. A public app needs a distinct production host (`ARGS="--host=app.example.com"`); signed-in apps use the generated production address. Custom deployments remain explicit Git edits. Later runtime configuration changes also use Git; the [environment drift policy](#the-app-policy) still applies.

To guard the exact image reviewed, use:

```bash
make app-promote APP=<app> ARGS="--expect-image=ghcr.io/<owner>/<app>:<tag>@sha256:<digest>"
```

A mismatch or unhealthy staging refuses before production edits. In the console, **Review promotion** on Apps/Services or staging shows the exact image, destination and first-production changes, then **Promote to production** creates the PR. Eligible promotions merge automatically unless **Hold for manual review** is selected. Existing promotion PRs are recovered from GitHub after a restart and reused; a replacement image requires explicitly closing the old PR. See [merge controls and pending changes](console.md#auto-merge).

### Console promotion: what happens, and when

For example, promoting `weather-api/staging` selects its current healthy, digest-pinned image and proposes that image for `weather-api/prod`. The console's **Review promotion** page reads live staging health and compares the staging and production configuration in GitHub `main`. Submitting **Promote to production** rechecks that review, prepares the Git change with `main`'s own `app-promote` command, and opens a PR. The job finishing means the PR exists; it does not mean production has deployed.

```mermaid
sequenceDiagram
  actor Operator
  participant Console
  participant Cluster
  participant Platform as swhurl-platform
  participant Validate as Validate workflow
  participant Flux
  participant Prod as Production namespace
  participant CM as cert-manager
  participant Dash as dashboard sync

  Operator->>Console: Review promotion for app
  Console->>Cluster: Check staging unit, HelmRelease, replicas and running digest
  Console->>Platform: Read staging/prod config and production existence from main
  Console-->>Operator: Show exact image, changes and destination
  Operator->>Console: Promote to production
  Console->>Cluster: Recheck staging before preparing PR
  Console->>Platform: Run app-promote in a fresh main checkout and open console/promote PR
  Platform->>Validate: PR event runs Validate
  Validate-->>Platform: Checks pass
  Platform->>Platform: Trusted merge gate verifies exact promotion and current main
  Note over Operator,Platform: Eligible PR auto-merges and setup PR waits for operator edits and merge
  Platform->>Flux: Merge push to main invokes Receiver (or source poll)
  Flux->>Prod: First promotion creates prod unit and later promotion updates only image pin
  opt First production and app has a route
    Prod->>CM: New Ingress requests TLS certificate
    CM->>Prod: HTTP-01 through Traefik and certificate saved in prod TLS Secret
  end
  Dash->>Prod: Every minute list HelmRelease records created by app Flux units
  Dash->>Dash: Group matching staging and production records by app
  Dash->>Dash: Create or update App dashboard with production line
```

The review captures the staging image, applied revision, staging configuration hash and production configuration hash. The console checks again at submission, when the job starts, and after preparing the change; it refuses if staging stopped being healthy/current or either reviewed configuration changed. It also refuses the same digest (tags may differ while the digest is the same) and refuses a changed image repository. A known older `<run>-<sha>` image is called out as a rollback; database migrations are not rolled back.

On **first promotion**, `app-promote` derives a production environment from the supported staging settings and adds `apps/<app>/prod/`, `clusters/home/app-<app>-prod.yaml` and the production unit registration. If the app has Secret keys, it creates a separate encrypted production Secret with `REPLACE_ME` values; SQLite/persistent storage gets a separate retained claim that starts empty. Staging data and credential values are never copied. A secret-bearing PR also adds the production namespace to Reloader's watch list. A public app must have a distinct production hostname; signed-in apps get the generated production host. The staging environment and production environment are separate namespaces, so the same Secret/PVC names resolve to independent resources.

First promotion with a Secret stub or a public production host is not auto-merged. For a Secret, set the production values on the PR branch with `sops apps/<app>/prod/secret.sops.yaml`; review that file and the Reloader registration, then merge the PR yourself. For a public app, review the chosen distinct host and merge the PR yourself. A first promotion of an app that publishes or receives [events](#events) is not auto-merged either: it adds the production events key (`events.sops.yaml`), generated and already encrypted, so nothing is left to set; review it and merge the PR yourself. The job log says which of the two cases it is. A first promotion with no such setup can auto-merge after validation unless **Hold for manual review** was selected. For an eligible auto-merge, the trusted merge gate independently regenerates first production from the reviewed staging configuration and refuses extra or changed files.

After merge, the platform push webhook starts Flux's fetch/reconcile chain (the `GitRepository` polls every minute if the webhook is missed). For first production, Flux creates the new `app-<app>-prod` unit, which waits for `infra-base` and `platform-oauth2-proxy` when the app is signed in. It applies the production Namespace and HelmRelease, then Helm creates the production Deployment and, for web apps, Service; routed apps also get an Ingress. The storage claim is provisioned independently when configured. A routed first production Ingress causes cert-manager to request a certificate asynchronously: the issuer is copied from staging (default `letsencrypt-prod`), HTTP-01 uses Traefik for Let's Encrypt, and the resulting TLS Secret is used by Traefik. Certificate issuance does not hold the app Helm readiness check open. Private apps/workers have no Ingress and request no certificate.

The dashboard CronJob runs each minute. It sees the production HelmRelease once the production Flux unit has created it, then creates or updates the same `App: <app>` dashboard to include production as another environment line; it does not create a second dashboard or add dashboard files to the PR. It does not wait for production pods to become Ready and is independent of the app rollout. Details are in [Dashboards](#dashboards).

On **later promotions**, production already exists, so the reviewed PR changes only the image tag/digest in `apps/<app>/prod/helmrelease.yaml`. Production's runtime settings, host, Secret, storage, unit and Reloader registration remain as they were. No new certificate or other infrastructure is requested. The image-only PR auto-merges after `Validate` and the trusted merge gate unless held for review; Flux then rolls out the pinned image. Promotion does not run the app repository's build workflow and does not invoke the app's image automation. The image has already been built and deployed to staging before it is selected.

## Operate an instance

```bash
make app-status APP=demo-orders ENV=prod     # state, Git revision applied?, running image matches?, replicas, who can reach it, route, TLS, failures
make app-logs APP=demo-orders ENV=prod       # FOLLOW=true, TAIL=N, PREVIOUS=true (last crashed container)
make app-reconcile APP=demo-orders ENV=prod  # fetch Git and reconcile this instance's unit now
make app-check APP=demo-orders ENV=prod      # the app policy, offline
make app-scale APP=demo-shop ENV=staging ARGS="--replicas 2 --memory-limit 256Mi"   # Git edit: commit and push
```

The console's app page shows what `make app-status` shows and offers **Reconcile** and **Scale** (as a pull request). `app-scale`, `app-expose` and `app-promote` accept handwritten YAML using the app-template structure. They retain comments (including Flux image automation markers), quotation styles, key order, flow collections and consistent indentation. For example, scaling `memory: "128Mi" # measured limit` to 256Mi keeps the quotes and comment. Later promotion changes only the target image tag/digest; first promotion has the [supported conversion boundary](#promote-to-production). Exposure edits the main host, TLS host, sign-in middleware, Namespace label and required Flux dependencies; it keeps other annotations, middleware, paths, TLS secret names and extra dependencies.

The editor uses a pinned [ruamel.yaml](https://yaml.dev/doc/ruamel.yaml/detail/) dependency rather than a text-replacement parser. **Trade-off:** it preserves YAML presentation, not every byte; inconsistent indentation and unusual spacing can be normalized. Each file must contain one mapping document, without duplicate keys. A changed mapping that uses an anchor or merge is refused rather than changing shared settings unexpectedly. Exposure requires one main host and matching TLS entry; making an instance private refuses additional routes that would still expose it. Such custom structures need a manual edit and `make check-apps`. `app-remove` deletes and unregisters the instance; it does not rewrite its YAML.

Generation and scale/expose/promotion validate the resulting files against the app policy. A policy violation, missing tool or failed render returns nonzero. Files already written remain locally for review; fix the failure and run `make check-apps` before committing. The console uses the same commands and opens no PR when they fail. Only generation offers the explicit `--no-policy-check` opt-out.

The [notification expectations](services.md#notification-expectations) define deployment, rollback, uninstall and prolonged-health messages for each app/environment; the [current filters](services.md#current-notification-behavior) describe what is live today. When a pod fails, `make app-status` and the app page name the usual cause and the fix: out of memory (raise `--memory-limit`), an image the node cannot pull (missing tag, or a private GHCR package), a missing Secret value, a crash loop (`make app-logs APP= ENV= PREVIOUS=true` shows the crashed container's output; usually the wrong port or a write outside `/tmp`), or a failing readiness check (the app must answer its health path on its port).

An instance fails fast: if its pods are not Ready within 3 minutes of a change (`FAIL_AFTER` in [`contract.py`](../tools/swhurl/apps/contract.py): the unit's `timeout` and the HelmRelease's `timeout`), its unit turns red and `make flux-reconcile` reports it; Helm then retries once and stops. `make app-status` lists failing containers. After fixing the cause, push, or run `make app-reconcile`. An app that genuinely needs longer to start (a large image, a slow first migration) can raise both timeouts in its own files.


### Dashboards

The scheduled `console-dashboards` job gives every Flux-managed app a HyperDX dashboard named `App: <app>` (tag `swhurl-app`), with staging and production as separate lines:

| Row | Web app | Worker |
| --- | --- | --- |
| Traces (needs `--otlp`) | Requests per minute, errors, p95 latency of server spans | Runs per minute, errors, p95 duration of root spans |
| Logs (every app) | Error logs and log lines per minute | the same |
| Logs | The latest log lines, with their environment | the same |

The in-cluster `console-dashboards` CronJob runs every minute. It asks Kubernetes for HelmRelease records in all namespaces, then keeps only records whose Flux labels identify an `app-<name>-<env>` unit in `flux-system` and whose release name and namespace match that app and environment. This excludes shared services and manually installed releases. It does not check readiness, so discovery starts as soon as the app Flux unit creates the HelmRelease, even while Helm is still installing it. The job groups discovered environments by app and creates or updates one HyperDX dashboard named `App: <app>`; first promotion adds a production line to the dashboard created for staging. The API call is to HyperDX; dashboards are not Kubernetes resources or Git files. Each run also records each app's dashboard id in the `console/app-dashboards` ConfigMap, which is what the console's **Dashboard ↗** links read ([console](console.md#automatic-app-dashboards)). This works for console PRs and CLI changes, including auto-merges.

Every tile selects the app's namespaces, so an app without an SDK still gets its log rows, and trace tiles stay empty until the app has traffic (health checks are not traced). Each run overwrites dashboards that differ from [`dashboards.py`](../tools/swhurl/dashboards.py) (an edit made in the HyperDX UI is lost on the next run; save a copy without the `swhurl-app` tag to keep it). When an app's last HelmRelease is gone, the next run deletes its dashboard. Removing one environment only drops its line. The job deletes narrowly: only a tagged dashboard named exactly `App: <app name>`, so a tagged copy such as `App: hello-ts (copy)` stays and is reported; and nothing when it finds no app release at all (a cluster still being restored, or the last app removed). `make clickstack-dashboards` synchronizes against this checkout instead and deletes every tagged dashboard whose app is not in Git, which covers those two cases; `DRY_RUN=true` previews it. A deleted dashboard that comes back (an app re-added, a HelmRelease recreated) gets a new id, so old links to it break. Both paths read the admin account's access key from MongoDB and never print it. Failures exit nonzero and retry on the next minute; `make verify-platform` fails if the job has not succeeded within five minutes. No app deployment waits on ClickStack.

### Alerts

Every app also gets default alerts, kept by the same job as its dashboard: nothing is declared, and they come and go with the app.

| Alert | Fires when | Recovers |
| --- | --- | --- |
| `swhurl:<app>:error-logs` | 5 or more log lines of severity `error` or `fatal` from one environment of the app in 5 minutes (the dashboard's **Error logs** tile) | When a 5-minute window stays below that |
| `swhurl:<app>:errors` | 5 or more failed requests (web) or failed runs (worker) in 5 minutes (the **Errors** tile; needs traces, so `telemetry: otlp`) | The same |

```mermaid
flowchart LR
  tile[App dashboard tile<br/>per environment] --> rule[HyperDX alert<br/>swhurl:orders:error-logs]
  rule -- "webhook swhurl notify" --> notify[Notify service]
  notify --> channel[the team's channel]
```

A firing alert reaches the app's team's [notification channel](services.md#notification-channels) as `orders/staging alert: error logs`, high priority, with the count, the time range and a link to the dashboard; its recovery arrives as `orders/staging recovered: error logs`. A team without a channel receives nothing, so give the team one first. The operator's platform topic does not get app alerts.

The rules are in [`app_alerts.py`](../tools/swhurl/app_alerts.py). Each alert is tagged `swhurl-app` and with its app's name, so the HyperDX alerts page filters to one app by that tag. Each run of the dashboard job creates the missing ones, updates in place any that differ (an edit made in the HyperDX UI is lost, as for dashboards) and deletes those of apps that are gone; alerts not named `swhurl:...` are never touched, so add your own beside them. They need the `swhurl notify` webhook in HyperDX: `make clickstack-notify-webhook` creates it ([services](services.md#notify-service)). Until it exists the job fails, so `make verify-platform` and the console's Platform page show the dashboard and alert sync as failing, with that command in the message.

**An app's own settings.** An app that is noisier or quieter than the defaults says so once, for every environment:

```
make app-alerts APP=orders ARGS="--error-logs 20"      # fire at 20 error logs in 5 minutes instead of 5
make app-alerts APP=orders ARGS="--errors none"        # no alert on failed requests; error logs stay
make app-alerts APP=orders ARGS="--none"               # no default alerts at all
make app-alerts APP=orders ARGS="--default"            # back to the platform's
```

or at creation with `alerts:` in `swhurl.yaml` (`alerts: error-logs=20,errors=none`, or `alerts: none`). It is the `platform.swhurl.com/alerts` annotation on the app's namespaces, so it follows a promotion; `make check-apps` (rule `alerts`) refuses a value it cannot read. The word is `none`, not `off`, which YAML would read as false.

## Remove an app

`make app-remove APP=<app> ENV=<env>` (or **Uninstall** in the console, as a pull request) deletes the instance's files, its unit file and registration and its Reloader entry; once pushed, Flux uninstalls it, and within about a minute of the last environment's HelmRelease going the dashboard job deletes `App: <app>` ([dashboards](#dashboards); after removing the only app on the cluster, run `make clickstack-dashboards`). The app's telemetry stays in ClickStack until retention, searchable by namespace. An instance with `--persistence` keeps its namespace and claim on the cluster: deleting that data is a separate, explicit `make destroy-data` ([lifecycle](operations.md#lifecycle)). Removing staging also removes its automatic deploys; the app repository and its images are untouched.

## The app policy

`make check-apps` renders every instance with Helm and checks the Kubernetes objects: pinned images (digest in production), non-root, no privilege escalation, CPU/memory requests and a memory limit, no service-account token, no host access, exposure (private has no Ingress; hosts under `homelab.swhurl.com` need sign-in; public hosts stay outside it), TLS on every host, a named storage class, a single writer per volume (an instance that mounts a ReadWriteOnce claim runs one replica and stops the old pod before starting the new one), `HOST_IP` defined before an OTLP endpoint uses it, a registered team, the platform quota with room for the app's replicas, the platform NetworkPolicy on the app's pods only ([teams and isolation](#teams-and-isolation)), and its identity, declared calls and their tokens ([calling another app](#calling-another-app)), and the stream, consumers and NATS user that follow from its declared [events](#events); across an environment, every declared call has a callee that allows it and every subscription a publisher. Every instance under `apps/` must also be an app made from a stack template (rule `platform-image`, no exceptions): its image is `ghcr.io/samclement/<app>` and its staging has `image-automation.yaml` and the setter markers. It also compares the source manifests of an app's environments: they may differ only in namespace, hosts, image tag and digest, replicas, resources, issuer and exposure (when the environments' exposure differs, their routes are not compared; each is still checked on its own); encrypted Secrets and staging's `image-automation.yaml` are skipped. CI runs it on every push; the rules are listed in [`policy.py`](../tools/swhurl/apps/policy.py).

A reviewed exception goes on the HelmRelease, with a reason:

```yaml
metadata:
  annotations:
    platform.swhurl.com/policy-exceptions: "no-escalation=needs raw sockets for ICMP"
```

## Moving a host between instances

Deploy the new instance on a temporary host and check it. Then, in one commit, remove the old route and put the host on the new instance. Expect a few seconds of Traefik's default certificate while cert-manager issues the new one.

## Limits

- A declared caller is trusted not to forge a person: a web app cannot tell oauth2-proxy's `x-auth-request-email` header from one an allowed caller sent itself.
- Calls and events stay inside the cluster: there is no token for a caller outside it, and NATS has no route.
- A team cannot set its own secret values or merge its own changes: there is one encryption key and one repository, and the pull request is the other team's consent to a new caller.
- The only database is SQLite, so an app with one runs a single replica.
- The app policy is checked on pull requests (`make check-apps`), not at admission: a manifest applied by hand is not checked.
- Isolation is inbound only: an app's pods accept Traefik or nobody, but may connect out to anything. There is no per-team access control: a team is a label.
- Everything under `homelab.swhurl.com` shares the sign-in cookie.
- Only apps made from a stack template are supported, and only staging updates automatically; production changes through a promote ([deploy a new image](#deploy-a-new-image)).
- App images must be public: the cluster has no registry pull credentials.

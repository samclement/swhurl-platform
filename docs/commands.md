# Commands

Every `make` target, grouped by task. Each is stated once, in [`tools/swhurl/commands.py`](../tools/swhurl/commands.py): its variables, its help line, whether it takes `DRY_RUN=true` and what it touches. `make help` prints that list, a target refuses a missing variable with its usage line, and a test fails when a row here differs from it in name, variables, the † mark or the first word of **Touches**.

**Touches:** **Local** is this checkout at most (some read the network); **Git** edits files for you to commit; **Cluster (read)** reads the live cluster; **Cluster** changes it; **Cluster (throwaway)** creates and removes labelled test resources; **GitHub** creates or changes a repository there; **Host** installs system units. Targets marked † accept `DRY_RUN=true` to print the plan (or run only read-only checks) without acting. Variables in `[]` are optional.

## Deploy and verify

| Target | Does | Touches |
| --- | --- | --- |
| `flux-reconcile` | Fetch Git, reconcile the source layer and the stack, then wait until every unit is Ready at that revision, or at a newer one if a commit lands meanwhile (`swhurl flux-wait`: stops at the first unit that fails, skips suspended ones) | Cluster |
| `install` † | `flux-reconcile`, then `verify-platform` (`SKIP_VERIFY=1` skips the checks) | Cluster |
| `verify-platform` | Every live check of the platform, listed in [operations](operations.md#check-health); warns or fails, never prints a key | Cluster (read), S3 (list) |
| `notifications-check` | Preview [lifecycle and health notifications](services.md#alerts) from the live cluster without posting or changing incident state | Cluster (read) |
| `notifications-heartbeat [ARGS=--dry-run]` | Check `console-notifications`; by default alert on stale/missing/suspended status, remind hourly and send recovery when it succeeds again. Pass `ARGS=--dry-run` to preview without posting or saving state | Cluster (read); ntfy and local state on transitions |
| `verify-logs [MINUTES=15]` | Summarize fresh ClickStack log parser, severity, trace and original-line coverage per running container; report quiet containers separately, without printing bodies. `MINUTES` is 1 to 1440 | Cluster (read) |
| `clickstack-bootstrap` † | After a ClickStack install: register the admin from SOPS if no team exists, set the team ingestion key to `CLICKSTACK_INGESTION_KEY`; idempotent, never prints values ([services](services.md#clickstack-and-otel)) | Cluster |
| `clickstack-dashboards` † | A HyperDX dashboard per app in `apps/`: create, update to match Git, delete those of removed apps; `DRY_RUN=true` lists changes. The scheduled job does the same from the cluster's HelmReleases, so this is for a check or the cases it skips ([apps](apps.md#dashboards)) | Cluster |
| `clickstack-ntfy-webhook` † | Create or update the reusable [ClickStack ntfy webhook](services.md#clickstack-and-otel) from the live platform topic in `console/notification-ntfy`; `DRY_RUN=true` previews without writing. Run after rotating that topic | Cluster |
| `clickstack-notify-webhook` † | Create or update the `swhurl notify` webhook in HyperDX, which hands every app's [default alerts](apps.md#alerts) to the [notify service](services.md#notify-service) with the secret from the live `notify/notify-alerts`; `DRY_RUN=true` previews without writing. Idempotent; run it once after bootstrap and again after changing that secret | Cluster |
| `reconcile UNIT=<name>` † | Fetch Git and reconcile one Flux unit, for example after changing its Secret | Cluster |
| `flux-install` † | Install or upgrade the Flux controllers at `FLUX_VERSION` in `tools/swhurl/flux.py` with the patches in `clusters/home/flux-system/install`; refuses a different `flux` CLI; `DRY_RUN=true` prints the live diff | Cluster |
| `flux-bootstrap` | Apply the root units and sources in `clusters/home/flux-system` (Flux must already be installed) | Cluster |

## Apps

In the order of an app's life ([apps](apps.md)): create, set access, promote, operate, remove; then the console's own image.

| Target | Does | Touches |
| --- | --- | --- |
| `app-repo NAME=<app>` † | Create the app's public GitHub repository from a stack's Copier template (`TYPE=web-app`, `api`, `worker` or `scheduled-job`, required, which sets every answer; `STACK=typescript` or `kotlin`, the type's first by default; `DESCRIPTION=`), push it, add the [image webhook](services.md#image-webhook), wait for its first image and print the `app-new` line ([start a new app](apps.md#start-a-new-app)) | GitHub |
| `app-hooks` † | Create the [image webhook](services.md#image-webhook) on the repository of every app with automatic deploys, or repoint it after its token was rotated; leaves a correct one alone. Uses your `gh` login and reads the token from SOPS | GitHub |
| `app-new NAME=<app> ARGS="--from-repo samclement/<app> --image ..."` | Generate the staging instance of an app made by `app-repo` or the console (the line they print) and check it against the app policy; defaults come from the app's [`swhurl.yaml`](apps.md#swhurlyaml). Refuses any other repository or image | Git |
| `app-expose APP= ENV= ARGS="--exposure ... [--host H]"` | Change who can reach an instance: `private` (removes the route), `authenticated-web` (Google sign-in; keeps or derives the host) or `public` (needs `--host` outside `homelab.swhurl.com`); updates the route, the Namespace label and the unit's dependencies | Git |
| `team-add NAME= [DESCRIPTION=]` | Register a team in `apps/teams.yaml` ([teams](apps.md#teams-and-isolation)) | Git edit |
| `team-edit NAME= ARGS="--description TEXT"`, `"--rename NEW"` or `"--notify CHANNEL"` | Change a team's description, name or [notification channel](services.md#notification-channels) (one per team; `--notify ""` clears it). A rename relabels every app of the team in the same change; the default team cannot be renamed | Git edit |
| `team-remove NAME= [ARGS="--move-apps-to OTHER"]` | Remove a team. Refused while apps belong to it, unless they are moved to another team in the same change | Git edit |
| `team-channel NAME= [ARGS="--remove"]` | Give a team its one [notification channel](services.md#notification-channels), or a new address for the one it has: prompts for the address without showing it (or reads one line from standard input) and writes it as an encrypted Secret named after the team. `--remove` takes the channel away. Needs `sops`; decrypts nothing | Git edit |
| `channel-add NAME= [ARGS="--replace"]` | Add a [notification channel](services.md#notification-channels): prompts for its address without showing it (or reads one line from standard input) and writes it as an encrypted Secret. `--replace` gives an existing channel a new address. Needs `sops`; decrypts nothing | Git edit |
| `channel-remove NAME=` | Remove a channel. Refused while a team notifies it | Git edit |
| `app-team APP= ARGS="--team T"` | Move an app, in every environment, to a team registered in `apps/teams.yaml` (the Namespace label); also writes the instance's quota and NetworkPolicy when they are missing ([teams](apps.md#teams-and-isolation)) | Git edit |
| `app-alerts APP= ARGS="--error-logs N\|none --errors N\|none"`, `"--none"` or `"--default"` | Change an app's [default alerts](apps.md#alerts) in every environment: the count at which each fires, one or all switched off, or back to the defaults. The dashboard job replaces the alerts within a minute of the reconcile | Git edit |
| `platform-signing-keys` | Read the cluster's public token-signing keys and write them to `apps/signing-keys.json` and into every web app instance ([calling another app](apps.md#calling-another-app)). Needed only when the cluster's service-account key changes | Git edit (reads the cluster) |
| `app-calls APP= ENV= ARGS="--add A,B"` or `"--remove C"` | Change which apps an instance may call. Writes the caller's address variable and token file, and the callee's allowed callers and NetworkPolicy, in one change ([calling another app](apps.md#calling-another-app)) | Git edit |
| `app-egress APP= ENV= ARGS="--add api.stripe.com"` or `"--remove api.stripe.com"` | Change which hosts outside the cluster an instance may reach: comma-separated `host[:port]` (443 by default; `*.example.com` for any name under it). Writes the release's `platform.swhurl.com/egress` annotation and the egress proxy's configuration; the proxy logs whether a grant covers each request ([reaching outside the cluster](apps.md#reaching-outside-the-cluster)) | Git edit |
| `app-events APP= ENV= ARGS="--add-subscribe APP.EVENT"`, `"--remove-subscribe …"`, or `"--subscribe APP.EVENT,…"` | Grant the events an instance receives: add/remove names or replace its subscription list (`""` clears it). Publications come only from the image's AsyncAPI document; there are no publishing flags. Creates and encrypts a missing NATS key, writes consumers and regenerates the user list ([events](apps.md#events)) | Git edit (needs `sops`) |
| `app-offers [APP=] [ENV=]` or `[ARGS="--all staging"]` | Read what an app offers from its repository, at the commit its image was built from: the OpenAPI and AsyncAPI documents its `swhurl.yaml` names. Keeps them in `apps/<app>/<env>/offers/` and sets what the instance publishes from the AsyncAPI document ([what an app offers](apps.md#what-an-app-offers)). The Offers workflow runs it after every new staging image | Git edit (reads GitHub; needs `sops` the first time an app describes events) |
| `app-deploy [APP=] [ARGS=--refresh]` | Bring staging to each app's newest image (or one app's): the newest tag its ImagePolicy accepts and its digest, read from GHCR, with what that image's `swhurl.yaml` needs (port, health path, uid, telemetry, minimum resources) and offers (read as `app-offers` does). An image whose needs or offers are refused is held and the others still deploy. `--refresh` reads images staging already runs again. The [Deploy workflow](apps.md#deploy-a-new-image) runs it on each new image and hourly | Git |
| `app-sync` | Rewrite what the platform derives in every instance: its identity variables, the signing keys, each callee's allowed callers and NetworkPolicy, each instance's stream and consumers, and the NATS user list. Run it after `platform-signing-keys`; otherwise the other commands keep these in step | Git edit |
| `app-promote APP= [ARGS="--expect-image=..."]` | Create production from staging, or update its image; staging → production only; `--expect-image` guards the reviewed image ([promotion](apps.md#promote-to-production)) | Git |
| `app-status APP= ENV=` | Its state (the console's verdict), whether Git is applied and the running image matches it (compared by digest), replicas, who can reach it, route, certificate, failing containers | Cluster (read) |
| `app-logs APP= ENV=` | Recent workload logs (`FOLLOW=true`, `TAIL=N`, `PREVIOUS=true` for the last crashed container) | Cluster (read) |
| `app-reconcile APP= ENV=` | Fetch Git and reconcile only that instance | Cluster |
| `app-check APP= ENV=` | Render one instance and check it against the app policy | Local |
| `app-secret APP= ENV= NAME=` | Set one secret value of an instance, read from standard input or asked for without echo, into `apps/<app>/<env>/secret.sops.yaml` through `sops` (needs the age key: run it in your own terminal). A name the image does not declare yet is accepted ([secrets](apps.md#secrets)) | Git |
| `app-schedule APP= ENV= ARGS="--schedule '...'"` | Change when a [job](apps.md#scheduled-jobs) runs: five cron fields in `Etc/UTC` (`"*/10 * * * *"`) or `@hourly`, `@daily`, `@weekly`, `@monthly`, `@yearly`; refused for anything but a job | Git |
| `app-scale APP= ENV= ARGS="..."` | Change `--replicas` (0 to 10), `--cpu`, `--memory` or `--memory-limit`; never below the minimum in the running image's `swhurl.yaml` | Git |
| `app-remove APP= ENV=` | Delete the instance's files, its unit file and registration, and its Reloader namespace; warns when a retained volume will be kept | Git |
| `console-image` | Pin `platform/console` and the [notify service](services.md#notify-service) to the [console](console.md#deploy-a-new-console) image published for this commit: the `src-<hash>` tag of its inputs and its digest on GHCR. Refuses with uncommitted inputs or before the image is published. Validate's deploy job does this itself (`--expect src-<hash> --commit`); by hand it is the fallback | Git |
| `console-dev [PORT=8080]` | The [console](console.md) on `http://127.0.0.1:8080` as a fixed dev identity. It uses your kubeconfig, so its buttons act with your rights, and opens real PRs if `GITHUB_TOKEN` is set. Needs `uv` | Cluster, GitHub |

App creation/editing and template checks use `uv` with locked dependencies. App creation and edits fail if requested policy validation cannot run. Scale, expose and promote accept handwritten YAML; see [editing boundaries and formatting trade-offs](apps.md#operate-an-instance).

## Settings

| Target | Does | Touches |
| --- | --- | --- |
| `platform-certs-staging`, `platform-certs-prod` † | Set `CERT_ISSUER` in `platform-settings`: only issuers defined in Git, one line changed, comments kept | Git |

## Lifecycle, backup and recovery

| Target | Does | Touches |
| --- | --- | --- |
| `suspend`, `resume` `TARGET=kustomization/<name>\|helmrelease/<ns>/<name>` † | `flux suspend/resume`; workloads and data untouched | Cluster |
| `destroy-data TARGET=pvc/<ns>/<name>\|pv/<name> CONFIRM=<TARGET>` † | Delete a released claim or volume and its host data ([lifecycle](operations.md#lifecycle)) | Cluster |
| `backup-mongodb` † | Start a Job from ClickStack MongoDB's backup CronJob (`observability/backup-mongodb`), wait for it and show its log; the job dumps, encrypts and publishes to `clickstack-mongodb/` ([operations](operations.md#backups-and-recovery)) | Cluster, S3 |
| `backup-sqlite` † | Start a Job from each app SQLite database's backup CronJob (`<app>-backup`), wait for them and show their logs; each copies, checks, encrypts and publishes to `app-sqlite/<namespace>/`. No app with a database: nothing to do | Cluster, S3 |
| `restore-sqlite APP= ENV= CONFIRM=<app>/<env>` † | Restore an app's SQLite database from its newest backup in S3 (or `BACKUP_FILE`; `AGE_KEY_FILE`; needs S3 read access): suspends its HelmRelease, stops the app, checks the copy, keeps the replaced files in `before-restore-<UTC>/`, starts the app ([backups](operations.md#backups-and-recovery)) | Cluster |

## Offline checks

Verbs: `check` and `check-*` never touch the cluster and `verify-*` only read it (both tested against the Touches column); `live-test-*` act on the live cluster, on throwaway resources except where the row says otherwise.

| Target | Does | Touches |
| --- | --- | --- |
| `check` | `check-repo`, `test`, `check-apps`, `check-otel`, `check-lint` in parallel: everything CI runs except `check-templates`, which CI runs on every push. Run before every push; run `check-templates` too when a change touches a template pin, the app types or what `app-new` writes | Local |
| `check-repo` | Render every active Flux path, validate schemas, SOPS structure, shell syntax and doc links, check that every unit that decrypts has a Secret file and that `BASE_DOMAIN` and `CERT_ISSUER` are set, and compare the [Flux units table](architecture.md#flux-units) with the units in Git | Local |
| `test [K=<pattern>]` | Unit tests: lifecycle guards, verifier, policies, generator, console, fixtures, Flux unit rules, and every dry run that plans without touching anything (runs in `uv`'s locked environment, tests in parallel; a class with `setUpClass` stays together), then the slowest classes. `K` runs only the classes whose dotted name contains it (`K=test_console` while working on the console); `check` always runs them all | Local |
| `check-apps` | Render every app instance with Helm and check the app policy, and that the [app model](plan.md#24-code-simplification--design-contracts-18-october-2026) reproduces every instance | Local |
| `check-templates` | Render each app type from its stack's pinned template; each `swhurl.yaml` must pass `app-new` and the app policy in staging and prod, and the template must refuse answers no type has; one process per CPU, about 15 s ([apps](apps.md#stacks-and-features)) | Local (reads GitHub; needs `sops` for a template that declares events) |
| `check-otel` | Render both collectors and validate with the exact `otelcol-k8s` release and feature gates (binary cached and SHA-256 checked); run log fixtures through their actual processors and container framing; warn on deprecated component names | Local |
| `check-lint` | Ruff on the Python tooling and ShellCheck on the remaining bash (needs `uv`) | Local |
| `check-secrets` | Decrypt every tracked Secret in memory; fail on empty or `REPLACE_ME`, warn on probable double encoding. Needs the age key, so CI cannot run it | Local |
| `help` | List every target with its variables, the † mark and what it touches | Local |

## Live tests

| Target | Does | Touches |
| --- | --- | --- |
| `live-test-lifecycle` † | Prove lifecycle commands on a throwaway app | Cluster (throwaway) |
| `live-test-reloader` † | Prove Reloader's opt-in and namespace scope | Cluster (throwaway) |
| `live-test-app-template` † | Deploy the generated fixtures through Flux, check, remove | Cluster (throwaway) |
| `live-test-restore-mongodb` † | Restore the newest backup in S3 (or `BACKUP_FILE`) into a throwaway namespace and check it (`AGE_KEY_FILE`, `KEEP=true`; needs S3 read access) | Cluster (throwaway) |
| `live-test-restore-sqlite` † | Back up a throwaway app's SQLite database with the backup job, change it, restore it from S3 with `restore-sqlite` and check the rows, the kept files and the resumed HelmRelease (`AGE_KEY_FILE`, `KEEP=true`) | Cluster (throwaway) |
| `live-test-call-identity` † | Prove a callee with no API token verifies a caller's short-lived, audience-bound service-account token against the cluster's public keys handed to it as a ConfigMap, and that the Kubernetes API refuses that token. Waits about 8 minutes for the kubelet to replace the token (`SKIP_ROTATION=true` skips that) | Cluster (throwaway) |
| `live-test-team-scenario CALLER= CALLEE= SUBSCRIBER= [ENV=staging]` † | Send one request through three deployed, linked apps (CALLER calls CALLEE, which publishes an event SUBSCRIBER receives) and check in ClickStack that one trace holds all three, with the caller named and each app's team. The apps need the stack templates' example routes | Cluster (one request; creates nothing) |
| `live-test-network-policy` † | Prove a NetworkPolicy selecting an app's pods blocks another namespace while Traefik, probes and an unselected pod on 8089 keep working | Cluster (throwaway) |

## Host

| Target | Does | Touches |
| --- | --- | --- |
| `host-heartbeat`, `host-heartbeat-delete` † | Install or remove the five-minute notification checker heartbeat timer (`swhurl-notification-heartbeat`) | Host |
| `host-dns`, `host-dns-delete` † | Install or remove the Route 53 dynamic DNS system timer (`aws-dns-updater`, every 10 minutes; records in `host/dns.env`) | Host |

All three use [`host/install-timer.sh`](../host/install-timer.sh): a system unit that runs a script from this checkout as you, so edits to the script or `host/dns.env` apply at the next run; only unit template changes need a reinstall. All ask for `sudo`, so run them in your own terminal. Output goes to `/var/log/swhurl-platform/<unit>.log` and ClickStack.

Environment variables the targets read: `DRY_RUN`, `SKIP_VERIFY`, and the per-target ones in the tables. Host DNS records are in [`host/dns.env`](../host/dns.env). Cluster settings live in Git, not here ([services](services.md#settings)).

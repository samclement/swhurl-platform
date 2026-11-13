# Commands

Every `make` target, grouped by task. Each is stated once, in [`tools/swhurl/commands.py`](../tools/swhurl/commands.py): its variables, its help line, whether it takes `DRY_RUN=true` and what it touches. `make help` prints that list, a target refuses a missing variable with its usage line, and a test fails when a row here differs from it in name, variables, the † mark or the first word of **Touches**.

**Touches:** **Local** is this checkout at most (some read the network); **Git** edits files for you to commit; **Cluster (read)** reads the live cluster; **Cluster** changes it; **Cluster (throwaway)** creates and removes labelled test resources; **GitHub** creates or changes a repository there; **Host** installs system units. Targets marked † accept `DRY_RUN=true` to print the plan (or run only read-only checks) without acting; any other target that runs through `swhurl` or [`host/ops.sh`](../host/ops.sh) refuses `DRY_RUN=true` and does nothing. Variables in `[]` are optional.

## Deploy and verify

| Target | Does | Touches |
| --- | --- | --- |
| `flux-reconcile` | Fetch Git, reconcile the source layer and the stack, then wait until every unit is Ready at that revision, or at a newer one if a commit lands meanwhile (`swhurl-v2 flux-wait`: stops at the first unit that fails, skips suspended ones) | Cluster |
| `install` † | `flux-reconcile`, then `verify-platform` (`SKIP_VERIFY=1` skips the checks) | Cluster |
| `verify-platform` | Every live check of the platform, listed in [operations](operations.md#check-health): builds `swhurl-v2` and runs all twenty-four as `swhurl-v2 verify --scope platform` (needs Go 1.26.0), which reads the cluster, this checkout, the registry, S3 and GitHub, runs fixed read-only pod queries and judges the host facts this recipe supplies — the console content tag and `systemctl show` of the heartbeat units (AWS credentials from `aws configure export-credentials` unless `AWS_ACCESS_KEY_ID` is set; hook listings use `GH_TOKEN` or `gh auth token`); warns or fails, never prints a key | Cluster (read), Secrets (read), S3 (list), GitHub (read), commands in pods, this host |
| `notifications-check` | Build `swhurl-v2` and preview [lifecycle and health notifications](services.md#alerts) from the live cluster, always with `--dry-run`: no posts or incident-state changes (needs Go 1.26.0, kubectl and jq) | Cluster (read) |
| `notifications-heartbeat [ARGS="..."]` † | Check `console-notifications` with `swhurl-v2 notifications-heartbeat` (needs Go 1.26.0): alert on stale/missing/suspended status, remind hourly and send recovery when it succeeds again ([operations](operations.md#notification-checker-heartbeat)). `DRY_RUN=true` previews without posting or saving state; `ARGS` takes `--max-age D` and `--state PATH` | Cluster (read); ntfy and local state on transitions |
| `clickstack-bootstrap` † | After a ClickStack install: register the admin from SOPS if no team exists, set the team ingestion key to `CLICKSTACK_INGESTION_KEY` (`swhurl-v2 clickstack-bootstrap`, needs Go 1.26.0); idempotent, never prints values ([services](services.md#clickstack-and-otel)) | Cluster |
| `clickstack-dashboards` † | Create or repair the two [platform-owned HyperDX webhooks](services.md#clickstack-and-otel) before app alerts, even with no apps; a HyperDX dashboard per app in `v2-records/`: create, update to match Git, delete those of removed apps; `DRY_RUN=true` plans and writes nothing. Runs `swhurl-v2 clickstack-dashboards --records v2-records` with the caller's kubeconfig (needs Go 1.26.0). The scheduled job does the same from the cluster's HelmReleases, so this is for a check or the cases it skips ([apps](apps.md#dashboards)) | Cluster |
| `reconcile UNIT=<name>` † | Fetch Git and reconcile one Flux unit, for example after changing its Secret (`swhurl-v2 unit`: the source, then the unit; refuses `cluster-sources` and `cluster-stack`, which `flux-reconcile` reconciles) | Cluster |
| `flux-install` † | Install or upgrade the Flux controllers at the version in `clusters/home/flux-system/install/flux.json` with the patches beside it; refuses a different `flux` CLI (`host/flux-install.sh`); `DRY_RUN=true` prints the live diff | Cluster |
| `flux-bootstrap` | Apply the root units and sources in `clusters/home/flux-system` (Flux must already be installed) | Cluster |

The `notifications-check` recipe exports the caller's kubeconfig (`KUBECONFIG` or kubectl's default) as JSON into
a private temporary file, removed on success or failure. Kubeconfig export or profile lookup failure stops before
Go runs. Console links default to `https://console.<routing.baseDomain>` from `v2/testdata/profile.json`; set
`CONSOLE_URL` to override without reading the profile. No notification topic or token is needed. The target always
passes `--dry-run`, regardless of `DRY_RUN` or `ARGS`. For the scheduled command and direct Go invocation's delivery
credentials and effects, see [current notification behavior](services.md#current-notification-behavior).

## Apps

Creation, the GitHub side, the shared team registry and the read-only operations that remain. An instance's settings are v2's: use `swhurl-v2 edit`, `remove` or `promote` ([console](console.md#instance-changes)).

| Target | Does | Touches |
| --- | --- | --- |
| `app-repo NAME=<app> TYPE=<type> TEAM=<team> [STACK=<stack>] [DESCRIPTION=<text>] [RESUME=true]` † | Create the app's public GitHub repository with `swhurl-v2 app-repo`: `TYPE=web-app`, `api` or `worker` sets every template answer; `STACK=typescript` or `kotlin` defaults to the type's first; `DESCRIPTION=`; it renders, commits, adds the [image webhook](services.md#image-webhook), waits for its first image and opens the pull request that creates its staging instance, which you merge ([start a new app](apps.md#start-a-new-app)). Reads `CONSOLE_TOKEN`, `CONSOLE_REPO`, `APP_REPOS_TOKEN` and `IMAGE_WEBHOOK_TOKEN` from the environment | GitHub |
| `app-hooks` † | Keep the [image webhook](services.md#image-webhook) on the repository of every app whose staging release publishes its own image, `ghcr.io/samclement/<app>`, with `swhurl-v2 app-hooks`: creates a missing one, repoints one that is stale (after a token rotation), disabled or takes other events, leaves a correct one alone and deletes none. An app whose record cannot be read or whose repository GitHub refuses is an `[ERROR]` line and the others still run; the target then fails. `GH_TOKEN` (else `gh auth token`) lists and writes the webhooks; the token is decrypted from SOPS with `SOPS_AGE_KEY_FILE` (else `./age.agekey`); it stops before building without either, and both reach v2 in its environment only. `DRY_RUN=true` lists the webhooks and writes nothing. Needs Go 1.26.0 | GitHub |
| `team-add NAME= [DESCRIPTION=]` | Register a team in v2's registry, `v2-records/teams.json` ([teams](apps.md#teams-and-isolation)), with `swhurl-v2 team`. Needs Go 1.26.0, like every team target | Git edit |
| `team-edit NAME= [DESCRIPTION=] [RENAME=]` | Change a team's description (an empty `DESCRIPTION=` clears it) and/or its name. A rename is refused while a v2 app names the team or an `allows` grants it: a rename moves nothing, so move them first with `swhurl-v2 edit --set team=…` | Git edit |
| `team-remove NAME=` | Remove a team nothing names and that has no channel. Refused while a v2 app names it or an `allows` grants it (move the apps with `swhurl-v2 edit --set team=…`, remove the grants), or while it has a channel (`team-channel REMOVE=true` first) | Git edit |
| `team-channel NAME= [REMOVE=true]` | Give a team its one [notification channel](services.md#notification-channels), or a new address for the one it has: prompts for the address without showing it (or reads one line from standard input), hands it to v2 on standard input, and v2 writes it as an encrypted Secret named after the team, then the routes. `REMOVE=true` takes the channel away and deletes its Secret. Needs `sops`; decrypts nothing | Git edit |
| `app-status APP= ENV=` | Its state against v2's records at `HEAD` (`swhurl-v2 status`: suspended, failing, updating, stopped, unknown or healthy, with the fact that decided it), whether Git is applied and the running image is the record's (compared by digest), replicas, route, certificate, failing containers. Needs Go 1.26.0 | Cluster (read) |
| `app-logs APP= ENV=` | Recent workload logs (`FOLLOW=true`, `TAIL=N`, `PREVIOUS=true` for the last crashed container) | Cluster (read) |
| `app-reconcile APP= ENV=` † | Fetch Git and reconcile only that instance | Cluster |
| `console-image` | Pin `platform/console` and the [notify service](services.md#notify-service) to the [console](console.md#deploy-a-new-console) image published for this commit: the `src-<hash>` tag of its inputs and its digest on GHCR. Refuses with uncommitted inputs or before the image is published ([`images/pin-console.sh`](../images/pin-console.sh)). Validate's deploy job does this itself (`host/push-main.sh console-image --expect src-<hash>`); by hand it is the fallback | Git |
| `console-dev [PORT=8080]` | The [console](console.md) on `http://127.0.0.1:8080` as a fixed dev identity, built from `v2/`. It uses your kubeconfig, so its buttons act with your rights, opens real PRs if `GITHUB_TOKEN` is set, and keeps its jobs in `console/console-jobs-dev`. Needs Go 1.26.0 | Cluster, GitHub |

`check-templates` runs Copier through `uvx` (or a `copier` on `PATH`) and reads the catalogue with `jq`. It needs the v2 binary, and builds one from `v2/` when `SWHURL_V2` names none (CI builds one and sets `SWHURL_V2`); naming a binary you built yourself (`cd v2 && go build -o /tmp/swhurl-v2 ./cmd/swhurl-v2`, then `SWHURL_V2=/tmp/swhurl-v2 make check-templates`) skips the build.

## Settings

| Target | Does | Touches |
| --- | --- | --- |
| `platform-certs-staging`, `platform-certs-prod` † | Set `CERT_ISSUER` in `platform-settings` (`swhurl-v2 platform-certs`; needs Go 1.26.0): only `letsencrypt-*` issuers defined in Git, one line changed, comments kept | Git |

## Lifecycle, backup and recovery

| Target | Does | Touches |
| --- | --- | --- |
| `suspend`, `resume` `TARGET=kustomization/<name>\|helmrelease/<ns>/<name>` † | `swhurl-v2 unit --target`; workloads and data untouched; resume waits for Flux to apply it (needs Go 1.26.0). Dry run only prints a plan ([lifecycle](operations.md#lifecycle)) | Cluster |
| `destroy-data TARGET=pvc/<ns>/<name>\|pv/<name> CONFIRM=<TARGET>` † | Delete a released claim or volume and its host data (`swhurl-v2 destroy-data`; needs Go 1.26.0; [lifecycle](operations.md#lifecycle)) | Cluster |
| `backup-mongodb` † | Start a Job from ClickStack MongoDB's backup CronJob (`observability/backup-mongodb`), wait for it and show its log ([`host/ops.sh`](../host/ops.sh)); the job dumps, encrypts and publishes to `clickstack-mongodb/` ([operations](operations.md#backups-and-recovery)) | Cluster, S3 |
| `backup-sqlite` † | Start a Job from each app SQLite database's backup CronJob (`<app>-backup`), wait for them and show their logs; each copies, checks, encrypts and publishes to `app-sqlite/<namespace>/`. No app with a database: nothing to do | Cluster, S3 |
| `restore-sqlite APP= ENV= CONFIRM=<app>/<env>` † | Restore an app's SQLite database with `swhurl-v2 restore` from its newest backup in S3 (`FROM=<key>` and `MAX_AGE=<duration>` for an older one; `AGE_KEY_FILE`; needs S3 read access and Go 1.26.0): suspends its HelmRelease, stops the app, restores and checks the copy in a job, keeps the replaced files in `before-restore-<UTC>/`, starts the app. `DRY_RUN=true` prints v2's plan ([backups](operations.md#backups-and-recovery)) | Cluster |

## Offline checks

Verbs: `check` and `check-*` never touch the cluster and `verify-*` only read it (both tested against the Touches column); `live-test-*` act on the live cluster, on throwaway resources except where the row says otherwise.

| Target | Does | Touches |
| --- | --- | --- |
| `check` | `check-repo`, `test`, `check-apps`, `check-otel`, `check-lint`, `check-codegraph`, `check-docs` in parallel: everything CI runs except `check-templates`, which CI runs on every push, and `check-v2`. Run before every push; run `check-templates` too when a change touches a template pin, the app types or what the image-declaration producer writes | Local |
| `check-repo [SCHEMA=1]` | `v2/check-repo.sh`: `swhurl-v2 check-repo` decides that relative doc links and their headings resolve, that the [Flux units table](architecture.md#flux-units) and the [app interface table](apps.md#platform-provided-interface) match Git, that v2's compiled tree gives apps only listed names, that every Secret is SOPS age ciphertext and each unit that decrypts holds one, that `BASE_DOMAIN` and `CERT_ISSUER` are set, and which folders Flux renders with what each substitutes; the script renders each with `kubectl kustomize`, substitutes it as Flux does (an unresolved `${...}` fails) and parses every shell file with `bash -n`. `SCHEMA=1` also validates each rendered folder with `flux-schema` against the Kubernetes and Flux schemas, the one check that catches a misspelled field the API server would silently drop; CI does not run it. Needs Go and `jq` (builds `swhurl-v2` unless `SWHURL_V2` names one) | Local |
| `test [K=<pattern>]` | Unit tests: lifecycle guards, verifier, policies, generator, console, fixtures, Flux unit rules, and every dry run that plans without touching anything (runs in `uv`'s locked environment, tests in parallel; a class with `setUpClass` stays together), then the slowest classes. `K` runs only the classes whose dotted name contains it (`K=test_console` while working on the console); `check` always runs them all | Local |
| `check-apps` | Without Python, acquire the profile-pinned local chart with `swhurl-v2 chart`, compare every v2 record's compiled files with Git (`render --check`) and run read-only Go Kubernetes policy over **all instances including production**; any acquisition/render/rule failure fails. The script accepts no arguments: explicit legacy paths and flags exit 2 before reads or commands. Needs Go and Helm; [policy and acquisition](apps.md#the-app-policy) | Local |
| `check-templates` | `v2/check-templates.sh`: render each app type from its stack's pinned template (`v2/contract/stacks.json`), run `swhurl-v2 names` and `swhurl-v2 image-declaration --app-dir DIR` over it, and hand the label to `swhurl-v2 declaration`, which applies the creation rules with no image and no write; the template must refuse answers no type has, and a kind v2 does not model yet (the job kind) is a warning; runs in parallel, about 10 s, no Python but Copier ([apps](apps.md#stacks-and-features)) | Local (reads GitHub; builds `swhurl-v2` unless `SWHURL_V2` names one) |
| `check-otel [CONTAINER_RUNTIME=docker]` | `platform/otel/check.sh`: `helm template` each collector's HelmRelease from its HelmRepository; run `validate` in the image the rendered workload names, with the rendered config unmodified and the rendered feature gates, stubbing only the pod's environment (empty volume mounts, a service-account placeholder, `valueFrom` variables); then run `tests/fixtures/logs.json` through the logs pipeline's transform and filter processors and container framing in the same image (`platform/otel/fixtures.jq`). Needs Helm, `kubectl` (offline YAML reading), `jq`, `openssl`, `curl` and podman (CI sets `CONTAINER_RUNTIME=docker`); reads the chart and image from the network | Local |
| `check-lint` | Ruff on the Python tooling (including `devtools`) and ShellCheck on the remaining bash (needs `uv`) | Local |
| `check-codegraph` | Unit tests for the [code graph](visual-change-tool.md) development aid in `devtools/codegraph` (standard library only) | Local |
| `check-docs` | Compare the [workstream registry](workstreams.md) and its status pages with Git (`swhurl-v2 workstreams`, which only reads Git): columns, kinds and states, one workstream in progress, a resolvable page and a stated next gate per row, page/registry agreement, `Uncommitted` against `git status`, cited commits a ref reaches, a change under a workstream's paths that also updates its page, and each progress page's shape and size; needs Go 1.26.0 ([rules](workstreams.md#what-make-check-docs-enforces)) | Local |
| `v2-deps` | Download the pinned YAML parser with checksum verification into the Go module cache before offline v2 builds/checks; needs Go 1.26.0 and network access ([build contract](iteration-2-plan.md#offline-cli-and-build)) | Local |
| `check-v2` | `gofmt`, `go vet` and `go test` for the Go [v2 module](iteration-2-plan.md#guardrails) in `v2/`, its design guardrails included, then [`check-rendered.sh`](../v2/check-rendered.sh): `platform/egress`, `platform/auth`, `platform/nats` and `platform/reloader` rendered from the working tree's records into a temporary directory and compared with Git; needs Go 1.26.0 and `make v2-deps` run first, and runs offline; CI runs it, `check` does not | Local |
| `v2-render` | Render everything v2 derives from the records when the tree disagrees with them: the resolution (`v2-records/calls.json`), every compiled folder and unit (`v2-apps/`, `clusters/home/app-*.json`), the egress allocation and the proxy's objects, `platform/auth/`, [NATS's user list](services.md#nats) and [Reloader's watch list](services.md#reloader) (`swhurl-v2 render`; the [Render workflow](../.github/workflows/render.yml), the console's image pin and the [v2 Deploy workflow](../.github/workflows/deploy-v2.yml) run it). One render writes every one of them, so nothing else produces a platform file from the records. A tree that already matches is left alone — the check ignores the commit the two documents record — so a run that finds nothing to do writes nothing. Needs Go 1.26.0 | Git |
| `v2-deploy` | Bring every **v2-owned staging instance** to the newest image its repository publishes: for each `v2-records/<app>/staging`, the platform's tag policy picks the newest tag, v2 reads the declaration the image's build wrote into its config labels, refuses a fact the instance cannot take, and writes the record pair and the compiled files (`swhurl-v2 deploy --records`; the [v2 Deploy workflow](../.github/workflows/deploy-v2.yml) runs it). One instance that refuses or fails keeps its image while the others deploy, and the run fails. Needs Go 1.26.0 and read-only access to the public registry ([deploy a new image](apps.md#deploy-a-new-image)) | Git |
| `v2-report [BASE=origin/main] [AFTER=<rev>] [FORMAT=json]` | What a change does to v2: `v2/` in the working tree as Git carries it, ignored files left out, (or at `AFTER`) against the merge base with `BASE`, as exported API, budget, package and layer, import, file and rule-setting changes, with the ones to justify listed under review; a [report, not a gate](iteration-2-plan.md#change-report). `FORMAT=json` prints the document for agents | Local |
| `check-secrets` | Decrypt every tracked Secret in memory; fail on empty or `REPLACE_ME`, warn on probable double encoding. Needs the age key, so CI cannot run it | Local |
| `help` | List every target with its variables, the † mark and what it touches | Local |

## Live tests

| Target | Does | Touches |
| --- | --- | --- |
| `live-test-lifecycle` † | `swhurl-v2 lifecycle-test`: on a throwaway app, prove that suspend leaves it running and resume reconciles, that uninstall keeps the prune-protected namespace, claim and data, that destroy-data refuses without a confirmation and then deletes the claim, volume and data, and that deleting an `Orphan` unit leaves its workload; cleans up after itself; needs Go 1.26.0 | Cluster (throwaway) |
| `live-test-app-template` † | Deploy the generated fixtures through Flux, check, remove | Cluster (throwaway) |
| `live-test-team-scenario [ENV=staging]` † | `swhurl-v2 team-scenario`: find three apps the records link in ENV (an app calling an app that publishes an event a third app subscribes to; the first such callee by name, with its first caller and subscriber) and check in ClickStack the newest trace of the last 10 minutes in which the callee served the caller: the caller named, the event published and processed in it, each app's team as its records state, and the callee's log line naming the caller. Something must drive the call (demo-shopper does); needs Go 1.26.0 | Cluster (read) |

## Host

| Target | Does | Touches |
| --- | --- | --- |
| `host-heartbeat`, `host-heartbeat-delete` † | Install or remove the five-minute notification checker heartbeat timer (`swhurl-notification-heartbeat`) | Host |
| `host-dns`, `host-dns-delete` † | Install or remove the Route 53 dynamic DNS system timer (`aws-dns-updater`, every 10 minutes; records in `host/dns.env`) | Host |
| `host-probe`, `host-probe-delete` † | Install or remove the connectivity probe timer (`connectivity-probe`, every minute; one NDJSON line per run in `/var/log/swhurl-platform/connectivity-probe.log`) | Host |

All four use [`host/install-timer.sh`](../host/install-timer.sh): a system unit that runs a script from this checkout as you, so edits to the script or `host/dns.env` apply at the next run; only unit template changes need a reinstall. All ask for `sudo`, so run them in your own terminal. Output goes to `/var/log/swhurl-platform/<unit>.log` and ClickStack.

Environment variables the targets read: `DRY_RUN`, `SKIP_VERIFY`, and the per-target ones in the tables. Host DNS records are in [`host/dns.env`](../host/dns.env). Cluster settings live in Git, not here ([services](services.md#settings)).

## Offline image declaration

Run in your own terminal, from an app checkout, with a built `swhurl-v2` on `PATH`:

```sh
swhurl-v2 image-declaration                 # cwd is the app directory
swhurl-v2 image-declaration --app-dir DIR   # an explicit checkout
```

Prints exactly one `platform.swhurl.com/declaration=<compact JSON>` line. It reads only `swhurl.yaml`
and the documents that file explicitly names, with no image build, registry, GitHub, credential or
cluster access and no writes. Exit 0 is a label, 2 is usage/input refusal (diagnostic only), and 1 is an
output-write failure, following the Go CLI's standard I/O conventions. See the
[producer contract](iteration-2-plan.md#producer-first-python-retirement-approved-5-november-2026)
for byte identity, limits and the distinction between producing a declaration and accepting deployment.
The Python command no longer exists in this checkout; historical pinned workflows are not upgraded.

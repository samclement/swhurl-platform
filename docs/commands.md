# Commands

Every `make` target, grouped by task. **Cluster** means the target reads or changes the live cluster; **Git** means it edits files for you to commit; **GitHub** means it creates a repository there. `help` prints the same list from the `##` comment on each target in the Makefile; a test fails if a target is missing from this page. Targets marked † accept `DRY_RUN=true` to print the plan (or run only read-only checks) without acting.

## Deploy and verify

| Target | Does | Touches |
| --- | --- | --- |
| `flux-reconcile` | Fetch Git, reconcile the source layer and the stack, then wait until every unit is Ready at that revision (`swhurl flux-wait`: stops at the first unit that fails, skips suspended ones) | Cluster |
| `install` † | `check-config`, `flux-reconcile`, `verify-platform` (`SKIP_VERIFY=1` skips the checks) | Cluster |
| `verify-platform` | Every Flux unit Ready, HTTP→HTTPS redirect, ingestion key matches ClickStack (bytes, never printed), ClickStack registration closed, retention settings, no ClickHouse merge failed in the last hour, newest backup locally and in S3 younger than `BACKUP_MAX_AGE_HOURS` (26), console image built from the current tooling (warns otherwise), GitHub accepts the console's token and it does not expire within 14 days (warns) | Cluster (read), S3 (list) |
| `clickstack-bootstrap` † | After a ClickStack install: register the admin from SOPS if no team exists, set the team ingestion key to `CLICKSTACK_INGESTION_KEY`; idempotent, never prints values ([services](services.md#clickstack-and-otel)) | Cluster |
| `reconcile UNIT=<name>` | Fetch Git and reconcile one Flux unit, for example after changing its Secret | Cluster |
| `flux-install` † | Install or upgrade the Flux controllers at `FLUX_VERSION` in `tools/swhurl/flux.py` with the patches in `clusters/home/flux-system/install`; refuses a different `flux` CLI; `DRY_RUN=true` prints the live diff | Cluster |
| `flux-bootstrap` | Apply the root units and sources in `clusters/home/flux-system` (Flux must already be installed) | Cluster |

## Apps

In the order of an app's life ([apps](apps.md)): create, set access, promote, operate, remove; then the console's own image.

| Target | Does | Touches |
| --- | --- | --- |
| `app-repo NAME=<app>` † | Create the app's public GitHub repository from a stack's Copier template (`STACK=typescript`, `DESCRIPTION=`), push it, wait for its first image and print the `app-new` line ([start from the template](apps.md#start-from-the-template)) | GitHub |
| `app-new NAME=<app> ARGS="..."` | Generate an app instance and check it against the app policy; `--from-repo OWNER/REPO` takes its defaults from the app's [`swhurl.yaml`](apps.md#swhurlyaml) | Git |
| `app-expose APP= ENV= ARGS="--exposure ... [--host H]"` | Change who can reach an instance: `private` (removes the route), `authenticated-web` (Google sign-in; keeps or derives the host) or `public` (needs `--host` outside `homelab.swhurl.com`); updates the route, the Namespace label and the unit's dependencies | Git |
| `app-promote APP=` | Copy the staging image (tag and digest) into prod (`FROM=`, `TO=` override); refuses without a digest, a different repository, or no change | Git |
| `app-status APP= ENV=` | Whether Git is applied and the running image matches it (compared by digest), replicas, who can reach it, route, certificate, failing containers | Cluster (read) |
| `app-logs APP= ENV=` | Recent workload logs (`FOLLOW=true`, `TAIL=N`, `PREVIOUS=true` for the last crashed container) | Cluster (read) |
| `app-reconcile APP= ENV=` | Fetch Git and reconcile only that instance | Cluster |
| `app-check APP= ENV=` | Render one instance and check it against the app policy | Local |
| `app-scale APP= ENV= ARGS="..."` | Change `--replicas` (0 to 10), `--cpu`, `--memory` or `--memory-limit` | Git |
| `app-remove APP= ENV=` | Delete the instance's files, its unit file and registration, and its Reloader namespace; warns when a retained volume will be kept | Git |
| `console-image` | Pin `platform/console` to the [console](console.md#deploy-a-new-console) image published for this commit: the `src-<hash>` tag of its inputs and its digest on GHCR. Refuses with uncommitted inputs or before the publish run. The publish run does this itself (`--expect src-<hash> --commit`); by hand it is the fallback | Git |
| `console-dev` | The [console](console.md) on `http://127.0.0.1:8080` (`PORT=`) as a fixed dev identity. It uses your kubeconfig, so its buttons act with your rights, and opens real PRs if `GITHUB_TOKEN` is set. Needs `uv` | Cluster, GitHub | Cluster (read) |

## Settings

| Target | Does | Touches |
| --- | --- | --- |
| `platform-certs-staging`, `platform-certs-prod` † | Set `CERT_ISSUER` in `platform-settings`: only issuers defined in Git, one line changed, comments kept | Git |

## Lifecycle, backup and recovery

| Target | Does | Touches |
| --- | --- | --- |
| `suspend`, `resume` `TARGET=kustomization/<name>\|helmrelease/<ns>/<name>` † | `flux suspend/resume`; workloads and data untouched | Cluster |
| `destroy-data TARGET=pvc/<ns>/<name>\|pv/<name> CONFIRM=<TARGET>` † | Delete a released claim or volume and its host data ([lifecycle](operations.md#lifecycle)) | Cluster |
| `backup-mongodb` † | Encrypted MongoDB dump to `~/.local/state/swhurl-platform/backups` (`BACKUP_DIR`), prune to 7 daily + 4 weekly (`PRUNE=false` skips), then upload files the bucket lacks to `BACKUP_S3_URI` (empty skips; AWS CLI default credentials or `AWS_PROFILE`) | Cluster (read), local, S3 |
| `backup-sqlite` † | For each app with a SQLite database (`DATABASE_PATH`): a short-lived pod running as the app's user copies it with `sqlite3 .backup`, checks it (`integrity_check`), and streams it through age to `BACKUP_DIR/sqlite/<namespace>/`; prune to 7 daily + 4 weekly, then upload to `SQLITE_S3_URI<namespace>/`. No app with a database: nothing to do | Cluster, S3 |
| `restore-sqlite APP= ENV= CONFIRM=<app>/<env>` † | Restore an app's SQLite database from its newest backup in `BACKUP_DIR/sqlite/<app>-<env>/` (`BACKUP_FILE`, `AGE_KEY_FILE`): suspends its HelmRelease, stops the app, checks the copy, keeps the replaced files in `before-restore-<UTC>/`, starts the app ([backups](operations.md#backups-and-recovery)) | Cluster |

## Offline checks

Verbs: `check-*` never touch the cluster, `verify-*` read the live cluster, `live-test-*` create and remove throwaway resources on it.

| Target | Does | Touches |
| --- | --- | --- |
| `check` | `check-repo`, `test`, `check-apps`, `check-otel`, `check-lint` in parallel: everything CI runs. Run before every push | Local |
| `check-repo` | Render every active Flux path, validate schemas, SOPS structure, shell syntax and doc links | Local |
| `test` | Unit tests: lifecycle guards, verifier, policies, generator, console, fixtures, Flux unit rules (runs in `uv`'s locked environment, test classes in parallel) | Local |
| `check-apps` | Render every app instance with Helm and check the app policy | Local |
| `check-otel` | Render both OTel collectors as Flux would and run that chart's exact `otelcol-k8s` release's `validate` on the config (binary downloaded once per version, SHA-256 checked, cached); fails on config the collector would reject, warns on deprecated component names | Local |
| `check-lint` | Ruff on the Python tooling and ShellCheck on the remaining bash (needs `uv`) | Local |
| `check-config` | The required Secret files and the `BASE_DOMAIN` and `CERT_ISSUER` settings exist | Local |
| `check-secrets` | Decrypt every tracked Secret in memory; fail on empty or `REPLACE_ME`, warn on probable double encoding. Needs the age key, so CI cannot run it | Local |

## Live tests

| Target | Does | Touches |
| --- | --- | --- |
| `live-test-lifecycle` † | Prove lifecycle commands on a throwaway app | Cluster (throwaway) |
| `live-test-reloader` † | Prove Reloader's opt-in and namespace scope | Cluster (throwaway) |
| `live-test-app-template` † | Deploy the generated fixtures through Flux, check, remove | Cluster (throwaway) |
| `live-test-restore-mongodb` † | Restore the latest backup into a throwaway namespace and check it (`BACKUP_FILE`, `AGE_KEY_FILE`, `KEEP=true`) | Cluster (throwaway) |
| `live-test-restore-sqlite` † | Back up a throwaway app's SQLite database, change it, restore it with `restore-sqlite` and check the rows, the kept files and the resumed HelmRelease (`AGE_KEY_FILE`, `KEEP=true`) | Cluster (throwaway) |

## Host

| Target | Does | Touches |
| --- | --- | --- |
| `host-backup`, `host-backup-delete` † | Install or remove the daily system timer (`swhurl-backup-mongodb`) that runs `backup-mongodb` and then `backup-sqlite`, and so the S3 uploads, at 03:30 | Host |
| `host-dns`, `host-dns-delete` † | Install or remove the Route 53 dynamic DNS system timer (`aws-dns-updater`, every 10 minutes; records in `host/dns.env`) | Host |

Both use [`host/install-timer.sh`](../host/install-timer.sh): a system unit that runs a script from this checkout as you, so edits to the script or `host/dns.env` apply at the next run; only unit template changes need a reinstall. Both ask for `sudo`, so run them in your own terminal. Output goes to `/var/log/swhurl-platform/<unit>.log` and ClickStack.

Environment variables the targets read: `DRY_RUN`, `SKIP_VERIFY`, and the per-target ones in the tables. Host DNS records are in [`host/dns.env`](../host/dns.env). Cluster settings live in Git, not here ([services](services.md#settings)).

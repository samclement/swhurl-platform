# Operations

Day-to-day work on the live cluster. Every `make` target is listed in [commands](commands.md). Export `KUBECONFIG=$HOME/.kube/config` first: on this host `kubectl` is the k3s wrapper and non-interactive shells otherwise read `/etc/rancher/k3s/k3s.yaml`.

## Check health

```bash
make verify-platform              # Flux units, HTTPS redirect, ingestion key, ClickStack sign-up closed, retention, backup age
flux get kustomizations           # one line per Flux unit
make app-status APP=hello ENV=prod
```

A unit showing `DependencyNotReady` is waiting for another unit; fix that one first ([dependencies](architecture.md#flux-units)).

## Secrets

Each Secret is a SOPS-encrypted Kubernetes Secret beside its consumer: platform Secrets in `platform/<service>/secret.sops.yaml`, app Secrets in `apps/<app>/<env>/secret.sops.yaml`. SOPS encrypts only `data`/`stringData`, and the consuming Flux unit decrypts in-cluster with `flux-system/sops-age`. There is no password; you need the age private key (`SOPS_AGE_KEY_FILE=./age.agekey`).

Rules:

- **Write new values as `stringData`** (plain text inside the encrypted file). Flux applies it correctly; generated app stubs use it.
- **In `data`, base64-encode exactly once.** A doubly encoded ingestion key silently dropped all telemetry once. The oauth2-proxy Secret still uses `data`; the ClickStack and OTel Secrets use `stringData`.
- **Never print values.** Check with `make check-secrets` (decrypts in memory; fails on empty or `REPLACE_ME` values and when the two `CLICKSTACK_INGESTION_KEY` copies differ, warns on probable double encoding) and `make verify-platform` (compares the ingestion key by bytes).

Rotate a value:

```bash
sops platform/oauth2-proxy/secret.sops.yaml
make check-secrets
git commit -am "secrets: rotate oauth2-proxy client secret" && git push
make reconcile UNIT=platform-oauth2-proxy   # the unit that holds the Secret; apps: make app-reconcile
```

Reloader then restarts the workloads that opted in: oauth2-proxy, both OTel collectors, the console, and generated apps with Secrets. Anything else that reads the Secret needs a manual `kubectl rollout restart`; if Reloader ever misses the collectors: `kubectl -n logging rollout restart deploy/otel-k8s-cluster-opentelemetry-collector ds/otel-k8s-daemonset-opentelemetry-collector-agent`, then `make verify-platform`. Which Secret belongs to which service: [services](services.md).

Rotate the ClickStack ingestion key ([what it is](services.md#clickstack-and-otel)):

1. Set `CLICKSTACK_INGESTION_KEY` to the same new value (for example from `uuidgen`) in both `sops platform/clickstack/secret.sops.yaml` and `sops platform/otel/secret.sops.yaml`.
2. `make check-secrets` (fails if the copies differ), commit, push, `make reconcile UNIT=platform-clickstack` and `make reconcile UNIT=platform-otel`. Reloader restarts the collectors.
3. `make clickstack-bootstrap` writes the new key into the team.
4. `make verify-platform` compares the live Secret with the team key by bytes; check the collector logs show no HTTP 401. If the collector pods did not restart, restart them with the `kubectl` command above.

Replace the console's GitHub token (before it expires; `make verify-platform` warns 14 days ahead):

1. On GitHub: Settings → Developer settings → Fine-grained tokens → Generate. Repository access: only `samclement/swhurl-platform`; permissions: Contents and Pull requests, read and write; the longest expiry offered.
2. In your own terminal: `sops platform/console/secret.sops.yaml` and replace the `GITHUB_TOKEN` value. Never paste the token anywhere else.
3. `make check-secrets`, commit, push, `make reconcile UNIT=platform-console`. Reloader restarts the console; `make verify-platform` shows the new expiry. Revoke the old token on GitHub.

Rotate the push webhook token ([what it is](services.md#push-webhook)); GitHub rejects nothing in between, but Flux ignores pushes until both sides match, so do it in one go:

1. In your own terminal: `sops platform/flux-webhook/secret.sops.yaml` and set `token` to the output of `openssl rand -hex 32`.
2. `make check-secrets`, commit, push, `make reconcile UNIT=platform-flux-webhook` (the receiver reads the Secret on each request; nothing restarts).
3. On GitHub: Settings → Webhooks → the `flux-webhook` hook → Secret: paste the same value, save, then **Redeliver** the latest delivery and check it returns 200.

Rotate the image automation deploy key ([what it does](apps.md#deploy-a-new-image)), adding the new key before removing the old so automatic deploys keep working:

1. In your own terminal: `ssh-keygen -t ed25519 -N '' -C flux-image-automation -f /tmp/flux-deploy`.
2. On GitHub: Settings → Deploy keys → add `/tmp/flux-deploy.pub` as `flux-image-automation` with **Allow write access** (keep the old key for now).
3. `sops platform/image-automation/secret.sops.yaml`: replace `identity` and `identity.pub` with the two files' contents, then delete `/tmp/flux-deploy*`. `make check-secrets`, commit, push, `make reconcile UNIT=platform-image-automation`.
4. `make verify-platform` shows Image Automation Ready; then delete the old deploy key on GitHub.

Rotate the ntfy topic ([alerts](services.md#alerts)) when it may have leaked: in your own terminal, `sops` both `platform/alerts/secret-failures.sops.yaml` and `secret-deploys.sops.yaml` and replace the topic (`swhurl-` and 24 hex characters, for example from `openssl rand -hex 12`) in each `address`; `make check-secrets`, commit, push, `make reconcile UNIT=platform-alerts`; subscribe to the new topic in the ntfy app and unsubscribe from the old.

## Certificate mode

`CERT_ISSUER` in `platform-settings` selects the Let's Encrypt issuer for platform hosts (sign-in, ClickStack). Apps choose their own issuer.

```bash
make platform-certs-staging       # or platform-certs-prod; edits the file only
git commit -am "platform: CERT_ISSUER=letsencrypt-staging" && git push && make flux-reconcile
kubectl get ingress -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,ISSUER:.metadata.annotations.cert-manager\\.io/cluster-issuer
```

## Lifecycle

Deploy, update and uninstall through Git. Removing an app instance's unit from `clusters/home/kustomization.yaml` uninstalls it (`make app-remove` or the console's Uninstall does that and removes its files, [apps](apps.md#operate-an-instance)); namespaces and claims annotated `kustomize.toolkit.fluxcd.io/prune: disabled` stay. For what Git cannot express:

| Command | Effect | Data |
| --- | --- | --- |
| `make suspend TARGET=kustomization/<name>` | Stop applying Git changes to a unit | Kept; workloads keep running |
| `make suspend TARGET=helmrelease/<ns>/<name>` | Freeze one Helm release (suspending a unit does not) | Kept |
| `make resume TARGET=...` | Reconcile from Git again | Kept |
| `make destroy-data TARGET=pvc/<ns>/<name> CONFIRM=pvc/<ns>/<name>` | Delete an unused claim, its volume and host directory | **Destroyed** |
| `make destroy-data TARGET=pv/<name> CONFIRM=pv/<name>` | Delete a `Released` retained volume and its directory | **Destroyed** |

`destroy-data` refuses while a pod mounts the claim or a Helm release or Flux unit still manages it, because they would recreate it. `DRY_RUN=true` runs only the checks.

Deleting a shared Flux unit by mistake is safe: shared units use `deletionPolicy: Orphan`, so their resources keep running unmanaged. Re-create a deleted root unit with `make flux-bootstrap`. There is no whole-platform reset.

## Chart updates

[Renovate](https://docs.renovatebot.com/modules/manager/flux/) proposes chart version bumps as pull requests; nothing updates a chart without your merge. It runs as the Mend-hosted Renovate GitHub App (access to this repository, mode Interactive on developer.mend.io; in silent mode it scans but opens nothing; run logs are there).

```text
new chart release → Renovate run → PR from a renovate/* branch, e.g. #9 "chore(charts): update helm release opentelemetry-collector to v0.174.0" → CI (Validate) renders it
  → you review and merge → push webhook → Flux applies → helm-controller upgrades each release using the chart
```

- **What it watches** ([`renovate.json`](../renovate.json)): the Flux manager on files under `apps/`, `clusters/`, `infra/` and `platform/`, so every HelmRelease's `chart.spec.version` against its `HelmRepository`: cert-manager, ClickStack and its operators, both OTel collectors, oauth2-proxy, Reloader and app-template. Image tags and digests are switched off (`matchDatasources: docker`); app images are updated by hand ([apps](apps.md#deploy-a-new-image)).
- **One PR per chart**, a separate one for a major version. A chart used by several releases is bumped in all of them in one PR: the OTel chart for both collectors, and **app-template for every app instance, staging and production together**, plus the console. App maintainers review those PRs for their app.
- The repository's Dependency Dashboard issue lists open PRs and pending updates; ticking a rate-limited entry opens its PR on the next run, usually within a minute.

For each PR: read the chart's release notes and compare its `appVersion` (`helm pull <chart> --repo <url> --version <v>` for both versions); charts that leave image tags unpinned upgrade the app with it, so a patch bump can be a large app upgrade (ClickStack 1.1.2 moved HyperDX from 2.8.0 to 2.19.0). Run `make backup-mongodb` before a ClickStack bump. CI renders every change, `make check-apps` renders each app instance with the new app-template, and `make check-otel` validates the collector config with the new collector version. After merging, the webhook applies it within seconds; `make flux-reconcile` waits for every unit, then `make verify-platform` (and `make app-status` for app-template). To roll back, revert the merge commit.

Renovate does not see the tool versions: `kubectl` and `helm` are pinned in both [`validate.yml`](../.github/workflows/validate.yml) and the console image ([`images/console/Dockerfile`](../images/console/Dockerfile)), with `sops` pinned in the image only. Bump a version in both places, with its SHA-256 in the Dockerfile, and push: the publish run builds and deploys the new console ([console](console.md#deploy-a-new-console)). Flux's controllers are installed by `make flux-install`, at `FLUX_VERSION` in [`tools/swhurl/flux.py`](../tools/swhurl/flux.py) and with the patches in [`clusters/home/flux-system/install`](../clusters/home/flux-system/install/kustomization.yaml) (the upstream manifests themselves are not in Git). To upgrade Flux: bump `FLUX_VERSION`, install that `flux` CLI locally, run `make flux-install DRY_RUN=true` to see the live diff, then `make flux-install`, and push the change (it is under `tools/`, so the publish run also deploys a matching console). Don't run plain `flux install`: it drops the patches, and `make verify-platform` then warns.

## Backups and recovery

| Data | Class | Where it survives |
| --- | --- | --- |
| Manifests and encrypted Secrets | Irreplaceable | GitHub |
| age private key | Irreplaceable | Encrypted off-host copy (location kept outside Git) |
| ClickStack MongoDB (team, users, sources, dashboards) | Irreplaceable | Daily `make backup-mongodb` to this host and S3; data volume on `local-path-retain` |
| App SQLite databases (`--database sqlite`) | Irreplaceable | Daily `make backup-sqlite` to this host and S3 (`app-sqlite/<namespace>/`); data volume on `local-path-retain` |
| ClickHouse telemetry | Expendable | Expires after 30 days; ClickHouse's own logs after 7 |
| Cluster state (k3s SQLite datastore, `/var/lib/rancher/k3s/server/db`) | Reconstructible | Rebuilt from Git by Flux ([bootstrap](bootstrap.md)); not backed up |

```bash
make backup-mongodb              # encrypted dump to ~/.local/state/swhurl-platform/backups, then S3
make live-test-restore-mongodb   # restores the latest into a throwaway namespace and checks it
make backup-sqlite               # every app SQLite database, encrypted, then S3
make host-backup                 # install the daily timer (03:30: MongoDB, then SQLite; catches up after downtime)
systemctl status swhurl-backup-mongodb  # last run; output: /var/log/swhurl-platform/swhurl-backup-mongodb.log
```

The backup streams `mongodump` through `age`, so no plaintext touches disk, and writes an archive plus metadata (checksum, version, counts). Encryption needs only the age public key, so the backup host never holds the private key. Each run keeps the newest local backup for each of the last 7 days that have one and each of the last 4 weeks (`KEEP_DAILY`, `KEEP_WEEKLY`, `PRUNE=false`), then uploads every local file missing from `s3://swhurl-platform-backups-110927251694/clickstack-mongodb/` (`BACKUP_S3_URI`). The bucket (`eu-west-2`) blocks public access, is versioned, and deletes backups after 90 days and replaced versions after 30. The upload and the timer use the AWS CLI's default profile (IAM user `sam`); a dedicated write-only user is a planned hardening step.

Like the dynamic DNS timer, it is a system unit under `/etc/systemd/system` that runs as the user who installed it, so it runs whether or not you are logged in. Its output goes to `/var/log/swhurl-platform/swhurl-backup-mongodb.log` and to ClickStack as service `swhurl-backup-mongodb` ([services](services.md#clickstack-and-otel)); the journal keeps start, finish and failure lines. `make verify-platform` fails when the newest backup, locally or in S3, is older than 26 hours (`BACKUP_MAX_AGE_HOURS`).

**App SQLite databases** are copied by a short-lived pod in the app's namespace (pinned `keinos/sqlite3` image), which runs as the app's user, mounts the app's claim and uses SQLite's online backup, so the app keeps running. The copy must pass `PRAGMA integrity_check` before it is encrypted; it streams through `kubectl exec` into age, so no plaintext reaches this host. Metadata records the source, table count, the archive's checksum and the plaintext's size and SHA-256 (measured in the pod); an archive smaller than the plaintext means the stream was cut short, and the run fails. The timer runs it after the MongoDB backup, so a failed MongoDB backup skips it; `make verify-platform` checks each database's newest backup (local and S3) is under 26 hours old.

Restore an app's database from its newest local backup (or `BACKUP_FILE=`; on another machine, first copy one from `s3://…/app-sqlite/<app>-<env>/` into `BACKUP_DIR/sqlite/<app>-<env>/`):

```bash
make restore-sqlite APP=notes ENV=prod DRY_RUN=true          # checks the backup, prints the plan
make restore-sqlite APP=notes ENV=prod CONFIRM=notes/prod    # the app is down for about a minute
```

It refuses a backup whose checksum or source instance does not match, then suspends the instance's HelmRelease (so no upgrade starts the app mid-restore), scales the app to 0, and streams `age -d` into a pod running as the app's user on its claim. The received file must match the backup's plaintext SHA-256, pass `integrity_check` and have the recorded table count, or the live database is left untouched. The current database and its `-wal`/`-shm` files move to `before-restore-<UTC>/` beside it (delete that directory yourself once you are happy); then the app is scaled back, the HelmRelease resumed and the rollout awaited, even when a step fails. `make live-test-restore-sqlite` proves the whole cycle on a throwaway app.

**Targets:** at most 24 hours of MongoDB or app database changes lost (daily backups); about an hour from a bare host to working ClickStack.

**Recovering on a new machine** needs Git (GitHub), the age private key (its off-host copy), the newest archive and metadata from S3 (`aws s3 cp s3://…/clickstack-mongodb/<name> .`), and read access to that bucket. Follow [bootstrap](bootstrap.md) to step 4, restore MongoDB as below, then run `make clickstack-bootstrap` and `make verify-platform`. The Google OAuth client and every other credential come from SOPS.

The restore test passes only if the checksum, collection counts and restored ingestion key (against the Git Secret) all match; it never touches live workloads.

Without a backup, a fresh install needs only `make clickstack-bootstrap`: it registers the admin from SOPS and sets the Git ingestion key, and HyperDX recreates the default connection and sources. Dashboards, saved searches and other users are lost.

Restore into the running service. MongoDB requires a login, so the connection string goes into a private file in the pod (never a command line) while the archive streams on stdin:

```bash
kubectl -n observability scale deploy/clickstack-app --replicas=0
kubectl -n observability wait --for=delete pod -l app=clickstack --timeout=120s   # HyperDX takes ~40 s to stop
kubectl -n observability get secret clickstack-mongodb-hyperdx-hyperdx -o jsonpath='{.data.connectionString\.standard}' \
  | base64 -d | python3 -c 'import json,sys; print("uri: " + json.dumps(sys.stdin.read()))' \
  | kubectl -n observability exec -i clickstack-mongodb-0 -c mongod -- sh -c 'umask 077; cat > /tmp/login.yaml'
age -d -i age.agekey <archive> | kubectl -n observability exec -i clickstack-mongodb-0 -c mongod -- \
  mongorestore --config=/tmp/login.yaml --archive --gzip --drop
kubectl -n observability exec clickstack-mongodb-0 -c mongod -- rm -f /tmp/login.yaml
kubectl -n observability scale deploy/clickstack-app --replicas=1
make clickstack-bootstrap   # the restored team may hold an older ingestion key
make verify-platform
```

Compare `mongorestore`'s document count with the backup's `.json` metadata. Restored users keep the passwords they had when the backup was taken. Backups from the chart 1.x install hold the same `hyperdx` database. Run on the live cluster on 29 September 2026 ([current state](current-state.md#clickstack-340-fresh-install)); ClickStack was down for about 15 seconds plus the app's shutdown.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| 403 after signing in, only on `http://` | No HTTP→HTTPS redirect, so the `Secure` sign-in cookie is not sent. Check `make verify-platform`'s Ingress line; the redirect is `ports.web.redirections` in `infra/traefik/helmchartconfig.yaml` (the old `redirectTo` key is ignored by Traefik 3). |
| `redirect_uri_mismatch` from Google | The OAuth client's allowed redirect URI differs from `https://oauth.<BASE_DOMAIN>/oauth2/callback`. |
| Collector logs show HTTP 401 or `Unauthenticated` | The team's ingestion key differs from `CLICKSTACK_INGESTION_KEY` (after a reinstall, a restore, or a rotation in the UI). `make verify-platform` says so; run `make clickstack-bootstrap`. |
| A signed-in route shows Traefik's default certificate for a few seconds | Normal while cert-manager issues a certificate for a host that just moved. |
| `flux reconcile` seems ignored | A previous revision is still running health checks (up to the unit's timeout); new requests queue behind it. |
| Deleting a broken app takes about 3 minutes | Its Helm install is still in progress; the finalizer waits for the Helm timeout (`FAIL_AFTER`, [apps](apps.md)). |
| A certificate stays not Ready on a new host | DNS has not propagated or Let's Encrypt cannot reach port 80; check `kubectl get challenges -A` and the router forward. |
| A new host doesn't resolve | Multi-label names are not covered by the wildcard; add them to `DYNAMIC_DNS_RECORDS` and re-run `make host-dns`. |
| ClickHouse has `*_log_0` tables after a restart | ClickHouse renamed system log tables whose definition changed. Check, then drop them, or leave them to expire (they keep their 7-day TTL). |
| ClickHouse uses far more CPU than its queries explain; `make verify-platform` reports failed merges | A merge is failing and retrying at once (`SELECT table, error, exception FROM system.part_log WHERE event_type = 'MergeParts' AND error != 0 ORDER BY event_time DESC LIMIT 5`). Code 241 on a very wide table (`system.metric_log` has about 1,350 columns) means a horizontal merge needs more than the memory limit: give that table `settings: vertical_merge_algorithm_min_rows_to_activate = 1` under its entry in `clickhouse.cluster.spec.settings.extraConfig` (as `metric_log` has). The restart renames the old table to `<name>_0` with the old settings; `ALTER TABLE system.<name>_0 MODIFY SETTING vertical_merge_algorithm_min_rows_to_activate = 1` lets it merge too. |

# Operations

Day-to-day work on the live cluster. Every `make` target is listed in [commands](commands.md). Export `KUBECONFIG=$HOME/.kube/config` first: on this host `kubectl` is the k3s wrapper and non-interactive shells otherwise read `/etc/rancher/k3s/k3s.yaml`.

## Check health

```bash
make verify-platform              # every check in the table below; expect "Validation passed."
flux get kustomizations           # one line per Flux unit
make app-status APP=demo-orders ENV=prod
```

A unit showing `DependencyNotReady` is waiting for another unit; fix that one first ([dependencies](architecture.md#flux-units)).

`make verify-platform` only reads. It builds `swhurl-v2` (it needs Go 1.26.0) and runs `swhurl-v2 verify --scope platform`, which is every check below, in this order ([`v2/observe/platform.go`](../v2/observe/platform.go), [`platform_scope.go`](../v2/observe/platform_scope.go), [`platform_host.go`](../v2/observe/platform_host.go)); it prints `[OK]`, `[WARN]` or `[BAD]` lines under a heading for each, and exits 1 on any `[BAD]`. The table below is [the catalogue](../v2/observe/platform_catalogue.go) the report is built from, name, section and order alike (a Go test fails when they differ). The first six only read the cluster; the console's Platform page and Overview run those six (`--scope cluster`) through the same binary with the pod's own credential. The next nine also compare the cluster with this checkout, the registry (anonymously) and S3, with the AWS credentials the recipe exports from `aws configure export-credentials` unless `AWS_ACCESS_KEY_ID` is set. Where MongoDB's backups are is the live `backup-mongodb` CronJob's `BACKUP_S3_URI`, a channel's Secret is one the notify service reads (by its label, with an address), and the console's image is compared with this checkout's content tag ([`images/content-tag.sh`](../images/content-tag.sh) `console`, what Validate tags it with); a commit tag cannot be compared and warns. The next three read GitHub: the webhook checks use the installed `flux-system/platform-settings.BASE_DOMAIN` and the platform GitRepository named by the checkout profile's `delivery.source`; image webhook repositories are the staging release records' declared sources for platform-built images. Hook listings use `GH_TOKEN`, exported by the recipe from `gh auth token` when unset or empty (a failed login lookup does not stop other checks). The console's two tokens come from `console/console-github` and both are checked against the installed platform repository. A failed Secret/settings read is bad, not absence. Webhooks match an exact parsed host, never a substring; delivery text is limited to standard HTTP reasons so it cannot echo credentials or private webhook URLs. An input the Go checks need but were not given, AWS credentials, the tag or the operator GitHub token, is a `[BAD]` saying it was not supplied.

The next four Go groups check the collector ingestion Secret, its bytes against the unique ClickStack team key,
closed HyperDX registration, and retention. Fixed read-only MongoDB, Node installation GET and ClickHouse SELECT
queries run in their installed pod/container through the Kubernetes API server's WebSocket v5 exec protocol;
MongoDB's URI travels only on stdin. The app Deployment's selector chooses a ready, nonterminating app pod.
Output is bounded; transport/protocol failure, missing status, nonzero exit and malformed results fail closed,
without raw output or stderr in reports. Failed Secret reads remain distinct from empty Secrets, and double
base64 encoding fails the byte comparison naturally. The system-log query checks TTL presence, not its duration;
the retained "7 days" message describes the chart's intended setting, not a duration measurement.

The last two groups judge the host. `flux-controllers` compares each controller's live Deployment with
[`clusters/home/flux-system/install/flux.json`](../clusters/home/flux-system/install/flux.json), the one place the Flux
version, the extra components and the controller arguments are stated (`make flux-install` installs from the same
file): another version or a missing argument warns, a settings file that cannot be read is `[BAD]` and no version is
guessed. `notification-heartbeat` reads no host itself: the recipe writes `systemctl show --timestamp=unix` of the
heartbeat timer and service to a file and passes it as `--heartbeat`, and Go parses the two blocks by their `Id`.
Facts that were not supplied, or a file it cannot read, are `[BAD]`, never a skipped check. `--scope cluster`, the
console's, reads neither and refuses both flags.


| Check | Passes when | Reads |
| --- | --- | --- |
| `flux-units` | Every Flux unit is Ready | Cluster (read), `swhurl-v2 verify` |
| `ingress` | Traefik redirects HTTP to HTTPS | Cluster (read), `swhurl-v2 verify` |
| `alerts` | The Flux failure Alert exists and its provider does (a missing Alert warns) | Cluster (read), `swhurl-v2 verify` |
| `notify-service` | The notify service runs, on the console's image (another image warns) | Cluster (read), `swhurl-v2 verify` |
| `dashboards` | The platform webhook, dashboard and alert sync succeeded within five minutes | Cluster (read), `swhurl-v2 verify` |
| `notifications` | The notification checker succeeded within five minutes | Cluster (read), `swhurl-v2 verify` |
| `image-automation` | Every app's ImagePolicy is Ready and staging runs the image its record states (warns otherwise) | Cluster (read), this checkout, registry (read), `swhurl-v2 verify` |
| `isolation` | Every instance v2 compiled has its namespace, a registered team, the platform quota and a NetworkPolicy | Cluster (read), this checkout, `swhurl-v2 verify` |
| `signing-keys` | The signing keys and token issuer apps were given are the cluster's | Cluster (read), this checkout, `swhurl-v2 verify` |
| `events` | NATS and its controller run, and every stream and consumer v2 rendered is Ready | Cluster (read), this checkout, `swhurl-v2 verify` |
| `egress` | The egress proxy runs both replicas, and each instance that reaches it does so on a port of its own from the allocation | Cluster (read), this checkout, `swhurl-v2 verify` |
| `channels` | The team routes are Git's and every channel has its Secret (a team without a channel warns) | Cluster (read), this checkout, Secrets (read), `swhurl-v2 verify` |
| `backups` | The newest MongoDB backup in S3, archive and metadata, is younger than `BACKUP_MAX_AGE_HOURS` (26) | Cluster (read), S3 (list), `swhurl-v2 verify` |
| `sqlite-backups` | Each instance whose release declares a SQLite database has a backup in S3 as young | this checkout, S3 (list), `swhurl-v2 verify` |
| `console` | The console image is built from this checkout's tooling (warns otherwise) and its job record exists | Cluster (read), this checkout, `swhurl-v2 verify` |
| `push-webhook` | Flux's GitHub receiver is Ready; GitHub's latest push delivery returned 200 (warns otherwise) | Cluster (read), this checkout, GitHub (read), `swhurl-v2 verify` |
| `image-webhooks` | The image receiver is Ready; each auto-deploy app's repository calls it (warns otherwise) | Cluster (read), this checkout, GitHub (read), `swhurl-v2 verify` |
| `console-token` | GitHub accepts the console's tokens and none expires within 14 days (warns) | Cluster (read), this checkout, Secrets (read), GitHub (read), `swhurl-v2 verify` |
| `flux-controllers` | The Flux controllers run the pinned version with the settings in Git (warns otherwise) | Cluster (read), this checkout (`flux.json`), `swhurl-v2 verify` |
| `runtime-secrets` | The collector ingestion key Secret is present and nonempty | Cluster (read), Secrets (read), `swhurl-v2 verify` |
| `ingestion-key` | The collectors' ingestion key is the ClickStack team's, compared by bytes and never printed | Cluster (read), Secrets (read), commands in pods, `swhurl-v2 verify` |
| `registration` | A HyperDX team exists, so registration is closed | Cluster (read), commands in pods, `swhurl-v2 verify` |
| `retention` | Telemetry expires after 30 days and ClickHouse system logs after 7, no merge failed in the last hour, and the MongoDB volume is retained | Cluster (read), commands in pods, `swhurl-v2 verify` |
| `notification-heartbeat` | The host heartbeat timer is active and its service's last run succeeded within 15 minutes | this host (`--heartbeat`), `swhurl-v2 verify` |

## Secrets

Each Secret is a SOPS-encrypted Kubernetes Secret beside its consumer: platform Secrets in `platform/<service>/secret.sops.yaml`, app Secrets beside an instance's v1 records in `apps/<app>/<env>/secret.sops.yaml` (v2 models no Secret yet). SOPS encrypts only `data`/`stringData`, and the consuming Flux unit decrypts in-cluster with `flux-system/sops-age`. There is no password; you need the age private key (`SOPS_AGE_KEY_FILE=./age.agekey`).

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

Replace one of the console's two GitHub tokens ([what each is for](console.md#github-tokens)) before it expires; `make verify-platform` warns 14 days ahead:

1. On GitHub: Settings → Developer settings → Personal access tokens → Fine-grained tokens → Generate, resource owner `samclement`, the longest expiry offered:
   - `GITHUB_TOKEN` (name `swhurl-console`): repository access only `samclement/swhurl-platform`; Contents and Pull requests, read and write.
   - `APP_REPOS_TOKEN` (name `swhurl-console-app-repos`): **All repositories** (GitHub requires it to create repositories); Administration, Contents, Workflows and Webhooks read and write, Actions read-only.
2. In your own terminal, from the repository root, set the value without echoing it (replace `KEY` with `GITHUB_TOKEN` or `APP_REPOS_TOKEN`, then paste the token): `read -rs T && SOPS_AGE_KEY_FILE=./age.agekey sops set platform/console/secret.sops.yaml '["stringData"]["KEY"]' "\"$T\""; unset T`. Never paste the token anywhere else. `GITHUB_TOKEN` also starts the [Deploy workflow](apps.md#deploy-a-new-image): in the same `read`, add `&& SOPS_AGE_KEY_FILE=./age.agekey sops set platform/image-automation/dispatch-secret.sops.yaml '["stringData"]["token"]' "\"$T\""` before `; unset T`.
3. `make check-secrets` (the two copies of `GITHUB_TOKEN` must match), commit, push, `make flux-reconcile`. Reloader restarts the console; `make verify-platform` shows the new expiry. Revoke the old token on GitHub.

Rotate the push webhook token ([what it is](services.md#push-webhook)); GitHub rejects nothing in between, but Flux ignores pushes until both sides match, so do it in one go:

1. In your own terminal: `sops platform/flux-webhook/secret.sops.yaml` and set `token` to the output of `openssl rand -hex 32`.
2. `make check-secrets`, commit, push, `make reconcile UNIT=platform-flux-webhook` (the receiver reads the Secret on each request; nothing restarts).
3. On GitHub: Settings → Webhooks → the `flux-webhook` hook → Secret: paste the same value, save, then **Redeliver** the latest delivery and check it returns 200.

Rotate the image webhook token ([what it is](services.md#image-webhook)); until step 3, app repositories call the old address and new images wait for the hourly scan:

1. In your own terminal, from the repository root, write one new value to both copies:

   ```bash
   T=$(openssl rand -hex 32)
   SOPS_AGE_KEY_FILE=./age.agekey sops set platform/flux-webhook/image-secret.sops.yaml '["stringData"]["token"]' "\"$T\""
   SOPS_AGE_KEY_FILE=./age.agekey sops set platform/console/secret.sops.yaml '["stringData"]["IMAGE_WEBHOOK_TOKEN"]' "\"$T\""
   unset T
   ```
2. `make check-secrets` (the two copies must match), commit, push, `make flux-reconcile` (Reloader restarts the console).
3. `make app-hooks` repoints every app repository's webhook; `make verify-platform` shows them under Image Webhooks after each repository's next publish.

Replace a [notification channel](services.md#notification-channels)'s address when it may have leaked or was lost: in your own terminal, `make team-channel NAME=<team>` (it prompts without showing the address), then `make check-v2`, commit, push and `make flux-reconcile`; subscribe to the new topic and unsubscribe from the old.

Rotate the ntfy topic ([alerts](services.md#alerts)) when it may have leaked: in your own terminal, `sops platform/alerts/secret-platform.sops.yaml` and replace the topic (`swhurl-` and 24 hex characters, for example from `openssl rand -hex 12`) in `address`. In `platform/console/notification-secret.sops.yaml`, set `NTFY_PLATFORM_URL` to the same topic URL without the rendering query string. Then `make check-secrets`, commit, push, `make flux-reconcile` (both consumers must update) and wait for the next successful dashboard job (or run `make clickstack-dashboards`); subscribe to the new topic in the ntfy app and unsubscribe from the old.

**Test webhook reconciliation without sending a notification.** Run with `!`:

```bash
DRY_RUN=true make clickstack-dashboards
```

This reads live credentials and previews repairs to the two [platform-owned webhooks](services.md#clickstack-and-otel), dashboards and alerts without writing or sending a message. To inspect the scheduled owner, run with `!`:

```bash
make verify-platform
kubectl -n console get jobs --sort-by=.metadata.creationTimestamp
kubectl -n console logs job/<newest-console-dashboards-job>
```

Look for a recent successful job and both reserved names reported up to date or repaired; do not print webhook bodies or Secret values. A duplicate reserved name, conflicting service type or truncated API listing needs investigation before retrying: the job refuses all webhook writes in those cases. A successful repair proves configuration, not end-to-end alert delivery. Manual reconciliation is `make clickstack-dashboards`; bootstrap remains `make clickstack-bootstrap` and is never automatic.

### Signing keys

Apps check each other's calls ([calling another app](apps.md#calling-another-app)) against the cluster's public service-account signing keys. They are public keys, not a Secret, and they live in one place: the `auth` block of the platform profile, [`v2/testdata/profile.json`](../v2/testdata/profile.json), from which v2 renders [`platform/auth`](../platform/auth) for every managed namespace. (`apps/signing-keys.json`, v1's copy, is gone: nothing wrote it any more, so a rotation would have left two answers.) k3s creates the key pair once and keeps it, so the profile changes only when the cluster is rebuilt or the key is rotated by hand. Until it follows, every call between apps is refused with 401 while browser access keeps working. `make verify-platform` (section "Call identity") reports the difference; to fix it, read the cluster's key set and put it in the profile's `auth.keys` as one JSON string, then render:

```bash
kubectl get --raw /openid/v1/jwks          # the key set, and the issuer from /.well-known/openid-configuration
cd v2 && go run ./cmd/swhurl-v2 render --records ../v2-records \
     --profile testdata/profile.json --out .. --revision "$(git rev-parse HEAD)"
cd .. && make check-apps
git add platform v2/testdata v2-apps && git commit -m "keys: refresh the token signing keys" && git push
```

The keys live in `platform/auth`, one `swhurl-auth` ConfigMap per managed namespace, rendered by `swhurl-v2 render` from the profile and applied by the `platform-auth` unit — so a rotation rewrites that one folder, and every managed namespace gets it together. A v1-rendered callee verifies with them from its environment, which only a new pod reads, so it restarts when Flux
applies the commit. A v2-rendered callee mounts the keys and its allow-list only as files (`SWHURL_AUTH_KEYS_FILE`,
`SWHURL_ALLOWED_CALLERS_FILE`) and picks a rotation up in about a minute **without a restart**; its image must be
built from a template that reads them (TypeScript `v0.2.19`, Kotlin `v0.2.18` on), or it refuses every token.

## Certificate mode

`CERT_ISSUER` in `platform-settings` selects the Let's Encrypt issuer for platform hosts (sign-in, ClickStack). Apps choose their own issuer.

```bash
make platform-certs-staging       # or platform-certs-prod; edits the file only
git commit -am "platform: CERT_ISSUER=letsencrypt-staging" && git push && make flux-reconcile
kubectl get ingress -A -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,ISSUER:.metadata.annotations.cert-manager\\.io/cluster-issuer
```

## Lifecycle

Deploy, update and uninstall through Git. Removing an app instance's unit from `clusters/home/kustomization.yaml` uninstalls it (`swhurl-v2 remove` or the console's Uninstall does that and removes its files, [apps](apps.md#operate-an-instance)); namespaces and claims annotated `kustomize.toolkit.fluxcd.io/prune: disabled` stay. For what Git cannot express:

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
- **One PR per chart**, a separate one for a major version. A chart used by several releases is bumped in all of them in one PR: the OTel chart for both collectors, and app-template for the console, the egress proxy and notify.
- **app-template needs a hand step.** v2 owns the app instances' chart version: it is `chart.version` in [`v2/testdata/profile.json`](../v2/testdata/profile.json), and each instance's `v2-apps/instances/<app>/<env>/helmrelease.json` is rendered from it. Renovate does not see the profile and cannot run the render, so its PR fails `make check-v2`: `TestProxyManifests` renders the egress proxy with its own chart version, and `make check-apps` caches only the profile's. On the PR's branch, set the same version in the profile, run `make v2-render`, update the v2 tests and fixtures that name the version (`make check-v2` lists them; `go test ./kernel -run TestGoldenCompilations -update` rewrites the compiled goldens), then run `make check` and `make check-v2`, and push. Merging it upgrades **every app instance, staging and production together**. App maintainers review those PRs for their app.
- The repository's Dependency Dashboard issue lists open PRs and pending updates; ticking a rate-limited entry opens its PR on the next run, usually within a minute.

For each PR: read the chart's release notes and compare its `appVersion` (`helm pull <chart> --repo <url> --version <v>` for both versions); charts that leave image tags unpinned upgrade the app with it, so a patch bump can be a large app upgrade (ClickStack 1.1.2 moved HyperDX from 2.8.0 to 2.19.0). Run `make backup-mongodb` before a ClickStack bump. CI renders every change, `make check-apps` checks render equality and actual chart-output policy over every compiled v2 instance ([the app policy](apps.md#the-app-policy)), and `make check-otel` validates the collector config with the new collector version. After merging, the webhook applies it within seconds; `make flux-reconcile` waits for every unit, then `make verify-platform` (and `make app-status` for app-template). To roll back, revert the merge commit.

Renovate does not see the tool versions: `kubectl` and `helm` are pinned in both [`validate.yml`](../.github/workflows/validate.yml) and the console image ([`images/console/Dockerfile`](../images/console/Dockerfile)), with `sops` pinned in the image only. Bump a version in both places, with its SHA-256 in the Dockerfile, and push: the publish run builds and deploys the new console ([console](console.md#deploy-a-new-console)). Flux's controllers are installed by `make flux-install`, at the version in [`clusters/home/flux-system/install/flux.json`](../clusters/home/flux-system/install/flux.json) and with the patches in [`kustomization.yaml`](../clusters/home/flux-system/install/kustomization.yaml) beside it (the upstream manifests themselves are not in Git). To upgrade Flux: bump `version` in `flux.json`, install that `flux` CLI locally, run `make flux-install DRY_RUN=true` to see the live diff, then `make flux-install`, and push the change (a `flux.json` change alone deploys no console: the file is under `clusters/`, not `tools/`). Don't run plain `flux install`: it drops the patches, and `make verify-platform` then warns.

## Notification checker heartbeat

The host timer checks the in-cluster `console-notifications` CronJob every five minutes. It posts a high-priority alert when the Job is missing, suspended or has not succeeded for 10 minutes, reminds once an hour while stale, and sends a normal-priority recovery after the checker succeeds again. The service uses the user's kubeconfig and reads the existing ntfy failure destination from `console/notification-ntfy` for each post; it stores only incident timestamps in `~/.local/state/swhurl-platform/notification-heartbeat.json` (directory mode 0700, file mode 0600). Its log is `/var/log/swhurl-platform/swhurl-notification-heartbeat.log` and is collected by ClickStack.

The job it watches is the notification checker (10 minutes), listed in `Watched` in [`v2/heartbeat/heartbeat.go`](../v2/heartbeat/heartbeat.go). Add another row only after its CronJob is deployed and unsuspended, because a missing or suspended job is reported as stale. The timer runs `swhurl-v2 notifications-heartbeat`, built from this checkout on each run (Go is on the unit's `PATH`), so a change takes effect on the next run without reinstalling; a change to the unit template needs `make host-heartbeat` again.

The heartbeat runs outside the console image, its deploy job and the Kubernetes scheduler. It still needs this host, the Kubernetes API, the Secret and ntfy to work. It cannot notify while the host or API is down. It alerts after 10 minutes without a successful checker Job plus up to one five-minute timer interval and one second. On this host, `make verify-platform` checks that the timer is active and the service's last run succeeded within 15 minutes.

Run installation and removal in your own terminal; both commands need `sudo`:

```bash
make host-heartbeat
systemctl status swhurl-notification-heartbeat.timer
systemctl status swhurl-notification-heartbeat.service
make notifications-heartbeat DRY_RUN=true
tail -n 100 /var/log/swhurl-platform/swhurl-notification-heartbeat.log
make host-heartbeat-delete
```

Removal leaves the incident file and log in place. To test delivery without touching the real incident flags, use a throwaway state file: `make notifications-heartbeat ARGS="--max-age 1s --state /tmp/hb.json"` sends a real stale message, and the same command without `--max-age 1s` then sends recovery.

## Connectivity probe

The host timer records whether the path to the internet works, once a minute, as one NDJSON line per run in `/var/log/swhurl-platform/connectivity-probe.log` (collected by ClickStack, `service.name` = `connectivity-probe`). The line is prefixed `[INFO]` or `[ERROR]` for the severity parser — the same texts the other host timers use, so `SeverityText='error'` covers the probe — and carries one timing per layer, so the layer that broke is named by the data instead of guessed after the fact: `gw_icmp` (WiFi link and router), `resolver_dns` plus `resolver_rcode` (the host's own resolver, which every service uses), `wan_dns` plus `wan_dns_rcode` (DNS over TLS to 1.1.1.1:853, past the router), `wan_icmp` and `tcp443` (the WAN without DNS). A layer that does not answer inside its timeout is `null`, and `ok` in the body repeats the prefix as a flag.

`ok` is true when `resolver_dns` and `tcp443` answered: those are the two things this host's workloads depend on. The ICMP probes and `wan_dns` only localise a failure, because a firewall may drop ICMP or DNS-over-TLS legitimately; one of them going null on its own is worth reading but is not an outage.

The router answers **every** UDP/53 query whatever its destination address (measured 10 November 2026: a query sent to TEST-NET `192.0.2.1` is answered), so a plain query to `1.1.1.1` tests the router and not the WAN. That is why `wan_dns` uses DNS over TLS, which also verifies the peer is really Cloudflare rather than a local proxy. Both DNS probes ask for a random label in `swhurl.com`, so an answer cannot come from a cache and `rcode` 3 (NXDOMAIN) proves the authority was reached.

The probe exits 0 even when layers fail; `[ERROR]` is the signal, and a failed unit would add systemd noise to the incident it is recording. Install or remove it in your own terminal:

```bash
make host-probe
systemctl status connectivity-probe.timer
tail -n 20 /var/log/swhurl-platform/connectivity-probe.log
make host-probe-delete
```

The script reads `/etc/resolv.conf` on each run, so it follows a resolver change without reinstalling; a change to a unit template needs `make host-probe` again.

## Incident reviewer

The [incident reviewer](services.md#incident-reviewer) and its namespace are removed from the platform. The encrypted model key remains in Git history, so deleting the Kubernetes Secret did not revoke it at the provider.

## Backups and recovery

| Data | Class | Where it survives |
| --- | --- | --- |
| Manifests and encrypted Secrets | Irreplaceable | GitHub |
| age private key | Irreplaceable | Encrypted off-host copy (location kept outside Git) |
| ClickStack MongoDB (team, users, sources, dashboards, alerts, webhooks) | Irreplaceable | Nightly backup job to S3 (`clickstack-mongodb/`); data volume on `local-path-retain` |
| App SQLite databases (`--database sqlite`) | Irreplaceable | Nightly backup job per database to S3 (`app-sqlite/<namespace>/`); data volume on `local-path-retain` |
| ClickHouse telemetry | Expendable | Expires after 30 days; ClickHouse's own logs after 7 |
| Cluster state (k3s SQLite datastore, `/var/lib/rancher/k3s/server/db`) | Reconstructible | Rebuilt from Git by Flux ([bootstrap](bootstrap.md)); not backed up |

```bash
kubectl get cronjobs -A -l platform.swhurl.com/backup=true   # every backup job, its schedule and last run
make backup-mongodb              # run MongoDB's job now and show its log
make backup-sqlite               # run every app database's job now and show their logs
make live-test-restore-mongodb   # restores the newest backup in S3 into a throwaway namespace and checks it
```

**One job per database.** `backup-mongodb` in `observability` (unit `platform-backup`, 03:30 UTC) and `<app>-backup` in each namespace with a SQLite database (rendered with the instance, at a minute between 03:35 and 04:24 UTC that its namespace picks) run the `ghcr.io/samclement/swhurl-backup` image ([`images/backup/Dockerfile`](../images/backup/Dockerfile), [`images/backup/tool`](../images/backup/tool)): one Go binary does the copy, the encryption and the upload — no Python, no AWS CLI and no `age` CLI — beside `mongodump`, on Debian slim because `mongodump` is a glibc binary. A failed job affects only its database, and nothing depends on this host. MongoDB streams `mongodump` straight into `age` with the login on stdin, then records the server version and each collection's count. A SQLite job runs as the app's user with the app's claim mounted, takes SQLite's online backup (the app keeps running) into a scratch volume, requires `PRAGMA integrity_check` to pass, records the table count and the plaintext's size and SHA-256, and encrypts it. Encryption needs only the age public key, so no job holds the private key.

Each job then publishes its archive and, last, its metadata to `s3://swhurl-platform-backups-110927251694/` (`eu-west-2`, public access blocked, versioned), with S3 checking each file's SHA-256 and refusing to replace an object. Each location is stated once, where its job reads it: MongoDB's is `BACKUP_S3_URI` in [`platform/backup/mongodb.yaml`](../platform/backup/mongodb.yaml), the apps' is `backup.s3Uri` in [`v2/testdata/profile.json`](../v2/testdata/profile.json) (one prefix per namespace below it). The restores read those files and `make verify-platform` reads the apps' one and, for MongoDB, the live CronJob's `BACKUP_S3_URI`; no variable overrides them, and `make check` fails if the two name different buckets. The bucket deletes backups under `clickstack-mongodb/` and `app-sqlite/` after 90 days and replaced versions after 30. The upload key belongs to IAM user `swhurl-backup-writer`, which may only upload to those two prefixes over TLS (its key is also in the gitignored `secrets/backup-writer.sops.yaml`); it reaches an app namespace as `<app>-backup`, whose values Flux substitutes from the `backup-writer` Secret in `flux-system` (`platform/backup/writer.sops.yaml`). A SQLite job has no service-account token and reaches S3 through the instance's egress proxy port. Job logs reach ClickStack with the Job's name as the service.

`make verify-platform` judges freshness from S3 alone: a backup counts once its archive and its metadata are both there, and every instance whose `release.json` declares a SQLite database must have one younger than 26 hours (`BACKUP_MAX_AGE_HOURS`). This inventory reads v2's records, including record-only instances that have not rendered yet; unreadable records fail the check rather than being treated as no databases. **Pause scheduled backups:** v2 renders every SQLite backup CronJob's `suspend` from the profile, so set `backup.suspended: true` in [`v2/testdata/profile.json`](../v2/testdata/profile.json), and set `spec.suspend: true` in [`platform/backup/mongodb.yaml`](../platform/backup/mongodb.yaml) too, because MongoDB's own job is not v2's. Re-render (`cd v2 && go run ./cmd/swhurl-v2 render --records ../v2-records --profile testdata/profile.json --out .. --revision "$(git rev-parse HEAD)"`), validate with `make check`, then commit, push and reconcile. `make check-apps` fails while a committed CronJob and the profile disagree. It prevents new scheduled runs, not jobs already running or manual `make backup-*` requests. To resume schedules, set both to `false` and repeat the same sequence. Suspension does not waive the 26-hour freshness check. A present `platform/backup/` directory with a missing or invalid `mongodb.yaml` (including a non-boolean `spec.suspend`) is a validation error, never an implicit resume.

Restore an app's database from S3. `make restore-sqlite` runs `swhurl-v2 restore` on the instance's records at `HEAD`: it lists `app-sqlite/<app>-<env>/` with the operator's AWS credentials (they need S3 read access; the jobs' key cannot read; v2 reads them from the environment, so unless `AWS_ACCESS_KEY_ID` is set the target exports the AWS CLI's own for that one run), takes the newest archive by upload time and plans the restore from it, refusing it as `incomplete` when its metadata is missing or does not verify. Without `CONFIRM` it needs `DRY_RUN=true` and only prints that plan, whose step names the archive it chose (`FROM=`), so running that step restores what was planned. To restore an older one, name its key with `FROM`; one older than 26 hours also needs `MAX_AGE`:

```bash
make restore-sqlite APP=notes ENV=prod DRY_RUN=true          # checks the backup, prints v2's plan, changes nothing
make restore-sqlite APP=notes ENV=prod CONFIRM=notes/prod     # the app is down for about a minute
aws s3 ls s3://swhurl-platform-backups-110927251694/app-sqlite/notes-prod/   # an older one:
make restore-sqlite APP=notes ENV=prod CONFIRM=notes/prod MAX_AGE=200h \
  FROM=app-sqlite/notes-prod/sqlite-YYYYMMDDTHHMMSSZ.db.age
```

The app images are distroless — no `ls`, no `sh` — so `kubectl exec … ls /data` fails with `executable file not
found in $PATH`. To look inside a claim, mount it read-only in a one-shot pod instead:

```bash
kubectl -n <namespace> run inspect --rm -i --restart=Never --image=busybox:1.37 --overrides='
{"spec":{"containers":[{"name":"inspect","image":"busybox:1.37","command":["ls","-la","/data"],
 "resources":{"requests":{"cpu":"10m","memory":"64Mi"},"limits":{"memory":"256Mi"}},
 "volumeMounts":[{"name":"data","mountPath":"/data","readOnly":true}]}],
 "volumes":[{"name":"data","persistentVolumeClaim":{"claimName":"<claim>"}}]}}'
```

The `resources` are not optional: an instance namespace's ResourceQuota is sized for the app plus its backup
pod, so a pod without `requests.cpu`, `requests.memory` and `limits.memory` is refused
(`failed quota: … must specify …`). The values above are the backup job's, which the quota already counts.

The restore needs no decryption on this host. It refuses a plan that is not `ready` (an archive without verifying metadata, or older than `MAX_AGE`), then suspends the instance's HelmRelease (so no upgrade starts the app mid-restore) and its backup CronJob, scales the app to 0, and runs the backup image's restore job as the app's user on its claim, with a presigned read of that one archive and the age identity from `AGE_KEY_FILE` (else `SOPS_AGE_KEY_FILE`, else `./age.agekey`) in a Secret it creates and deletes. The job checks the archive against its metadata and the decrypted copy against the backup's plaintext SHA-256, `integrity_check` and the recorded table count, or the live database is left untouched. The current database and its `-wal`/`-shm` files move to `before-restore-<UTC>/` beside it (delete that directory yourself once you are happy); then the app is scaled back and the HelmRelease and the schedule resumed, even when a step fails. The command prints a `v2-restore` document of every step and the job's log. It was proved live on 27 October 2026 by restoring `swhurl-try-10` staging over its newest archive in 30 seconds ([evidence](current-state.md#the-first-restore-v2-performs-swhurl-try-10-staging-newest-archive-over-itself-27-october-2026)) and is tested offline in `v2/` and `images/backup/tool`; no live test repeats it.

**Targets:** at most 24 hours of MongoDB or app database changes lost (daily backups); about an hour from a bare host to working ClickStack.

**Recovering on a new machine** needs Git (GitHub), `kubectl`, `flux`, `helm`, `sops`, `age`, Python 3.11+ with PyYAML, AWS CLI v2, the age private key and read access to the backup bucket. Find the key through your own recovery notes or the person responsible for its encrypted off-host copy; put it at `age.agekey` in the repository root and set `SOPS_AGE_KEY_FILE=./age.agekey`. Do not copy the key into Git. Follow [bootstrap](bootstrap.md) through Flux setup, then list the MongoDB backups and copy both the chosen archive (normally the newest) and its matching metadata file into a private working directory:

```bash
aws s3 ls s3://swhurl-platform-backups-110927251694/clickstack-mongodb/
mkdir -m 700 -p "$HOME/restore-backups" && cd "$HOME/restore-backups"
ARCHIVE=clickstack-mongodb-YYYYMMDDTHHMMSSZ  # choose the same timestamped base name from the listing
aws s3 cp "s3://swhurl-platform-backups-110927251694/clickstack-mongodb/$ARCHIVE.archive.gz.age" .
aws s3 cp "s3://swhurl-platform-backups-110927251694/clickstack-mongodb/$ARCHIVE.json" .
export BACKUP_FILE="$PWD/$ARCHIVE.archive.gz.age" && cd -
export AGE_KEY_FILE="$PWD/age.agekey"
```

Replace `YYYYMMDDTHHMMSSZ` with the same timestamp shown by `aws s3 ls`. Point `KUBECONFIG` at the cluster being recovered before running any `kubectl`, `flux` or `make` target that uses the cluster. Follow the restore commands below; compare `mongorestore`'s counts with the copied `.json` metadata. Then run `make clickstack-bootstrap` and `make verify-platform`. The Google OAuth client and every other credential come from SOPS.

The restore test passes only if the checksum, collection counts and restored ingestion key (against the Git Secret) all match; it never touches live workloads.

Without a backup, a fresh install needs only `make clickstack-bootstrap`: it registers the admin from SOPS and sets the Git ingestion key, and HyperDX recreates the default connection and sources. Dashboards, saved searches and other users are lost.

Restore into the running service. MongoDB requires a login, so the connection string goes into a private file in the pod (never a command line) while the archive streams on stdin:

```bash
kubectl config current-context   # confirm this is the cluster being recovered
kubectl -n observability scale deploy/clickstack-app --replicas=0
kubectl -n observability wait --for=delete pod -l app=clickstack --timeout=120s   # HyperDX takes ~40 s to stop
kubectl -n observability get secret clickstack-mongodb-hyperdx-hyperdx -o jsonpath='{.data.connectionString\.standard}' \
  | base64 -d | python3 -c 'import json,sys; print("uri: " + json.dumps(sys.stdin.read()))' \
  | kubectl -n observability exec -i clickstack-mongodb-0 -c mongod -- sh -c 'umask 077; cat > /tmp/login.yaml'
age -d -i age.agekey "$BACKUP_FILE" | kubectl -n observability exec -i clickstack-mongodb-0 -c mongod -- \
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
| A CronJob stops starting jobs right after a deploy (`kubectl -n console get cronjob`: LAST SCHEDULE keeps growing, not suspended, nothing active) | The k3s CronJob controller failed to sync it while Helm was updating it and did not queue it again (`journalctl -u k3s` shows `error syncing CronJobController`; seen once, 12 October 2026, on `console-notifications`). Any change to the object makes the controller look again: add and remove an annotation, `kubectl -n console annotate cronjob <name> platform.swhurl.com/resync=1` then the same with `resync-`. `make verify-platform` and the host heartbeat report a checker that has stopped. |
| `flux reconcile` seems ignored | A previous revision is still running health checks (up to the unit's timeout); new requests queue behind it. |
| A HelmRelease is `Stalled` with `MissingRollbackTarget: cannot remediate failed release`, and the workload now looks healthy | The earlier attempt failed as an *install*, so there is no revision to roll back to and Flux will not retry on its own. `flux reconcile helmrelease <name> -n <namespace> --force` resets the failure counters and retries the upgrade; seen 25 October 2026 on the first v2 instance, whose first image crash-looped ([current state](current-state.md#first-v2-instance-live-25-october-2026)) |
| Deleting a broken app takes about 3 minutes | Its Helm install is still in progress; the finalizer waits for the Helm timeout (`FAIL_AFTER`, [apps](apps.md)). |
| A certificate stays not Ready on a new host | DNS has not propagated or Let's Encrypt cannot reach port 80; check `kubectl get challenges -A` and the router forward. |
| A new host doesn't resolve | Multi-label names are not covered by the wildcard; add them to `DYNAMIC_DNS_RECORDS` and re-run `make host-dns`. |
| ClickHouse has `*_log_0` tables after a restart | ClickHouse renamed system log tables whose definition changed. Check, then drop them, or leave them to expire (they keep their 7-day TTL). |
| ClickHouse uses far more CPU than its queries explain; `make verify-platform` reports failed merges | A merge is failing and retrying at once (`SELECT table, error, exception FROM system.part_log WHERE event_type = 'MergeParts' AND error != 0 ORDER BY event_time DESC LIMIT 5`). Code 241 on a very wide table (`system.metric_log` has about 1,350 columns) means a horizontal merge needs more than the memory limit: give that table `settings: vertical_merge_algorithm_min_rows_to_activate = 1` under its entry in `clickhouse.cluster.spec.settings.extraConfig` (as `metric_log` has). If vertical merges fail too, or old days outlive the 7-day TTL (deleting rows rewrites the whole part), also give it `partition_by: toYYYYMMDD(event_date)` and `ttl_only_drop_parts = 1`, as `metric_log` has. `system.part_log` does not record every failure: `SELECT count() FROM system.text_log WHERE logger_name = 'MergeTreeBackgroundExecutor' AND level = 'Error' AND event_time > now() - INTERVAL 1 HOUR` counts them all, and the part name in the message (`{<table uuid>::<part>}`) names the table (`SELECT database, name FROM system.tables WHERE uuid = '…'`). The restart renames the old table to `<name>_N` with the old settings; `ALTER TABLE system.<name>_N MODIFY SETTING …` with the new settings applies them there too. |

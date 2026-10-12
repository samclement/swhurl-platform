# Services

The shared services every app can rely on. Each is its own Flux unit ([architecture](architecture.md#flux-units)); operating them is covered in [operations](operations.md).

| Service | Flux unit · path | Namespace | Host | Chart |
| --- | --- | --- | --- | --- |
| Namespaces, storage classes | `infra-base` · [`infra/base`](../infra/base) | — | — | — |
| cert-manager | `infra-cert-manager` · [`infra/cert-manager`](../infra/cert-manager) | `cert-manager` | — | cert-manager |
| ClusterIssuers | `infra-issuers` · [`infra/issuers`](../infra/issuers) | — | — | plain manifests |
| Traefik settings | `infra-traefik` · [`infra/traefik`](../infra/traefik) | `kube-system` | — | k3s packaged (chart 38, Traefik 3.6) |
| Sign-in (oauth2-proxy) | `platform-oauth2-proxy` · [`platform/oauth2-proxy`](../platform/oauth2-proxy) | `ingress` | `oauth.` | oauth2-proxy |
| ClickStack operators | `platform-clickstack-operators` · [`platform/clickstack-operators`](../platform/clickstack-operators) | `observability` | — | clickstack-operators (MongoDB and ClickHouse operators and CRDs) |
| ClickStack | `platform-clickstack` · [`platform/clickstack`](../platform/clickstack) | `observability` | `clickstack.` | clickstack |
| OTel collectors | `platform-otel` · [`platform/otel`](../platform/otel) | `logging` | — | opentelemetry-collector |
| Reloader | `platform-reloader` · [`platform/reloader`](../platform/reloader) | `platform-system` | — | reloader |
| [NATS](#nats) | `platform-nats` · [`platform/nats`](../platform/nats) | `messaging` | — | nats, nack |
| [Console](console.md) | `platform-console` · [`platform/console`](../platform/console) | `console` | `console.` | app-template, image from this repo |
| [Image automation](apps.md#deploy-a-new-image) | `platform-image-automation` · [`platform/image-automation`](../platform/image-automation) | `flux-system` | — | Flux image controllers (from `make flux-install`) |
| [Alerts](#alerts) | `platform-alerts` · [`platform/alerts`](../platform/alerts) | `flux-system` | — | The Flux notification Provider and Alert, to ntfy.sh |
| [Notify service](#notify-service) and [notification channels](#notification-channels) | `platform-notify` · [`platform/notify`](../platform/notify) | `notify` | — | app-template, the console's image; channel Secrets and the generated routes as plain manifests |
| [Push webhook](#push-webhook) and [image webhook](#image-webhook) | `platform-flux-webhook` · [`platform/flux-webhook`](../platform/flux-webhook) | `flux-system` | `flux-webhook.` | plain manifests (two Flux `Receiver`s) |

Hosts are under `BASE_DOMAIN` (`homelab.swhurl.com`). Chart versions are pinned in each HelmRelease and updated by Renovate ([chart updates](operations.md#chart-updates)).

## Settings

| Setting | Where | Used by |
| --- | --- | --- |
| `CERT_ISSUER` | [`platform-settings`](../clusters/home/flux-system/sources/configmap-platform-settings.yaml) | Platform ingresses (sign-in, ClickStack) via Flux substitution; change with `make platform-certs-*` |
| `BASE_DOMAIN` | same | Parent of every platform host (`oauth.`, `clickstack.`) and of the sign-in cookie and redirect allowlist; `swhurl` reads it for the app policy's cookie-domain rule. `make test` fails if a platform manifest writes the domain literally |
| `DYNAMIC_DNS_RECORDS` | [`host/dns.env`](../host/dns.env) | Host DNS updater only, never the cluster |

`CERT_ISSUER` switches only the platform's own certificates (sign-in, ClickStack); each app names its issuer in its own HelmRelease. App hosts are written literally by `make app-new`. Two things stay literal on purpose: the Let's Encrypt account email (`ops@homelab.swhurl.com`, a mailbox rather than a host) and the approved sign-in addresses. A unit substitutes settings only if its manifests use them (the OTel unit also substitutes, to unescape `$${...}`); `make test` checks both directions.

Changing `BASE_DOMAIN` moves every platform host and the cookie: it needs DNS records, new certificates, a new Google redirect URI, re-generated app hosts, and everyone signs in again.

## Sign-in

oauth2-proxy signs users in with Google (OIDC) and serves the Traefik middleware `ingress-oauth-auth-shared@kubernetescrd`. Any Ingress that references it requires sign-in; apps get it with `--exposure authenticated-web`.

- **Who may sign in:** only the addresses in `authenticatedEmailsFile.restricted_access` in [`platform/oauth2-proxy/helmrelease.yaml`](../platform/oauth2-proxy/helmrelease.yaml) (currently `sam@swhurl.com`). The chart's default "any domain" is overridden with `email_domains = []`; `make test` fails if either returns. Add an address there and push.
- **Cookie scope:** the session cookie covers `.homelab.swhurl.com`, so every host under it receives it. Put public or untrusted apps on a different parent domain.
- **HTTPS only:** cookies are `Secure`, so sign-in fails with 403 over plain HTTP. Traefik redirects all HTTP to HTTPS.
- **Secret** `ingress/oauth2-proxy-shared-secret`: `client-id`, `client-secret` (from the Google OAuth client, which must allow `https://oauth.<BASE_DOMAIN>/oauth2/callback`) and `cookie-secret` (random, for example `openssl rand -base64 32`). Reloader restarts oauth2-proxy when it changes.
- **How ForwardAuth works:** oauth2-proxy runs with `upstream=static://202`, and the middleware ([`middleware.yaml`](../platform/oauth2-proxy/middleware.yaml)) calls its root `/` rather than `/oauth2/auth`, so an unauthenticated browser gets a redirect it can follow. The callback is `https://oauth.<BASE_DOMAIN>/oauth2/callback`. The return URL after sign-in comes from `X-Forwarded-*` headers: Traefik's entrypoints drop client-supplied ones, and oauth2-proxy accepts them only from the k3s pod CIDR `10.42.0.0/16` (`trusted-proxy-ip`), where Traefik runs; any pod can still set them, since pods are not a trust boundary here ([limits](apps.md#limits)). If the pod CIDR changes, sign-in returns users to the wrong URL. Sign-in uses PKCE (`S256`).
- ClickStack uses its own login, not this middleware.

## ClickStack and OTel

ClickStack stores logs, metrics and traces: the HyperDX app (UI and API), ClickStack's own OTel collector, MongoDB (team, users, sources) and ClickHouse (telemetry). MongoDB and ClickHouse are run by operators that `platform-clickstack-operators` installs first (MongoDB Community operator; ClickHouse operator with a Keeper). Two OTel collectors in `logging`, a per-node DaemonSet and a cluster Deployment, send node and cluster telemetry to ClickStack's collector (`clickstack-otel-collector.observability:4318`). The DaemonSet also accepts OTLP logs, metrics and traces from apps on the node IP (host networking, ports 4317 and 4318) and forwards them with the same key; apps send no key of their own ([apps](apps.md#telemetry)). It normalizes logs ([formats](#structured-logs)), attaches their trace context, and drops the spans of Kubernetes probe requests (`filter/kube-probes`, user agent `kube-probe/…`). The cluster Deployment's OTLP Service is the chart default and forwards nothing that apps send.

### Structured logs

For example, `{"level":30,"msg":"request completed","req":{"method":"GET"},"status":200}` becomes body `request completed`, severity INFO, and searchable `LogAttributes.req.method` and `LogAttributes.status`. The node collector parses after container framing and partial-line reassembly; ClickStack receives normalized OTLP, without a second platform parser at ingestion. Existing rows keep their original format.

| Source | Handling |
| --- | --- |
| Apps, Flux, MongoDB, MongoDB operator, cert-manager, Traefik, Reloader, console, OTel collectors; any JSON object | `msg`/`message` becomes the body; other fields become attributes. HyperDX's `[API]`, `[APP]` and `[ALERT-TASK]` JSON prefixes are accepted. |
| ClickHouse and Keeper | Timestamp, thread/query IDs, level, logger and message from their text prefix. |
| ClickHouse operator and platform collectors | Tab-separated logger output and its trailing JSON fields. |
| metrics-server and older cert-manager logs | Kubernetes klog prefix (level, caller, PID, message and quoted key/value fields). |
| oauth2-proxy | Request fields (method, path, status, client, user agent, sizes); a separate parser for application lines. |
| CoreDNS | Bracketed level and message. |
| local-path provisioner; older Traefik and Reloader logs | Quoted logfmt fields. |
| Older console logs | Uvicorn access/application lines and audit prefixes. |
| Host timers | Existing filename-to-service mapping and `[OK]`/`[WARN]`/`[BAD]`/`[ERROR]` prefixes. |
| Kubernetes events | Message or note becomes body, Warning becomes WARN and Normal becomes INFO; event metadata uses `k8s.event.*` attributes. |
| Helpers, shell output, MongoDB agent, unmatched or malformed lines | Body passes unchanged; `log.parser=plain` explicitly identifies the fallback. No severity is guessed from words in the message. |

`LogAttributes.log.parser` identifies the matched format. Explicit string levels, Pino numeric levels and MongoDB's `s` field become OTel severity. Valid trace/span IDs at the top level or under `mdc` populate the record so a trace opens its logs. Existing OTLP severity, timestamps, attributes, resource identity and trace context take precedence. Source timestamps fill missing record timestamps; container timestamps remain authoritative.

cert-manager's three components, Traefik's existing log streams, Reloader, the platform collectors and console emit native JSON. cert-manager's klog JSON uses `v` for informational records and `err` for error records; the source-specific mapping handles these without guessing from message text. Console access logs include method, path and status; its action audit records include identity, action, unit, state, job ID and link ([console](console.md#how-it-is-protected)). Verbosity and access-log enablement stay at their existing settings.

HyperDX also sends already structured native OTLP logs directly to ClickStack, alongside its stdout stream. Those retain their SDK attributes and trace context; `verify-logs` identifies scope `node-logger` as `native-otlp`, without claiming they passed through a node parser. Missing SDK severity remains unspecified. Native map-valued OTLP bodies passing through the node collector remain structured maps.

Nested objects flatten to dotted attribute names, arrays remain JSON strings, and objects below four map levels remain JSON strings. The collector's `ottl.functions.enableLambda` gate enables the bounded array-preserving mapping; `make check-otel` uses that same gate and runs synthetic fixtures through the actual deployed collector version, including CRI partial lines and Docker framing. This catches changed parser behaviour during collector upgrades.

When the body changes, `LogAttributes.log.record.original` retains the complete original line (additional storage, under the same retention). A JSON object without a message retains its JSON body and still exposes its fields. Malformed input never drops the record. Parsers are scoped to the source workload for non-JSON formats.

**Deliberately not collected.** ClickStack's own pods are the noisiest log source, nearly all of it health-probe and connection chatter nobody reads. The node collector's `filter/platform-noise` drops, after parsing: Keeper and ClickHouse operator lines below INFO (the Keeper logs every kube-probe at trace level, the operator each reconcile step at debug) and MongoDB's connection lifecycle (`Connection accepted`, `Connection ended`, `client metadata`, `Authentication succeeded`). Warnings and errors from those pods, a line whose severity could not be parsed, and every other pod's lines are kept. The pods' own container logs are untouched, so `kubectl -n observability logs <pod>` still shows everything; to look at the dropped lines in ClickStack, remove the filter from the logs pipeline in [`helmrelease-daemonset.yaml`](../platform/otel/helmrelease-daemonset.yaml). The fixtures in `tests/fixtures/logs.json` (`dropped: true` cases) prove each drop and each keep through the real collector (`make check-otel`).

**Volume.** HyperDX's own traces are sampled at 10% (`OTEL_TRACES_SAMPLER_ARG` in `hyperdx.config`). Host metrics are scraped every 30 s, kubelet metrics and cluster state every 60 s (`collection_interval` in the two OTel HelmReleases). ClickStack's scrape of ClickHouse's own metrics (about half of all metric rows) stays at 30 s: the chart's `global.otelCollector.customConfig` is merged below the remote configuration HyperDX pushes over OpAMP, which defines that receiver, so it has no effect there. These cut stored rows, not ClickHouse's CPU, which is dominated by merging many tiny inserts ([current state](current-state.md#steps-2-and-3-and-the-final-comparison)).

In ClickStack's Logs source, try `LogAttributes.log.parser:json`, or `ResourceAttributes.k8s.namespace.name:demo-orders-prod AND LogAttributes.method:GET`; open a result to see fields, its original line and any linked trace. `make verify-logs` reports fresh format/severity/trace coverage for every running container without printing bodies or attribute values; quiet containers are reported separately. It also includes events and host timers that logged within the selected window (`MINUTES=`, default 15). The systemd/k3s journal is not collected.

**Sign-in.** `https://clickstack.<BASE_DOMAIN>` sits behind Google sign-in, then HyperDX's own login. HyperDX has no setting for its first account: `make clickstack-bootstrap` registers `CLICKSTACK_ADMIN_EMAIL` with `CLICKSTACK_ADMIN_PASSWORD` while no team exists, and HyperDX closes registration once one does (`make verify-platform` checks that it is closed). Read the password with `sops -d --extract '["stringData"]["CLICKSTACK_ADMIN_PASSWORD"]' platform/clickstack/secret.sops.yaml`. Invite further users from the UI.

**Keys and passwords** live in [`platform/clickstack/secret.sops.yaml`](../platform/clickstack/secret.sops.yaml) (`observability/clickstack-runtime-inputs`, `stringData`):

| Value | Used by | Purpose |
| --- | --- | --- |
| `CLICKSTACK_INGESTION_KEY` | The team's `apiKey` in MongoDB (written by `make clickstack-bootstrap`); the app's own logs (`hyperdx.secrets.HYPERDX_API_KEY`); a copy in `logging/clickstack-ingestion-key` ([file](../platform/otel/secret.sops.yaml)) that the collectors send | The only key ClickStack's collector accepts. `make check-secrets` fails if the two SOPS copies differ; `make verify-platform` fails if the live copy differs from the team key. |
| `CLICKSTACK_ADMIN_EMAIL`, `CLICKSTACK_ADMIN_PASSWORD` | `make clickstack-bootstrap` | The first HyperDX account |
| `MONGODB_PASSWORD`, `CLICKHOUSE_PASSWORD`, `CLICKHOUSE_APP_PASSWORD` | The chart's `hyperdx.secrets.*`, through `valuesFrom` | Internal logins, replacing the chart's public defaults. A render test fails if any chart secret is not taken from SOPS. |

Personal API keys in the HyperDX UI belong to users (for HyperDX's external API; `make clickstack-dashboards` uses the admin's, [apps](apps.md#dashboards)) and have nothing to do with ingestion. Don't rotate the ingestion key in the UI: that moves the team away from Git and the collectors get HTTP 401. Rotate it in SOPS ([operations](operations.md#secrets)).

**Alert destination.** `make clickstack-ntfy-webhook` creates or updates the reusable `ntfy: platform` Generic webhook in HyperDX from the live `console/notification-ntfy` platform topic. It sends JSON to `https://ntfy.sh/` with the topic, ClickStack title and message, a link back to ClickStack, high priority when an alert fires and normal priority when it recovers. The topic stays out of Git and command output; HyperDX stores it in the webhook body in MongoDB, where users who can manage ClickStack webhooks can read it. The command is idempotent, does not create an alert rule or send a test notification, and can be previewed with `DRY_RUN=true`. After a topic rotation, run it again. To use it, open Search, enter a query, choose **Alerts** and select `ntfy: platform` as the destination. The existing Alerts page lists saved alert rules; an empty list is expected until one is saved. HyperDX does not send an alert from the destination alone.

**Known limitation:** the chart writes `MONGO_URI`, including the MongoDB password, into the `clickstack-config` ConfigMap, and the ClickHouse passwords into the `ClickHouseCluster` resource. Anyone who can read ConfigMaps or those resources in `observability` can read them.

**Retention and storage:** telemetry expires after 30 days (`HYPERDX_OTEL_EXPORTER_TABLES_TTL`), ClickHouse's own system logs after 7 days (set by the chart in `clickhouse.cluster.spec.settings.extraConfig`; our values add vertical merges for `metric_log`, whose horizontal merges exceed the memory limit. HyperDX's ClickHouse page charts read `metric_log`, so keep it); `make verify-platform` checks both. MongoDB's data volume uses `local-path-retain`, so it outlives its claim; ClickHouse and Keeper use `local-path`. Helm uninstall leaves the operators' claims in place; delete them with `make destroy-data`.

**Chart quirks:** `hyperdx.frontendUrl` is ignored; the URL is `hyperdx.config.FRONTEND_URL`. ClickStack's collector image ignores the subchart's `config`; customise it with `global.otelCollector.customConfig`.

The DaemonSet also reads the host timers' output, `/var/log/swhurl-platform/<unit>.log` through its read-only `/hostfs` mount: backups arrive as service `swhurl-backup-mongodb` and dynamic DNS as `aws-dns-updater`, with `[ERROR]`/`[BAD]` lines at severity Error and `[WARN]` at Warn, which a HyperDX alert can match. Only lines written while the collector runs are read (`start_at: end`). Each unit rotates its own file before a run once it passes 5 MiB (to `<unit>.log.1`, replacing the previous one), so each keeps at most about 10 MiB; ClickStack holds the searchable 30 days. systemd itself rotates only the journal, and logrotate is not installed.

The collectors' `authorization` header reads `${env:CLICKSTACK_INGESTION_KEY}` as written: `platform-otel` does no Flux substitution, so nothing needs escaping.

## Reloader

Restarts a workload when a Secret it names changes, so rotations need no manual restart. It is opt-in (`secret.reloader.stakater.com/reload: "<secret>"` on the Deployment or DaemonSet) and scoped: it watches only the namespaces listed in [`platform/reloader/helmrelease.yaml`](../platform/reloader/helmrelease.yaml) (`ingress`, `logging`, `console`), with a Role in each and no cluster-wide Secret access. `make app-new --secret-keys` adds the app's namespace and `make app-remove` takes it out; for anything else, add the namespace before opting a workload in. ConfigMaps are ignored. Current opt-ins: oauth2-proxy (`oauth2-proxy-shared-secret`), both OTel collectors (`clickstack-ingestion-key`) and the console (`console-github`). Reloader restarts by patching a pod-template annotation; a later Helm upgrade may drop it and roll the pods once more, which is harmless.

## NATS

The event broker: apps publish events and subscribe to each other's ([events](apps.md#events)). One NATS server with JetStream in `messaging`, reachable only inside the cluster at `nats://nats.messaging.svc.cluster.local:4222`, and the JetStream controller (NACK) beside it.

- **Staging and production share the server.** They are kept apart by name and permission: an event's subject is `<env>.<app>.<event>`, and an app's user may publish only its own declared subjects in its own environment.
- **Who may do what** is [`users.yaml`](../platform/nats/users.yaml): one user per app instance that declares events, generated from the instances by `make app-events` and `make app-sync`. It holds public keys and permissions only, so it is plain YAML in Git. Flux upgrades the release when it changes and NATS reloads without restarting. Do not edit it by hand.
- **Credentials.** Every client signs in with an nkey: a key pair whose public half is in `users.yaml` and whose seed is a Secret. Each app's seed is in its own `events.sops.yaml`, generated and encrypted when the app first declares events and never printed. The controller's seed is [`secret-controller.sops.yaml`](../platform/nats/secret-controller.sops.yaml) and its public key `controller-nkey.pub`; it may use the JetStream API and nothing else. There is no admin login and no default account.
- **Streams and consumers** are manifests in each app instance (`events.yaml`), which the controller creates in NATS. It runs with `controlLoop`, so a stream changed or deleted by hand is put back. The chart's default permission to read every Secret in the cluster is removed.
- **What is kept.** Each publishing app has one stream, limited to seven days and 64Mi, oldest first out. The server may use 1Gi of a 2Gi `local-path` volume. Streams are **not backed up**: an event stream is not a system of record, and after a loss subscribers see only new events.
- **Replacing a key.** For an app: clear its events and declare them again (`make app-events APP= ENV= ARGS='--publish "" --subscribe ""'`, then the real lists); the second command makes a new key, and its subscriptions start again from the oldest retained event. For the controller: generate a pair with `tools/swhurl/apps/nkeys.py`, put the seed in `secret-controller.sops.yaml` and the public key in `controller-nkey.pub`, then `make app-sync`.
- **When a change takes effect.** Flux upgrades the release as soon as `users.yaml` changes, but the server reads its configuration from a mounted file that Kubernetes refreshes within a minute or two. Until then a new app is refused ("Authorization Violation") and the stack templates' helper retries.
- **Network.** A NetworkPolicy admits app namespaces and the controller to port 4222, nothing else.
- **Logs** from both pods reach ClickStack like any container's (namespace `messaging`).

## Alerts

### Notification expectations

Every managed app environment (staging and production), the console and the authorised app-template fixtures use the same lifecycle/health contract. For example, `demo-shop/staging deployed` means Helm completed the install/upgrade with its readiness checks; pushing the image pin to Git does not notify. Application error rates, latency, business events and other app-specific alerts belong to the operator's ClickStack rules.

| Event | Send when | Message and priority |
| --- | --- | --- |
| Deployed | Helm emits `InstallSucceeded` or `UpgradeSucceeded`, the release is current and Ready, and the requested image is running. The app's Flux unit need not be Ready: any push to `main` has it wait on its dependencies for a few seconds. A pass that holds the message logs why (`Holding <app>/<env> deployed: ...`) | `<app>/<env> deployed`; chart revision and requested image when the current release condition matches the event; normal |
| Rolled back | Helm emits `RollbackSucceeded` after a failed upgrade | `<app>/<env> rolled back`; high because the requested upgrade failed. A healthy remediated rollback can subsequently recover the availability incident; this does not claim the requested image deployed |
| Uninstalled | Helm emits `UninstallSucceeded`, or a previously observed managed release disappears after its unit is removed | `<app>/<env> uninstalled`; normal. The latter uses Helm's uninstall finalizer as completion evidence when namespace deletion has already removed the Events. Retained storage is named as retained; uninstall never means data was deleted |
| Deployment/removal failed | Helm emits `InstallFailed`, `UpgradeFailed`, `RollbackFailed` or `UninstallFailed`; or the app/console unit emits `ReconciliationFailed`, `BuildFailed`, `ValidationFailed`, `ArtifactFailed` or `PruneFailed` | `<app>/<env> deployment failed` or `uninstall failed`, reason and console link; high. Retry events for the same action are grouped |
| Unhealthy too long | Desired main workloads remain below their desired Ready replicas continuously for 5 minutes; console allowance 10 minutes | `<app>/<env> unhealthy`, counts and pod waiting reasons; high. The allowance includes startup/rollout time, rather than being added after it |
| Deployment stalled | The release is not current/Ready, its requested digest is not running or its Flux unit remains blocked beyond the same allowance, even when the old workload is healthy | `<app>/<env> deployment stalled`; high. Unrelated repository revisions do not restart the clock |
| Recovered | An alerted health/deployment incident clears for 2 continuous minutes | `<app>/<env> recovered`; normal. No recovery for an incident that was never notified. Missing/unreadable health evidence cannot establish recovery |

The console's title is `console`. A successful Git revert or promotion of an older digest is a Helm upgrade and receives a deployment notification; the distinct rollback title covers Helm's explicit rollback action. Routine no-op reconciliation, Helm tests, image pin pushes and pod restarts are silent. Desired scale-to-zero stops health tracking without a recovery message. Suspension suppresses stalled-deployment alerts but still checks the desired running workload's health. An action-failure/rollback message starts one incident and postpones related health reminders for an hour. Other continuing health incidents also remind hourly; recovery and recurrence start a new incident.

### Current notification behavior

**Checker:** [`notifications` package](../tools/swhurl/notifications/__init__.py) reads Kubernetes once a minute as `console/notification-check`, through the bounded `console-notifications` CronJob in the [console HelmRelease](../platform/console/helmrelease.yaml). It discovers instances from app Flux paths (`./apps/<app>/<env>`, plus authorised app-template fixtures), not a manually maintained app list. It reads release events, desired/current release status, main-workload replicas and running images. It never reads Secrets, pod logs or GitHub credentials and cannot modify workloads. Its dedicated [RBAC](../platform/console/notification-rbac.yaml) permits list-only discovery and get/patch of one ConfigMap. It hands every message about an app to the [notify service](#notify-service) as well, which knows the [channels](#notification-channels) of the app's team.

```mermaid
flowchart LR
    git[Git image or config change] --> flux[Flux and Helm]
    flux -->|lifecycle Events and release status| checker[Kubernetes notification checker]
    pods[Main workload replicas and images] --> checker
    checker -->|incident clocks and pending messages| state[notification-state ConfigMap]
    checker -->|JSON publish| ntfy[ntfy push notification]
    pods --> otel[OTel collectors]
    otel --> clickstack[ClickStack app-specific rules]
```

**State and delivery:** `console/notification-state` keeps incident clocks and a durable pending-message list across image updates and console restarts. Flux owns the ConfigMap metadata; the checker owns `data.state.json`, which is absent from its Git manifest. The ConfigMap uses Flux’s `ssa: Merge` policy and the checker patches with its own field manager so reconciliation preserves that field. Delivered-event deduplication and retired app identities expire after two hours; action-failure history retains the latest 16 signatures per instance. The state has a 700 KB ceiling and refuses overflow/corruption rather than discarding history. No database or backup is needed for this transient operational history; losing the ConfigMap creates a fresh baseline and loses existing clocks/pending messages. On its first run it does not replay historical events. Kubernetes Event retention still limits historical lifecycle coverage after an extended checker outage; workload health is checked from current state even without Events. Apps created and removed entirely between successful discovery passes can be missed.

Pending messages are checkpointed before posting and removed only after ntfy accepts them. Failed posts stop the job and retry on the next minute, with no secret URL or response body in the error. Delivery is **at least once**: a crash after acceptance but before the checkpoint can repeat a message. Acceptance proves ntfy received the post, not that a phone displayed it. Incident messages can be delayed while delivery is unavailable. The 50-second job timeout, `Forbid` concurrency, zero in-job retries and one successful/failed Job retained bound the checker workload; JSON logs reach ClickStack through the existing node collector. The image shares the console's pinned tag/digest and publication workflow. The platform ntfy destination is copied into [the checker's SOPS Secret](../platform/console/notification-secret.sops.yaml); every new Job reads the current values, and `make check-secrets` compares the two copies by hash. Topic rotation needs updating both the provider and checker copies ([Secrets](operations.md#secrets)).

**Infrastructure/source failures:** the [shared Flux failure Alert](../platform/alerts/alerts.yaml) still sends high-priority errors for infrastructure units, Git/Helm repositories, image repositories/policies and image automation. Infrastructure Kustomizations opt in with the `platform.swhurl.com/alert: failures` label; apps and `platform-console` stay out and are handled by the checker. A wiring test enforces that boundary. The expected image-policy messages `no tags in database` and `referenced ImageRepository does not exist` are excluded. These native Flux notifications have the controller's default [five-minute duplicate limit](https://fluxcd.io/flux/components/notification/events/#rate-limiting), separate from checker incident grouping.

**Coupling and heartbeat:** the checker runs from the console image and release, so a broken or unpublished console image stops its app/console messages. The host's `swhurl-notification-heartbeat.timer` checks the CronJob every five minutes; it alerts if the Job is missing or suspended, or has no success in the last 10 minutes, reminds hourly, and reports recovery after a successful Job. The host's `make verify-platform` check reports an inactive timer or a service run that did not succeed within 15 minutes. The heartbeat still depends on the host, Kubernetes API, the checker's existing `notification-ntfy` Secret and ntfy. If Kubernetes or the host is unreachable, it cannot send an alert; a checker failure can silence its own lifecycle notifications for up to 15 minutes plus one second. The ntfy destination exists in two Secrets, [`platform/alerts/secret-platform.sops.yaml`](../platform/alerts) and the checker's, kept equal by `make check-secrets`.

**Verify:** `make notifications-check` reads and evaluates without sending or saving; `make verify-platform` and the console's Platform checks fail if the checker is suspended, missing or has no successful job within five minutes. A successful pass establishes that the snapshot and outbox processing finished; it does not prove subscriber delivery. A snapshot read failure retains app clocks, cancels recovery stability and sends a monitor failure after five minutes if state storage and ntfy are still reachable. If the state cannot be read/written, the scheduler never runs, or the whole cluster is down, the checker cannot reliably notify about itself; verification shows it stale. The existing [console deployment checks](console.md#deploy-a-new-console) establish the running version.

**Remaining boundaries:** HTTP errors and failed console actions, expired tokens, dashboard-job failure, Validate/Publish workflow failure, stale backups, disk pressure, certificates and external availability need their own rules/checks. `/healthz` only answers `ok`; workload readiness does not exercise GitHub, sign-in or all application functions. External availability needs a monitor outside this cluster. App-specific ClickStack rules belong to the operator; remaining platform work is in [plan section 0](plan.md#0-where-this-paused-and-what-is-left).

- **One topic:** the Flux Alert, the checker, the heartbeat and ClickStack's `ntfy: platform` webhook all post to the same platform topic; each message carries its own priority (high for failures, normal for deployments and recoveries).
- **Subscribe** (once per phone or browser): install the ntfy app, then subscribe to the topic. The topic name is the credential (anyone who knows it can read and post), so it is only in SOPS; show the topic name in your own terminal with `SOPS_AGE_KEY_FILE=./age.agekey sops decrypt --extract '["stringData"]["address"]' platform/alerts/secret-platform.sops.yaml | sed 's|https://ntfy.sh/||; s|?.*||'` (from the repository root; the key file is not in Git).
- **Native Flux provider:** it posts event JSON through a generic provider and ntfy renders it with the template in the address. The checker uses [ntfy's JSON publish API](https://docs.ntfy.sh/publish/#publish-as-json) directly so lifecycle titles differ by action and identify the environment. Tapping opens the relevant app page, Activity for uninstall, or Platform for console/monitor failures.

### Notification channels

A team's apps can also notify the team. A **channel** is a named destination, and a team names the one channel that gets a copy of its apps' [lifecycle and health messages](#notification-expectations). For example, giving team `payments` its own ntfy topic:

```
make team-channel NAME=payments          # prompts for https://ntfy.sh/<topic>, not shown
```

On the console's [Teams page](console.md#use-it), **Channel** on the team's card does the same as a pull request; its **Generate** button makes a random topic in your browser and shows the link to subscribe with before you submit. The same command and button give a team that has a channel a new address, and `ARGS=--remove` or **Remove the channel** takes it away. The channel is named after the team, so there is nothing to name or pick. The parts underneath can also be handled one by one (`make channel-add`, `make team-edit --notify`, `make channel-remove`), for example to let two teams share a channel.

```mermaid
flowchart LR
    teams[apps/teams.yaml<br/>payments: notify payments-phone] -->|generated| routes[notify/notification-routes]
    add[make channel-add] -->|encrypts| secret[notify/channel-payments-phone]
    checker[Notification checker] -->|every message| platform[platform topic]
    checker -->|"copy, about orders/staging"| notify[Notify service]
    routes --> notify
    secret --> notify
    ns[App namespace<br/>team label] --> notify
    notify --> phone[payments-phone topic]
```

| Part | Where | Notes |
| --- | --- | --- |
| Channel | [`platform/notify/channel-<name>.sops.yaml`](../platform/notify): Secret `notify/channel-<name>` | Its transport is a plain label (`ntfy` is the only one); its address is the credential and is encrypted. One file per channel, so adding one decrypts nothing |
| Team's channel | `notify:` on the team in [`apps/teams.yaml`](../apps/teams.yaml) | One channel per team (the commands and rule `notify-channels` refuse a second), for every app of the team in every environment. A team without `notify` gets no copy. Two teams may name the same channel |
| Routes | [`platform/notify/routes.yaml`](../platform/notify/routes.yaml): ConfigMap `notify/notification-routes` | Generated from the channel files and the registry, names only: never edit it. `make app-sync` rewrites it and `make check-apps` (rule `notify-channels`) fails when it, a team's list or the channel files disagree |

**Channels are write-only.** Whoever creates a channel supplies its address and subscribes to it themselves; the platform stores it and never shows it again, in the tooling, the console or a log. A new app in a team gets the team's channels, so nothing secret changes hands. A lost or leaked address is replaced by running `make team-channel NAME=<team>` again (or **Channel** on the console); `make channel-remove` is refused while a team notifies the channel.

**What a channel receives.** A copy of each message about the team's apps, with the same title, priority and link. The platform topic still receives every message, and the console's own messages, the notification monitor and the [Flux failure Alert](#current-notification-behavior) go only there. The [notify service](#notify-service) reads each app's team from its namespace label and the channel addresses from the `notify` namespace, whose only other Secret is Helm's record of the service's own release (chart values, no credential).

**When a channel cannot be reached.** Its copy stays in the notify service's outbox and is retried for two hours, then dropped with an error in the service's logs; it never delays other channels or the platform topic. The same happens to a channel whose Secret is missing. `make verify-platform` (check `channels`) fails when the routes in the cluster are not Git's or a channel has no Secret, and warns about each team that owns apps and has no channel, since their alerts and messages reach nobody.

### Notify service

One small service, `notify` in the `notify` namespace, turns a message about an app into deliveries to the channels of the app's team. It runs the console's image ([`service.py`](../tools/swhurl/notifications/service.py)); its callers are the notification checker, the [apps themselves](apps.md#notifications) and ClickStack, for every app's [default alerts](apps.md#alerts).

```
POST http://notify.notify.svc.cluster.local:8080/v1/notify      Authorization: Bearer <token for the audience notify>
{"app": "orders", "env": "staging", "key": "event:abc@notify", "title": "orders/staging deployed",
 "message": "Flux UpgradeSucceeded ...", "priority": "normal", "click": "https://console.../apps/orders/staging"}
```

| Answer | Meaning |
| --- | --- |
| `202 {"channels": ["payments-phone"]}` | Saved in the outbox for these channels and delivered from there. An empty list is normal: the app's team notifies no channel, the app is unknown, or this `key` was already accepted |
| `400`, `413` | The body is not what the table below allows; the reason is in `error` |
| `401` | No token, or one Kubernetes does not vouch for with the audience `notify` |
| `403` | A real token of a caller that may not send (a person, the console, any service account that is not an app's own), or an app naming another app or environment |
| `429` | An app instance has had 30 messages accepted in the last hour |
| `503` | The token, the routes or the outbox could not be read or saved: nothing was accepted, so send it again |

| Field | Rule |
| --- | --- |
| `app`, `env` | The app the message is about; `env` is `staging` or `prod`. Its namespace's team label decides the channels. An app leaves them out: they come from its token, and naming anything else is refused |
| `key` | The same key is delivered once per channel, so a retry is harmless (remembered for two hours after delivery). Required from the checker; optional from an app, which gets a random one |
| `title`, `message` | A title of at most 250 characters; a message of at most 3,500. An app's title is delivered as `<app>/<env>: <title>`, so the team sees who sent it |
| `priority` | `normal` (default) or `high` |
| `click` | Optional `https://` link opened from the notification |

**Who a caller is.** The service asks Kubernetes (a `TokenReview`, restricted to the audience `notify`) whose token it was given; nothing in the body is trusted for identity. The checker's pod carries such a token as a file, valid ten minutes and for this audience only, so the Kubernetes API and every other service refuse it. Two kinds of caller are accepted. The checker (`PLATFORM_SENDERS`) says which app a message is about. An app's own service account, `<app>` in the namespace `<app>-<env>`, sends about itself only: every instance carries such a token ([apps](apps.md#notifications)). A NetworkPolicy admits only the checker's pods and app namespaces.

**Outbox.** An accepted message is one entry per channel in the `notify/notify-outbox` ConfigMap, written before 202 is answered and before anything is sent (Flux owns the object, the service owns its `state.json`, as for the [checker's state](#current-notification-behavior)). Delivery starts at once and is retried every 30 seconds; a new pod sends what the last one accepted. Delivery is at least once. The outbox holds at most 500 entries and answers 503 beyond that. Routes, the app's team and channel addresses are read when a message arrives and when it is sent, never cached, so a new channel works as soon as Flux has applied it.

**When the service is down.** The checker keeps each copy in its own outbox and offers it again every minute for two hours; the platform topic is unaffected because the checker posts there itself. After five minutes of refusals the operator gets `notify service unreachable` on the platform topic, a reminder every hour, and `notify service recovered` when a copy is accepted again. `make verify-platform` (check `notify-service`, also on the console's Platform page) fails when the Deployment is not running and warns when it runs another image than the console.

**Alerts from ClickStack** arrive at `POST /v1/alert` from the one HyperDX webhook `swhurl notify`. A HyperDX webhook cannot present a token that rotates, or a header, so it proves itself with a shared secret in its body: 64 random hex characters in [`secret-alerts.sops.yaml`](../platform/notify/secret-alerts.sops.yaml) (Secret `notify/notify-alerts`), which the service reads when an alert arrives and compares in constant time. A NetworkPolicy also admits only ClickStack's app pod for this. `make clickstack-notify-webhook` writes the webhook with that secret; HyperDX stores it in MongoDB, where anyone who can manage ClickStack webhooks can read it. The service takes the app from the alert's name (`swhurl:<app>:<preset>`) and the environment from the namespace the alert fired for, and refuses anything else with 400; a wrong or missing secret gets 401. To change the secret: in your own terminal `sops platform/notify/secret-alerts.sops.yaml`, then commit, push, `make flux-reconcile` and `make clickstack-notify-webhook`.

**Access.** Its ServiceAccount may list ConfigMaps and Secrets in `notify`, read and patch its outbox, read a namespace and create TokenReviews ([`rbac.yaml`](../platform/notify/rbac.yaml)). It holds no GitHub token and cannot change a workload. Logs reach ClickStack through the node collector: one line per accepted message (title, app, sender, number of channels), per delivery and per refusal, never a token or an address.

## Incident reviewer

The reviewer is removed from the platform configuration. Its prior collect-only run and the rollback evidence are recorded in [current state](current-state.md).

## Push webhook

GitHub calls `https://flux-webhook.<BASE_DOMAIN>/hook/<path>` on every push to this repository; the Flux `Receiver` then fetches `main` at once, instead of within the `GitRepository`'s 1-minute interval. Pushes to other branches, such as the console's `console/*` PR branches, are ignored (`resourceFilter`).

- **Token:** a random shared secret (`openssl rand -hex 32`) in [`platform/flux-webhook/secret.sops.yaml`](../platform/flux-webhook/secret.sops.yaml) and, with the same value, as the webhook's secret in the repository settings on GitHub. GitHub signs each request with it; the receiver rejects anything else. It is not a GitHub credential. Rotate it: [operations](operations.md#secrets).
- **Route:** the only platform Ingress without sign-in (GitHub cannot sign in); it serves only `/hook/` and only Flux's `webhook-receiver` Service (`make check` enforces both). A NetworkPolicy admits Traefik to cert-manager's HTTP-01 solver in `flux-system`, which Flux's own policy would block.
- **GitHub side:** Settings → Webhooks → `flux-webhook.<BASE_DOMAIN>`, content type `application/json`, events: push. Recent Deliveries shows each call and its response.
- If GitHub cannot reach it, nothing breaks: Flux still polls every minute.

## Image webhook

The repository of each app with automatic deploys calls `https://flux-webhook.<BASE_DOMAIN>/hook/<path>` when its workflow publishes an image to GHCR. The Flux `Receiver` `app-images` then scans the apps' image repositories at once, and the rest of the [deploy chain](apps.md#deploy-a-new-image) follows from events:

```text
image published → GitHub webhook (package events) → Receiver app-images
  → ImageRepository scan → ImagePolicy picks the tag → ImageUpdateAutomation commits the pin
  → push webhook → Flux applies
```

- **Scope:** the receiver can only start a scan of `ImageRepository` objects labelled `platform.swhurl.com/managed` (`make check` enforces it). One delivery scans all of them, one registry call each: the receiver sees only an object's metadata, so it cannot match a package to the image an object scans.
- **Fallback:** each `ImageRepository` and `ImageUpdateAutomation` still runs hourly (`interval: 1h`, written by `make app-new`), so a missed delivery delays a deploy by up to an hour instead of losing it.
- **Token:** a second random shared secret, in [`platform/flux-webhook/image-secret.sops.yaml`](../platform/flux-webhook/image-secret.sops.yaml), in every app repository's webhook, and as `IMAGE_WEBHOOK_TOKEN` in the console's Secret so [the console](console.md#github-tokens) can create the webhook on a new repository. `make check-secrets` fails unless the two SOPS copies match. It is separate from the push webhook's token: whoever holds it can start image scans and nothing else. The receiver's path is a hash of the token, so rotating it changes the URL too ([operations](operations.md#secrets)).
- **Route:** the same Ingress as the push webhook.
- **GitHub side:** one webhook per app repository, content type `application/json`, events: packages and registry packages. `make app-repo` and the console's **Start a new app** create it; `make app-hooks` creates or repoints it for every app (idempotent), which is how an app whose webhook could not be created at the time gets one. `make verify-platform` lists each app's webhook and its latest delivery.

## Certificates, ingress and storage

- **Issuers:** `selfsigned`, `letsencrypt-staging` and `letsencrypt-prod` (HTTP-01 through Traefik), plain manifests with no settings substituted. `infra-issuers` waits for cert-manager, so a fresh bootstrap cannot race its CRDs.
- **Traefik:** k3s owns the Traefik install; this repo owns only its `HelmChartConfig`: NodePorts `31514` (HTTP) and `30313` (HTTPS), and a permanent HTTP→HTTPS redirect set with `ports.web.redirections.entryPoint` (chart 38 silently ignores the older `redirectTo`; `make verify-platform` checks the redirect). Let's Encrypt follows the redirect, so HTTP-01 still works.
- **Storage classes:** `local-path` (k3s default, `Delete`: deleting a claim deletes its data) and `local-path-retain` (`Retain`: the volume and its directory under `/var/lib/rancher/k3s/storage` survive). Use `local-path-retain` for anything irreplaceable; the app generator does.

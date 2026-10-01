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
| [Console](console.md) | `platform-console` · [`platform/console`](../platform/console) | `console` | `console.` | app-template, image from this repo |
| [Image automation](apps.md#deploy-a-new-image) | `platform-image-automation` · [`platform/image-automation`](../platform/image-automation) | `flux-system` | — | Flux image controllers (from `make flux-install`) |
| [Alerts](#alerts) | `platform-alerts` · [`platform/alerts`](../platform/alerts) | `flux-system` | — | Flux notification Providers and Alerts, to ntfy.sh |
| [Push webhook](#push-webhook) | `platform-flux-webhook` · [`platform/flux-webhook`](../platform/flux-webhook) | `flux-system` | `flux-webhook.` | plain manifests (Flux `Receiver`) |

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

ClickStack stores logs, metrics and traces: the HyperDX app (UI and API), ClickStack's own OTel collector, MongoDB (team, users, sources) and ClickHouse (telemetry). MongoDB and ClickHouse are run by operators that `platform-clickstack-operators` installs first (MongoDB Community operator; ClickHouse operator with a Keeper). Two OTel collectors in `logging`, a per-node DaemonSet and a cluster Deployment, send node and cluster telemetry to ClickStack's collector (`clickstack-otel-collector.observability:4318`). The DaemonSet also accepts OTLP logs, metrics and traces from apps on the node IP (host networking, ports 4317 and 4318) and forwards them with the same key; apps send no key of their own ([apps](apps.md#telemetry)). The cluster Deployment's OTLP Service is the chart default and forwards nothing that apps send.

**Sign-in.** `https://clickstack.<BASE_DOMAIN>` sits behind Google sign-in, then HyperDX's own login. HyperDX has no setting for its first account: `make clickstack-bootstrap` registers `CLICKSTACK_ADMIN_EMAIL` with `CLICKSTACK_ADMIN_PASSWORD` while no team exists, and HyperDX closes registration once one does (`make verify-platform` checks that it is closed). Read the password with `sops -d --extract '["stringData"]["CLICKSTACK_ADMIN_PASSWORD"]' platform/clickstack/secret.sops.yaml`. Invite further users from the UI.

**Keys and passwords** live in [`platform/clickstack/secret.sops.yaml`](../platform/clickstack/secret.sops.yaml) (`observability/clickstack-runtime-inputs`, `stringData`):

| Value | Used by | Purpose |
| --- | --- | --- |
| `CLICKSTACK_INGESTION_KEY` | The team's `apiKey` in MongoDB (written by `make clickstack-bootstrap`); the app's own logs (`hyperdx.secrets.HYPERDX_API_KEY`); a copy in `logging/clickstack-ingestion-key` ([file](../platform/otel/secret.sops.yaml)) that the collectors send | The only key ClickStack's collector accepts. `make check-secrets` fails if the two SOPS copies differ; `make verify-platform` fails if the live copy differs from the team key. |
| `CLICKSTACK_ADMIN_EMAIL`, `CLICKSTACK_ADMIN_PASSWORD` | `make clickstack-bootstrap` | The first HyperDX account |
| `MONGODB_PASSWORD`, `CLICKHOUSE_PASSWORD`, `CLICKHOUSE_APP_PASSWORD` | The chart's `hyperdx.secrets.*`, through `valuesFrom` | Internal logins, replacing the chart's public defaults. A render test fails if any chart secret is not taken from SOPS. |

Personal API keys in the HyperDX UI belong to users (for HyperDX's external API) and have nothing to do with ingestion. Don't rotate the ingestion key in the UI: that moves the team away from Git and the collectors get HTTP 401. Rotate it in SOPS ([operations](operations.md#secrets)).

**Known limitation:** the chart writes `MONGO_URI`, including the MongoDB password, into the `clickstack-config` ConfigMap, and the ClickHouse passwords into the `ClickHouseCluster` resource. Anyone who can read ConfigMaps or those resources in `observability` can read them.

**Retention and storage:** telemetry expires after 30 days (`HYPERDX_OTEL_EXPORTER_TABLES_TTL`), ClickHouse's own system logs after 7 days (set by the chart in `clickhouse.cluster.spec.settings.extraConfig`; our values add vertical merges for `metric_log`, whose horizontal merges exceed the memory limit. HyperDX's ClickHouse page charts read `metric_log`, so keep it); `make verify-platform` checks both. MongoDB's data volume uses `local-path-retain`, so it outlives its claim; ClickHouse and Keeper use `local-path`. Helm uninstall leaves the operators' claims in place; delete them with `make destroy-data`.

**Chart quirks:** `hyperdx.frontendUrl` is ignored; the URL is `hyperdx.config.FRONTEND_URL`. ClickStack's collector image ignores the subchart's `config`; customise it with `global.otelCollector.customConfig`.

The DaemonSet also reads the host timers' output, `/var/log/swhurl-platform/<unit>.log` through its read-only `/hostfs` mount: backups arrive as service `swhurl-backup-mongodb` and dynamic DNS as `aws-dns-updater`, with `[ERROR]`/`[BAD]` lines at severity Error and `[WARN]` at Warn, which a HyperDX alert can match. Only lines written while the collector runs are read (`start_at: end`). Each unit rotates its own file before a run once it passes 5 MiB (to `<unit>.log.1`, replacing the previous one), so each keeps at most about 10 MiB; ClickStack holds the searchable 30 days. systemd itself rotates only the journal, and logrotate is not installed.

The collectors' `authorization` header reads `${env:CLICKSTACK_INGESTION_KEY}` as written: `platform-otel` does no Flux substitution, so nothing needs escaping.

## Reloader

Restarts a workload when a Secret it names changes, so rotations need no manual restart. It is opt-in (`secret.reloader.stakater.com/reload: "<secret>"` on the Deployment or DaemonSet) and scoped: it watches only the namespaces listed in [`platform/reloader/helmrelease.yaml`](../platform/reloader/helmrelease.yaml) (`ingress`, `logging`, `console`), with a Role in each and no cluster-wide Secret access. `make app-new --secret-keys` adds the app's namespace and `make app-remove` takes it out; for anything else, add the namespace before opting a workload in. ConfigMaps are ignored. Current opt-ins: oauth2-proxy (`oauth2-proxy-shared-secret`), both OTel collectors (`clickstack-ingestion-key`) and the console (`console-github`). Reloader restarts by patching a pod-template annotation; a later Helm upgrade may drop it and roll the pods once more, which is harmless.

## Alerts

Things now change without you (staging deploys, Renovate merges), so Flux sends a push notification to the [ntfy](https://ntfy.sh) app:

| Notification | When | Priority |
| --- | --- | --- |
| `<Kind> <name>: <reason>` 🚨 | A unit cannot apply or its workloads fail their health check (an app's 3-minute fail-fast included), a Git or Helm source cannot fetch, or image automation cannot scan or push | High |
| `Staging deploy` 🚀 | Image automation pushed a new app image to staging | Normal |

Tapping a notification opens the console. A unit that keeps failing notifies again on each retry (about every 10 minutes).

- **Subscribe** (once per phone or browser): install the ntfy app, then subscribe to the topic. The topic name is the credential (anyone who knows it can read and post), so it is only in SOPS; show the topic name in your own terminal with `SOPS_AGE_KEY_FILE=./age.agekey sops decrypt --extract '["stringData"]["address"]' platform/alerts/secret-failures.sops.yaml | sed 's|https://ntfy.sh/||; s|?.*||'` (from the repository root; the key file is not in Git).
- **How:** Flux has no ntfy provider, so the `generic` provider posts each event as JSON and ntfy renders it with the template in the address (`tpl=yes&t=…&m={{.message}}`). Rotate: [operations](operations.md#secrets).
- `make verify-platform` checks both alerts exist and point at existing providers; Flux reports no delivery status for them.

## Push webhook

GitHub calls `https://flux-webhook.<BASE_DOMAIN>/hook/<path>` on every push to this repository; the Flux `Receiver` then fetches `main` at once, instead of within the `GitRepository`'s 1-minute interval. Pushes to other branches, such as the console's `console/*` PR branches, are ignored (`resourceFilter`).

- **Token:** a random shared secret (`openssl rand -hex 32`) in [`platform/flux-webhook/secret.sops.yaml`](../platform/flux-webhook/secret.sops.yaml) and, with the same value, as the webhook's secret in the repository settings on GitHub. GitHub signs each request with it; the receiver rejects anything else. It is not a GitHub credential. Rotate it: [operations](operations.md#secrets).
- **Route:** the only platform Ingress without sign-in (GitHub cannot sign in); it serves only `/hook/` and only Flux's `webhook-receiver` Service (`make check` enforces both). A NetworkPolicy admits Traefik to cert-manager's HTTP-01 solver in `flux-system`, which Flux's own policy would block.
- **GitHub side:** Settings → Webhooks → `flux-webhook.<BASE_DOMAIN>`, content type `application/json`, events: push. Recent Deliveries shows each call and its response.
- If GitHub cannot reach it, nothing breaks: Flux still polls every minute.

## Certificates, ingress and storage

- **Issuers:** `selfsigned`, `letsencrypt-staging` and `letsencrypt-prod` (HTTP-01 through Traefik), plain manifests with no settings substituted. `infra-issuers` waits for cert-manager, so a fresh bootstrap cannot race its CRDs.
- **Traefik:** k3s owns the Traefik install; this repo owns only its `HelmChartConfig`: NodePorts `31514` (HTTP) and `30313` (HTTPS), and a permanent HTTP→HTTPS redirect set with `ports.web.redirections.entryPoint` (chart 38 silently ignores the older `redirectTo`; `make verify-platform` checks the redirect). Let's Encrypt follows the redirect, so HTTP-01 still works.
- **Storage classes:** `local-path` (k3s default, `Delete`: deleting a claim deletes its data) and `local-path-retain` (`Retain`: the volume and its directory under `/var/lib/rancher/k3s/storage` survive). Use `local-path-retain` for anything irreplaceable; the app generator does.

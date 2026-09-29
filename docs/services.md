# Services

The shared services every app can rely on. Each is its own Flux unit ([architecture](architecture.md#flux-units)); operating them is covered in [operations](operations.md).

| Service | Flux unit · path | Namespace | Host | Chart |
| --- | --- | --- | --- | --- |
| Namespaces, storage classes | `infra-base` · [`infra/base`](../infra/base) | — | — | — |
| cert-manager | `infra-cert-manager` · [`infra/cert-manager`](../infra/cert-manager) | `cert-manager` | — | cert-manager v1.19.3 |
| ClusterIssuers | `infra-issuers` · [`infra/issuers`](../infra/issuers) | — | — | plain manifests |
| Traefik settings | `infra-traefik` · [`infra/traefik`](../infra/traefik) | `kube-system` | — | k3s packaged (chart 38, Traefik 3.6) |
| Sign-in (oauth2-proxy) | `platform-oauth2-proxy` · [`platform/oauth2-proxy`](../platform/oauth2-proxy) | `ingress` | `oauth.` | oauth2-proxy 10.1.3 |
| ClickStack | `platform-clickstack` · [`platform/clickstack`](../platform/clickstack) | `observability` | `clickstack.` | clickstack 1.1.1 |
| OTel collectors | `platform-otel` · [`platform/otel`](../platform/otel) | `logging` | — | opentelemetry-collector 0.145.0 |
| Reloader | `platform-reloader` · [`platform/reloader`](../platform/reloader) | `platform-system` | — | reloader 2.2.17 |

Hosts are under `BASE_DOMAIN` (`homelab.swhurl.com`).

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

ClickStack stores logs, metrics and traces: the HyperDX app (UI and API), ClickStack's own OTel collector, MongoDB (team, users, sources) and ClickHouse (telemetry). MongoDB and ClickHouse are run by operators that `platform-clickstack-operators` installs first (MongoDB Community operator; ClickHouse operator with a Keeper). Two OTel collectors in `logging`, a per-node DaemonSet and a cluster Deployment, send node and cluster telemetry to ClickStack's collector (`clickstack-otel-collector.observability:4318`).

**Sign-in.** `https://clickstack.<BASE_DOMAIN>` sits behind Google sign-in, then HyperDX's own login. HyperDX has no setting for its first account: `make clickstack-bootstrap` registers `CLICKSTACK_ADMIN_EMAIL` with `CLICKSTACK_ADMIN_PASSWORD` while no team exists, and HyperDX closes registration once one does (`make verify-platform` checks that it is closed). Read the password with `sops -d --extract '["stringData"]["CLICKSTACK_ADMIN_PASSWORD"]' platform/clickstack/secret.sops.yaml`. Invite further users from the UI.

**Keys and passwords** live in [`platform/clickstack/secret.sops.yaml`](../platform/clickstack/secret.sops.yaml) (`observability/clickstack-runtime-inputs`, `stringData`):

| Value | Used by | Purpose |
| --- | --- | --- |
| `CLICKSTACK_INGESTION_KEY` | The team's `apiKey` in MongoDB (written by `make clickstack-bootstrap`); the app's own logs (`hyperdx.secrets.HYPERDX_API_KEY`); a copy in `logging/clickstack-ingestion-key` ([file](../platform/otel/secret.sops.yaml)) that the collectors send | The only key ClickStack's collector accepts. `make check-secrets` fails if the two SOPS copies differ; `make verify-platform` fails if the live copy differs from the team key. |
| `CLICKSTACK_ADMIN_EMAIL`, `CLICKSTACK_ADMIN_PASSWORD` | `make clickstack-bootstrap` | The first HyperDX account |
| `MONGODB_PASSWORD`, `CLICKHOUSE_PASSWORD`, `CLICKHOUSE_APP_PASSWORD` | The chart's `hyperdx.secrets.*`, through `valuesFrom` | Internal logins, replacing the chart's public defaults. A render test fails if any chart secret is not taken from SOPS. |

Personal API keys in the HyperDX UI belong to users (for HyperDX's external API) and have nothing to do with ingestion. Don't rotate the ingestion key in the UI: that moves the team away from Git and the collectors get HTTP 401. Rotate it in SOPS ([operations](operations.md#secrets)).

**Known limitation:** the chart writes `MONGO_URI`, including the MongoDB password, into the `clickstack-config` ConfigMap, and the ClickHouse passwords into the `ClickHouseCluster` resource. Anyone who can read ConfigMaps or those resources in `observability` can read them.

**Retention and storage:** telemetry expires after 30 days (`HYPERDX_OTEL_EXPORTER_TABLES_TTL`), ClickHouse's own system logs after 7 days (set by the chart in `clickhouse.cluster.spec.settings.extraConfig`); `make verify-platform` checks both. MongoDB's data volume uses `local-path-retain`, so it outlives its claim; ClickHouse and Keeper use `local-path`. Helm uninstall leaves the operators' claims in place; delete them with `make destroy-data`.

**Chart quirks:** `hyperdx.frontendUrl` is ignored; the URL is `hyperdx.config.FRONTEND_URL`. ClickStack's collector image ignores the subchart's `config`; customise it with `global.otelCollector.customConfig`.

The OTel collectors need their Flux unit's substitution even though they use no settings: it turns `$${env:CLICKSTACK_INGESTION_KEY}` into the collector's `${env:...}` reference.

## Reloader

Restarts a workload when a Secret it names changes, so rotations need no manual restart. It is opt-in (`secret.reloader.stakater.com/reload: "<secret>"` on the Deployment or DaemonSet) and scoped: it watches only the namespaces listed in [`platform/reloader/helmrelease.yaml`](../platform/reloader/helmrelease.yaml) (`ingress`, `logging`), with a Role in each and no cluster-wide Secret access. `make app-new --secret-keys` adds the app's namespace; for anything else, add the namespace before opting a workload in. ConfigMaps are ignored. Current opt-ins: oauth2-proxy (`oauth2-proxy-shared-secret`) and both OTel collectors (`hyperdx-secret`). Reloader restarts by patching a pod-template annotation; a later Helm upgrade may drop it and roll the pods once more, which is harmless.

## Certificates, ingress and storage

- **Issuers:** `selfsigned`, `letsencrypt-staging` and `letsencrypt-prod` (HTTP-01 through Traefik), plain manifests with no settings substituted. `infra-issuers` waits for cert-manager, so a fresh bootstrap cannot race its CRDs.
- **Traefik:** k3s owns the Traefik install; this repo owns only its `HelmChartConfig`: NodePorts `31514` (HTTP) and `30313` (HTTPS), and a permanent HTTP→HTTPS redirect set with `ports.web.redirections.entryPoint` (chart 38 silently ignores the older `redirectTo`; `make verify-platform` checks the redirect). Let's Encrypt follows the redirect, so HTTP-01 still works.
- **Storage classes:** `local-path` (k3s default, `Delete`: deleting a claim deletes its data) and `local-path-retain` (`Retain`: the volume and its directory under `/var/lib/rancher/k3s/storage` survive). Use `local-path-retain` for anything irreplaceable; the app generator does.

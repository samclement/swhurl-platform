# Platform Services

## Overview

The platform is composed from shared infrastructure plus shared application services:

- infrastructure: cert-manager, ClusterIssuers, Traefik configuration, MinIO, and shared namespaces
- platform services: shared oauth2-proxy, ClickStack, OpenTelemetry collectors, and Reloader

Each service is its own Flux unit, defined in [`clusters/home/infrastructure.yaml`](../clusters/home/infrastructure.yaml) and [`clusters/home/platform.yaml`](../clusters/home/platform.yaml); see the [ownership map](architecture.md#current-reconciliation-ownership).

## Service Architecture

The active stack is layered like this:

1. cert-manager and ClusterIssuers provide TLS automation
2. packaged k3s Traefik provides ingress
3. shared oauth2-proxy provides cluster-wide edge authentication middleware
4. ClickStack provides observability storage and UI
5. OTel collectors forward logs and metrics into ClickStack
6. MinIO provides in-cluster object storage

## Shared Namespaces

Current shared namespaces come from [`infrastructure/namespaces/namespaces.yaml`](../infrastructure/namespaces/namespaces.yaml) and service manifests:

- `cert-manager`
- `ingress`
- `logging`
- `observability`
- `storage`
- `flux-system`

## Services

### cert-manager

- Path: [`infrastructure/cert-manager/base`](../infrastructure/cert-manager/base)
- Namespace: `cert-manager`
- Purpose: certificate controller and CRDs

### ClusterIssuers

- Path: [`infrastructure/cert-manager/issuers`](../infrastructure/cert-manager/issuers)
- Scope: cluster-wide
- Modes: `selfsigned`, `letsencrypt-staging`, `letsencrypt-prod`
- Active selector: `flux-system/platform-settings.CERT_ISSUER`

### Traefik

- Path: [`infrastructure/ingress-traefik/base`](../infrastructure/ingress-traefik/base)
- Provider: packaged k3s Traefik with HelmChartConfig overrides
- Notes: NodePorts are pinned to `31514` for HTTP and `30313` for HTTPS. HTTP permanently redirects to HTTPS (`ports.web.redirections.entryPoint`); sign-in needs HTTPS because oauth2-proxy cookies are `Secure`

### MinIO

- Path: [`infrastructure/storage/minio/base`](../infrastructure/storage/minio/base)
- Namespace: `storage`
- Hosts: `minio.homelab.swhurl.com`, `minio-console.homelab.swhurl.com`

### Storage classes

- `local-path` (k3s default): `Delete` reclaim; deleting a claim deletes its data. Used by existing volumes.
- `local-path-retain` ([`infrastructure/storage/local-path-retain`](../infrastructure/storage/local-path-retain)): `Retain` reclaim; deleting a claim leaves the PV `Released` and its directory under `/var/lib/rancher/k3s/storage`. Use it for new irreplaceable data.

### Shared oauth2-proxy

- Path: [`platform-services/oauth2-proxy/base`](../platform-services/oauth2-proxy/base)
- Namespace: `ingress`
- Release: `oauth2-proxy-shared`
- Runtime inputs: `client-id`, `client-secret`, and `cookie-secret` in the service-local SOPS Secret; `OAUTH_HOST` in `platform-settings`
- Shared middleware: `ingress/oauth-auth-shared`
- Approved sign-in: `sam@swhurl.com`, declared in `values.authenticatedEmailsFile.restricted_access`. The chart mounts this ConfigMap as its authenticated-email file. Both the command-line wildcard and the chart default `email_domains = ["*"]` are removed; `config.configFile` explicitly sets `email_domains = []`.
- The chart checksums config and approved-email changes to trigger rollout. Existing sessions for disallowed emails are rejected by oauth2-proxy session validation after rollout; the cookie key is unchanged. A real approved/denied Google login remains an operator acceptance check.
- Shared cookies cover `.homelab.swhurl.com`. Public or untrusted apps must use a domain outside that scope or an isolated proxy with a host-only cookie. MinIO and ClickStack retain their application authentication; this change restricts routes using the shared middleware.

### ClickStack

- Path: [`platform-services/clickstack/base`](../platform-services/clickstack/base)
- Namespace: `observability`
- Release: `clickstack`
- Runtime input: `CLICKSTACK_API_KEY`
- Host: `clickstack.homelab.swhurl.com`
- Retention: telemetry 30 days (collector image default, checked by `make verify-platform`); ClickHouse system logs 7 days (Git-managed `config.d` override); PVCs kept on Helm uninstall. Details in the [component README](../platform-services/clickstack/base/README.md#retention).

### Reloader

- Path: [`platform-services/reloader/base`](../platform-services/reloader/base)
- Namespace: `platform-system`; Flux unit `homelab-reloader`
- Restarts a workload when a Secret it names changes. Opt-in per workload (`secret.reloader.stakater.com/reload: "<secret>"`), scoped to `ingress` and `logging` with namespaced Roles only; ConfigMaps ignored.
- Opted in: `oauth2-proxy-shared` → `oauth2-proxy-shared-secret`; both OTel collectors → `hyperdx-secret`. Details in the [component README](../platform-services/reloader/base/README.md).

### OpenTelemetry collectors

- Path: [`platform-services/otel/base`](../platform-services/otel/base)
- Namespace: `logging`
- Releases:
  - `otel-k8s-daemonset`
  - `otel-k8s-cluster`
- Runtime input: `HYPERDX_API_KEY` via `logging/hyperdx-secret`

### Runtime secret targets

Each Git-managed SOPS manifest is the final Kubernetes Secret applied by its service unit (`homelab-auth`, `homelab-clickstack` or `homelab-otel`):

- [`oauth2-proxy-shared-secret`](../platform-services/oauth2-proxy/base/secret-oauth2-proxy-shared.sops.yaml) in `ingress`
- [`clickstack-runtime-inputs`](../platform-services/clickstack/base/secret-clickstack-runtime-inputs.sops.yaml) in `observability`
- [`hyperdx-secret`](../platform-services/otel/base/secret-hyperdx.sops.yaml) in `logging`

`CLICKSTACK_API_KEY` is the ClickStack chart bootstrap/app key. The live team ingestion key is held in ClickStack MongoDB after first-login setup; the standalone OTel collectors use `HYPERDX_API_KEY`, which must match that live ingestion key. These keys may match on a fresh install but are separate in steady state. Kubernetes `data.HYPERDX_API_KEY` must have exactly one base64 layer around the actual ingestion token. The verifier checks decoded bytes and never prints keys; extra encoding is an error. It requires exactly one distinct nonempty ClickStack team key. After a change, restart collectors and check logs plus newly received telemetry.

## Getting Started

### Update service secrets

Edit the affected SOPS target Secret, commit, push, then reconcile:

```bash
SOPS_AGE_KEY_FILE=./age.agekey sops platform-services/oauth2-proxy/base/secret-oauth2-proxy-shared.sops.yaml
git add platform-services/oauth2-proxy/base/secret-oauth2-proxy-shared.sops.yaml
git commit -m "runtime-inputs: update platform secrets"
git push
make runtime-inputs-sync
```

Reloader restarts `oauth2-proxy-shared` once the changed Secret is applied.

If ClickStack ingestion credentials changed, use:

```bash
make runtime-inputs-refresh-otel
```

`make runtime-inputs-sync` applies the changed Secret, and Reloader then restarts the workloads that consume it (oauth2-proxy, both OTel collectors). `make runtime-inputs-refresh-otel` still exists as an explicit fallback: it also waits for the Secret and restarts the collectors itself.

### Change platform certificate mode

Edit the Git-tracked setting with a helper target:

```bash
make platform-certs-staging
# or
make platform-certs-prod
```

Then commit, push, and reconcile:

```bash
git add clusters/home/flux-system/sources/configmap-platform-settings.yaml
git commit -m "platform: change certificate issuer mode"
git push
make flux-reconcile
```

### Verify service state

```bash
make verify-platform
```

The current verifier can print both key values when the ingestion-key comparison fails. Until its output is made safe, do not run it in shared logs; `make install` invokes it by default.

## Caveats

- `config.env` is not a full service configuration source. Several hosts remain hardcoded in manifests, including ClickStack, MinIO, and the sample app domains.
- `OAUTH_HOST` is runtime-configurable, but the shared oauth2-proxy `cookie-domain` and `whitelist-domain` are still hardcoded to `.homelab.swhurl.com` in [`platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml`](../platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml).
- Secret changes restart only workloads opted in to Reloader in a watched namespace. A new consumer of a rotated Secret needs the annotation (and its namespace in `reloader.namespaces`), otherwise restart it manually.
- ClickStack requires first-time setup in the UI after deployment.
- The platform assumes packaged k3s Traefik and `local-path` storage remain available.
- Only services that use Flux substitutions react to `CERT_ISSUER`. Hostnames and several service manifests still need manual edits if the domain model changes.

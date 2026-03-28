# Platform Services

## Overview

The platform is composed from shared infrastructure plus shared application services:

- infrastructure: cert-manager, ClusterIssuers, Traefik configuration, MinIO, and shared namespaces
- platform services: shared oauth2-proxy, ClickStack, and OpenTelemetry collectors

These layers are reconciled through:

- [`infrastructure/overlays/home`](../infrastructure/overlays/home)
- [`platform-services/overlays/home`](../platform-services/overlays/home)

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
- Notes: NodePorts are pinned to `31514` for HTTP and `30313` for HTTPS

### MinIO

- Path: [`infrastructure/storage/minio/base`](../infrastructure/storage/minio/base)
- Namespace: `storage`
- Hosts: `minio.homelab.swhurl.com`, `minio-console.homelab.swhurl.com`

### Shared oauth2-proxy

- Path: [`platform-services/oauth2-proxy/base`](../platform-services/oauth2-proxy/base)
- Namespace: `ingress`
- Release: `oauth2-proxy-shared`
- Runtime inputs: `SHARED_OIDC_CLIENT_ID`, `SHARED_OIDC_CLIENT_SECRET`, `OAUTH_COOKIE_SECRET`, `OAUTH_HOST`
- Shared middleware: `ingress/oauth-auth-shared`

### ClickStack

- Path: [`platform-services/clickstack/base`](../platform-services/clickstack/base)
- Namespace: `observability`
- Release: `clickstack`
- Runtime input: `CLICKSTACK_API_KEY`
- Host: `clickstack.homelab.swhurl.com`

### OpenTelemetry collectors

- Path: [`platform-services/otel/base`](../platform-services/otel/base)
- Namespace: `logging`
- Releases:
  - `otel-k8s-daemonset`
  - `otel-k8s-cluster`
- Runtime input: `CLICKSTACK_INGESTION_KEY` via `logging/hyperdx-secret`

### Runtime secret targets

- Path: [`platform-services/runtime-inputs`](../platform-services/runtime-inputs)
- Source secret: `flux-system/platform-runtime-inputs`
- Purpose: bridge Git-managed SOPS inputs into runtime Kubernetes secrets used by shared services

## Getting Started

### Update service secrets

Edit the SOPS source secret, commit, push, then reconcile:

```bash
sops clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
git add clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
git commit -m "runtime-inputs: update platform secrets"
git push
make runtime-inputs-sync
```

If ClickStack ingestion credentials changed, use:

```bash
make runtime-inputs-refresh-otel
```

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

## Caveats

- `config.env` is not a full service configuration source. Several hosts remain hardcoded in manifests, including ClickStack, MinIO, and the sample app domains.
- `OAUTH_HOST` is runtime-configurable, but the shared oauth2-proxy `cookie-domain` and `whitelist-domain` are still hardcoded to `.homelab.swhurl.com` in [`platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml`](../platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml).
- oauth2-proxy credential changes do not currently trigger an automatic rollout restart. After updating shared oauth credentials, restart `deployment/oauth2-proxy-shared` in `ingress`.
- OTel collectors do not hot-reload the `HYPERDX_API_KEY` secret. Use `make runtime-inputs-refresh-otel` after ingestion key changes.
- ClickStack requires first-time setup in the UI after deployment.
- The platform assumes packaged k3s Traefik and `local-path` storage remain available.
- Only services that use Flux substitutions react to `CERT_ISSUER`. Hostnames and several service manifests still need manual edits if the domain model changes.

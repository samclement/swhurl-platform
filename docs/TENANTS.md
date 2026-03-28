# Tenants

## Overview

The tenant model is split into two layers:

- [`tenants/app-envs`](../tenants/app-envs): shared landing zones for tenant environments
- [`tenants/apps`](../tenants/apps): application manifests and overlays

The active example app is reconciled separately through [`clusters/home/app-example.yaml`](../clusters/home/app-example.yaml), which points to [`tenants/apps/example`](../tenants/apps/example).

## Current Architecture

### Landing zones

Current tenant namespaces are:

- `apps-staging`
- `apps-prod`

They are defined in:

- [`tenants/app-envs/staging`](../tenants/app-envs/staging)
- [`tenants/app-envs/prod`](../tenants/app-envs/prod)

### Example application

The example app uses:

- base manifests in [`tenants/apps/example/base`](../tenants/apps/example/base)
- environment overlays in [`tenants/apps/example/overlays/staging`](../tenants/apps/example/overlays/staging) and [`tenants/apps/example/overlays/prod`](../tenants/apps/example/overlays/prod)

Current hosts:

- staging: `staging-hello.homelab.swhurl.com`
- prod: `hello.homelab.swhurl.com`

Shared auth is applied at the ingress layer through the Traefik middleware reference:

- `ingress-oauth-auth-shared@kubernetescrd`

## Getting Started

### Add a new landing zone

1. Add a namespace manifest under `tenants/app-envs/<env>`.
2. Add that path to the relevant tenant Kustomization if needed.
3. Reconcile with `make flux-reconcile`.

### Add a new app

1. Create `tenants/apps/<app>/base` with the base workload manifests.
2. Add environment overlays under `tenants/apps/<app>/overlays/...`.
3. Create a Flux Kustomization under `clusters/home/app-<app>.yaml` that points to `./tenants/apps/<app>`.
4. If the app has encrypted manifests, add `spec.decryption.secretRef.name: sops-age` to that app-level Flux Kustomization.
5. Commit, push, and run `make flux-reconcile`.

### Reuse shared edge authentication

For Traefik ingress auth, reference the shared middleware from the app ingress:

```yaml
traefik.ingress.kubernetes.io/router.middlewares: ingress-oauth-auth-shared@kubernetescrd
```

The example app overlays are the current reference implementation.

## Current Constraints

- The tenants landing-zone layer only creates namespaces. App deployment is handled by separate Flux Kustomizations.
- There is no tenant scaffolding command or generator in the repo.
- There is no generic tenant contract document for quotas, network policies, or RBAC defaults.
- App hostnames are currently hardcoded in the example overlays, not derived from `config.env`.

## Caveats

- The example app base defaults to staging-oriented values, but both staging and prod overlays currently override the certificate issuer to `letsencrypt-prod`.
- Shared auth depends on the `ingress` namespace middleware created by the shared oauth2-proxy service. If that middleware is absent, tenant ingresses that reference it will fail.
- The current tenant model assumes one cluster with shared platform services and environment-specific namespaces rather than hard isolation between tenants.
- Additional tenant apps require explicit Flux wiring in `clusters/home`; adding manifests under `tenants/apps` alone does not deploy them.

# Toggle Platform Certificate Issuer (Staging vs Prod)

This runbook covers homelab platform components that share one environment
(no separate platform staging/prod overlays) while user apps can still have
separate staging/prod overlays.

Platform cert intent is controlled by one variable:
- `PLATFORM_CLUSTER_ISSUER=letsencrypt-staging|letsencrypt-prod`

Both concrete ClusterIssuers stay deployed:
- `letsencrypt-staging`
- `letsencrypt-prod`

## Prerequisites

1. DNS records for platform hosts resolve to your ingress endpoint.
2. `ACME_EMAIL` is set in `profiles/secrets.env`.
3. `profiles/secrets.env` / profile has the desired `PLATFORM_CLUSTER_ISSUER` value.

## Switch Platform Components to Production Certificates

Set:

```bash
PLATFORM_CLUSTER_ISSUER=letsencrypt-prod
```

in your active config/profile, then run:

```bash
make runtime-inputs-sync
make flux-reconcile
```

## Roll Back Platform Components to Staging Certificates

Set:

```bash
PLATFORM_CLUSTER_ISSUER=letsencrypt-staging
```

then run:

```bash
make runtime-inputs-sync
make flux-reconcile
```

## Verify Effective Platform Issuer

```bash
flux get kustomizations -n flux-system

kubectl get ingress -A \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,ISSUER:.metadata.annotations.cert-manager\\.io/cluster-issuer

kubectl get certificate -A
```

Expected:
- Platform ingresses (`hubble-ui`, `oauth2-proxy`, `clickstack`, `minio`, `minio-console`) show the configured `PLATFORM_CLUSTER_ISSUER`.
- New/renewed certificates are issued by the selected ClusterIssuer.

## Note on User App Cert/URL Intent

User app deployment intent is runtime-input driven:
- `APP_HOST`
- `APP_NAMESPACE`
- `APP_CLUSTER_ISSUER`

This app path remains independent from the single platform issuer toggle.

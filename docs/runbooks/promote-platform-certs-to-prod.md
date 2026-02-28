# Promote Cluster Certificate Mode To Production

This runbook covers cert issuer mode switching for shared infrastructure and platform-services:
- Infrastructure: Hubble UI / MinIO ingresses
- Platform-services: oauth2-proxy / clickstack ingresses

Mode is path-selected in Flux CRDs (no runtime-input issuer toggles):
- `clusters/home/infrastructure.yaml`
- `clusters/home/platform.yaml`

Both concrete ClusterIssuers stay deployed in the cluster:
- `letsencrypt-staging`
- `letsencrypt-prod`

## Prerequisites

1. DNS records for platform hosts resolve to your ingress endpoint.
2. You are reconciling from `clusters/home/*.yaml`.

## Switch To Production Certificates

Use:

```bash
make platform-certs CERT_ENV=prod
```

This updates:
- `clusters/home/infrastructure.yaml -> ./infrastructure/overlays/home-letsencrypt-prod`
- `clusters/home/platform.yaml -> ./platform-services/overlays/home-letsencrypt-prod`

## Roll Back To Staging Certificates

```bash
make platform-certs CERT_ENV=staging
```

## Verify Effective Issuer

```bash
flux get kustomizations -n flux-system

kubectl get ingress -n ingress oauth2-proxy \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,ISSUER:.metadata.annotations.cert-manager\\.io/cluster-issuer

kubectl get ingress -n observability clickstack-app-ingress \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,ISSUER:.metadata.annotations.cert-manager\\.io/cluster-issuer

kubectl get ingress -n storage minio \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,ISSUER:.metadata.annotations.cert-manager\\.io/cluster-issuer

kubectl get ingress -n storage minio-console \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,ISSUER:.metadata.annotations.cert-manager\\.io/cluster-issuer

kubectl get ingress -n kube-system hubble-ui \
  -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name,ISSUER:.metadata.annotations.cert-manager\\.io/cluster-issuer
```

Expected:
- With staging paths (`.../overlays/home`): `letsencrypt-staging`
- With prod paths (`.../overlays/home-letsencrypt-prod`): `letsencrypt-prod`

## Notes

- App URL/issuer test mode is separate and controlled by `make app-test APP_ENV=... LE_ENV=...`
  (updates `clusters/home/tenants.yaml` path).

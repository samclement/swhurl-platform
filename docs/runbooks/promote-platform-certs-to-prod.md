# Promote Cluster Certificate Mode To Production

This runbook covers cert issuer mode switching for shared infrastructure and platform-services:
- Infrastructure: Hubble UI / MinIO ingresses
- Platform-services: oauth2-proxy / clickstack ingresses

Mode is Git-managed in:
- `clusters/home/flux-system/sources/configmap-platform-settings.yaml`
- key: `CERT_ISSUER` (`letsencrypt-staging|letsencrypt-prod`)

Both concrete ClusterIssuers stay deployed in the cluster:
- `letsencrypt-staging`
- `letsencrypt-prod`

## Prerequisites

1. DNS records for platform hosts resolve to your ingress endpoint.
2. You are reconciling from `clusters/home/*.yaml`.

## Switch To Production Certificates

Use:

```bash
make platform-certs-prod
```

This updates `CERT_ISSUER=letsencrypt-prod` in `clusters/home/flux-system/sources/configmap-platform-settings.yaml`.

Then:
1. Commit
2. Push
3. Reconcile

Apply changes:

```bash
git add clusters/home/flux-system/sources/configmap-platform-settings.yaml
git commit -m "platform: switch CERT_ISSUER to letsencrypt-prod"
git push
make flux-reconcile
```

## Roll Back To Staging Certificates

```bash
make platform-certs-staging
git add clusters/home/flux-system/sources/configmap-platform-settings.yaml
git commit -m "platform: switch CERT_ISSUER to letsencrypt-staging"
git push
make flux-reconcile
```

## Verify Effective Issuer

```bash
flux get kustomizations -n flux-system

kubectl -n flux-system get configmap platform-settings \
  -o custom-columns=NAME:.metadata.name,CERT_ISSUER:.data.CERT_ISSUER

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
- `platform-settings.CERT_ISSUER=letsencrypt-staging` -> platform ingresses use `letsencrypt-staging`
- `platform-settings.CERT_ISSUER=letsencrypt-prod` -> platform ingresses use `letsencrypt-prod`

## Notes

- Example app overlays are fixed at `clusters/home/app-example.yaml -> ./tenants/apps/example`.
- Both example app overlays (`staging` and `prod`) use `letsencrypt-prod`.

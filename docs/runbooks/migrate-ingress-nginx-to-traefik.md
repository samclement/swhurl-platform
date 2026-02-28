# Runbook: Migrate Ingress NGINX to Traefik

This runbook switches Flux stack ingress ownership from `ingress-nginx` to `ingress-traefik`.

## Preconditions

1. Cluster is healthy:

```bash
make flux-reconcile
./scripts/91_verify_platform_state.sh
```

2. Backup/snapshot exists.

## Migration

1. Update shared infrastructure composition in `infrastructure/overlays/home/kustomization.yaml`:
- replace: `../../ingress-nginx/base`
- with: `../../ingress-traefik/base`

2. Commit the path change.

3. Reconcile:

```bash
make flux-reconcile
```

4. Verify:

```bash
kubectl get ingress -A
./scripts/91_verify_platform_state.sh
```

## Rollback

Revert the composition change in `infrastructure/overlays/home/kustomization.yaml`, then:

```bash
make flux-reconcile
```

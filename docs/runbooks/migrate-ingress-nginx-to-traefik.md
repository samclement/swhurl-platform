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

1. Update Flux stack ingress path in `cluster/overlays/homelab/flux/stack-kustomizations.yaml`:
- from: `./cluster/overlays/homelab/providers/ingress-nginx`
- to: `./cluster/overlays/homelab/providers/ingress-traefik`

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

Revert the path change in `stack-kustomizations.yaml`, then:

```bash
make flux-reconcile
```

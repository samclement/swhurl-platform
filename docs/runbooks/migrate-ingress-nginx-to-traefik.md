# Runbook: Migrate Ingress NGINX to Traefik

This runbook migrates a legacy cluster from Flux-managed `ingress-nginx` to native k3s `traefik`.

## Preconditions

1. Cluster is healthy:

```bash
make flux-reconcile
./scripts/91_verify_platform_state.sh
```

2. Backup/snapshot exists.

## Migration

1. Update shared infrastructure composition in `infrastructure/overlays/home/kustomization.yaml`:
   - remove: `../../ingress-nginx/base`
   - ensure `../../metrics-server/base` is also removed (native k3s `metrics-server` path)

2. Ensure k3s packaged Traefik is enabled (no `--disable traefik` in k3s server args).

3. Ensure cert-manager ACME solvers use Traefik class:
   - set `class: traefik` in:
     - `infrastructure/cert-manager/issuers/letsencrypt-staging/clusterissuer-letsencrypt-staging.yaml`
     - `infrastructure/cert-manager/issuers/letsencrypt-prod/clusterissuer-letsencrypt-prod.yaml`

4. Align ingress manifests/charts to Traefik:
   - set `ingressClassName`/`className: traefik` for:
     - `infrastructure/cilium/base/ingress-hubble-ui.yaml`
     - `platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy.yaml`
     - `platform-services/clickstack/base/helmrelease-clickstack.yaml`
     - `infrastructure/storage/minio/base/helmrelease-minio.yaml`
     - `tenants/apps/example/base/ingress-hello-web.yaml`
   - remove NGINX-only annotations where required.

5. Commit these changes, but do not reconcile yet if external `:80/:443` traffic still lands on ingress-nginx.

6. Cut over external `:80/:443` traffic to Traefik (router/NAT/service-LB path).
   - Preferred: update router/NAT to point at Traefik entrypoints directly.
   - Transitional (when router still targets legacy nginx NodePorts): move Traefik service NodePorts to the legacy values (`31514`/`30313`) before removing ingress-nginx.
   If external traffic still lands on ingress-nginx while manifests are switched to Traefik, host routes will return 404s.

7. Reconcile:

```bash
make flux-reconcile
```

8. Verify ingress class alignment and endpoint health:

```bash
kubectl -n kube-system get deploy traefik metrics-server
kubectl get ingress -A -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,CLASS:.spec.ingressClassName,HOSTS:.spec.rules[*].host'
curl -I https://hubble.homelab.swhurl.com
curl -I https://oauth.homelab.swhurl.com
curl -I https://clickstack.homelab.swhurl.com
curl -I https://staging-hello.homelab.swhurl.com
curl -I https://hello.homelab.swhurl.com
curl -I https://minio.homelab.swhurl.com
curl -I https://minio-console.homelab.swhurl.com
./scripts/91_verify_platform_state.sh
```

Expected auth behavior during current Traefik forward-auth mode:
- `hello` / `staging-hello` redirect to oauth2-proxy sign-in when unauthenticated.
- `hubble` is currently left unauthenticated (`200`) until redirect-capable edge-auth is added.

## Rollback

Re-add `../../ingress-nginx/base` to `infrastructure/overlays/home/kustomization.yaml`, revert manifest ingress-class changes, then:

```bash
make flux-reconcile
```

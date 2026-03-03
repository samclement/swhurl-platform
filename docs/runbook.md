# Platform Runbook (Flux-First)

This repo is operated through Flux GitOps with optional script orchestration (`run.sh`).

## Manual k3s prerequisite

Install k3s manually before Flux bootstrap. Keep packaged `traefik` and `metrics-server` enabled:

```bash
curl -sfL https://get.k3s.io | sudo INSTALL_K3S_EXEC="server --flannel-backend=none --disable-network-policy" sh -
sudo cp /etc/rancher/k3s/k3s.yaml "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config"
chmod 600 "$HOME/.kube/config"
kubectl -n kube-system get deploy traefik metrics-server
```

Bootstrap Cilium before Flux:

```bash
make cilium-bootstrap
```

Optional k3s auto-deploy mode:

```bash
sudo install -D -m 0644 bootstrap/k3s-manifests/cilium-helmchart.yaml \
  /var/lib/rancher/k3s/server/manifests/cilium-helmchart.yaml
kubectl -n kube-system rollout status ds/cilium --timeout=10m
kubectl -n kube-system rollout status deploy/cilium-operator --timeout=10m
./scripts/bootstrap/patch-hubble-relay-hostnetwork.sh
```

Migration safety note:
- `infrastructure/cilium/base/helmrelease-cilium.yaml` remains suspended as a handoff placeholder for existing clusters. Active Cilium install ownership is the k3s bootstrap manifest.
- Keep `hubble.listenAddress: "0.0.0.0:4244"` in `bootstrap/k3s-manifests/cilium-helmchart.yaml` (and the suspended handoff HelmRelease) so `hubble-relay` can maintain peer connectivity on IPv4-only node addressing.
- Cilium chart `v1.19.0` does not expose `hubble-relay` host-network values; install flows patch `kube-system/hubble-relay` to `hostNetwork=true` so relay reconnects do not fail on node-IP peer dialing.

## Standard Operations

### Bootstrap

```bash
make cilium-bootstrap
flux check --pre
flux install --namespace flux-system
make flux-bootstrap
```

Behavior:
- `make cilium-bootstrap` applies `bootstrap/k3s-manifests/cilium-helmchart.yaml`, waits for Cilium readiness, then patches `kube-system/hubble-relay` to `hostNetwork=true`.
- Flux installation is manual (outside repo scripts).
- `make flux-bootstrap` applies `clusters/home/flux-system` bootstrap manifests only.
- If `homelab-infrastructure` fails with `no matches for kind "ClusterIssuer" in version "cert-manager.io/v1"`, install cert-manager CRDs once and rerun reconcile:

```bash
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/download/v1.19.3/cert-manager.crds.yaml
make flux-reconcile
```

### Reconcile

```bash
make flux-reconcile
```

Behavior:
- Syncs `flux-system/platform-runtime-inputs` from local config (`config.env` + `profiles/local.env` + `profiles/secrets.env`, plus optional ad-hoc `--profile` overrides).
- Reconciles `swhurl-platform` source, `homelab-flux-sources`, then `homelab-flux-stack`.

### Full apply via orchestrator

```bash
./run.sh
```

### Full delete via orchestrator

```bash
./run.sh --delete
```

Delete ordering is intentional:
1. Remove Flux stack kustomizations.
2. Clean cert-manager finalizers/CRDs.
3. Teardown namespaces/secrets/CRDs.
4. Remove Cilium last.
5. Uninstall Flux controllers (via `flux uninstall` inside `32_reconcile_flux_stack.sh --delete` when Flux CLI is present).
6. Verify cleanup.

## Active Flux Dependency Chain

Parent level:
- `homelab-flux-sources -> homelab-flux-stack`

Cluster level (`clusters/home/*.yaml`):
- `homelab-infrastructure -> homelab-platform -> homelab-tenants -> homelab-app-example`
- `homelab-app-example-keycloak-canary` (isolated canary route, depends on `homelab-app-example`)

Layer composition:
- `homelab-infrastructure` points to `infrastructure/overlays/home`.
- `homelab-platform` points to `platform-services/overlays/home`.
- `homelab-tenants` points to `tenants/app-envs` (tenant env namespaces only).
- `homelab-app-example` points to `tenants/apps/example` (sample app staging+prod overlays).
- `homelab-app-example-keycloak-canary` points to `tenants/apps/example/canary/keycloak` (canary ingress/cert/middleware only).
- Platform cert issuer intent is post-build substitution from `flux-system/platform-settings` (`CERT_ISSUER`).

## Runtime Inputs

Targets are declarative under:
- `platform-services/runtime-inputs`

Source secret is external:
- `flux-system/platform-runtime-inputs`

Sync/update source secret:

```bash
make runtime-inputs-sync
```

Note:
- `logging/hyperdx-secret` value changes do not hot-reload into already-running `otel-k8s-*` collectors because `secretKeyRef` env vars are read at container start. If Kubernetes telemetry keeps returning 401 after key rotation, restart collector workloads.
- For ClickStack key rotations, prefer:

```bash
make runtime-inputs-refresh-otel
```

## Keycloak Staged Rollout (Safety-First)

Keycloak manifests are present as a platform-service component, but the HelmRelease is intentionally suspended by default:

- `platform-services/keycloak/base/helmrelease-keycloak.yaml`
- `spec.suspend: true`

Compatibility note:
- Current repo pin uses Bitnami chart `24.2.0` plus `bitnamilegacy/*` image overrides to avoid OCI chart-source and image-tag pull failures seen with older Flux/controller environments.

Safety sequence:
1. Set `FEAT_KEYCLOAK=true` and provide `KEYCLOAK_ADMIN_PASSWORD` + `KEYCLOAK_POSTGRES_PASSWORD`.
2. `make runtime-inputs-sync`
3. `make flux-reconcile`
4. Unsuspend Keycloak (`spec.suspend: false`) and reconcile again.
5. Verify Keycloak realm/client setup and `https://keycloak.homelab.swhurl.com` end-to-end.
6. Only after verification, migrate oauth2-proxy `oidc-issuer-url` from the current provider to Keycloak.

Canary oauth2-proxy sequence:
1. Set `FEAT_KEYCLOAK_CANARY=true` and provide:
   - `KEYCLOAK_CANARY_OIDC_CLIENT_ID`
   - `KEYCLOAK_CANARY_OIDC_CLIENT_SECRET`
   - `KEYCLOAK_CANARY_OAUTH_COOKIE_SECRET` (16/24/32 chars)
2. `make runtime-inputs-sync`
3. `make flux-reconcile`
4. Unsuspend `platform-services/oauth2-proxy-keycloak-canary/base/helmrelease-oauth2-proxy-keycloak-canary.yaml`.
5. Reconcile and validate:
   - `https://oauth-keycloak.homelab.swhurl.com`
   - `https://keycloak-canary-hello.homelab.swhurl.com`
6. Keep existing app ingresses on the current oauth2-proxy issuer until canary auth flow is verified end-to-end.

## Verification

Core checks:
- `scripts/94_verify_config_inputs.sh`
- `scripts/91_verify_platform_state.sh`

## Promotion / Profiles

- Infrastructure/platform cert issuer mode is Git-managed in:
  - `clusters/home/flux-system/sources/configmap-platform-settings.yaml`
  - `CERT_ISSUER=letsencrypt-staging|letsencrypt-prod`
- Sample app path is fixed via `clusters/home/app-example.yaml`:
  - `./tenants/apps/example`
- Example app staging/prod overlays both use `letsencrypt-prod`.
- Provider selection is controlled by composition entries in `infrastructure/overlays/home/kustomization.yaml`.

## Addendum: Native k3s Metrics Server + Traefik

Current default composition uses Flux-managed `metrics-server` and `ingress-nginx`.

To switch to native k3s packaged components:

1. Update host defaults (`host/config/homelab.env`):
   - `K3S_INGRESS_MODE=traefik`
   - `K3S_DISABLE_PACKAGED=` (do not disable `metrics-server`)
2. Update infra composition (`infrastructure/overlays/home/kustomization.yaml`):
   - remove `../../metrics-server/base`
   - remove `../../ingress-nginx/base`
3. Set verification/provider intent in `config.env`:
   - `INGRESS_PROVIDER=traefik`
4. Update ACME solver ingress class from `nginx` to `traefik` in:
   - `infrastructure/cert-manager/issuers/letsencrypt-staging/clusterissuer-letsencrypt-staging.yaml`
   - `infrastructure/cert-manager/issuers/letsencrypt-prod/clusterissuer-letsencrypt-prod.yaml`
5. Migrate ingresses and auth config:
   - `ingressClassName: traefik`
   - replace `nginx.ingress.kubernetes.io/*` annotations
   - use Traefik `Middleware` + `ForwardAuth` for oauth2-proxy auth flows
6. Reconcile:

```bash
make flux-reconcile
```

7. Verify:

```bash
kubectl -n kube-system get deploy metrics-server traefik
kubectl get ingress -A
./scripts/91_verify_platform_state.sh
```

Note: `infrastructure/ingress-traefik/base` is currently scaffold-only, so this path assumes k3s-packaged Traefik rather than a Flux-managed Traefik chart in this repo.

## TODO

- Add a host-level remove workflow for `/var/lib/rancher/k3s/server/manifests/cilium-helmchart.yaml` when using k3s auto-deploy mode, so teardown does not resurrect Cilium.
- Replace post-install `hubble-relay` hostNetwork patching with chart-native values once Cilium exposes relay host-network configuration.
- Add a dedicated Keycloak cutover runbook covering realm bootstrap, oauth2-proxy client config, and rollback to prior issuer.
- Document Keycloak chart-source migration (or Flux controller upgrade) before raising `platform-services/keycloak/base/helmrelease-keycloak.yaml` above chart `24.2.0` (Bitnami index URLs switch to `oci://`).

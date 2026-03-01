# Platform Runbook (Flux-First)

This repo is operated through Flux GitOps with optional script orchestration (`run.sh`).

## Standard Operations

### Bootstrap

```bash
flux check --pre
flux install --namespace flux-system
make flux-bootstrap
```

Behavior:
- Flux installation is manual (outside repo scripts).
- `make flux-bootstrap` applies `clusters/home/flux-system` bootstrap manifests only.

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

Layer composition:
- `homelab-infrastructure` points to `infrastructure/overlays/home`.
- `homelab-platform` points to `platform-services/overlays/home`.
- `homelab-tenants` points to `tenants/app-envs` (tenant env namespaces only).
- `homelab-app-example` points to `tenants/apps/example` (sample app staging+prod overlays).
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

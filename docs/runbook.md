# Platform Runbook (Flux-First)

This repo is operated through Flux GitOps with Makefile-first orchestration.

## Manual k3s prerequisite

Install k3s manually before Flux bootstrap. Keep packaged `traefik` and `metrics-server` enabled:

```bash
curl -sfL https://get.k3s.io | sudo INSTALL_K3S_EXEC="server" sh -
sudo cp /etc/rancher/k3s/k3s.yaml "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config"
chmod 600 "$HOME/.kube/config"
kubectl -n kube-system get deploy traefik metrics-server
```

## Standard Operations

Preferred day-to-day entrypoints:
- `make install`
- `make teardown`

### Bootstrap

```bash
flux check --pre
flux install --namespace flux-system
make flux-bootstrap
```

Behavior:
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
- Syncs `flux-system/platform-runtime-inputs` from local config (`config.env` + `profiles/local.env` + `profiles/secrets.env`, plus optional `PROFILE_FILE=...` overrides).
- Reconciles `swhurl-platform` source, `homelab-flux-sources`, then `homelab-flux-stack`.

### Full apply (`make install`)

```bash
make install
```

Flow:
1. `make verify-config` (when `FEAT_VERIFY=true`)
2. `make flux-reconcile`
3. `make verify-platform` (when `FEAT_VERIFY=true`)

### Full delete (`make teardown`)

```bash
make teardown
```

Delete ordering is intentional:
1. Remove Flux stack kustomizations.
2. Clean cert-manager finalizers/CRDs.
3. Teardown namespaces/secrets/CRDs.
4. Uninstall Flux controllers (via `flux uninstall` inside `32_reconcile_flux_stack.sh --delete` when Flux CLI is present).
5. Verify cleanup.

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

Note:
- `logging/hyperdx-secret` value changes do not hot-reload into already-running `otel-k8s-*` collectors because `secretKeyRef` env vars are read at container start.
- `make runtime-inputs-refresh-otel` now waits for `hyperdx-secret` propagation before collector restart to avoid stale-token rollouts.
- For ClickStack key rotations, prefer:

```bash
make runtime-inputs-refresh-otel
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

## Native k3s Defaults

Active `home` composition assumes:
- k3s default CNI (`flannel`)
- k3s packaged `traefik`
- k3s packaged `metrics-server`
- Traefik NodePorts are pinned declaratively through k3s `HelmChartConfig` in `infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml`:
  - HTTP `80 -> 31514`
  - HTTPS `443 -> 30313`

Legacy optional manifests (`infrastructure/metrics-server/base`, `infrastructure/ingress-nginx/base`) are kept in-repo for compatibility but are not part of `infrastructure/overlays/home`.

## TODO

- Add an oauth2-proxy refresh workflow after runtime credential changes (rollout restart or checksum strategy) so `ingress/oauth2-proxy-shared` picks up updated client credentials automatically.

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
kubectl -n flux-system create secret generic sops-age \
  --from-file=age.agekey=./age.agekey \
  --dry-run=client -o yaml | kubectl apply -f -
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
- Reconciles runtime input source (`homelab-flux-sources`) and stack (`homelab-flux-stack`) from Git.
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

Delete behavior is stack-only:
1. Delete Flux stack kustomizations (`homelab-flux-stack`, `homelab-flux-sources`).
2. Let Flux prune stack-managed resources.

Not part of default teardown:
- Flux controller uninstall
- cert-manager/CRD cleanup
- cluster-wide namespace/secret sweeping

## Host Dynamic DNS (Optional)

Host DNS automation is a standalone entrypoint:

```bash
make host-dns
make host-dns-delete
```

Direct script usage:

```bash
./host/dynamic-dns.sh [--dry-run] [--delete]
```

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

Source secret is Git-managed and SOPS-encrypted:
- `clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml`
- applied to cluster as `flux-system/platform-runtime-inputs` by `homelab-flux-sources` decryption

Secret boundary:
- Keep shared platform values in `secret-platform-runtime-inputs.sops.yaml`.
- Keep app-only secrets with the app (`tenants/apps/<app>/.../secret-*.sops.yaml`).
- App onboarding example: `docs/runbooks/onboard-app-with-sops-secrets.md`

After editing encrypted runtime inputs in Git:

```bash
git add clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
git commit -m "runtime-inputs: update platform secrets"
git push
make runtime-inputs-sync
```

Note:
- `logging/hyperdx-secret` value changes do not hot-reload into already-running `otel-k8s-*` collectors because `secretKeyRef` env vars are read at container start.
- `make runtime-inputs-refresh-otel` now waits for `hyperdx-secret` propagation before collector restart to avoid stale-token rollouts.
- For ClickStack key rotations, prefer:

```bash
make runtime-inputs-refresh-otel
```

## ClickStack First-Login and Key Rotation

ClickStack first-login flow:
1. Open `https://${CLICKSTACK_HOST}` and complete first team/user setup.
2. In ClickStack UI, create a new ingestion key for OTel collectors.
3. Copy the ingestion key.
4. Update the Git-managed SOPS source:

```bash
sops clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
```

Set `data.CLICKSTACK_INGESTION_KEY` to the new value and save.

5. Commit, push, and apply:

```bash
git add clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
git commit -m "runtime-inputs: rotate clickstack ingestion key"
git push
make runtime-inputs-refresh-otel
```

6. Verify source/target secret alignment (without printing secret values):

```bash
src="$(kubectl -n flux-system get secret platform-runtime-inputs -o jsonpath='{.data.CLICKSTACK_INGESTION_KEY}')"
dst="$(kubectl -n logging get secret hyperdx-secret -o jsonpath='{.data.HYPERDX_API_KEY}')"
test -n "$src" && test "$src" = "$dst" && echo "OK: ingestion key propagated to logging/hyperdx-secret"
```

## Gotchas

1. k3s prerequisite: use default networking (`flannel`) with packaged `traefik` + `metrics-server` enabled.
2. Runtime inputs are Git-managed via SOPS: commit + push encrypted changes before `make runtime-inputs-sync` (or `make flux-reconcile`).
3. DNS wildcard scope: `*.homelab.swhurl.com` matches one-label hosts only; multi-label names need explicit records (or deeper wildcard). Add explicit hosts to `DYNAMIC_DNS_RECORDS` in `config.env`.
4. Dynamic DNS timer config: after changing `DYNAMIC_DNS_RECORDS`, `AWS_ZONE_ID`, or `AWS_PROFILE`, rerun `make host-dns` so `/etc/swhurl-platform/dynamic-dns.env` is regenerated for systemd.
5. cert-manager issuance timing: first reconcile can fail until DNS propagates and ACME HTTP-01 checks can reach ingress.
6. ClickStack ingestion timing: OTLP ingestion is not fully active until initial team setup completes in UI.
7. OTel collector key reload: after key rotation, restart collectors (or use `make runtime-inputs-refresh-otel`) because `secretKeyRef` env values do not hot-reload in running pods.

## Verification

Core checks:
- `make verify-config` (config inputs)
- `make verify-platform` (Flux kustomization health + token alignment)
- `make verify` (both)

Architecture chart generation:
- C4 source files: `docs/charts/c4/*.d2`
- Render command: `make charts-generate`
- Output path: `docs/charts/c4/rendered/*.svg`

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

Legacy provider manifests were removed from this repo; `infrastructure/overlays/home` now targets only active paths.

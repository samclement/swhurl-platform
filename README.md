# Swhurl Platform (k3s-only)

## Scope of the repo

This repo manages a homelab Kubernetes platform with Flux GitOps.

Default stack components:
- Cilium
- cert-manager + ClusterIssuers (`selfsigned`, `letsencrypt-staging`, `letsencrypt-prod`)
- k3s-packaged Traefik ingress controller
- k3s-packaged metrics-server
- oauth2-proxy
- keycloak (optional, suspended-by-default rollout skeleton)
- ClickStack + OTel collectors
- MinIO
- sample app (`hello-web`) with app-overlay selected hostname/issuer/namespace mode

## Dependencies that need to be installed

Required for cluster orchestration (`make install`, `./run.sh`, Flux reconcile):
- `bash`
- `kubectl`
- `helm`
- `curl`
- `rg` (ripgrep)
- `envsubst` (usually from `gettext`)
- `base64`
- `hexdump`

Required for host tasks (`./host/run-host.sh`):
- Linux with `systemd` (`host/tasks/10_dynamic_dns.sh`)
- `aws` CLI configured with Route53 permissions (`host/tasks/10_dynamic_dns.sh`)

Optional tooling used in some workflows:
- Flux CLI (`flux`) for reconcile/bootstrap operations.
- `jq`, `yq`, `sops`, `age`.

## Manual k3s prerequisite

Install k3s manually before running Flux workflows. Keep packaged `traefik` and `metrics-server` enabled, and disable flannel/network-policy for Cilium:

```bash
curl -sfL https://get.k3s.io | sudo INSTALL_K3S_EXEC="server --flannel-backend=none --disable-network-policy" sh -
sudo cp /etc/rancher/k3s/k3s.yaml "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config"
chmod 600 "$HOME/.kube/config"
kubectl get nodes
kubectl -n kube-system get deploy traefik metrics-server
```

Bootstrap Cilium before Flux:

```bash
make cilium-bootstrap
```

Optional k3s auto-deploy mode (persisted at host level):

```bash
sudo install -D -m 0644 bootstrap/k3s-manifests/cilium-helmchart.yaml \
  /var/lib/rancher/k3s/server/manifests/cilium-helmchart.yaml
kubectl -n kube-system rollout status ds/cilium --timeout=10m
kubectl -n kube-system rollout status deploy/cilium-operator --timeout=10m
./scripts/bootstrap/patch-hubble-relay-hostnetwork.sh
```

Migration safety note:
- The repo keeps `infrastructure/cilium/base/helmrelease-cilium.yaml` suspended as a handoff placeholder for existing clusters. Active install ownership is the k3s bootstrap manifest.
- Keep `hubble.listenAddress: "0.0.0.0:4244"` in Cilium bootstrap values so `hubble-relay` can keep peer connectivity stable on IPv4-only node addressing.
- Cilium chart `v1.19.0` does not expose a `hubble-relay` host-network value. Install flows patch `kube-system/hubble-relay` to `hostNetwork=true` (`ClusterFirstWithHostNet`) so relay reconnects do not fail on node-IP peer dialing.

## Quickstart

1. Configure non-secrets in `config.env`.
2. Configure secrets in `profiles/secrets.env` (copy `profiles/secrets.example.env`).
3. Optional host bootstrap (dynamic DNS only): `./host/run-host.sh`.
4. Bootstrap Cilium (pre-Flux): `make cilium-bootstrap`
5. Install Flux manually (one-time per cluster):

```bash
flux check --pre
flux install --namespace flux-system
```

6. Apply Flux bootstrap manifests:
   - `make flux-bootstrap`
7. Reconcile sources + stack (includes runtime-input secret sync):
   - `make flux-reconcile`

### Manual Flux Installation (No Repo Installer Script)

Install Flux CLI locally (if missing):

```bash
curl -fsSL https://fluxcd.io/install.sh | bash
```

Manual bootstrap sequence:

```bash
# 1) Verify cluster reachability
kubectl get nodes

# 2) Bootstrap Cilium (pre-Flux requirement)
make cilium-bootstrap

# 3) Install Flux controllers
flux check --pre
flux install --namespace flux-system

# 4) Apply repo bootstrap manifests
kubectl apply -k clusters/home/flux-system

# 5) Sync runtime inputs and reconcile
./scripts/bootstrap/sync-runtime-inputs.sh
./scripts/32_reconcile_flux_stack.sh
```

Teardown (manual Flux uninstall):

```bash
flux uninstall --silent
```

Equivalent single-command cluster path:
- Apply: `./run.sh`
- Delete: `./run.sh --delete`

Layer selection note:
- Shared infrastructure composition is declared in `infrastructure/overlays/home/kustomization.yaml`.
- Shared platform services composition is declared in `platform-services/overlays/home/kustomization.yaml`.
- Tenant environments are declared in `tenants/app-envs`.
- App composition is declared in `tenants/apps/example` (deploys both `staging` and `prod` overlays).
- Platform cert issuer selection is post-build substitution from `flux-system/platform-settings` (`CERT_ISSUER`).
- Example app issuer intent is fixed in overlays (`staging` and `prod` both use `letsencrypt-prod`).

Layer boundaries:
- `clusters/home/` is the Flux cluster entrypoint layer (`flux-system`, source + stack Kustomizations).
- `infrastructure/` is shared cluster infra (networking, cert-manager, issuers, ingress/storage providers).
- `platform-services/` is shared platform service installs.
- `tenants/` is app-environment scope (staging/prod namespaces + sample app).
- `platform-runtime-inputs` is the only env-input bridge layer (`make runtime-inputs-sync`).
- `Makefile` is the operator API layer (invokes sync + reconcile workflows).

## Common Use Cases

### 1) Clean install / teardown

Install:

```bash
cp -n profiles/secrets.example.env profiles/secrets.env
$EDITOR config.env profiles/secrets.env
make install
```

Teardown:

```bash
make teardown
```

Notes:
- `./run.sh --delete` removes Flux stack kustomizations, performs teardown cleanup, removes Cilium, uninstalls Flux controllers, and runs delete verification.
- `DELETE_SCOPE=dedicated-cluster` enables aggressive secret cleanup for dedicated clusters.
- Makefile shortcuts:
  - `make install` (cluster default apply path)
  - `make teardown` (cluster default delete path)
  - `make reinstall` (teardown then install)
- Host layer remains direct: `./host/run-host.sh` (`--dry-run` or `--delete` as needed).

### 2) Promote infrastructure/platform cert mode: staging -> prod

```bash
make platform-certs-staging
make platform-certs-prod
```

`platform-certs-*` targets update:
- `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (`CERT_ISSUER=letsencrypt-staging|letsencrypt-prod`)

Important:
- These targets edit local Git-tracked files only.
- Commit + push first, then run `make flux-reconcile`.

### 3) Post-install secrets updates + ClickStack caveats

After editing `profiles/secrets.env`:

```bash
make runtime-inputs-sync
make flux-reconcile
```

If you changed ClickStack keys (`CLICKSTACK_INGESTION_KEY` and/or `CLICKSTACK_API_KEY`), use:

```bash
make runtime-inputs-refresh-otel
```

Important contracts:
- `OAUTH_COOKIE_SECRET` must be exactly 16, 24, or 32 characters.
- `CLICKSTACK_API_KEY` is required when ClickStack/OTel are enabled.
- `CLICKSTACK_INGESTION_KEY` is optional initially; when unset it falls back to `CLICKSTACK_API_KEY`.

ClickStack first-login flow:
1. Open `https://${CLICKSTACK_HOST}` and complete first team/user setup.
2. Create/copy ingestion key from ClickStack UI.
3. Set `CLICKSTACK_INGESTION_KEY` in `profiles/secrets.env`.
4. Re-sync:

```bash
make runtime-inputs-refresh-otel
```

### 4) Example app deployment defaults

URL mapping:
- staging URL: `staging-hello.homelab.swhurl.com`
- prod URL: `hello.homelab.swhurl.com`

Certificate issuer mapping:
- staging URL certificate issuer: `letsencrypt-prod`
- prod URL certificate issuer: `letsencrypt-prod`

Detailed cert runbook: `docs/runbooks/promote-platform-certs-to-prod.md`

### 5) Stage Keycloak rollout safely (no oauth2-proxy cutover yet)

This repo now includes a Keycloak HelmRelease skeleton for `https://keycloak.homelab.swhurl.com`, but it is suspended by default for safe rollout.

1. Configure Keycloak secrets in `profiles/secrets.env`.
2. Enable Keycloak feature gate:

```bash
# config.env
FEAT_KEYCLOAK=true
```

3. Sync runtime inputs and reconcile:

```bash
make runtime-inputs-sync
make flux-reconcile
```

4. Unsuspend Keycloak by editing:
   - `platform-services/keycloak/base/helmrelease-keycloak.yaml` (`spec.suspend: false`)
5. Reconcile and validate Keycloak login + OIDC discovery endpoints before changing oauth2-proxy issuer.

### 6) Enable oauth2-proxy Keycloak canary (separate host)

This canary path is isolated from existing protected app routes and keeps current oauth2-proxy behavior unchanged.

1. Set canary secrets in `profiles/secrets.env`:
   - `KEYCLOAK_CANARY_OIDC_CLIENT_ID`
   - `KEYCLOAK_CANARY_OIDC_CLIENT_SECRET`
   - `KEYCLOAK_CANARY_OAUTH_COOKIE_SECRET` (16/24/32 chars)
2. Enable feature gate:

```bash
# config.env
FEAT_KEYCLOAK_CANARY=true
```

3. Sync and reconcile:

```bash
make runtime-inputs-sync
make flux-reconcile
```

4. Unsuspend canary release:
   - `platform-services/oauth2-proxy-keycloak-canary/base/helmrelease-oauth2-proxy-keycloak-canary.yaml`
   - set `spec.suspend: false`
5. Unsuspend canary app-route kustomization:
   - `clusters/home/app-example-keycloak-canary.yaml`
   - set `spec.suspend: false`
6. Reconcile and test:
   - `https://oauth-keycloak.homelab.swhurl.com`
   - `https://keycloak-canary-hello.homelab.swhurl.com`

## New Machine Gotchas

1. Cilium prerequisite: k3s must be installed with flannel/network-policy disabled (`--flannel-backend=none --disable-network-policy`) and Cilium must be bootstrapped (`make cilium-bootstrap`) before Flux install/reconcile.
2. Runtime inputs are external to Git: after changing local config/secrets, run `make runtime-inputs-sync` before `make flux-reconcile`.
3. DNS wildcard scope: `*.homelab.swhurl.com` only matches one-label hosts. Multi-label names like `staging.hello.homelab.swhurl.com` need explicit records (or a deeper wildcard) in Route53.
4. cert-manager issuance timing: first reconcile can fail until DNS records propagate and ACME HTTP-01 checks can reach ingress.
5. ClickStack ingestion timing: OTLP ingestion is not fully active until initial ClickStack team setup is completed in the UI.
6. OTel collector key reload: after rotating ClickStack keys, restart collector pods (or use `make runtime-inputs-refresh-otel`) because `secretKeyRef` env values do not hot-reload in running pods.

## Addendum: Native k3s Metrics Server + Traefik

This repo currently deploys `metrics-server` and `ingress-nginx` via Flux by default. To move to native k3s components:

1. Update host defaults in `host/config/homelab.env`:
   - `K3S_INGRESS_MODE=traefik`
   - `K3S_DISABLE_PACKAGED=` (ensure `metrics-server` is not disabled)
2. Update infrastructure composition in `infrastructure/overlays/home/kustomization.yaml`:
   - remove `../../metrics-server/base`
   - remove `../../ingress-nginx/base`
3. Set operator intent in `config.env`:
   - `INGRESS_PROVIDER=traefik`
4. Update cert-manager ACME solvers to Traefik ingress class:
   - `infrastructure/cert-manager/issuers/letsencrypt-staging/clusterissuer-letsencrypt-staging.yaml`
   - `infrastructure/cert-manager/issuers/letsencrypt-prod/clusterissuer-letsencrypt-prod.yaml`
   - change solver `class: nginx` to `class: traefik`
5. Migrate app/platform ingresses from NGINX-specific config:
   - change `ingressClassName: nginx` to `traefik`
   - replace/remove `nginx.ingress.kubernetes.io/*` annotations
   - add Traefik `Middleware` resources for oauth2-proxy `ForwardAuth` if edge auth is still required
6. Reconcile and verify:

```bash
make flux-reconcile
kubectl -n kube-system get deploy metrics-server traefik
kubectl get ingress -A
```

Important: `infrastructure/ingress-traefik/base` is currently scaffold-only. Native Traefik mode in this repo means relying on the k3s-packaged Traefik, plus ingress/annotation migration to Traefik conventions.

## Orchestration

`run.sh` is the cluster orchestrator. Default apply flow:
1. cluster access check
2. config contract verify
3. runtime-input secret sync
4. Flux reconcile
5. verification

Default delete flow:
1. cluster access check
2. delete Flux stack kustomizations
3. cert-manager cleanup helper
4. teardown cleanup (`99`)
5. Cilium cleanup
6. Flux uninstall
7. delete verification

Show plans:

```bash
./run.sh --dry-run
./run.sh --dry-run --delete
```

## Useful Targets

- `make help`
- `make install`
- `make teardown`
- `make reinstall`
- `make cilium-bootstrap`
- `make flux-bootstrap`
- `make runtime-inputs-sync`
- `make runtime-inputs-refresh-otel`
- `make otel-collectors-restart`
- `make flux-reconcile`
- `make platform-certs-staging|platform-certs-prod`
- `make verify`

## Repo Layout

- `clusters/`: Flux cluster entrypoints and bootstrap manifests
- `infrastructure/`: shared infrastructure manifests
- `platform-services/`: shared platform-service manifests
- `tenants/`: app environment manifests
- `scripts/`: orchestration and verification scripts
- `host/`: optional host-layer bootstrap tasks
- `profiles/`: local optional overrides (`local.env`) and secret template/example (`secrets*.env`)
- `docs/`: runbooks, ADRs, and architecture/design notes

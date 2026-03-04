# Swhurl Platform (k3s-only)

## Scope of the repo

This repo manages a homelab Kubernetes platform with Flux GitOps.

Default stack components:
- cert-manager + ClusterIssuers (`selfsigned`, `letsencrypt-staging`, `letsencrypt-prod`)
- k3s-packaged Traefik ingress controller
- k3s-packaged metrics-server
- oauth2-proxy
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

Install k3s manually before running Flux workflows. Keep packaged `traefik` and `metrics-server` enabled:

```bash
curl -sfL https://get.k3s.io | sudo INSTALL_K3S_EXEC="server" sh -
sudo cp /etc/rancher/k3s/k3s.yaml "$HOME/.kube/config"
sudo chown "$(id -u):$(id -g)" "$HOME/.kube/config"
chmod 600 "$HOME/.kube/config"
kubectl get nodes
kubectl -n kube-system get deploy traefik metrics-server
```

## Quickstart

1. Configure non-secrets in `config.env`.
2. Configure secrets in `profiles/secrets.env` (copy `profiles/secrets.example.env`).
3. Optional host bootstrap (dynamic DNS only): `./host/run-host.sh`.
4. Install Flux manually (one-time per cluster):

```bash
flux check --pre
flux install --namespace flux-system
```

5. Apply Flux bootstrap manifests:
   - `make flux-bootstrap`
6. Reconcile sources + stack (includes runtime-input secret sync):
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

# 2) Install Flux controllers
flux check --pre
flux install --namespace flux-system

# 3) Apply repo bootstrap manifests
kubectl apply -k clusters/home/flux-system

# 4) Sync runtime inputs and reconcile
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
- `./run.sh --delete` removes Flux stack kustomizations, performs teardown cleanup, uninstalls Flux controllers, and runs delete verification.
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
 - `HELLO_OIDC_CLIENT_ID` / `HELLO_OIDC_CLIENT_SECRET` are used by shared hello/staging oauth2-proxy auth.
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

## New Machine Gotchas

1. k3s prerequisite: install k3s with default networking (`flannel`) and packaged `traefik` + `metrics-server` enabled.
2. Runtime inputs are external to Git: after changing local config/secrets, run `make runtime-inputs-sync` before `make flux-reconcile`.
3. DNS wildcard scope: `*.homelab.swhurl.com` only matches one-label hosts. Multi-label names like `staging.hello.homelab.swhurl.com` need explicit records (or a deeper wildcard) in Route53.
4. cert-manager issuance timing: first reconcile can fail until DNS records propagate and ACME HTTP-01 checks can reach ingress.
5. ClickStack ingestion timing: OTLP ingestion is not fully active until initial ClickStack team setup is completed in the UI.
6. OTel collector key reload: after rotating ClickStack keys, restart collector pods (or use `make runtime-inputs-refresh-otel`) because `secretKeyRef` env values do not hot-reload in running pods.

## Native k3s Defaults

Active composition already assumes native k3s components:
- CNI: flannel (k3s default)
- ingress: Traefik (k3s packaged)
- metrics: metrics-server (k3s packaged)
- Traefik NodePorts are declaratively pinned via Flux (`infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml`):
  - `80 -> 31514`
  - `443 -> 30313`
- Flux-managed `infrastructure/ingress-nginx/base` and `infrastructure/metrics-server/base` remain legacy/optional manifests and are not part of `infrastructure/overlays/home`.

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
5. Flux uninstall
6. delete verification

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

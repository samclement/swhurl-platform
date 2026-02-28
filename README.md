# Swhurl Platform (k3s-only)

## Scope of the repo

This repo manages a homelab Kubernetes platform with Flux GitOps.

Default stack components:
- Cilium
- cert-manager + ClusterIssuers (`selfsigned`, `letsencrypt-staging`, `letsencrypt-prod`)
- ingress-nginx
- oauth2-proxy
- ClickStack + OTel collectors
- MinIO
- sample app (`hello-web`) with tenant-overlay selected hostname/issuer/namespace mode

## Dependencies that need to be installed

Required:
- `bash`
- `kubectl`
- `helm`
- `curl`
- `rg` (ripgrep)
- `envsubst` (usually from `gettext`)
- `base64`
- `hexdump`

Notes:
- Flux CLI can be auto-installed by `make flux-bootstrap` (`AUTO_INSTALL_FLUX=true` by default).
- Optional tooling used in some workflows: `jq`, `yq`, `sops`, `age`.

## Quickstart

1. Configure non-secrets in `config.env`.
2. Configure secrets in `profiles/secrets.env` (copy `profiles/secrets.example.env`).
3. Optional host bootstrap: `./host/run-host.sh`.
4. Bootstrap Flux controllers + bootstrap manifests:
   - `make flux-bootstrap`
5. Reconcile sources + stack (includes runtime-input secret sync):
   - `make flux-reconcile`

Equivalent single-command cluster path:
- Apply: `./run.sh`
- Delete: `./run.sh --delete`

Layer selection note:
- Shared infrastructure composition is declared in `infrastructure/overlays/home/kustomization.yaml`.
- Shared platform services composition is declared in `platform-services/overlays/home/kustomization.yaml`.
- Tenant app URL/issuer composition is declared in `tenants/overlays/*`.
- Cert and app mode selection is path-driven in Flux CRDs under `clusters/home/*.yaml`.
- Prefer Makefile args for mode switching (`CERT_ENV`, `APP_ENV`, `LE_ENV`); `--profile` is optional compatibility for ad-hoc overrides.

Layer boundaries:
- `clusters/home/` is the Flux cluster entrypoint layer (`flux-system`, source + stack Kustomizations).
- `infrastructure/` is shared cluster infra (networking, cert-manager, issuers, ingress/storage providers, runtime-input targets).
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
make platform-certs CERT_ENV=staging
make platform-certs CERT_ENV=prod
```

`platform-certs` updates Flux paths:
- `clusters/home/infrastructure.yaml`:
  - `./infrastructure/overlays/home`
  - `./infrastructure/overlays/home-letsencrypt-prod`
- `clusters/home/platform.yaml`:
  - `./platform-services/overlays/home`
  - `./platform-services/overlays/home-letsencrypt-prod`

### 3) Post-install secrets updates + ClickStack caveats

After editing `profiles/secrets.env`:

```bash
make runtime-inputs-sync
make flux-reconcile
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
make runtime-inputs-sync
flux reconcile kustomization homelab-infrastructure -n flux-system --with-source
flux reconcile kustomization homelab-platform -n flux-system --with-source
```

### 4) App deployment test matrix (URL x Let's Encrypt)

Use one target with explicit intent flags:

```bash
make app-test APP_ENV=staging LE_ENV=staging
make app-test APP_ENV=staging LE_ENV=prod
make app-test APP_ENV=prod LE_ENV=staging
make app-test APP_ENV=prod LE_ENV=prod
```

URL mapping:
- staging URL: `staging-hello.homelab.swhurl.com`
- prod URL: `hello.homelab.swhurl.com`

Detailed cert runbook: `docs/runbooks/promote-platform-certs-to-prod.md`

## Orchestration

`run.sh` is the cluster orchestrator. Default apply flow:
1. cluster access check
2. config contract verify
3. Flux bootstrap
4. runtime-input secret sync
5. Flux reconcile
6. verification

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
- `make flux-bootstrap`
- `make runtime-inputs-sync`
- `make flux-reconcile`
- `make platform-certs CERT_ENV=staging|prod`
- `make app-test APP_ENV=staging|prod LE_ENV=staging|prod`
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

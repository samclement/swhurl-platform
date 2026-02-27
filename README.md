# Swhurl Platform (k3s-only)

This repo manages a homelab Kubernetes platform with Flux GitOps. Default stack components:
- Cilium
- cert-manager + ClusterIssuers (`selfsigned`, `letsencrypt-staging`, `letsencrypt-prod`, `letsencrypt` alias)
- ingress-nginx
- oauth2-proxy
- ClickStack + OTel collectors
- MinIO
- sample app (`hello-web`) with runtime-input driven hostname/issuer/namespace

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

Overlay selection note:
- Active ingress/storage overlays are selected declaratively in `cluster/overlays/homelab/flux/stack-kustomizations.yaml`.
- App URL/issuer/namespace are runtime-input driven (`APP_HOST`, `APP_CLUSTER_ISSUER`, `APP_NAMESPACE`).
- `--profile` values drive runtime-input intent (including app URL/issuer and cert mode).

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
  - `make install-all` / `make teardown-all` (include host layer)

### 2) Promote platform certs: staging -> prod

```bash
# staging platform cert intent
make platform-certs-staging

# production platform cert intent
make platform-certs-prod
```

These targets sync runtime inputs then reconcile the Flux stack.

### 3) Post-install secrets updates + ClickStack caveats

After editing `profiles/secrets.env`:

```bash
make runtime-inputs-sync
make flux-reconcile
```

Important contracts:
- `OAUTH_COOKIE_SECRET` must be exactly 16, 24, or 32 characters.
- `ACME_EMAIL` is required.
- `CLICKSTACK_API_KEY` is required when ClickStack/OTel are enabled.
- `CLICKSTACK_INGESTION_KEY` is optional initially; when unset it falls back to `CLICKSTACK_API_KEY`.

ClickStack first-login flow:
1. Open `https://${CLICKSTACK_HOST}` and complete first team/user setup.
2. Create/copy ingestion key from ClickStack UI.
3. Set `CLICKSTACK_INGESTION_KEY` in `profiles/secrets.env`.
4. Re-sync:

```bash
make runtime-inputs-sync
flux reconcile kustomization homelab-runtime-inputs -n flux-system --with-source
flux reconcile kustomization homelab-otel -n flux-system --with-source
```

### 4) Platform cert mode: staging vs production Let's Encrypt

Both ClusterIssuers remain deployed. Platform components switch issuer via `PLATFORM_CLUSTER_ISSUER`.

Use runtime-input toggle:

```bash
# staging
PLATFORM_CLUSTER_ISSUER=letsencrypt-staging make runtime-inputs-sync
make flux-reconcile

# production
PLATFORM_CLUSTER_ISSUER=letsencrypt-prod make runtime-inputs-sync
make flux-reconcile
```

Detailed runbook: `docs/runbooks/promote-platform-certs-to-prod.md`

### 5) App deployment test matrix (URL x Let's Encrypt)

The following targets update `APP_HOST` + `APP_CLUSTER_ISSUER` intent and reconcile:

```bash
make app-test-staging-le-staging
make app-test-staging-le-prod
make app-test-prod-le-staging
make app-test-prod-le-prod
```

URL mapping:
- staging URL: `staging.hello.homelab.swhurl.com`
- prod URL: `hello.homelab.swhurl.com`

## Orchestration

`run.sh` is the cluster orchestrator. Default apply flow:
1. prerequisites + cluster access checks
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
- `make flux-bootstrap`
- `make runtime-inputs-sync`
- `make flux-reconcile`
- `make cluster-apply`
- `make cluster-delete`
- `make all-apply`
- `make all-delete`
- `make verify`

## Repo Layout

- `cluster/`: Flux bootstrap and GitOps stack manifests
- `scripts/`: orchestration and verification scripts
- `host/`: optional host-layer bootstrap tasks
- `profiles/`: local and secret overlays for runtime configuration
- `docs/`: runbooks, ADRs, and architecture/design notes

# Swhurl Platform (k3s-only)

This repo manages a homelab Kubernetes platform with Flux GitOps. Default stack components:
- Cilium
- cert-manager + ClusterIssuers (`selfsigned`, `letsencrypt-staging`, `letsencrypt-prod`, `letsencrypt` alias)
- ingress-nginx
- oauth2-proxy
- ClickStack + OTel collectors
- MinIO
- sample app overlays (`apps/staging`, `apps/prod`)

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
- Active ingress/storage/app overlays are selected declaratively in `cluster/overlays/homelab/flux/stack-kustomizations.yaml`.
- `--profile` values (for example `profiles/overlay-staging.env`, `profiles/overlay-prod.env`) affect runtime-input/cert intent, not Flux overlay path selection.

## Common Use Cases

### 1) Clean install / teardown

Install:

```bash
cp -n profiles/secrets.example.env profiles/secrets.env
$EDITOR config.env profiles/secrets.env
./host/run-host.sh
make flux-bootstrap
make flux-reconcile
```

Teardown:

```bash
./run.sh --delete

# Optional host-layer cleanup
./host/run-host.sh --delete
```

Notes:
- `./run.sh --delete` removes Flux stack kustomizations, performs teardown cleanup, removes Cilium, uninstalls Flux controllers, and runs delete verification.
- `DELETE_SCOPE=dedicated-cluster` enables aggressive secret cleanup for dedicated clusters.

### 2) Partial operation (platform components only)

Suspend the sample app kustomization and reconcile only platform components:

```bash
flux suspend kustomization homelab-example-app -n flux-system
make flux-reconcile
```

Re-enable later:

```bash
flux resume kustomization homelab-example-app -n flux-system
flux reconcile kustomization homelab-example-app -n flux-system --with-source
```

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

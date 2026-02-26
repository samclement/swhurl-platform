# Swhurl Platform (k3s-only)

This repo provides a k3s-focused, declarative platform setup: Cilium CNI, cert-manager, ingress-nginx, oauth2-proxy, ClickStack (ClickHouse + HyperDX + OTel Collector), MinIO, and a staging/prod sample app model. The cluster layer is Flux-managed from `cluster/`.

## Quick Start

1. Configure non-secrets in `config.env`.
2. Configure secrets in `profiles/secrets.env` (gitignored, see `profiles/secrets.example.env`).
3. Optional host overrides: copy `host/config/host.env.example` to `host/config/host.env` and edit.
4. Optional host dry-run: `./host/run-host.sh --dry-run`
5. Optional host apply: `./host/run-host.sh`
6. Bootstrap Flux + apply GitOps sources: `make flux-bootstrap`
7. Reconcile the stack: `make flux-reconcile`
8. Compatibility path (legacy orchestrator): `./run.sh`
9. Destructive repeat-test profiles:
   - `profiles/test-loop.env` (Let’s Encrypt alias + prod endpoint overridden to staging)
   - `profiles/test-loop-selfsigned.env` (workloads selfsigned; ACME endpoints overridden to staging)
10. Promotion overlay profiles:
   - `./run.sh --profile profiles/overlay-staging.env` (apps namespace `apps-staging`, host `staging.hello.${BASE_DOMAIN}`)
   - `./run.sh --profile profiles/overlay-prod.env` (apps namespace `apps-prod`, host `prod.hello.${BASE_DOMAIN}`)

Optional unified run (host + cluster):

```bash
./run.sh --with-host
./run.sh --with-host --delete
```

You can also set `RUN_HOST_LAYER=true` in env/config to make this the default behavior.

## Clean Install (From Scratch)

Use this for repeatable rebuild/testing loops:

```bash
# 0) Optional local reset
sudo /usr/local/bin/k3s-uninstall.sh || true
sudo rm -rf /etc/rancher/k3s /var/lib/rancher/k3s

# 1) Configure repo inputs
cp -n profiles/secrets.example.env profiles/secrets.env
$EDITOR config.env profiles/secrets.env

# 2) Install host deps + k3s
./host/run-host.sh

# 3) Bootstrap Flux
#    - auto-installs flux CLI when missing
#    - auto-bootstraps Cilium first when no ready CNI exists
make flux-bootstrap

# 4) Reconcile the stack
make flux-reconcile

# 5) Watch progress
flux get kustomizations -n flux-system --watch
flux get helmreleases -A --watch
kubectl -n flux-system get events --sort-by=.lastTimestamp -w
```

Notes:
- First-time image pulls (ClickStack/MinIO) can make initial reconciles take several minutes.
- Host defaults disable bundled k3s `metrics-server`; this repo deploys metrics-server declaratively.

Docs:
- Phase runbook: `docs/runbook.md`
- Orchestration API (CLI + env contracts): `docs/orchestration-api.md`
- Contracts (env/tool/delete): `docs/contracts.md`
- Homelab intent and design direction: `docs/homelab-intent-and-design.md`
- Target tree and migration checklist: `docs/target-tree-and-migration-checklist.md`
- Migration runbooks:
  - `docs/runbooks/migrate-ingress-nginx-to-traefik.md`
  - `docs/runbooks/migrate-minio-to-ceph.md`
- ADRs:
  - `docs/adr/0001-ingress-provider-strategy.md`
  - `docs/adr/0002-storage-provider-strategy.md`
- Add feature checklist: `docs/add-feature-checklist.md`
- Migration plan (local charts): `docs/migration-plan-local-charts.md`

Operational helpers:
- `scripts/bootstrap/install-flux.sh` bootstraps Flux controllers and applies `cluster/flux` source manifests.
- `scripts/compat/verify-legacy-contracts.sh` runs the legacy verification suite.

Convenience task runner:
- `make help` prints common host/cluster/bootstrap targets (`host-plan`, `host-apply`, `cluster-plan`, `cluster-apply-traefik`, `cluster-apply-ceph`, `verify-provider-matrix`, `test-loop`, `all-apply`, `flux-bootstrap`, `flux-reconcile`, etc.).

GitOps sequencing:
- `cluster/overlays/homelab/flux/stack-kustomizations.yaml` defines the active Flux `dependsOn` chain (`namespaces -> cilium -> {metrics-server, cert-manager -> issuers -> ingress-provider -> {oauth2-proxy, clickstack -> otel, storage}} -> example-app`).
- `cluster/overlays/homelab/kustomization.yaml` is the default composition (nginx + minio + staging app overlay).
- `cluster/overlays/homelab/providers/` and `cluster/overlays/homelab/platform/` provide explicit promotion/provider overlays.

CI validation:
- `.github/workflows/validate.yml` runs shell syntax checks, dry-run command checks, `kubectl kustomize` rendering checks for scaffolded GitOps paths, and provider-matrix verification via `scripts/97_verify_provider_matrix.sh`.

## How This Repo Is Structured

The platform is run in explicit phases so you can apply/verify/debug in stages. The orchestrator (`run.sh`) does not discover scripts dynamically; it runs an explicit plan.

To print the plan without executing:

```bash
./scripts/02_print_plan.sh
./scripts/02_print_plan.sh --delete
```

## Phases (Install)

This is the top-level structure the repo follows (each phase has its own verification gates). The concrete script mapping is in `docs/runbook.md`.

1. Prerequisites & verify
2. Basic Kubernetes Cluster (kubeconfig) & verify
3. Environment (profiles/secrets) & verification
4. Cluster Deps (Helm, namespaces, Cilium) & verification
5. Cluster Platform Services (core: cert-manager + ingress, then platform: oauth/clickstack/otel/minio) & verification
6. Test application & verification
7. Cluster verification suite

### Legacy Compatibility: Helmfile Script Pipeline

Legacy scripts still support Helmfile-driven applies/deletes in two grouped phases:

- **Core**: cert-manager + ingress layer (ingress-nginx only when `INGRESS_PROVIDER=nginx`) (Helmfile label `phase=core`)
- **Platform**: oauth2-proxy + clickstack + otel-k8s collectors + minio (Helmfile label `phase=platform`)

The default `./run.sh` path uses these scripts:

- `scripts/31_sync_helmfile_phase_core.sh`: `helmfile sync/destroy -l phase=core` and waits for cert-manager webhook CA injection (needed before creating issuers).
- `scripts/36_sync_helmfile_phase_platform.sh`: `helmfile sync/destroy -l phase=platform`.

Runtime input source/target secrets are declarative in `cluster/base/runtime-inputs`.
Delete-time runtime input cleanup is handled by `scripts/99_execute_teardown.sh`.
`scripts/30_manage_cert_manager_cleanup.sh --delete` still exists as a delete-helper for cert-manager finalizers/CRDs; the apply path is driven by the Helmfile phase scripts above.

Run everything:

```bash
./run.sh
```

Use a profile:

```bash
./run.sh --profile profiles/minimal.env
./run.sh --profile profiles/overlay-staging.env
./run.sh --profile profiles/overlay-prod.env
```

## Key Flags and Inputs

Common inputs (see `docs/contracts.md`, `scripts/00_feature_registry_lib.sh`, and `scripts/00_verify_contract_lib.sh` for the full contract):

- `KUBECONFIG`: kubectl context for the target cluster (or use `~/.kube/config`).
- `BASE_DOMAIN`: base domain used to compute ingress hosts (defaults to `127.0.0.1.nip.io`).
- `PLATFORM_CLUSTER_ISSUER`: issuer for platform components (`selfsigned|letsencrypt|letsencrypt-staging|letsencrypt-prod`; default `letsencrypt-staging`).
- `APP_CLUSTER_ISSUER`: issuer for app certificates (defaults to `PLATFORM_CLUSTER_ISSUER`).
- `APP_NAMESPACE`: sample-app target namespace (`apps-staging|apps-prod`; default `apps-staging`).
- `APP_HOST`: sample-app ingress host (default `staging.hello.${BASE_DOMAIN}`).
- `LETSENCRYPT_ENV`: `staging` or `prod` (default `staging`) for alias issuer `letsencrypt`.
- `LETSENCRYPT_STAGING_SERVER`: optional staging ACME endpoint override.
- `LETSENCRYPT_PROD_SERVER`: optional prod ACME endpoint override (set to staging for repeated scratch-cycle safety).
- `TIMEOUT_SECS`: Helm/Helmfile timeouts (default `300`).
- `INGRESS_PROVIDER`: provider intent flag (`nginx` or `traefik`; migration scaffolding).
- `OBJECT_STORAGE_PROVIDER`: provider intent flag (`minio` or `ceph`; migration scaffolding).
  - Current effect: ingress-nginx is installed only when `INGRESS_PROVIDER=nginx`; MinIO is installed only when `OBJECT_STORAGE_PROVIDER=minio`.

Feature flags:

- `FEAT_CILIUM`: install Cilium (default `true`).
- `FEAT_OAUTH2_PROXY`: install oauth2-proxy (default `true`).
- `FEAT_CLICKSTACK`: install ClickStack (default `true`).
- `FEAT_OTEL_K8S`: install OTel k8s collectors (default `true`).
- `FEAT_MINIO`: install MinIO (default `true`).
- `FEAT_VERIFY`: run core verification gates during `./run.sh` (`94`, `91`, `92`; default `true`).
- `FEAT_VERIFY_DEEP`: run extra verification/diagnostics (`90`, `93`, `95`, `96`, `97`; default `false`).

Delete controls:

- `DELETE_SCOPE`: `managed` (default) or `dedicated-cluster` (more aggressive; see Teardown section).

## How Environment Variables Flow (kubectl, Helm, Helmfile)

This repo uses shell-sourced config (`config.env` + profiles) and relies on **exported** environment variables to drive Helmfile templating.

1) `./run.sh` and `./scripts/*`
- `./run.sh` sources `config.env` and an optional `--profile FILE` for orchestration decisions (feature flags, plan).
- `./run.sh` uses the same layering semantics as `scripts/00_lib.sh` so the plan matches the effective config used by all steps.
- Each script then sources `scripts/00_lib.sh`, which layers config in this order:
  - `config.env`
  - `profiles/local.env` (if present)
  - `profiles/secrets.env` (if present)
  - `$PROFILE_FILE` (when `--profile` is used; highest precedence)
- If you want a standalone profile that does *not* also load `profiles/local.env` and `profiles/secrets.env`, set:
  - `PROFILE_EXCLUSIVE=true`
- `scripts/00_lib.sh` uses `set -a` while sourcing so variables are **exported** for child processes (notably `helmfile`).

2) `helmfile` (templating and values)
- Helmfile templates read environment variables via Go templating: `{{ env "VAR" }}`.
- This repo’s Helmfile is `helmfile.yaml.gotmpl` and its environment values come from:
  - `environments/common.yaml.gotmpl` (derives `.Environment.Values` from exported env vars)
  - plus `environments/default.yaml` or `environments/minimal.yaml`
- Feature flags are applied twice:
  - Orchestration: `run.sh` decides which scripts to run.
  - Declarative state: Helmfile uses `.Environment.Values.features.*` to set `installed:` on releases.
- Helm installs are grouped via Helmfile labels (`phase=core`, `phase=platform`) so the default pipeline can `sync/destroy` by phase.

If you run Helmfile manually, you must export variables yourself, e.g.:
```bash
set -a
source ./config.env
source ./profiles/secrets.env
set +a
helmfile -f helmfile.yaml.gotmpl -e "${HELMFILE_ENV:-default}" diff
```

3) `helm` (charts)
- Helm itself does not substitute env vars into chart values automatically.
- In this repo, Helm is invoked via Helmfile; chart configuration comes from the rendered YAML in `infra/values/*`.

4) `kubectl` (apply and context)
- `kubectl` reads `KUBECONFIG` (or `~/.kube/config`) for cluster access.
- `kubectl apply --dry-run=server` talks to the API server and **runs admission** (including validating webhooks). You can’t “skip admission hooks” in server dry-run; if you need webhook-free validation, use client dry-run.
- Runtime input source/target secrets are declarative under `cluster/base/runtime-inputs`.

5) `kustomize` (optional)
- Kustomize is not used by the default pipeline anymore; it’s kept as an optional tool for teams that prefer raw manifests for apps.
- Kustomize does not read arbitrary environment variables by default.
- If you use Kustomize and need runtime values, prefer Helmfile/local charts for platform components, and keep Kustomize to apps-only overlays.
- This repo does not ship an `infra/manifests/` Kustomize tree by default.

Environment layering
- `config.env`: non-secret defaults (committed).
- `profiles/secrets.env`: secrets (gitignored).
- `--profile FILE`: additional overrides (exported to child scripts as `PROFILE_FILE`).

ACME / Let’s Encrypt
- Default is staging: `LETSENCRYPT_ENV=staging`
- `scripts/31_sync_helmfile_phase_core.sh` ensures:
  - `selfsigned`, `letsencrypt-staging`, and `letsencrypt-prod` exist
  - `letsencrypt` is an alias issuer that points to the selected env (so most ingresses can keep `cert-manager.io/cluster-issuer: letsencrypt`)
- For repeat destructive testing without production calls:
  - use `./run.sh --profile profiles/test-loop.env` (staging alias + prod endpoint overridden to staging), or
  - use `./run.sh --profile profiles/test-loop-selfsigned.env` (workloads use selfsigned and ACME endpoints are overridden to staging).
- Automated scratch loop helper:
  - `./scripts/compat/repeat-scratch-cycles.sh --yes --cycles 3 --profile profiles/test-loop.env`

k3s bootstrap (optional)
- Cluster provisioning is not the default workflow. If you want the repo to install k3s on the local host:
  - Run `scripts/manual_install_k3s_minimal.sh` (wrapper to host layer task; defaults to `K3S_INGRESS_MODE=traefik` with flannel disabled for Cilium)
  - Verify kubeconfig and API reachability with `scripts/15_verify_cluster_access.sh`

Verification toggles
- `FEAT_VERIFY=true|false` controls core verification gates (`94`, `91`, `92`) during `./run.sh`.
- `FEAT_VERIFY_DEEP=true|false` controls extra checks/diagnostics (`90`, `93`, `95`, `96`, `97`).

Helmfile drift checks (`scripts/92_verify_helmfile_drift.sh`)
- Requires the `helm-diff` plugin:
  - `helm plugin install https://github.com/databus23/helm-diff`
- Server-side dry-run uses the API server and will run admission webhooks. If your cluster rejects dry-run due to validating webhooks (common with ingress duplicate host/path), you can skip server dry-run and still validate render sanity:
  - `HELMFILE_SERVER_DRY_RUN=false ./scripts/92_verify_helmfile_drift.sh`

## Teardown (Delete)

```bash
./run.sh --delete
```

Delete ordering is intentionally strict:
- Platform services are removed first.
- `scripts/99_execute_teardown.sh` sweeps managed namespaces, non-k3s-native secrets, and platform CRDs.
- Cilium is deleted last (`scripts/26_manage_cilium_lifecycle.sh --delete`), so k3s/local-path helper pods can still run during PVC/namespace cleanup.
- `scripts/98_verify_teardown_clean.sh` runs last and fails the delete if anything remains.

Delete scope (shared clusters)
- By default, deletion only sweeps Secrets in the namespaces this repo manages.
- For a dedicated cluster wipe, opt in explicitly:
  - `DELETE_SCOPE=dedicated-cluster ./run.sh --delete`

k3s uninstall is manual unless `K3S_UNINSTALL=true`:

```bash
sudo /usr/local/bin/k3s-uninstall.sh
```

## Repo Layout (What Goes Where)

This repo separates **declarative state** (Helmfile/local charts) from **orchestration** (scripts):

- `run.sh`: phase-based orchestrator (apply and delete ordering).
- `config.env`: committed, non-secret defaults and feature flags.
- `profiles/`: local overrides and secrets (layered on top of `config.env`).
- `environments/`: Helmfile environments and derived values (`.Environment.Values`) used by `helmfile.yaml.gotmpl`.
- `helmfile.yaml.gotmpl`: declarative Helm releases (Cilium, cert-manager, ingress-nginx, oauth2-proxy, ClickStack, OTel collectors, MinIO).
- `charts/`: local Helm charts for repo-owned Kubernetes resources (e.g. managed namespaces; later issuers/apps).
- `infra/`: declarative Kubernetes inputs owned by this repo.
- `infra/values/`: Helm chart values files (referenced by Helmfile releases).
- `scripts/`: thin step scripts used by `run.sh` (apply/delete) plus verification scripts.
- `scripts/00_feature_registry_lib.sh`: canonical feature registry used by verification contracts (flags, required vars, expected releases).
- `scripts/00_verify_contract_lib.sh`: centralized verification and teardown expectations consumed by the 9x verify scripts (sourced by `scripts/00_lib.sh`).
- `docs/`: runbook and architecture diagram sources.

```
docs/
environments/
infra/
  values/
profiles/
scripts/
config.env
helmfile.yaml.gotmpl
run.sh
```

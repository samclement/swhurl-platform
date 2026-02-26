# Swhurl Platform (k3s-only)

This repo provides a k3s-focused, declarative platform setup: Cilium CNI, cert-manager, ingress-nginx, oauth2-proxy, ClickStack (ClickHouse + HyperDX + OTel Collector), and MinIO. Scripts are thin orchestrators around Helm + manifests in `infra/`.

## Quick Start

1. Configure non-secrets in `config.env`.
2. Configure secrets in `profiles/secrets.env` (gitignored, see `profiles/secrets.example.env`).
3. Optional host overrides: copy `host/config/host.env.example` to `host/config/host.env` and edit.
4. Optional host dry-run: `./host/run-host.sh --dry-run`
5. Optional host apply: `./host/run-host.sh`
6. Print the cluster plan: `./scripts/02_print_plan.sh`
7. Apply cluster layer: `./run.sh`

Optional unified run (host + cluster):

```bash
./run.sh --with-host
./run.sh --with-host --delete
```

You can also set `RUN_HOST_LAYER=true` in env/config to make this the default behavior.

Docs:
- Phase runbook: `docs/runbook.md`
- Contracts (env/tool/delete): `docs/contracts.md`
- Homelab intent and design direction: `docs/homelab-intent-and-design.md`
- Target tree and migration checklist: `docs/target-tree-and-migration-checklist.md`
- Add feature checklist: `docs/add-feature-checklist.md`
- Migration plan (local charts): `docs/migration-plan-local-charts.md`

Compatibility helpers:
- `scripts/compat/run-legacy-pipeline.sh` forwards to the current legacy `run.sh` flow.
- `scripts/compat/verify-legacy-contracts.sh` runs the existing verification script suite.
- `scripts/bootstrap/install-flux.sh` bootstraps Flux controllers and applies `cluster/flux` source manifests.

Convenience task runner:
- `make help` prints common host/cluster/bootstrap targets (`host-plan`, `host-apply`, `cluster-plan`, `all-apply`, `flux-bootstrap`, etc.).

GitOps sequencing scaffold:
- `cluster/overlays/homelab/flux/stack-kustomizations.yaml` defines a Flux `dependsOn` chain (`namespaces -> cilium -> core -> platform -> example-app`).
- Only `namespaces` is active by default; later layers are intentionally `suspend: true` until migrated.

CI validation:
- `.github/workflows/validate.yml` runs shell syntax checks, dry-run command checks, and `kubectl kustomize` rendering checks for scaffolded GitOps paths.

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

### Platform Installs Are Helmfile-Driven

Most platform services are installed declaratively via Helmfile and grouped into two phases:

- **Core**: cert-manager + ingress-nginx (Helmfile label `phase=core`)
- **Platform**: oauth2-proxy + clickstack + otel-k8s collectors + minio (Helmfile label `phase=platform`)

The default `./run.sh` path uses these scripts:

- `scripts/31_sync_helmfile_phase_core.sh`: `helmfile sync/destroy -l phase=core` and waits for cert-manager webhook CA injection (needed before creating issuers).
- `scripts/29_prepare_platform_runtime_inputs.sh`: creates/deletes the small set of non-Helm resources the charts depend on (Secrets/ConfigMaps).
- `scripts/36_sync_helmfile_phase_platform.sh`: `helmfile sync/destroy -l phase=platform`.

`scripts/30_manage_cert_manager_cleanup.sh --delete` still exists as a delete-helper for cert-manager finalizers/CRDs; the apply path is driven by the Helmfile phase scripts above.

Run everything:

```bash
./run.sh
```

Use a profile:

```bash
./run.sh --profile profiles/minimal.env
```

## Key Flags and Inputs

Common inputs (see `docs/contracts.md`, `scripts/00_feature_registry_lib.sh`, and `scripts/00_verify_contract_lib.sh` for the full contract):

- `KUBECONFIG`: kubectl context for the target cluster (or use `~/.kube/config`).
- `BASE_DOMAIN`: base domain used to compute ingress hosts (defaults to `127.0.0.1.nip.io`).
- `CLUSTER_ISSUER`: `selfsigned` or `letsencrypt` (default `selfsigned`).
- `LETSENCRYPT_ENV`: `staging` or `prod` (default `staging`) when `CLUSTER_ISSUER=letsencrypt`.
- `TIMEOUT_SECS`: Helm/Helmfile timeouts (default `300`).
- `INGRESS_PROVIDER`: provider intent flag (`nginx` or `traefik`; migration scaffolding).
- `OBJECT_STORAGE_PROVIDER`: provider intent flag (`minio` or `ceph`; migration scaffolding).

Feature flags:

- `FEAT_CILIUM`: install Cilium (default `true`).
- `FEAT_OAUTH2_PROXY`: install oauth2-proxy (default `true`).
- `FEAT_CLICKSTACK`: install ClickStack (default `true`).
- `FEAT_OTEL_K8S`: install OTel k8s collectors (default `true`).
- `FEAT_MINIO`: install MinIO (default `true`).
- `FEAT_VERIFY`: run core verification gates during `./run.sh` (`94`, `91`, `92`; default `true`).
- `FEAT_VERIFY_DEEP`: run extra verification/diagnostics (`90`, `93`, `95`, `96`; default `false`).

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
- Some charts rely on runtime Secrets/ConfigMaps that are created with `kubectl` (see `scripts/29_prepare_platform_runtime_inputs.sh`). Those resources are labeled `platform.swhurl.io/managed=true` for scoped deletion in `--delete`.

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
  - `letsencrypt-staging` and `letsencrypt-prod` exist
  - `letsencrypt` is an alias issuer that points to the selected env (so most ingresses can keep `cert-manager.io/cluster-issuer: letsencrypt`)

k3s bootstrap (optional)
- Cluster provisioning is not the default workflow. If you want the repo to install k3s on the local host:
  - Run `scripts/manual_install_k3s_minimal.sh` (wrapper to host layer task; defaults to `K3S_INGRESS_MODE=traefik` with flannel disabled for Cilium)
  - Verify kubeconfig and API reachability with `scripts/15_verify_cluster_access.sh`

Verification toggles
- `FEAT_VERIFY=true|false` controls core verification gates (`94`, `91`, `92`) during `./run.sh`.
- `FEAT_VERIFY_DEEP=true|false` controls extra checks/diagnostics (`90`, `93`, `95`, `96`).

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

# Platform Infrastructure Guide (Agents)

## Agents Operating Notes

- Always update this AGENTS.md with new learnings, gotchas, and environment-specific fixes discovered while working on the repo. Keep entries concise, actionable, and tied to the relevant scripts/config.
- Prefer adding learnings in the sections below. If a learning implies a code change, also open a TODO in the relevant script and reference it here.

### Current Learnings

- Documentation workflow
  - `showboat` is not installed globally in this repo environment; run it via `uvx showboat ...` (for example `uvx showboat init walkthrough.md ...`).
  - After editing executable walkthrough docs, run `uvx showboat verify <file>` to catch malformed code fences/empty exec blocks before committing.
  - For architecture refactors, keep a concrete “current file -> target ownership/path” mapping document (`docs/target-tree-and-migration-checklist.md`) so sequencing and responsibility shifts are explicit.
  - Keep the migration phase status snapshot in `docs/target-tree-and-migration-checklist.md` current when major scaffolding/migration milestones land.
  - GitOps scaffolding now lives under `cluster/` with Flux sources in `cluster/flux/`; use `scripts/bootstrap/install-flux.sh` to install controllers and apply bootstrap manifests.
  - Use `Makefile` targets for common operations (`host-plan`, `host-apply`, `cluster-plan`, `all-apply`, `flux-bootstrap`) to keep operator flows consistent as scripts evolve.
  - Provider profile examples now live in `profiles/provider-traefik.env`, `profiles/provider-ceph.env`, and `profiles/provider-traefik-ceph.env`; prefer these for repeatable provider-intent test runs instead of ad-hoc inline env vars.
  - Provider migration runbooks should reference committed provider profiles (`profiles/provider-traefik.env`, `profiles/provider-ceph.env`) and use inline rollback overrides (`INGRESS_PROVIDER=nginx`, `OBJECT_STORAGE_PROVIDER=minio`) instead of ad-hoc profile filenames.
  - CI dry-run validation now uses `./run.sh --dry-run` directly; the legacy compat alias wrapper was removed.
  - Flux dependency sequencing now actively reconciles default homelab layers in `cluster/overlays/homelab/flux/stack-kustomizations.yaml` (`namespaces -> cilium -> cert-manager -> issuers -> ingress-provider -> {oauth2-proxy, clickstack -> otel, storage} -> example-app`).
  - `cluster/overlays/homelab/kustomization.yaml` now represents the default composition (base platform + ingress-nginx + minio + app staging overlay); change this file when changing default provider/environment behavior.
  - Flux platform component paths in `cluster/overlays/homelab/flux/stack-kustomizations.yaml` now default to component base paths (`cluster/base/*`) where platform ingress annotations use `letsencrypt-staging`; promote to prod by switching paths to `cluster/overlays/homelab/platform/prod/*`.
  - Flux stack scaffolding now models component-level dependencies (`namespaces -> cilium -> cert-manager -> issuers -> ingress-provider -> platform components -> example-app`) instead of generic `core/platform` layers to keep sequencing explicit by technology.
  - Provider overlay scaffolds now live at `cluster/overlays/homelab/providers/{ingress-traefik,ingress-nginx,storage-minio,storage-ceph}`; keep overlay composition explicit by selecting one ingress overlay and one storage overlay in the active homelab kustomization.
  - Storage provider overlay `cluster/overlays/homelab/providers/storage-minio` now targets `cluster/base/storage/minio` (staging issuer intent in base values). Use `cluster/overlays/homelab/platform/prod/storage-minio` for prod annotation/host promotion.
  - Ingress provider overlay `cluster/overlays/homelab/providers/ingress-nginx/helmrelease-ingress-nginx.yaml` is active (no `spec.suspend`) and is the default ingress layer in Flux stack sequencing.
  - Component-level GitOps base scaffolds now exist under `cluster/base/` (`cert-manager`, `cert-manager/issuers`, `oauth2-proxy`, `clickstack`, `otel`, `storage/minio`, `storage/ceph`, plus `apps/example`); prefer component-level paths directly (legacy `core/` and `platform/` placeholders were removed).
  - Cert-manager and issuer Flux `HelmRelease` resources are active at `cluster/base/cert-manager/helmrelease-cert-manager.yaml` and `cluster/base/cert-manager/issuers/helmrelease-platform-issuers.yaml`.
  - Platform component Flux `HelmRelease` resources are active for `cilium`, `oauth2-proxy`, `clickstack`, `otel-k8s-daemonset`, `otel-k8s-cluster`, and `minio`.
  - Example app Flux `HelmRelease` is active at `cluster/base/apps/example/helmrelease-hello-web.yaml`; default stack deploys via `cluster/overlays/homelab/apps/staging`.
  - `make flux-reconcile` is the preferred operator command after `make flux-bootstrap` for GitOps-driven applies.
  - Migration runbooks for provider cutovers now live in `docs/runbooks/` (`migrate-ingress-nginx-to-traefik.md`, `migrate-minio-to-ceph.md`) and should be updated alongside provider behavior changes.
  - Provider strategy decisions are documented as ADRs in `docs/adr/0001-ingress-provider-strategy.md` and `docs/adr/0002-storage-provider-strategy.md`; update these when provider defaults, contracts, or rollout assumptions change.
  - CI validation now runs via `.github/workflows/validate.yml` (shell syntax checks, host/cluster dry-run checks, kustomize structure rendering for flux scaffolding, and provider-matrix validation through `scripts/97_verify_provider_matrix.sh`).
  - Destructive lifecycle loop helper: `scripts/compat/repeat-scratch-cycles.sh` performs uninstall/install/apply/delete cycles and blocks Let’s Encrypt prod usage unless `ALLOW_LETSENCRYPT_PROD_LOOP=true`.

- k3s-only focus
  - kind/Podman provider support has been removed to reduce complexity. Cluster provisioning is out of scope; scripts assume a reachable kubeconfig.
  - `scripts/manual_install_k3s_minimal.sh` is a compatibility wrapper to `host/run-host.sh --only 20_install_k3s.sh` (default `K3S_INGRESS_MODE=traefik` unless overridden).
  - Cilium is the standard CNI. k3s must be installed with `--flannel-backend=none --disable-network-policy` before running `scripts/26_manage_cilium_lifecycle.sh`. The script will refuse to install if flannel annotations are detected unless `CILIUM_SKIP_FLANNEL_CHECK=true` is set.
  - `scripts/26_manage_cilium_lifecycle.sh --delete` now removes Cilium CRDs by default (`CILIUM_DELETE_CRDS=true`) to prevent orphaned Cilium API resources after teardown.
  - If Cilium/Hubble pods fail to pull from `quay.io` (DNS errors on `cdn01.quay.io`), fix node DNS or mirror images and override repositories in `infra/values/cilium-helmfile.yaml.gotmpl` with `useDigest: false`.
  - Hubble UI ingress is configured declaratively in `infra/values/cilium-helmfile.yaml.gotmpl` (no script-side envsubst).

- Orchestrator run order
  - Planned homelab direction: model ingress and object storage as provider choices (`INGRESS_PROVIDER`, `OBJECT_STORAGE_PROVIDER`) so transitions (nginx->traefik, minio->ceph) stay declarative and do not require script rewrites.
  - Config contract now validates provider intent flags in `scripts/94_verify_config_inputs.sh`: `INGRESS_PROVIDER=nginx|traefik`, `OBJECT_STORAGE_PROVIDER=minio|ceph`.
  - Provider gating is wired in `helmfile.yaml.gotmpl`: ingress-nginx installs only when `INGRESS_PROVIDER=nginx`, MinIO installs only when `OBJECT_STORAGE_PROVIDER=minio`, and ingress-nginx `needs` edges are conditional for provider-flexible releases.
  - Provider-aware ingress templating is centralized in `environments/common.yaml.gotmpl` as `computed.ingressClass`; chart values consume this for ingress class names so swapping `INGRESS_PROVIDER` does not require per-chart rewrites.
  - NGINX-specific auth/redirect annotations are now gated to `INGRESS_PROVIDER=nginx` in Helmfile values (`infra/values/*`) and in verification checks (`scripts/91_verify_platform_state.sh`) to avoid false drift under Traefik.
  - Verification gating follows provider intent: ingress-nginx-specific checks in `scripts/90_verify_runtime_smoke.sh` and `scripts/91_verify_platform_state.sh` are skipped when `INGRESS_PROVIDER!=nginx`; MinIO checks/required vars are skipped when `OBJECT_STORAGE_PROVIDER!=minio`.
  - Planned host direction: keep host automation in Bash (no Ansible), but organize it as `host/lib` + `host/tasks` + `host/run-host.sh` to keep sequencing and ownership explicit.
  - Host scaffolding now exists: `host/run-host.sh` orchestrates `host/tasks/00_bootstrap_host.sh`, `host/tasks/10_dynamic_dns.sh`, and `host/tasks/20_install_k3s.sh`; dynamic DNS management is now native in `host/lib/20_dynamic_dns_lib.sh` (systemd unit rendering/install), with `scripts/manual_configure_route53_dns_updater.sh` retained as legacy compatibility path.
  - `scripts/00_lib.sh` is a helper and is excluded by `run.sh`.
  - Script naming convention:
    - `00_*_lib.sh` for sourced helper libraries (not runnable steps; excluded from `run.sh` plans).
    - `NN_verify_*` for validation gates (e.g. `15_verify_cluster_access.sh`).
    - `NN_reconcile_*` / `NN_prepare_*` for declarative pre-Helm setup.
    - `NN_manage_*_lifecycle` / `NN_manage_*_cleanup` for exception scripts that own imperative lifecycle/finalizer behavior.
    - `NN_sync_helmfile_phase_*` for Helmfile phase wrappers.
    - Keep repo/app utility steps aligned to this convention (for example `25_prepare_helm_repositories.sh`, `75_manage_sample_app_lifecycle.sh`) so plan ordering and purpose are obvious.
  - Helm releases are declarative in `helmfile.yaml.gotmpl`; release scripts are thin wrappers that call shared `sync_release` / `destroy_release`.
  - Helmfile label conventions:
    - `component=<id>` is the stable selector for single-release scripts (`sync_release` / `destroy_release`).
    - `phase=core|core-issuers|platform` is reserved for Helmfile phase group sync/destroy.
  - `run.sh` uses an explicit phase plan (no implicit script discovery). Print the plan via `scripts/02_print_plan.sh`.
  - Host bootstrap (k3s install) is intentionally not part of the default platform pipeline. Run `scripts/manual_install_k3s_minimal.sh` manually if needed, then verify kubeconfig with `scripts/15_verify_cluster_access.sh`.
  - Helm repositories are managed via `scripts/25_prepare_helm_repositories.sh`.
  - Managed namespaces are created declaratively via a local Helm chart (`charts/platform-namespaces`) wired into Helmfile as release `platform-namespaces` (label `component=platform-namespaces`). This avoids Kustomize `commonLabels` deprecation noise and keeps namespace creation consistent with the Helmfile-driven model.
  - Adoption gotcha: Helm will refuse to install a release that renders pre-existing resources (notably `Namespace` and `ClusterIssuer`) unless those resources already have Helm ownership metadata. The scripts `scripts/20_reconcile_platform_namespaces.sh` and `scripts/31_sync_helmfile_phase_core.sh` pre-label/annotate existing namespaces/issuers so Helmfile can converge on existing clusters.
  - Ownership gotcha: `cilium-secrets` is owned by the Cilium chart. Do not include it in `platform-namespaces` or Cilium install will fail due to conflicting Helm ownership. `scripts/26_manage_cilium_lifecycle.sh` adopts `cilium-secrets` to the `cilium` release if it already exists.
  - In delete mode, `run.sh` keeps finalizers deterministic: `scripts/99_execute_teardown.sh` runs before `scripts/26_manage_cilium_lifecycle.sh` (Cilium last) and then `scripts/98_verify_teardown_clean.sh`.
  - Platform Helm installs are now grouped by Helmfile phase (fewer scripts in the default run):
    - `scripts/31_sync_helmfile_phase_core.sh`: sync/destroy Helmfile `phase=core` (cert-manager + ingress-nginx) and wait for webhook CA injection.
    - `scripts/36_sync_helmfile_phase_platform.sh`: sync/destroy Helmfile `phase=platform` (oauth2-proxy/clickstack/otel/minio).
    - `scripts/29_prepare_platform_runtime_inputs.sh` is now a manual runtime-secret bridge and delete helper for legacy managed leftovers (`otel-config-vars`, `minio-creds`); it is no longer part of the default apply plan.
  - `scripts/92_verify_helmfile_drift.sh` performs a real drift check via `helmfile diff` (requires the `helm-diff` plugin). Use `HELMFILE_SERVER_DRY_RUN=false` to avoid admission webhook failures during server dry-run.
  - `scripts/92_verify_helmfile_drift.sh` ignores known non-actionable drift from Cilium CA/Hubble cert secret rotation.
  - Helm lock gotcha: if a release is stuck in `pending-install`/`pending-upgrade`, Helmfile/Helm can fail with `another operation (install/upgrade/rollback) is in progress`. If workloads are already running, a simple way to clear the lock is to rollback to the last revision, e.g. `helm -n observability rollback clickstack 1 --wait` (creates a new deployed revision and unblocks upgrades).
  - Cilium delete fallback must handle missing Helm release metadata: `scripts/26_manage_cilium_lifecycle.sh --delete` now deletes known cilium/hubble controllers/services directly, then forces deletion of any stuck `app.kubernetes.io/part-of=cilium` pods.
  - Cert-manager Helm install: Some environments time out on the chart’s post-install API check job. `scripts/30_manage_cert_manager_cleanup.sh` disables `startupapicheck` and explicitly waits for Deployments instead. If you want the chart’s check back, set `CM_STARTUP_API_CHECK=true` and re-enable in the script.
  - cert-manager webhook CA injection can lag after install; `scripts/30_manage_cert_manager_cleanup.sh` now waits for the webhook `caBundle` and restarts webhook/cainjector once if it’s empty to avoid issuer validation failures.
  - `scripts/30_manage_cert_manager_cleanup.sh --delete` now removes cert-manager CRDs by default (`CM_DELETE_CRDS=true`) so delete verification does not fail on orphaned CRDs.
  - Delete hang gotcha: cert-manager CRD deletion can block on `Order`/`Challenge` finalizers if controllers are already gone. `scripts/30_manage_cert_manager_cleanup.sh --delete` now clears finalizers on cert-manager/acme custom resources, deletes instances, then deletes CRDs with `--wait=false`.
  - Let’s Encrypt issuer mode supports `LETSENCRYPT_ENV=staging|prod` (default staging). `scripts/31_sync_helmfile_phase_core.sh` applies ClusterIssuers via a local chart and always creates:
    - `selfsigned`
    - `letsencrypt-staging`
    - `letsencrypt-prod`
    - `letsencrypt` alias issuer pointing at `LETSENCRYPT_ENV`
  - ACME endpoint overrides are explicit: `LETSENCRYPT_STAGING_SERVER` and `LETSENCRYPT_PROD_SERVER`. For repeated scratch cycles, point `LETSENCRYPT_PROD_SERVER` to staging (see `profiles/test-loop.env` / `profiles/overlay-staging.env`) to avoid production ACME traffic.
  - Issuer intent is split by scope: `PLATFORM_CLUSTER_ISSUER` drives platform ingress/certs, `APP_CLUSTER_ISSUER` drives sample/app cert issuance, and `APP_NAMESPACE` defaults to `apps-staging`.
  - Managed app namespaces are now `apps-staging` and `apps-prod` (no shared `apps` default). `scripts/75_manage_sample_app_lifecycle.sh` deploys to `${APP_NAMESPACE}`.
  - Overlay profiles for promotion flow:
    - `profiles/overlay-staging.env`: staging issuer defaults and prod-named issuer routed to staging ACME.
    - `profiles/overlay-prod.env`: production issuer defaults and app namespace `apps-prod`.
  - `scripts/99_execute_teardown.sh --delete` now performs real cleanup (platform secret sweep, managed namespace deletion/wait, and platform CRD deletion) before optional k3s uninstall. Use `DELETE_SCOPE=dedicated-cluster` to opt into cluster-wide secret sweeping.
  - `scripts/99_execute_teardown.sh --delete` is a hard gate before `scripts/26_manage_cilium_lifecycle.sh --delete`: it now fails if managed namespaces or PVCs (including ClickStack PVCs in `observability`) still exist, preventing premature Cilium removal.
  - Delete gotcha: `kube-system/hubble-ui-tls` can be left behind after `--delete` because it is created by cert-manager (ingress-shim) and cert-manager/CRDs may be deleted before the shim can clean it up. `scripts/26_manage_cilium_lifecycle.sh --delete` deletes `hubble-ui-tls` explicitly, and `scripts/98_verify_teardown_clean.sh` checks it even when `DELETE_SCOPE=managed`.
  - `scripts/98_verify_teardown_clean.sh --delete` now includes a kube-system Cilium residue check and fails if any `app.kubernetes.io/part-of=cilium` resources remain.
  - Verification tiers:
    - Core (default, `FEAT_VERIFY=true`): `94_verify_config_inputs.sh`, `91_verify_platform_state.sh`, `92_verify_helmfile_drift.sh`.
    - Deep (opt-in, `FEAT_VERIFY_DEEP=true`): `90_verify_runtime_smoke.sh`, `93_verify_expected_releases.sh`, `95_capture_cluster_diagnostics.sh`, `96_verify_orchestrator_contract.sh`, `97_verify_provider_matrix.sh`.
  - `scripts/95_capture_cluster_diagnostics.sh` writes diagnostics to `./artifacts/cluster-diagnostics-<timestamp>/` by default (or a custom output dir when passed as arg).
  - `scripts/91_verify_platform_state.sh` only validates Hubble OAuth auth annotations when `FEAT_OAUTH2_PROXY=true` to avoid false mismatches on non-OAuth installs.
  - Verification/teardown invariants are centralized in `scripts/00_verify_contract_lib.sh` (sourced by `scripts/00_lib.sh`), including managed namespaces/CRD regex, ingress NodePort expectations, drift ignore headers, expected release inventory, and config-contract required variable sets.
  - Feature-level verification metadata is centralized in `scripts/00_feature_registry_lib.sh` (feature flags, required vars, expected releases). Keep `scripts/94_verify_config_inputs.sh` and `scripts/93_verify_expected_releases.sh` registry-driven to avoid per-feature duplication.
  - `scripts/96_verify_orchestrator_contract.sh` now enforces feature registry consistency against `config.env`/profiles and Helmfile release mappings.
  - `scripts/97_verify_provider_matrix.sh` validates provider flag behavior without cluster access by rendering Helmfile for provider combinations and asserting `ingress-nginx`/`minio` installed states.
  - `scripts/93_verify_expected_releases.sh` checks missing expected releases by default and can optionally fail on unexpected extras. Tune with:
    - `VERIFY_RELEASE_SCOPE=platform|cluster`
    - `VERIFY_RELEASE_ALLOWLIST` (comma-separated glob patterns, e.g. `kube-system/traefik,apps/custom-app`)
    - `VERIFY_RELEASE_STRICT_EXTRAS=true` to enable extra-release checks (default `false`)
  - Verification maintainability roadmap (including Keycloak as the pilot feature) is documented in `docs/verification-maintainability-plan.md`; use it as the source for future verification framework refactors and follow-up prompts.
  - Documentation consistency gotcha: avoid hard-coding hypothetical script paths in planning docs. Keep references either aligned to existing files or clearly marked as future/proposed so script-reference checks stay actionable.
  - For day-to-day maintainer changes, prefer explicit updates using `docs/add-feature-checklist.md` rather than introducing new script abstraction layers.
  - Delete paths are idempotent/noise-reduced: uninstall scripts check `helm status` before `helm uninstall` so reruns do not spam `release: not found`.
  - `scripts/75_manage_sample_app_lifecycle.sh --delete` now checks whether `certificates.cert-manager.io` exists before deleting `Certificate`, avoiding errors after CRD teardown.
  - `scripts/91_verify_platform_state.sh` compares live cluster state to local config (issuer email, ingress hosts/issuers, ClickStack resources) and suggests which scripts to re-run on mismatch.
  - `scripts/91_verify_platform_state.sh` no longer treats `ingress/oauth2-proxy-secret` as a required runtime invariant for default Helmfile installs; it validates oauth2-proxy deployment/ingress state instead.
  - Observability is installed via `scripts/36_sync_helmfile_phase_platform.sh` (Helmfile `phase=platform`).

- Domains and DNS registration
  - `SWHURL_SUBDOMAINS` accepts raw subdomain tokens and the updater appends `.swhurl.com`. Example: `oauth.homelab` becomes `oauth.homelab.swhurl.com`. Do not prepend `BASE_DOMAIN` to these tokens.
  - If `SWHURL_SUBDOMAINS` is empty and `BASE_DOMAIN` ends with `.swhurl.com`, `scripts/manual_configure_route53_dns_updater.sh` derives a sensible set: `<base> oauth.<base> staging.hello.<base> prod.hello.<base> clickstack.<base> hubble.<base> minio.<base> minio-console.<base>`.
  - To expose the sample app over DNS overlays, add `staging.hello.<base>` and `prod.hello.<base>` to `SWHURL_SUBDOMAINS`.
  - `scripts/manual_configure_route53_dns_updater.sh` uses the standard env layering (`scripts/00_lib.sh`) so domain/subdomain inputs are consistent with `./run.sh` (and it honors `PROFILE_FILE` / `PROFILE_EXCLUSIVE`). Note: the installed systemd unit runs the helper with explicit args; rerun `scripts/manual_configure_route53_dns_updater.sh` when desired subdomains change.
  - `scripts/manual_configure_route53_dns_updater.sh` is a manual prerequisite (not part of `run.sh`); run it once per host to install/update the systemd timer, and run with `--delete` to uninstall.

- OIDC for applications
  - Use oauth2-proxy at the edge and add NGINX auth annotations to your app’s Ingress:
    - `nginx.ingress.kubernetes.io/auth-url: https://oauth.${BASE_DOMAIN}/oauth2/auth`
    - `nginx.ingress.kubernetes.io/auth-signin: https://oauth.${BASE_DOMAIN}/oauth2/start?rd=$scheme://$host$request_uri`
    - Optionally: `nginx.ingress.kubernetes.io/auth-response-headers: X-Auth-Request-User, X-Auth-Request-Email, Authorization`
  - Ensure `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, and `OAUTH_COOKIE_SECRET` in `config.env`/`profiles/secrets.env`; oauth2-proxy is installed by `scripts/36_sync_helmfile_phase_platform.sh`.
  - See README “Add OIDC To Your App” for a complete Ingress example.
  - Chart values quirk: the oauth2-proxy chart expects `ingress.hosts` as a list of strings, not objects. Scripts set `ingress.hosts[0]="${OAUTH_HOST}"`. Do not set `ingress.hosts[0].host` for this chart.
  - Cookie secret length: oauth2-proxy requires a secret of exactly 16, 24, or 32 bytes (characters if ASCII). Avoid base64-generating 32 bytes (length becomes 44 chars). `OAUTH_COOKIE_SECRET` is now required in config/contracts for the default apply path; the legacy bridge script can still generate one when used manually.

- Observability with ClickStack
  - Default install path uses `scripts/36_sync_helmfile_phase_platform.sh` (Helmfile `phase=platform`) to install ClickStack (ClickHouse + HyperDX + OTel Collector) in `observability`.
  - Optional OAuth protection for HyperDX ingress is declarative in `infra/values/clickstack-helmfile.yaml.gotmpl` and enabled when `FEAT_OAUTH2_PROXY=true`.
  - Operational gotcha: ClickStack/HyperDX may generate or rotate runtime keys on first startup, so configured key values are not always deterministic post-install.
  - Default install path uses `scripts/36_sync_helmfile_phase_platform.sh` (installs ClickStack + OTel collectors).
  - OTel Helmfile values now render endpoint config from `infra/values/otel-k8s-daemonset.yaml.gotmpl` and `infra/values/otel-k8s-deployment.yaml.gotmpl` (`computed.clickstackOtelEndpoint`) and render `authorization` from `CLICKSTACK_INGESTION_KEY`; `otel-config-vars` is treated as legacy cleanup-only in `scripts/29_prepare_platform_runtime_inputs.sh --delete`.
  - MinIO Helmfile values now use `rootUser`/`rootPassword` directly in `infra/values/minio-helmfile.yaml.gotmpl`; `scripts/29_prepare_platform_runtime_inputs.sh` keeps `minio-creds` as delete-only legacy cleanup.
  - Node CPU/memory in HyperDX requires daemonset metrics collection (`kubeletMetrics` + `hostMetrics`) plus a daemonset `metrics` pipeline exporting `kubeletstats` and `hostmetrics` (configured in `infra/values/otel-k8s-daemonset.yaml.gotmpl`).
  - Source of truth for ingestion key is HyperDX UI (API Keys) after startup/login. Set `CLICKSTACK_INGESTION_KEY` from UI and rerun `scripts/36_sync_helmfile_phase_platform.sh`.
  - Symptom of mismatch: OTel exporters log `HTTP Status Code 401` with `scheme or token does not match`; fetch current key from UI and rerun `scripts/36_sync_helmfile_phase_platform.sh`.

- Secrets hygiene
  - Do not commit secrets in `config.env`. Use `profiles/secrets.env` (gitignored) for `ACME_EMAIL`, `OIDC_*`, `OAUTH_COOKIE_SECRET`, `MINIO_ROOT_PASSWORD`, `CLICKSTACK_API_KEY`, `CLICKSTACK_INGESTION_KEY`.
  - `scripts/00_lib.sh` layers config as: `config.env` -> `profiles/local.env` -> `profiles/secrets.env` -> `$PROFILE_FILE` (highest precedence). This makes direct script runs consistent with `./run.sh`.
  - For a standalone profile (do not load local/secrets), set `PROFILE_EXCLUSIVE=true`.
  - A sample `profiles/secrets.example.env` is provided. Copy to `profiles/secrets.env` and fill in.

- Architecture diagram (D2)
  - Source: `docs/architecture.d2`. Render to SVG: `d2 --theme 200 docs/architecture.d2 docs/architecture.svg`.
  - If the CLI complains about unknown shapes, ensure D2 v0.7+ or simplify shapes (use `rectangle`, `cloud`).

---

This guide explains how to stand up and operate a lightweight Kubernetes platform for development and small environments. It targets k3s only. It covers platform components: Cilium CNI, cert-manager, ingress with OAuth proxy, observability via ClickStack (ClickHouse + HyperDX + OTel Collector), and object storage (MinIO). It also includes best practices for secrets and RBAC.

If you already have a cluster, you can jump directly to the Bootstrap section.

## Prerequisites

- kubectl: Kubernetes CLI.
- Helm: Package manager for Kubernetes.
- age + sops: Secrets encryption for GitOps.
- Optional: yq/jq for YAML/JSON processing; kustomize if desired.
- k3s installed and kubeconfig set (local Linux host) or a reachable remote cluster.

Install on Linux (Debian/Ubuntu example):

```
sudo apt-get update
sudo apt-get install -y curl gnupg lsb-release jq
curl -fsSL https://get.helm.sh/helm-v3.14.4-linux-amd64.tar.gz | tar -xz && sudo mv linux-amd64/helm /usr/local/bin/helm
curl -LO "https://dl.k8s.io/release/$(curl -Ls https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && chmod +x kubectl && sudo mv kubectl /usr/local/bin/
sudo apt-get install -y sops
curl -fsSL https://github.com/FiloSottile/age/releases/latest/download/age-v1.1.1-linux-amd64.tar.gz | sudo tar -xz -C /usr/local/bin --strip-components=1 age/age age/age-keygen
```

Notes

- Ensure `kubectl` context points to your target cluster before running Helm installs.

## Choose a Cluster (k3s only)

Install k3s with flannel off (for Cilium). Keep Traefik enabled by default unless you explicitly set `K3S_INGRESS_MODE=none`:

```
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--flannel-backend=none --disable-network-policy" sh -
```

Kubeconfig path: `/etc/rancher/k3s/k3s.yaml` (copy to `~/.kube/config` or set `KUBECONFIG`). Example:

```
sudo cp /etc/rancher/k3s/k3s.yaml ~/.kube/config
sudo chown $(id -u):$(id -g) ~/.kube/config
```

If you already have a reachable cluster, just ensure `kubectl` points at it.

## Bootstrap (Helm)

We’ll install common namespaces, add Helm repos, then deploy core components. You can adjust names and values as needed.

Create namespaces

```
kubectl create namespace platform-system --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace ingress --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace cert-manager --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace logging --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace observability --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace storage --dry-run=client -o yaml | kubectl apply -f -
```

Add Helm repos

```
helm repo add jetstack https://charts.jetstack.io
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm repo add oauth2-proxy https://oauth2-proxy.github.io/manifests
helm repo add cilium https://helm.cilium.io/
helm repo add clickstack https://clickhouse.github.io/ClickStack-helm-charts
helm repo add open-telemetry https://open-telemetry.github.io/opentelemetry-helm-charts
helm repo add minio https://charts.min.io/
helm repo update
```

### TLS: cert-manager

Install CRDs and cert-manager:

```
helm upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --set installCRDs=true
```

ClusterIssuer examples (choose one):

1) Self-signed (for air-gapped/dev)

```
cat <<'EOF' | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: selfsigned
spec:
  selfSigned: {}
EOF
```

2) Let’s Encrypt HTTP-01 (requires publicly reachable ingress)

```
cat <<'EOF' | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    email: you@example.com
    server: https://acme-v02.api.letsencrypt.org/directory
    privateKeySecretRef:
      name: acme-account-key
    solvers:
    - http01:
        ingress:
          class: nginx
EOF
```

### Ingress: NGINX + OAuth2 Proxy

Install ingress-nginx:

```
helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress \
  --set controller.replicaCount=1 \
  --set controller.ingressClassResource.default=true
```

Install OAuth2 Proxy (configure with your OIDC provider, e.g., Google, GitHub, Auth0):

```
kubectl -n ingress create secret generic oauth2-proxy-secret \
  --from-literal=client-id="YOUR_CLIENT_ID" \
  --from-literal=client-secret="YOUR_CLIENT_SECRET" \
  --from-literal=cookie-secret="$(openssl rand -base64 32)"

helm upgrade --install oauth2-proxy oauth2-proxy/oauth2-proxy \
  --namespace ingress \
  --set config.existingSecret=oauth2-proxy-secret \
  --set extraArgs.provider=oidc \
  --set extraArgs.oidc-issuer-url="https://YOUR_ISSUER" \
  --set extraArgs.redirect-url="https://oauth.YOUR_DOMAIN/oauth2/callback" \
  --set extraArgs.email-domain="*" \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.annotations."cert-manager\.io/cluster-issuer"=letsencrypt \
  --set ingress.hosts[0]="oauth.YOUR_DOMAIN" \
  --set ingress.tls[0].hosts[0]="oauth.YOUR_DOMAIN" \
  --set ingress.tls[0].secretName=oauth2-proxy-tls
```

Protect an app behind OAuth2 Proxy by annotating its Ingress:

```
metadata:
  annotations:
    nginx.ingress.kubernetes.io/auth-url: "https://oauth.YOUR_DOMAIN/oauth2/auth"
    nginx.ingress.kubernetes.io/auth-signin: "https://oauth.YOUR_DOMAIN/oauth2/start?rd=$scheme://$host$request_uri"
```

### Observability: ClickStack

Install ClickStack (ClickHouse + HyperDX + OTel Collector):

```
./scripts/36_sync_helmfile_phase_platform.sh
```

This script syncs Helmfile releases labeled `phase=platform`, including ClickStack, and exposes HyperDX at `https://${CLICKSTACK_HOST}` with cert-manager TLS.

If oauth2-proxy is enabled, auth annotations are applied declaratively by Helmfile values.

### Kubernetes OTel Integration

Install Kubernetes-focused OTel collectors (daemonset + deployment) and forward telemetry to ClickStack:

```
./scripts/36_sync_helmfile_phase_platform.sh
```

This follows the ClickStack Kubernetes integration pattern using the upstream `open-telemetry/opentelemetry-collector` chart.

### Storage: MinIO (Object Storage)

For k3s, the default `local-path` StorageClass handles PVs for simple workloads. For S3-compatible object storage inside the cluster, deploy MinIO:

```
helm upgrade --install minio minio/minio \
  --namespace storage \
  --set mode=standalone \
  --set resources.requests.memory=512Mi \
  --set replicas=1 \
  --set persistence.enabled=true \
  --set persistence.size=20Gi \
  --set ingress.enabled=true \
  --set ingress.ingressClassName=nginx \
  --set ingress.annotations."cert-manager\.io/cluster-issuer"=letsencrypt \
  --set ingress.path="/" \
  --set ingress.hosts[0]="minio.YOUR_DOMAIN" \
  --set ingress.tls[0].hosts[0]="minio.YOUR_DOMAIN" \
  --set ingress.tls[0].secretName=minio-tls \
  --set consoleIngress.enabled=true \
  --set consoleIngress.ingressClassName=nginx \
  --set consoleIngress.annotations."cert-manager\.io/cluster-issuer"=letsencrypt \
  --set consoleIngress.hosts[0]="minio-console.YOUR_DOMAIN" \
  --set consoleIngress.tls[0].hosts[0]="minio-console.YOUR_DOMAIN" \
  --set consoleIngress.tls[0].secretName=minio-console-tls
```

Set access keys via Helm values (or an external secret manager in production). Example:

```
--set rootUser="minioadmin" \
--set rootPassword="CHANGE_ME_LONG_RANDOM"
```

## Secrets Management Best Practices

- Prefer GitOps-friendly encryption:
  - sops + age to encrypt YAML secrets in-repo.
  - Alternatively, Sealed Secrets (Bitnami) for controller-side decryption.
  - For cloud-managed secrets, use External Secrets Operator (ESO) to sync secrets from AWS/GCP/Azure.
- Never commit plaintext secrets. Enforce pre-commit hooks guarding against accidental leaks.
- Separate secrets by namespace and purpose; rotate regularly and on role changes.
- Use distinct client IDs/secrets per environment for OAuth/OIDC.

Quick start with sops + age

```
mkdir -p .keys
age-keygen -o .keys/age.key
echo "export SOPS_AGE_KEY_FILE=$(pwd)/.keys/age.key" >> .envrc
export SOPS_AGE_KEY_FILE=$(pwd)/.keys/age.key
cat > .sops.yaml <<'EOF'
creation_rules:
  - path_regex: secrets/.*\.ya?ml
    encrypted_regex: '^(data|stringData)$'
    age: ["REPLACE_WITH_YOUR_AGE_RECIPIENT"]
EOF
```

Generate an age recipient from the key:

```
age-keygen -y .keys/age.key
```

Create and encrypt a Kubernetes Secret manifest:

```
mkdir -p secrets
cat > secrets/oauth2-proxy.yaml <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: oauth2-proxy-secret
  namespace: ingress
type: Opaque
stringData:
  client-id: YOUR_CLIENT_ID
  client-secret: YOUR_CLIENT_SECRET
  cookie-secret: CHANGE_ME
EOF

sops -e -i secrets/oauth2-proxy.yaml
kubectl apply -f secrets/oauth2-proxy.yaml
```

External Secrets Operator (optional)

- Install ESO Helm chart and configure a SecretStore pointing to your cloud secret manager.
- Reference external secrets in namespaces using ExternalSecret resources.

## RBAC, Security, and Multi-Tenancy

- Namespaces: Isolate by domain/team; apply labels for ownership and cost tracking.
- Least privilege: Avoid `cluster-admin`. Bind narrow Roles to ServiceAccounts.
- Service accounts: One per app; mount only required secrets; use `automountServiceAccountToken: false` unless needed.
- Network policies: Default deny all; allow only necessary egress/ingress between namespaces.
- Pod Security: Enforce Kubernetes Pod Security Standards (baseline/restricted) via namespace labels.
- Supply-chain: Pin images by digest; use image pull secrets; enable admission controls as appropriate.

Example Role and RoleBinding

```
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  namespace: apps
  name: app-reader
rules:
  - apiGroups: [""]
    resources: ["configmaps", "secrets"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  namespace: apps
  name: app-reader-binding
subjects:
  - kind: ServiceAccount
    name: my-app
    namespace: apps
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: app-reader
```

Pod Security labels (restricted)

```
kubectl label namespace apps pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/audit=restricted \
  pod-security.kubernetes.io/warn=restricted
```

## DNS and Domains

- For local dev without DNS, use magic hosts like `127.0.0.1.nip.io` or `sslip.io` to test Ingress quickly.
- For remote clusters, create DNS A/CNAME records for `*.YOUR_DOMAIN` pointing to the ingress controller LB/IP.

## Day-2 Ops (Brief)

- Backups: Back up etcd (or for k3s, use etcd or external DB); back up MinIO buckets; back up ClickStack PVCs and ClickHouse data.
- Upgrades: Upgrade Helm charts one at a time; monitor logs/ingestion health in ClickStack; use staged environments when possible.
- Teardown: `sudo /usr/local/bin/k3s-uninstall.sh` (server).

## Troubleshooting

- Ingress 404s: Check `ingressClassName`, controller logs, and Service/Endpoints readiness.
- Certificates pending: Inspect cert-manager `Certificate`/`Order` events; verify DNS/HTTP-01 reachability and issuer name.
- OAuth loops: Validate `redirect-url`, cookie secret length (32+ bytes base64), and time skew.
- ClickStack ingestion gaps: verify app OTLP exporters target the ClickStack collector service and inspect `clickstack-otel-collector` logs.
- Node storage: Ensure enough disk for local PVs; adjust MinIO persistence and requests.

## Suggested Repo Structure (optional)

```
infra/
  values/
    cilium-helmfile.yaml.gotmpl
    cert-manager-helmfile.yaml.gotmpl
    ingress-nginx-logging.yaml
    oauth2-proxy-helmfile.yaml.gotmpl
    clickstack-helmfile.yaml.gotmpl
    otel-k8s-daemonset.yaml.gotmpl
    otel-k8s-deployment.yaml.gotmpl
    minio-helmfile.yaml.gotmpl
  manifests/
    issuers/
    apps/
    templates/
    namespaces.yaml
secrets/
  (sops-encrypted secrets)
```

This document is a baseline. Adjust chart values and security controls to meet your environment’s requirements.

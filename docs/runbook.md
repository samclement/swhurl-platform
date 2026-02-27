# Platform Runbook (Phases)

This repo is organized into explicit phases so you can run, verify, and debug the platform in stages.
Cluster ownership is Flux-first (`cluster/`), with legacy script orchestration retained as compatibility mode.

## Flux-First Operations

1. Bootstrap Flux controllers and source definitions:
   - `make flux-bootstrap`
   - If `flux` CLI is missing, bootstrap auto-installs it to `~/.local/bin` by default.
   - If no ready CNI exists yet, bootstrap auto-runs the Cilium lifecycle scripts first.
2. Sync runtime inputs from local env/profile:
   - `make runtime-inputs-sync`
3. Reconcile source + stack:
   - `make flux-reconcile`
4. Observe stack health:
   - `flux get kustomizations -n flux-system`
   - `flux get helmreleases -A`

Default active dependency chain:

- `homelab-flux-sources -> homelab-flux-stack`
- inside `homelab-flux-stack`: `namespaces -> cilium -> {metrics-server, cert-manager -> issuers -> ingress-provider -> {oauth2-proxy, clickstack -> otel, storage}} -> example-app`

Default deployed app path:

- staging overlay at `cluster/overlays/homelab/apps/staging`
- host `staging.hello.${BASE_DOMAIN}`

## Legacy Compatibility Operations

Print the legacy script plan:

```bash
./scripts/02_print_plan.sh
./scripts/02_print_plan.sh --delete
```

## Legacy Phases (Install)

1) Prerequisites & verify
- `scripts/01_check_prereqs.sh`

Manual prerequisite (optional): DNS registration for `.swhurl.com`
- `scripts/manual_configure_route53_dns_updater.sh` installs/updates a systemd service/timer that keeps Route53 records current.
- Run `scripts/manual_configure_route53_dns_updater.sh --delete` to remove the unit files.

2) Basic Kubernetes Cluster (kubeconfig)
- `scripts/15_verify_cluster_access.sh`
Manual prerequisite (optional): Local host bootstrap (k3s)
- `scripts/manual_install_k3s_minimal.sh` delegates to host layer task `host/tasks/20_install_k3s.sh` (default `K3S_INGRESS_MODE=traefik`, flannel disabled for Cilium, bundled `metrics-server` disabled so repo-managed metrics-server can be used).
- Verify kubeconfig and API reachability with `scripts/15_verify_cluster_access.sh`.

3) Environment (profiles/secrets) & verification
- `scripts/94_verify_config_inputs.sh` (feature-gated by `FEAT_VERIFY`)

4) Cluster deps (helm/cilium) & verification
- `scripts/25_prepare_helm_repositories.sh`
- `scripts/20_reconcile_platform_namespaces.sh` (Helmfile local chart: `component=platform-namespaces`)
- `scripts/26_manage_cilium_lifecycle.sh` (feature-gated by `FEAT_CILIUM`)

5) Platform services & verification
- `scripts/31_sync_helmfile_phase_core.sh` (Helmfile: `phase=core`, installs cert-manager, repo-managed metrics-server, and installs ingress-nginx only when `INGRESS_PROVIDER=nginx`)
- `scripts/31_sync_helmfile_phase_core.sh` also applies ClusterIssuers via a local Helm chart (Helmfile: `phase=core-issuers`), always creating `selfsigned`, `letsencrypt-staging`, `letsencrypt-prod`, and `letsencrypt` (alias from `LETSENCRYPT_ENV`)
- `scripts/36_sync_helmfile_phase_platform.sh` (Helmfile: `phase=platform`, installs oauth2-proxy/clickstack/otel/minio based on feature flags and provider settings; MinIO only when `OBJECT_STORAGE_PROVIDER=minio`)

Notes:
- Runtime input target secrets are declarative in `cluster/base/runtime-inputs`.
- Source secret `flux-system/platform-runtime-inputs` is external; sync it with `make runtime-inputs-sync` before `make flux-reconcile`.
- OTel exporters read `CLICKSTACK_INGESTION_KEY` from runtime inputs (falling back to `CLICKSTACK_API_KEY` when unset). After ClickStack first-login/team setup, update `profiles/secrets.env`, run `make runtime-inputs-sync`, then reconcile OTel.
- ClickStack first-team bootstrap is handled manually in the ClickStack UI.
- Delete-time runtime input cleanup is handled by `scripts/99_execute_teardown.sh`.
- `scripts/30_manage_cert_manager_cleanup.sh --delete` still exists as a delete-helper for cert-manager finalizers/CRDs; the apply path is driven by `scripts/31_sync_helmfile_phase_core.sh`.

6) Test application & verification
- `scripts/75_manage_sample_app_lifecycle.sh`
  - Uses `APP_NAMESPACE` (default `apps-staging`).
  - Default ingress host is `staging.hello.${BASE_DOMAIN}` unless `APP_HOST` is set.

7) Cluster verification suite
- Core gates (default; `FEAT_VERIFY=true`):
  - `scripts/91_verify_platform_state.sh`
  - `scripts/92_verify_helmfile_drift.sh`
- Extra checks/diagnostics (opt-in; `FEAT_VERIFY_DEEP=true`):
  - `scripts/90_verify_runtime_smoke.sh`
  - `scripts/93_verify_expected_releases.sh` (Flux-first inventory/health checks; Helm release inventory fallback via `VERIFY_INVENTORY_MODE=helm`)
  - `scripts/95_capture_cluster_diagnostics.sh` (writes diagnostics under `./artifacts/cluster-diagnostics-<timestamp>/` unless an output dir is passed)
  - `scripts/96_verify_orchestrator_contract.sh`
  - `scripts/97_verify_provider_matrix.sh` (renders Helmfile under provider combinations and validates release install gating)

## Legacy Phases (Delete)

Delete runs in reverse order with deterministic finalizers:

1) Remove apps and platform services (reverse order)
2) Perform teardown sweep (namespaces, non-k3s-native secrets, platform CRDs)
3) Remove Cilium last
4) Verify cluster is clean (`scripts/98_verify_teardown_clean.sh`)

Important: Cilium is deleted only after platform namespaces and PVCs are removed, so k3s/local-path helper pods can still run during namespace cleanup.

## Repeat Scratch Testing

To test full lifecycle repeatedly (k3s uninstall/install + apply/delete), use:

```bash
./scripts/compat/repeat-scratch-cycles.sh --yes --cycles 3 --profile profiles/test-loop.env
```

Notes:
- `profiles/test-loop.env` keeps ACME issuer endpoints on staging (including the prod-named issuer) to avoid production Let’s Encrypt traffic.
- Use `profiles/overlay-staging.env` for normal staging runs and `profiles/overlay-prod.env` for production promotion.

# Platform Runbook (Phases)

This repo is organized into explicit phases so you can run, verify, and debug the platform in stages. Scripts should be thin wrappers; declarative state lives in Helmfile and local charts (Kustomize is optional and currently unused by the default pipeline).

Print the current plan:

```bash
./scripts/02_print_plan.sh
./scripts/02_print_plan.sh --delete
```

## Phases (Install)

1) Prerequisites & verify
- `scripts/01_check_prereqs.sh`

Manual prerequisite (optional): DNS registration for `.swhurl.com`
- `scripts/manual_configure_route53_dns_updater.sh` installs/updates a systemd service/timer that keeps Route53 records current.
- Run `scripts/manual_configure_route53_dns_updater.sh --delete` to remove the unit files.

2) Basic Kubernetes Cluster (kubeconfig)
- `scripts/15_verify_cluster_access.sh`
Manual prerequisite (optional): Local host bootstrap (k3s)
- `scripts/manual_install_k3s_minimal.sh` delegates to host layer task `host/tasks/20_install_k3s.sh` (default `K3S_INGRESS_MODE=traefik`, flannel disabled for Cilium).
- Verify kubeconfig and API reachability with `scripts/15_verify_cluster_access.sh`.

3) Environment (profiles/secrets) & verification
- `scripts/94_verify_config_inputs.sh` (feature-gated by `FEAT_VERIFY`)

4) Cluster deps (helm/cilium) & verification
- `scripts/25_prepare_helm_repositories.sh`
- `scripts/20_reconcile_platform_namespaces.sh` (Helmfile local chart: `component=platform-namespaces`)
- `scripts/26_manage_cilium_lifecycle.sh` (feature-gated by `FEAT_CILIUM`)

5) Platform services & verification
- `scripts/31_sync_helmfile_phase_core.sh` (Helmfile: `phase=core`, installs cert-manager + ingress-nginx)
- `scripts/31_sync_helmfile_phase_core.sh` also applies ClusterIssuers via a local Helm chart (Helmfile: `phase=core-issuers`, default `LETSENCRYPT_ENV=staging`)
- `scripts/29_prepare_platform_runtime_inputs.sh` (kubectl: secrets/configmaps required by Helm releases)
- `scripts/36_sync_helmfile_phase_platform.sh` (Helmfile: `phase=platform`, installs oauth2-proxy/clickstack/otel/minio based on feature flags)

Notes:
- `scripts/30_manage_cert_manager_cleanup.sh --delete` still exists as a delete-helper for cert-manager finalizers/CRDs; the apply path is driven by `scripts/31_sync_helmfile_phase_core.sh`.

6) Test application & verification
- `scripts/75_manage_sample_app_lifecycle.sh`

7) Cluster verification suite
- Core gates (default; `FEAT_VERIFY=true`):
  - `scripts/91_verify_platform_state.sh`
  - `scripts/92_verify_helmfile_drift.sh`
- Extra checks/diagnostics (opt-in; `FEAT_VERIFY_DEEP=true`):
  - `scripts/90_verify_runtime_smoke.sh`
  - `scripts/93_verify_expected_releases.sh`
  - `scripts/95_capture_cluster_diagnostics.sh` (writes diagnostics under `./artifacts/cluster-diagnostics-<timestamp>/` unless an output dir is passed)
  - `scripts/96_verify_orchestrator_contract.sh`

## Phases (Delete)

Delete runs in reverse order with deterministic finalizers:

1) Remove apps and platform services (reverse order)
2) Perform teardown sweep (namespaces, non-k3s-native secrets, platform CRDs)
3) Remove Cilium last
4) Verify cluster is clean (`scripts/98_verify_teardown_clean.sh`)

Important: Cilium is deleted only after platform namespaces and PVCs are removed, so k3s/local-path helper pods can still run during namespace cleanup.

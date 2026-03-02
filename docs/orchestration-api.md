# Orchestration API

Last updated: 2026-03-02

This document defines the current command and environment contract for orchestration entrypoints.

## Cluster Orchestrator (`run.sh`)

Usage:

```bash
./run.sh [--profile FILE] [--only N[,N...]] [--dry-run] [--delete]
```

Options:
- `--profile FILE`: load additional env vars (highest precedence in cluster config layering). Use for ad-hoc overrides.
- `--only LIST`: comma-separated step numbers or basenames.
- `--dry-run`: print resolved plan and exit without executing.
- `--delete`: execute delete flow, passing `--delete` to delete-capable steps.

Environment controls:
- `ONLY`: fallback filter when `--only` is not provided.
- `PROFILE_EXCLUSIVE=true|false`: if `true`, skip auto-loading `profiles/local.env` and `profiles/secrets.env`.
- `FEAT_VERIFY=true|false`: include/exclude core verification steps.

Config layering (`run.sh`):
1. `config.env`
2. `profiles/local.env` (unless `PROFILE_EXCLUSIVE=true`)
3. `profiles/secrets.env` (unless `PROFILE_EXCLUSIVE=true`)
4. `--profile FILE` (`PROFILE_FILE`, highest precedence)

Default apply steps:
1. `15_verify_cluster_access.sh`
2. `16_verify_cilium_bootstrap.sh`
3. `94_verify_config_inputs.sh` (when `FEAT_VERIFY=true`)
4. `bootstrap/sync-runtime-inputs.sh`
5. `32_reconcile_flux_stack.sh`
6. `91_verify_platform_state.sh` (when `FEAT_VERIFY=true`)

Default delete steps:
1. `15_verify_cluster_access.sh`
2. `32_reconcile_flux_stack.sh --delete`
3. `30_manage_cert_manager_cleanup.sh --delete`
4. `99_execute_teardown.sh --delete`
5. `26_manage_cilium_lifecycle.sh --delete` (when `FEAT_CILIUM=true`)
6. `98_verify_teardown_clean.sh --delete`

Notes:
- Cilium is a pre-Flux dependency and is bootstrapped declaratively via k3s helm-controller manifest (`bootstrap/k3s-manifests/cilium-helmchart.yaml`).
- Flux retains a suspended Cilium HelmRelease (`infrastructure/cilium/base/helmrelease-cilium.yaml`) as a migration handoff placeholder for existing clusters.
- Flux CLI/controller installation is manual and documented in `README.md`.
- Bootstrap manifests must be applied first (`make flux-bootstrap`), then `32_reconcile_flux_stack.sh` reconciles source/stack.
- Runtime input target secrets are declarative in `platform-services/runtime-inputs`.
- Source secret `flux-system/platform-runtime-inputs` is external and synced by `scripts/bootstrap/sync-runtime-inputs.sh`.
- Shared infrastructure/platform composition is fixed to `infrastructure/overlays/home` and `platform-services/overlays/home`.
- Platform cert issuer intent is Git-managed in `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (`CERT_ISSUER`).
- Tenant environments are fixed in `clusters/home/tenants.yaml` (`./tenants/app-envs`).
- Example app deployment intent is fixed in `clusters/home/app-example.yaml` (`./tenants/apps/example`, staging+prod overlays).

## Host Orchestrator (`host/run-host.sh`)

Usage:

```bash
./host/run-host.sh [--host-env FILE] [--only N[,N...]] [--dry-run] [--delete]
```

Config layering (`host/run-host.sh`):
1. `config.env`
2. `host/config/homelab.env`
3. `host/config/host.env`
4. `--host-env FILE`

Default host apply tasks:
1. `host/tasks/10_dynamic_dns.sh`

Default host delete tasks:
1. `host/tasks/10_dynamic_dns.sh --delete`

Manual prerequisite:
- k3s installation is manual and documented in `README.md`.

## Script Contract

Step scripts should:
1. Parse `--delete` consistently.
2. Fail fast when called in an unsupported mode (apply-only/delete-only).
3. Exit non-zero on unrecoverable failures.
4. Keep output operator-readable.
5. Be idempotent where practical.

## Makefile Operator API

Key runtime-intent targets:
- `make platform-certs-staging|platform-certs-prod [DRY_RUN=true]`
  - Updates `CERT_ISSUER` in `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (local edit only).
- `make cilium-bootstrap`
  - Applies `bootstrap/k3s-manifests/cilium-helmchart.yaml` and waits for Cilium readiness before Flux bootstrap/reconcile.
- `make runtime-inputs-sync`
  - Syncs external source secret `flux-system/platform-runtime-inputs` from local env/profile.
- `make flux-reconcile`
  - Syncs runtime inputs then reconciles Flux source + stack.
- `make otel-collectors-restart`
  - Restarts `logging/otel-k8s-cluster-opentelemetry-collector` and `logging/otel-k8s-daemonset-opentelemetry-collector-agent`.
- `make runtime-inputs-refresh-otel`
  - Runs `flux-reconcile` plus collector restarts so rotated ClickStack ingestion keys are loaded by running OTel pods.

Design boundary:
- Runtime-input env vars are consumed only for runtime secrets (`oauth2-proxy` + ClickStack/OTel keys).
- Platform cert issuer mode is configmap-driven (`CERT_ISSUER`); app issuer/host intent is manifest-defined in app overlays.

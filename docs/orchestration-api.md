# Orchestration API

Last updated: 2026-02-28

This document defines the current command and environment contract for orchestration entrypoints.

## Cluster Orchestrator (`run.sh`)

Usage:

```bash
./run.sh [--profile FILE] [--only N[,N...]] [--dry-run] [--delete]
```

Options:
- `--profile FILE`: load additional env vars (highest precedence in cluster config layering). Prefer explicit Makefile mode targets for common runtime intent; use `--profile` for ad-hoc overrides.
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
2. `94_verify_config_inputs.sh` (when `FEAT_VERIFY=true`)
3. `bootstrap/install-flux.sh`
4. `bootstrap/sync-runtime-inputs.sh`
5. `32_reconcile_flux_stack.sh`
6. `91_verify_platform_state.sh` (when `FEAT_VERIFY=true`)

Default delete steps:
1. `15_verify_cluster_access.sh`
2. `32_reconcile_flux_stack.sh --delete`
3. `30_manage_cert_manager_cleanup.sh --delete`
4. `99_execute_teardown.sh --delete`
5. `26_manage_cilium_lifecycle.sh --delete` (when `FEAT_CILIUM=true`)
6. `bootstrap/install-flux.sh --delete`
7. `98_verify_teardown_clean.sh --delete`

Notes:
- Runtime input target secrets are declarative in `platform-services/runtime-inputs`.
- Source secret `flux-system/platform-runtime-inputs` is external and synced by `scripts/bootstrap/sync-runtime-inputs.sh`.
- Shared infrastructure/platform composition is fixed to `infrastructure/overlays/home` and `platform-services/overlays/home`.
- Platform cert issuer intent is Git-managed in `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (`CERT_ISSUER`).
- App deployment URL/issuer intent is path-based in `clusters/home/tenants.yaml` (`./tenants/overlays/app-*-le-*`).

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
2. `host/tasks/20_install_k3s.sh`

Default host delete tasks:
1. `host/tasks/20_install_k3s.sh --delete`
2. `host/tasks/10_dynamic_dns.sh --delete`

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
- `make app-test-staging-le-staging|app-test-staging-le-prod|app-test-prod-le-staging|app-test-prod-le-prod [DRY_RUN=true]`
  - Copies declarative mode templates from `clusters/home/modes/` into `clusters/home/tenants.yaml` (local edit only).

Mode target contract:
- Mode targets do not reconcile directly; commit + push first, then run `make flux-reconcile`.

Design boundary:
- Runtime-input env vars are consumed only for runtime secrets (`oauth2-proxy` + ClickStack/OTel keys).
- Platform cert issuer mode is configmap-driven (`CERT_ISSUER`); app URL/issuer mode remains path-selected in `clusters/home/tenants.yaml`.

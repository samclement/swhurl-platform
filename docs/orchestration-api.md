# Orchestration API

Last updated: 2026-02-26

This document defines the command and environment contract for orchestration entrypoints.

## Cluster Orchestrator (`run.sh`)

Usage:

```bash
./run.sh [--profile FILE] [--host-env FILE] [--only N[,N...]] [--dry-run] [--delete] [--with-host]
```

Options:

- `--profile FILE`: load additional env vars (highest precedence in cluster config layering).
- `--host-env FILE`: pass host-layer env overrides through to `./host/run-host.sh` when host layer is enabled.
- `--only LIST`: comma-separated step numbers or basenames (for example `31,36` or `31_sync_helmfile_phase_core.sh`).
- `--dry-run`: print resolved plan and exit without executing.
- `--delete`: execute delete flow (reverse lifecycle), passing `--delete` to delete-capable steps.
- `--with-host`: include host orchestration (`./host/run-host.sh`) before apply or after delete.

Environment controls:

- `RUN_HOST_LAYER=true|false`: default for host-layer inclusion (equivalent to `--with-host`).
- `ONLY`: fallback filter when `--only` is not provided.
- `PROFILE_EXCLUSIVE=true|false`: if `true`, skip auto-loading `profiles/local.env` and `profiles/secrets.env`.
- `FEAT_VERIFY=true|false`: include/exclude core verification steps.
- `FEAT_VERIFY_DEEP=true|false`: include/exclude deep verification steps (auto-disabled when `FEAT_VERIFY=false`).

Config layering (`run.sh`):

1. `config.env`
2. `profiles/local.env` (unless `PROFILE_EXCLUSIVE=true`)
3. `profiles/secrets.env` (unless `PROFILE_EXCLUSIVE=true`)
4. `--profile FILE` (`PROFILE_FILE`, highest precedence)

As-of-step contract (default apply):

1. `01_check_prereqs.sh`
2. `15_verify_cluster_access.sh`
3. `94_verify_config_inputs.sh` (when `FEAT_VERIFY=true`)
4. `25_prepare_helm_repositories.sh`
5. `20_reconcile_platform_namespaces.sh`
6. `26_manage_cilium_lifecycle.sh` (when `FEAT_CILIUM=true`)
7. `31_sync_helmfile_phase_core.sh`
8. `36_sync_helmfile_phase_platform.sh`
9. `75_manage_sample_app_lifecycle.sh`
10. `91_verify_platform_state.sh` (when `FEAT_VERIFY=true`)
11. `92_verify_helmfile_drift.sh` (when `FEAT_VERIFY=true`)
12. `90/93/95/96/97` deep checks (when `FEAT_VERIFY_DEEP=true`)

As-of-step contract (default delete):

1. `15_verify_cluster_access.sh`
2. `75_manage_sample_app_lifecycle.sh --delete`
3. `36_sync_helmfile_phase_platform.sh --delete`
4. `31_sync_helmfile_phase_core.sh --delete`
5. `30_manage_cert_manager_cleanup.sh --delete`
6. `20_reconcile_platform_namespaces.sh --delete`
7. `99_execute_teardown.sh --delete`
8. `26_manage_cilium_lifecycle.sh --delete` (when `FEAT_CILIUM=true`)
9. `98_verify_teardown_clean.sh --delete`

Notes:

- Runtime input target secrets are reconciled declaratively via `cluster/base/runtime-inputs`; source secret `flux-system/platform-runtime-inputs` is synced via `scripts/bootstrap/sync-runtime-inputs.sh` / `make runtime-inputs-sync` and also supplies `ACME_EMAIL` to `homelab-issuers` via Flux post-build substitution.
- `run.sh` apply/delete plans still have no dedicated runtime-input step.
- Delete-time legacy runtime-input cleanup is owned by `scripts/99_execute_teardown.sh`.

## Host Orchestrator (`host/run-host.sh`)

Usage:

```bash
./host/run-host.sh [--host-env FILE] [--only N[,N...]] [--dry-run] [--delete]
```

Options:

- `--host-env FILE`: load host-specific env overrides (highest precedence).
- `--only LIST`: comma-separated task numbers or basenames.
- `--dry-run`: print host plan and exit.
- `--delete`: run host delete plan.

Config layering (`host/run-host.sh`):

1. `config.env`
2. `host/config/homelab.env`
3. `host/config/host.env`
4. `--host-env FILE` (highest precedence)

As-of-task contract (apply):

1. `host/tasks/00_bootstrap_host.sh`
2. `host/tasks/10_dynamic_dns.sh`
3. `host/tasks/20_install_k3s.sh`

As-of-task contract (delete):

1. `host/tasks/20_install_k3s.sh --delete`
2. `host/tasks/10_dynamic_dns.sh --delete`

## Script Step API Contract

The orchestrators invoke step scripts as executable files. Step scripts should follow this contract:

1. Parse `--delete` and treat it as delete mode.
2. Use apply mode by default when `--delete` is absent.
3. Exit non-zero on unrecoverable failures.
4. Keep output operator-readable (short status logs).
5. Be idempotent where practical.

Compatibility notes:

- `scripts/manual_install_k3s_minimal.sh` and `scripts/manual_configure_route53_dns_updater.sh` are compatibility wrappers into host-layer tasks.
- Runtime input targets are declarative in `cluster/base/runtime-inputs`; source secret updates are done with `scripts/bootstrap/sync-runtime-inputs.sh` / `make runtime-inputs-sync` (also updating `ACME_EMAIL` for issuer reconciliation).

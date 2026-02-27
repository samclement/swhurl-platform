# Orchestration API

Last updated: 2026-02-27

This document defines the current command and environment contract for orchestration entrypoints.

## Cluster Orchestrator (`run.sh`)

Usage:

```bash
./run.sh [--profile FILE] [--host-env FILE] [--only N[,N...]] [--dry-run] [--delete] [--with-host]
```

Options:
- `--profile FILE`: load additional env vars (highest precedence in cluster config layering), mainly for runtime-input/cert intent and feature toggles.
- `--host-env FILE`: pass host-layer env overrides through to `./host/run-host.sh` when host layer is enabled.
- `--only LIST`: comma-separated step numbers or basenames.
- `--dry-run`: print resolved plan and exit without executing.
- `--delete`: execute delete flow, passing `--delete` to delete-capable steps.
- `--with-host`: include host orchestration (`./host/run-host.sh`) before apply or after delete.

Environment controls:
- `RUN_HOST_LAYER=true|false`: default for host-layer inclusion.
- `ONLY`: fallback filter when `--only` is not provided.
- `PROFILE_EXCLUSIVE=true|false`: if `true`, skip auto-loading `profiles/local.env` and `profiles/secrets.env`.
- `FEAT_VERIFY=true|false`: include/exclude core verification steps.
- `FEAT_VERIFY_DEEP=true|false`: include/exclude deep verification steps (auto-disabled when `FEAT_VERIFY=false`).

Config layering (`run.sh`):
1. `config.env`
2. `profiles/local.env` (unless `PROFILE_EXCLUSIVE=true`)
3. `profiles/secrets.env` (unless `PROFILE_EXCLUSIVE=true`)
4. `--profile FILE` (`PROFILE_FILE`, highest precedence)

Default apply steps:
1. `01_check_prereqs.sh`
2. `15_verify_cluster_access.sh`
3. `94_verify_config_inputs.sh` (when `FEAT_VERIFY=true`)
4. `bootstrap/install-flux.sh`
5. `bootstrap/sync-runtime-inputs.sh`
6. `32_reconcile_flux_stack.sh`
7. `91_verify_platform_state.sh` (when `FEAT_VERIFY=true`)
8. `90/93/95/96` deep checks (when `FEAT_VERIFY_DEEP=true`)

Default delete steps:
1. `15_verify_cluster_access.sh`
2. `32_reconcile_flux_stack.sh --delete`
3. `30_manage_cert_manager_cleanup.sh --delete`
4. `99_execute_teardown.sh --delete`
5. `26_manage_cilium_lifecycle.sh --delete` (when `FEAT_CILIUM=true`)
6. `bootstrap/install-flux.sh --delete`
7. `98_verify_teardown_clean.sh --delete`

Notes:
- Runtime input target secrets are declarative in `cluster/base/runtime-inputs`.
- Source secret `flux-system/platform-runtime-inputs` is external and synced by `scripts/bootstrap/sync-runtime-inputs.sh`.
- Provider/app overlay selection is path-based in `cluster/overlays/homelab/flux/stack-kustomizations.yaml`; `--profile` does not switch those Flux kustomization paths.

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
1. `host/tasks/00_bootstrap_host.sh`
2. `host/tasks/10_dynamic_dns.sh`
3. `host/tasks/20_install_k3s.sh`

Default host delete tasks:
1. `host/tasks/20_install_k3s.sh --delete`
2. `host/tasks/10_dynamic_dns.sh --delete`

## Script Contract

Step scripts should:
1. Parse `--delete` and treat it as delete mode.
2. Use apply mode by default.
3. Exit non-zero on unrecoverable failures.
4. Keep output operator-readable.
5. Be idempotent where practical.

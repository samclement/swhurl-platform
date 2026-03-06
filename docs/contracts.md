# Contracts (Config, Tooling, Delete)

This repo is Flux-first. Bash scripts are used for orchestration glue, runtime-input sync, and verification.

## Environment Contract

All scripts load config via `scripts/00_lib.sh` with precedence:
1. `config.env`
2. `profiles/local.env`
3. `profiles/secrets.env`
4. `$PROFILE_FILE` (for example: `PROFILE_FILE=profiles/foo.env make install`)

Operational preference:
- Keep committed profile files minimal (`profiles/local.env` and `profiles/secrets.example.env`).
- Express runtime intent via Makefile args; use `PROFILE_FILE=...` for ad-hoc temporary overrides.

If `PROFILE_EXCLUSIVE=true`, only `config.env` and `$PROFILE_FILE` are loaded.

Variables are exported (`set -a`) so child commands (for example `flux`, `helm`, `kubectl`) see the resolved values.

## Required Contract Sources

Source-of-truth files:
- `scripts/00_verify_contract_lib.sh`

Verification toggles:
- `FEAT_VERIFY=true|false`: core checks (`94`, `91`)

Cert issuer mode controls:
- Git-tracked ConfigMap value in `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (`CERT_ISSUER`).

## Tool Contract

### Flux

Primary declarative control plane:
- Bootstrap manifests: `clusters/home/flux-system`
- Stack manifests: `clusters/home` (infrastructure/platform/tenants/app Flux Kustomizations)

Primary operations:
- `scripts/32_reconcile_flux_stack.sh`
  - apply mode: reconciles source/stack (requires bootstrap manifests already applied)
  - delete mode: stack-only teardown (deletes `homelab-flux-stack` and `homelab-flux-sources`)

### Helm

Used by platform components and optional/manual cleanup helpers.

### kubectl

Used for cluster access checks, manifest apply/delete, and runtime verification.

## Delete Contract

Delete flow contract:
1. Remove Flux stack kustomizations.
2. Let Flux prune stack-managed resources.
3. Keep Flux controllers and cluster-level services installed by default.

Legacy/manual cleanup scripts (`scripts/30_manage_cert_manager_cleanup.sh`, `scripts/99_execute_teardown.sh`, `scripts/98_verify_teardown_clean.sh`) are intentionally not part of default `make teardown`.

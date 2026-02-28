# Contracts (Config, Tooling, Delete)

This repo is Flux-first. Bash scripts are used for orchestration glue, runtime-input sync, and teardown/finalizer cleanup.

## Environment Contract

All scripts load config via `scripts/00_lib.sh` with precedence:
1. `config.env`
2. `profiles/local.env`
3. `profiles/secrets.env`
4. `$PROFILE_FILE` (`./run.sh --profile ...`)

Operational preference:
- Keep committed profile files minimal (`profiles/local.env` and `profiles/secrets.example.env`).
- Express runtime intent via Makefile args; use `--profile` for ad-hoc temporary overrides.

If `PROFILE_EXCLUSIVE=true`, only `config.env` and `$PROFILE_FILE` are loaded.

Variables are exported (`set -a`) so child commands (for example `flux`, `helm`, `kubectl`) see the resolved values.

## Required Contract Sources

Source-of-truth files:
- `scripts/00_feature_registry_lib.sh`
- `scripts/00_verify_contract_lib.sh`

Verification toggles:
- `FEAT_VERIFY=true|false`: core checks (`94`, `91`)
- `FEAT_VERIFY_DEEP=true|false`: deep checks (`90`, `93`, `95`, `96`)

Let’s Encrypt controls:
- `LETSENCRYPT_ENV=staging|prod|production`
- `LETSENCRYPT_STAGING_SERVER`
- `LETSENCRYPT_PROD_SERVER`

## Tool Contract

### Flux

Primary declarative control plane:
- Bootstrap manifests: `clusters/home/flux-system`
- Stack manifests: `clusters/home` (infrastructure/platform/tenants Flux Kustomizations)

Primary operations:
- `scripts/bootstrap/install-flux.sh`
- `scripts/32_reconcile_flux_stack.sh`

### Helm

Used for imperative bootstrap/cleanup helpers where needed:
- Cilium pre-Flux bootstrap (`scripts/26_manage_cilium_lifecycle.sh`)
- cert-manager cleanup during delete (`scripts/30_manage_cert_manager_cleanup.sh`)

### kubectl

Used for cluster access checks, manifest apply/delete, and runtime verification.

## Delete Contract

Delete flow contract:
1. Remove Flux stack kustomizations.
2. Run cleanup helpers for cert-manager/finalizers.
3. Sweep managed namespaces/secrets/CRDs.
4. Remove Cilium last.
5. Uninstall Flux controllers.
6. Verify clean teardown.

Delete scopes:
- `DELETE_SCOPE=managed` (default)
- `DELETE_SCOPE=dedicated-cluster` (aggressive, dedicated clusters only)

# Platform Runbook (Flux-First)

This repo is operated through Flux GitOps with optional script orchestration (`run.sh`).

## Standard Operations

### Bootstrap

```bash
make flux-bootstrap
```

Behavior:
- Installs Flux controllers.
- Applies `clusters/home/flux-system` bootstrap manifests.
- Auto-bootstraps Cilium first when no ready CNI exists.

### Reconcile

```bash
make flux-reconcile
```

Behavior:
- Syncs `flux-system/platform-runtime-inputs` from local config (`config.env` + `profiles/local.env` + `profiles/secrets.env`, plus optional ad-hoc `--profile` overrides).
- Reconciles `swhurl-platform` source, `homelab-flux-sources`, then `homelab-flux-stack`.

### Full apply via orchestrator

```bash
./run.sh
```

### Full delete via orchestrator

```bash
./run.sh --delete
```

Delete ordering is intentional:
1. Remove Flux stack kustomizations.
2. Clean cert-manager finalizers/CRDs.
3. Teardown namespaces/secrets/CRDs.
4. Remove Cilium last.
5. Uninstall Flux controllers.
6. Verify cleanup.

## Active Flux Dependency Chain

Parent level:
- `homelab-flux-sources -> homelab-flux-stack`

Cluster level (`clusters/home/*.yaml`):
- `homelab-infrastructure -> homelab-platform -> homelab-tenants`

Layer composition:
- `homelab-infrastructure` points to `infrastructure/overlays/home` (shared infra + issuer variants + runtime-input targets).
- `homelab-platform` points to `platform-services/overlays/home` (shared platform services).
- `homelab-tenants` points to `tenants` (tenant env namespaces + sample app).

## Runtime Inputs

Targets are declarative under:
- `infrastructure/runtime-inputs`

Source secret is external:
- `flux-system/platform-runtime-inputs`

Sync/update source secret:

```bash
make runtime-inputs-sync
```

## Verification

Core checks:
- `scripts/94_verify_config_inputs.sh`
- `scripts/91_verify_platform_state.sh`

Deep checks (opt-in):
- `scripts/90_verify_runtime_smoke.sh`
- `scripts/93_verify_expected_releases.sh`
- `scripts/95_capture_cluster_diagnostics.sh`
- `scripts/96_verify_orchestrator_contract.sh`

## Promotion / Profiles

- Platform cert issuer mode is controlled by `PLATFORM_CLUSTER_ISSUER` and runtime-input sync.
- Sample app host/issuer/namespace intent is controlled by runtime-input vars (`APP_HOST`, `APP_CLUSTER_ISSUER`, `APP_NAMESPACE`) via Makefile args + sync/reconcile.
- Provider selection is controlled by composition entries in `infrastructure/overlays/home/kustomization.yaml`.

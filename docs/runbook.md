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
- `homelab-infrastructure` points to one of:
  - `infrastructure/overlays/home`
  - `infrastructure/overlays/home-letsencrypt-prod`
- `homelab-platform` points to one of:
  - `platform-services/overlays/home`
  - `platform-services/overlays/home-letsencrypt-prod`
- `homelab-tenants` points to one of `tenants/overlays/app-*-le-*` (tenant env namespaces + sample app mode).

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

- Infrastructure cert issuer mode is path-based via `clusters/home/infrastructure.yaml`:
  - `./infrastructure/overlays/home` (staging)
  - `./infrastructure/overlays/home-letsencrypt-prod` (prod)
- Platform-services cert issuer mode (`oauth2-proxy`, `clickstack`) is path-based via `clusters/home/platform.yaml`:
  - `./platform-services/overlays/home` (staging)
  - `./platform-services/overlays/home-letsencrypt-prod` (prod)
- Sample app URL/issuer mode is path-based via `clusters/home/tenants.yaml`:
  - `./tenants/overlays/app-staging-le-staging`
  - `./tenants/overlays/app-staging-le-prod`
  - `./tenants/overlays/app-prod-le-staging`
  - `./tenants/overlays/app-prod-le-prod`
- Provider selection is controlled by composition entries in `infrastructure/overlays/home/kustomization.yaml`.

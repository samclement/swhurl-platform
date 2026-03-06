# Orchestration API

Last updated: 2026-03-06

This document defines the current command and environment contract for orchestration entrypoints.

## Cluster Orchestration (Makefile-first)

Preferred entrypoints:
- `make install [DRY_RUN=true]`
- `make teardown [DRY_RUN=true]`
- `make reinstall`

Environment controls:
- `PROFILE_FILE=/path/to/profile.env` (highest-precedence config layer for scripts via `scripts/00_lib.sh`)
- `PROFILE_EXCLUSIVE=true|false`
- `FEAT_VERIFY=true|false` (only active feature switch)

Default apply flow (`make install`):
1. `make verify-config` (when `FEAT_VERIFY=true`)
2. `make flux-reconcile`
3. `make verify-platform` (when `FEAT_VERIFY=true`)

Default delete flow (`make teardown`):
1. `32_reconcile_flux_stack.sh --delete`
2. `30_manage_cert_manager_cleanup.sh --delete`
3. `99_execute_teardown.sh --delete`
4. `98_verify_teardown_clean.sh --delete`

State contracts:
- Flux CLI/controller installation is manual and documented in `README.md`.
- Bootstrap manifests must be applied first (`make flux-bootstrap`) before reconcile/apply flows.
- Runtime input target secrets are declarative in `platform-services/runtime-inputs`.
- Source secret `flux-system/platform-runtime-inputs` is external and synced by `scripts/bootstrap/sync-runtime-inputs.sh`.
- Shared infrastructure/platform composition is fixed to `infrastructure/overlays/home` and `platform-services/overlays/home`.
- k3s-packaged Traefik config is managed in `infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml` (NodePorts `31514`/`30313`).
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
- `make install [DRY_RUN=true]`
  - Runs optional verification (`FEAT_VERIFY`), runtime-input sync, and Flux reconcile.
- `make teardown [DRY_RUN=true]`
  - Runs Flux stack delete/uninstall, cert-manager cleanup, teardown cleanup, and delete verification.
- `make reinstall`
  - Runs `make teardown` then `make install`.
- `make platform-certs-staging|platform-certs-prod [DRY_RUN=true]`
  - Updates `CERT_ISSUER` in `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (local edit only).
- `make runtime-inputs-sync`
  - Syncs external source secret `flux-system/platform-runtime-inputs` from local env/profile.
- `make flux-reconcile`
  - Syncs runtime inputs then reconciles Flux source + stack.
- `make otel-collectors-restart`
  - Restarts `logging/otel-k8s-cluster-opentelemetry-collector` and `logging/otel-k8s-daemonset-opentelemetry-collector-agent`.
- `make runtime-inputs-refresh-otel`
  - Syncs runtime inputs, reconciles `homelab-platform`, waits for `logging/hyperdx-secret` propagation, then restarts collectors so rotated ClickStack ingestion keys are loaded by running OTel pods.

Design boundary:
- Runtime-input env vars are consumed only for runtime secrets (`oauth2-proxy-shared` and ClickStack/OTel keys in active composition).
- Platform cert issuer mode is configmap-driven (`CERT_ISSUER`); app issuer/host intent is manifest-defined in app overlays.

# Orchestration API

Last updated: 2026-03-06

This document defines the current command and environment contract for orchestration entrypoints.

## Environment Contract

Config source: `config.env` (included by Makefile).

Runtime cluster secrets are Git-managed via SOPS in `clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml` (not sourced from local env at reconcile time).

## Delete Contract

1. Remove Flux stack kustomizations.
2. Let Flux prune stack-managed resources.
3. Keep Flux controllers and cluster-level services installed by default.

## Cluster Orchestration (Makefile-first)

Preferred entrypoints:
- `make install [DRY_RUN=true]`
- `make teardown [DRY_RUN=true]`
- `make reinstall`

Environment controls:
- `FEAT_VERIFY=true|false` (only active feature switch, default: true)

Default apply flow (`make install`):
1. `make verify-config` (when `FEAT_VERIFY=true`)
2. `make flux-reconcile`
3. `make verify-platform` (when `FEAT_VERIFY=true`)

Default delete flow (`make teardown`):
1. Delete `homelab-flux-stack` and `homelab-flux-sources` kustomizations

State contracts:
- Flux CLI/controller installation is manual and documented in `README.md`.
- Bootstrap manifests must be applied first (`make flux-bootstrap`) before reconcile/apply flows.
- Runtime input target secrets are declarative in `platform-services/runtime-inputs`.
- Source secret is Git-managed and SOPS-encrypted in `clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml`, then decrypted/applied by `homelab-flux-sources`.
- Flux decryption key secret must exist in-cluster as `flux-system/sops-age`.
- Shared infrastructure/platform composition is fixed to `infrastructure/overlays/home` and `platform-services/overlays/home`.
- k3s-packaged Traefik config is managed in `infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml` (NodePorts `31514`/`30313`).
- Platform cert issuer intent is Git-managed in `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (`CERT_ISSUER`).
- Tenant environments are fixed in `clusters/home/tenants.yaml` (`./tenants/app-envs`).
- Example app deployment intent is fixed in `clusters/home/app-example.yaml` (`./tenants/apps/example`, staging+prod overlays).

## Host Dynamic DNS (`host/dynamic-dns.sh`)

Usage:

```bash
make host-dns [DRY_RUN=true]
make host-dns-delete [DRY_RUN=true]
```

Config is in `config.env` (`DYNAMIC_DNS_RECORDS`). Override `AWS_ZONE_ID` or `AWS_PROFILE` via environment if needed.

Manual prerequisite:
- k3s installation is manual and documented in `README.md`.

## Makefile Operator API

Key runtime-intent targets:
- `make install [DRY_RUN=true]`
  - Runs optional verification (`FEAT_VERIFY`) and Flux reconcile.
- `make teardown [DRY_RUN=true]`
  - Runs stack-only teardown by deleting `homelab-flux-stack` and `homelab-flux-sources`.
- `make reinstall`
  - Runs `make teardown` then `make install`.
- `make platform-certs-staging|platform-certs-prod [DRY_RUN=true]`
  - Updates `CERT_ISSUER` in `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (local edit only).
- `make runtime-inputs-sync`
  - Reconciles `homelab-flux-sources` so pushed Git-managed runtime input secret updates are applied.
- `make flux-reconcile`
  - Reconciles Flux source + stack.
- `make otel-collectors-restart`
  - Restarts `logging/otel-k8s-cluster-opentelemetry-collector` and `logging/otel-k8s-daemonset-opentelemetry-collector-agent`.
- `make runtime-inputs-refresh-otel`
  - Reconciles runtime inputs and `homelab-platform`, waits for `logging/hyperdx-secret` propagation, then restarts collectors so rotated ClickStack ingestion keys are loaded by running OTel pods.
- `make charts-generate`
  - Renders C4 architecture charts from `docs/charts/c4/*.d2` to `docs/charts/c4/rendered/*.svg`.
- `make host-dns [DRY_RUN=true]`
  - Configures the host dynamic DNS updater systemd service/timer.
- `make host-dns-delete [DRY_RUN=true]`
  - Removes host-managed dynamic DNS systemd service/timer.

Design boundary:
- Runtime-input secrets are Git-managed through SOPS (`platform-runtime-inputs` source -> `platform-services/runtime-inputs` targets).
- Platform cert issuer mode is configmap-driven (`CERT_ISSUER`); app issuer/host intent is manifest-defined in app overlays.

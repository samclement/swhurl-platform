# Orchestration API

Last updated: 2026-09-27

This document defines the current command and environment contract for orchestration entrypoints.

## Environment Contract

Config source: `config.env` (included by Makefile).

Runtime cluster secrets are Git-managed as final SOPS Secret manifests next to the platform service that consumes them (not sourced from local env at reconcile time).

## Delete Contract

`make teardown` and `make reinstall` are disabled and exit nonzero without invoking cluster tools; `DRY_RUN=true` reports the guard. Lifecycle is explicit instead: `make suspend|resume TARGET=...` and `make destroy-data TARGET=... CONFIRM=...`, the only data-deleting command. Shared Flux units use `deletionPolicy: Orphan`. See [lifecycle operations](runbook.md#lifecycle-operations).

## Cluster Orchestration (Makefile-first)

Preferred entrypoints:
- `make validate-repo`
- `make install [DRY_RUN=true]`
- `make teardown [DRY_RUN=true]`
- `make reinstall`

Environment controls:
- `FEAT_VERIFY=true|false` (only active feature switch, default: true)

Default apply flow (`make install`):
1. `make verify-config` (when `FEAT_VERIFY=true`)
2. `make flux-reconcile`
3. `make verify-platform` (when `FEAT_VERIFY=true`)

Routine deployment is commit, push, and reconciliation. Deletion is not part of this flow.

`make validate-repo` calls `scripts/validate-repo.py`. It discovers render entrypoints from the active Flux `spec.path` definitions, adds the two bootstrap paths, checks each shell file separately, verifies SOPS Secret structure and required substitutions, and validates rendered resources with Flux Schema. It never contacts the cluster. See [validation prerequisites](INFRASTRUCTURE.md#repository-validation).

State contracts:
- Flux CLI/controller installation is manual and documented in `README.md`.
- Bootstrap manifests must be applied first (`make flux-bootstrap`) before reconcile/apply flows.
- Runtime input target secrets are SOPS-encrypted final Secret manifests in service bases under `platform-services/*/base`.
- Flux decryption key secret must exist in-cluster as `flux-system/sops-age`.
- Shared infrastructure/platform composition is fixed to `infrastructure/overlays/home` and `platform-services/overlays/home`.
- k3s-packaged Traefik config is managed in `infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml` (NodePorts `31514`/`30313`).
- Platform cert issuer intent is Git-managed in `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (`CERT_ISSUER`).
- Shared oauth callback host intent is Git-managed in `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (`OAUTH_HOST`).
- Tenant environments are fixed in `clusters/home/tenants.yaml` (`./tenants/app-envs`).
- Example app deployment intent is fixed in `clusters/home/app-example.yaml` (`./tenants/apps/example`, staging+prod overlays).

## Host Dynamic DNS (`host/dynamic-dns.sh`)

Usage:

```bash
make host-dns [DRY_RUN=true]
make host-dns-delete [DRY_RUN=true]
```

Config is in `config.env` (`DYNAMIC_DNS_RECORDS`). Override `AWS_ZONE_ID` or `AWS_PROFILE` via environment if needed.

`make host-dns` writes the effective DNS environment to `/etc/swhurl-platform/dynamic-dns.env`; the systemd service reads that file on timer runs. Rerun `make host-dns` after changing DNS config.

Manual prerequisite:
- k3s installation is manual and documented in `README.md`.

## Makefile Operator API

Key runtime-intent targets:
- `make install [DRY_RUN=true]`
  - Runs optional verification (`FEAT_VERIFY`) and Flux reconcile.
- `make teardown [DRY_RUN=true]`
  - Disabled: exits nonzero without cluster operations; dry-run reports the guard.
- `make reinstall`
  - Disabled with the same guard as teardown; dry-run is supported.
- `make platform-certs-staging|platform-certs-prod [DRY_RUN=true]`
  - Updates `CERT_ISSUER` in `clusters/home/flux-system/sources/configmap-platform-settings.yaml` (local edit only).
- `make runtime-inputs-sync`
  - Reconciles `homelab-platform` so pushed Git-managed runtime input Secret updates are applied.
- `make flux-reconcile`
  - Reconciles Flux source + stack.
- `make otel-collectors-restart`
  - Restarts `logging/otel-k8s-cluster-opentelemetry-collector` and `logging/otel-k8s-daemonset-opentelemetry-collector-agent`.
- `make runtime-inputs-refresh-otel`
  - Reconciles runtime inputs and `homelab-platform`, waits for `logging/hyperdx-secret` propagation, then restarts collectors so rotated ClickStack UI ingestion keys are loaded by running OTel pods.
- `make suspend|resume TARGET=kustomization/<name>|helmrelease/<ns>/<name> [DRY_RUN=true]`
  - Wraps `flux suspend|resume`; workloads and data are untouched.
- `make destroy-data TARGET=pvc/<ns>/<name>|pv/<name> CONFIRM=<TARGET> [DRY_RUN=true]`
  - Deletes an unused, unmanaged claim or `Released` PV and its host data. Refuses without an exact `CONFIRM`.
- `make lifecycle-test [DRY_RUN=true]`
  - Runs the disposable lifecycle proof described in the [runbook](runbook.md#lifecycle-operations).
- `make backup-clickstack-mongodb [DRY_RUN=true]`
  - Streams a `hyperdx` MongoDB dump through `age` into `~/.local/state/swhurl-platform/backups` (read-only on the cluster), then prunes to 7 daily + 4 weekly backups. Manual only; there is no schedule.
- `make restore-test-clickstack-mongodb [DRY_RUN=true]`
  - Restores the latest backup and the Git-managed `hyperdx-secret` into a disposable `recovery-test` namespace, checks them, then deletes the namespace. See [runbook recovery](runbook.md#recovery).
- `make validate-repo`
  - Runs the same local manifest and shell validation as CI.
- `make charts-generate`
  - Renders C4 architecture charts from `docs/charts/c4/*.d2` to `docs/charts/c4/rendered/*.svg`.
- `make host-dns [DRY_RUN=true]`
  - Configures the host dynamic DNS updater systemd service/timer.
- `make host-dns-delete [DRY_RUN=true]`
  - Removes host-managed dynamic DNS systemd service/timer.

Design boundary:
- Runtime-input secrets are Git-managed directly as SOPS Secret manifests co-located with their consuming platform service.
- Platform cert issuer mode is configmap-driven (`CERT_ISSUER`); app issuer/host intent is manifest-defined in app overlays.

`make verify-platform` checks Flux health, retention (30-day telemetry TTL, 7-day ClickHouse system log TTL, MongoDB PV `Retain` and PVC keep annotation) and compares the once-decoded ingestion Secret with the unique ClickStack team key without printing credentials. It fails closed on lookup/decode errors, ambiguous team keys, or mismatches. Collector logs and fresh telemetry provide the separate delivery check. `make test-safety` tests the lifecycle guards and verifier offline with fake cluster commands, and checks that shared sign-in stays restricted to the approved email list.

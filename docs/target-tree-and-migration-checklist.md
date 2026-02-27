# Target Tree and Migration Checklist

## Scope

This document defines:

1. A concrete target repository tree.
2. A phased migration sequence.
3. A direct mapping from current files to target locations/ownership.

It is the implementation companion to `docs/homelab-intent-and-design.md`.

## Current Status Snapshot (2026-02-26)

- Phase 1 (Host Layer Introduction): complete (Bash-based host ownership)
  - `host/` task/lib structure is in place.
  - Legacy manual host scripts are compatibility wrappers.
- Phase 2 (GitOps Bootstrap): complete
  - `cluster/flux/*` and bootstrap helper exist.
  - Flux dependency chain is active in `cluster/overlays/homelab/flux/stack-kustomizations.yaml`.
- Phase 3/4 (Core + Platform GitOps Migration): complete for default provider path
  - Component-level Flux `HelmRelease` resources are defined and active for:
    `cilium`, `cert-manager`, `platform-issuers`, `oauth2-proxy`, `clickstack`,
    `otel-k8s-daemonset`, `otel-k8s-cluster`, `minio`, and `hello-web`.
  - Default homelab composition is explicit in `cluster/overlays/homelab/kustomization.yaml`
    (ingress-nginx + minio + staging app overlay).
  - Cert-manager issuer contract is explicit and always renders:
    `selfsigned`, `letsencrypt-staging`, `letsencrypt-prod`, and `letsencrypt` alias.
- Phase 5 (App + Contract Migration): mostly complete
  - Sample app has staging/prod namespace overlays with staging as the default deployed path.
  - Platform components deploy once in their usual namespaces and use a single
    runtime-configured issuer toggle (`PLATFORM_CLUSTER_ISSUER`), while user apps
    keep staging/prod overlay promotion paths.
  - Deep verification inventory now validates Flux stack/source health in `scripts/93_verify_expected_releases.sh` (with Helm inventory fallback for non-Flux compatibility mode).
- Phase 6 (Legacy Retirement): in progress
  - Legacy script orchestration remains available as compatibility mode.
  - Runtime secret targets are declarative in `cluster/base/runtime-inputs` via Flux (`homelab-runtime-inputs`).
  - Legacy bridge script `scripts/29_prepare_platform_runtime_inputs.sh` is retired.
  - Legacy runtime input cleanup on delete is owned by `scripts/99_execute_teardown.sh`.
  - Remaining major migration item is provider default/promotion completion (Traefik/Ceph path).

Provider migration status:
  - Provider intent flags (`INGRESS_PROVIDER`, `OBJECT_STORAGE_PROVIDER`) are wired into
    Helmfile gating, values, and verification.
  - NGINX/MinIO provider overlays are active by default.
  - Traefik/Ceph overlays and runbooks remain the planned migration targets.

## Target Technology Boundaries

Use a two-layer model:

1. Host layer (imperative, idempotent): Bash modules/tasks
2. Cluster layer (declarative, continuously reconciled): GitOps (FluxCD recommended)

Guiding rule:

- Host bootstraps host concerns (packages, systemd timers, k3s install/config).
- Cluster layer owns Kubernetes resources and sequencing (`dependsOn` graph), not bash step ordering.

## Target Repository Tree

```text
.
├─ host/
│  ├─ config/
│  │  ├─ homelab.env
│  │  └─ host.env.example
│  ├─ lib/
│  │  ├─ 00_common.sh
│  │  ├─ 10_packages_lib.sh
│  │  ├─ 20_dynamic_dns_lib.sh
│  │  └─ 30_k3s_lib.sh
│  ├─ tasks/
│  │  ├─ 00_bootstrap_host.sh
│  │  ├─ 10_dynamic_dns.sh
│  │  └─ 20_install_k3s.sh
│  ├─ templates/
│  │  └─ systemd/
│  │     ├─ dynamic-dns.service.tmpl
│  │     └─ dynamic-dns.timer.tmpl
│  └─ run-host.sh
│
├─ cluster/
│  ├─ flux/
│  │  ├─ gotk-components.yaml
│  │  ├─ gotk-sync.yaml
│  │  └─ sources/
│  │     ├─ helmrepositories.yaml
│  │     └─ gitrepositories.yaml
│  ├─ base/
│  │  ├─ namespaces/
│  │  ├─ cert-manager/
│  │  ├─ cilium/
│  │  ├─ oauth2-proxy/
│  │  ├─ clickstack/
│  │  ├─ otel/
│  │  ├─ storage/
│  │  │  ├─ minio/
│  │  │  └─ ceph/
│  │  └─ apps/
│  │     └─ example/
│  └─ overlays/
│     └─ homelab/
│        ├─ kustomization.yaml
│        ├─ platform/
│        │  ├─ staging/
│        │  └─ prod/
│        ├─ providers/
│        │  ├─ ingress-traefik/
│        │  ├─ ingress-nginx/
│        │  ├─ storage-minio/
│        │  └─ storage-ceph/
│        ├─ apps/
│        │  ├─ staging/
│        │  └─ prod/
│        └─ values/
│           ├─ domain.yaml
│           ├─ tls.yaml
│           └─ features.yaml
│
├─ docs/
│  ├─ homelab-intent-and-design.md
│  ├─ target-tree-and-migration-checklist.md
│  ├─ adr/
│  │  ├─ 0001-ingress-provider-strategy.md
│  │  └─ 0002-storage-provider-strategy.md
│  └─ runbooks/
│     ├─ migrate-ingress-nginx-to-traefik.md
│     └─ migrate-minio-to-ceph.md
│
├─ scripts/
│  ├─ compat/
│  │  └─ verify-legacy-contracts.sh
│  └─ bootstrap/
│     └─ install-flux.sh
│
├─ config/
│  ├─ platform.env.example
│  └─ profiles/
│     ├─ local.env
│     └─ secrets.env.example
└─ Makefile
```

Notes:

- Keep a temporary `scripts/compat/` area while migrating so existing workflows continue.
- Keep one primary templating mechanism in cluster definitions. Avoid stacking Helmfile gotmpl + Kustomize env substitution + ad-hoc bash templating together.

## Migration Phases

## Phase 0: Freeze and Baseline

Outcomes:

- Current pipeline still works.
- Baseline verification output is captured for regression comparison.

Tasks:

1. Record expected release/state inventory from current environment.
2. Freeze disruptive structural refactors while migration scaffolding is added.

## Phase 1: Host Layer Introduction (No Cluster Behavior Change)

Outcomes:

- Host package install, dynamic DNS setup, and k3s install become host Bash-managed (under `host/`).
- Existing `run.sh` cluster path remains unchanged.

Tasks:

1. Add `host/` with `lib/` + `tasks/` + `templates/` modules (`packages`, `dynamic-dns`, `k3s`).
2. Move logic from manual host scripts into host task scripts and shared libs.
3. Keep thin wrappers in old script paths that call `host/run-host.sh` phases.

## Phase 2: GitOps Bootstrap

Outcomes:

- Flux installed and syncing from this repo.
- Core namespaces and chart sources managed declaratively by Flux.

Tasks:

1. Add `cluster/flux/*` bootstrap manifests.
2. Add chart source definitions (`HelmRepository`) in GitOps layer.
3. Keep Helmfile pipeline active as fallback until parity checks pass.

## Phase 3: Core Platform Migration

Outcomes:

- cert-manager, issuers, and ingress provider are reconciled through GitOps.
- Ingress provider switch (`traefik` preferred) is overlay-driven.

Tasks:

1. Move core components from Helmfile into Flux `HelmRelease`/Kustomization graph.
2. Introduce provider overlays:
   - `providers/ingress-traefik`
   - `providers/ingress-nginx` (temporary compatibility)
3. Add migration runbook for nginx -> traefik.

## Phase 4: Platform Services Migration

Outcomes:

- oauth2-proxy, clickstack, otel, and storage provider managed by GitOps.
- Storage provider switch introduced (`minio` compatibility + `ceph` target).

Tasks:

1. Migrate `phase=platform` releases into GitOps.
2. Introduce storage overlays:
   - `providers/storage-minio`
   - `providers/storage-ceph`
3. Add runbook for minio -> ceph migration and data movement validation.

## Phase 5: App and Contracts

Outcomes:

- Example app demonstrates full platform integration path.
- Verification contracts adapted to GitOps ownership model.

Tasks:

1. Expand example app to include TLS, OIDC edge auth, telemetry, and object storage usage.
2. Replace legacy release-inventory checks with GitOps object/health checks.
3. Keep teardown safety gates for shared-cluster protection.

## Phase 6: Legacy Script Retirement

Outcomes:

- `run.sh` becomes compatibility/developer helper only, or is removed.
- Bash orchestration complexity is significantly reduced.

Tasks:

1. Move remaining reusable script logic into libraries or dedicated tools.
2. Archive or remove script phases after parity period.
3. Keep docs and ADRs as the source of truth for provider decisions.

## Current to Target Mapping

| Current path | Target ownership | Target path |
|---|---|---|
| `scripts/manual_install_k3s_minimal.sh` | Host Bash module | `host/tasks/20_install_k3s.sh` + `host/lib/30_k3s_lib.sh` |
| `scripts/manual_configure_route53_dns_updater.sh` | Host Bash module | `host/tasks/10_dynamic_dns.sh` + `host/lib/20_dynamic_dns_lib.sh` |
| `scripts/aws-dns-updater.sh` | Host Bash helper/template input | `host/templates/systemd/` + `host/lib/20_dynamic_dns_lib.sh` |
| `scripts/01_check_prereqs.sh` | Host validation + CI task | `host/tasks/00_bootstrap_host.sh` + `Makefile` target |
| `run.sh` | Legacy/developer orchestrator (current compatibility entrypoint) | `run.sh` |
| `helmfile.yaml.gotmpl` | GitOps definitions | `cluster/base/*` + `cluster/overlays/homelab/*` |
| `infra/values/*.yaml*` | HelmRelease/Kustomize values | `cluster/overlays/homelab/values/` + component dirs |
| `charts/platform-namespaces` | GitOps base manifests | `cluster/base/namespaces/` |
| `charts/platform-issuers` | GitOps cert-manager config | `cluster/base/cert-manager/issuers/` |
| `charts/apps-hello` | GitOps app example | `cluster/base/apps/example/` |
| `scripts/31_sync_helmfile_phase_core.sh` | GitOps dependency graph | `cluster/base/*` + overlay dependencies |
| `scripts/36_sync_helmfile_phase_platform.sh` | GitOps dependency graph | `cluster/base/*` + overlay dependencies |
| `scripts/29_prepare_platform_runtime_inputs.sh` | Retired legacy bridge | `cluster/base/runtime-inputs/` |
| `scripts/9*_verify_*.sh` | GitOps health + policy checks | `scripts/compat/verify-legacy-contracts.sh` then CI policy checks |

## Sequencing in the Target Model

Use dependency graph, not numeric script names:

1. `namespaces`
2. `cilium` and ingress provider baseline
3. `cert-manager`
4. `issuers`
5. `oauth2-proxy`
6. `clickstack`
7. `otel`
8. storage provider
9. example app

Implement with Flux `dependsOn` between Kustomizations/HelmReleases and health checks per layer.

## Guardrails During Migration

1. Do not migrate ingress and storage providers simultaneously.
2. Keep rollback path documented for each phase.
3. Maintain parity tests before retiring legacy scripts.
4. Keep one owner for sequencing logic at a time (either legacy scripts or GitOps graph, not both as co-owners).
5. Enforce docs-as-contract: update runbook + mapping table in the same change when moving ownership.

## Host Bash Conventions (to keep maintainability)

1. Keep host scripts modular:
- `host/lib/*_lib.sh` for reusable functions.
- `host/tasks/*.sh` for runnable steps.
- `host/run-host.sh` as the only host orchestrator entrypoint.

2. Keep task ordering explicit:
- Use numeric task prefixes and a static plan in `host/run-host.sh` (same pattern as current `run.sh`).

3. Keep scripts idempotent:
- Safe on rerun, `--delete` aware where relevant, and with deterministic logging.

4. Keep shell quality high:
- `set -Eeuo pipefail`, `shellcheck`, and small focused functions.

5. Keep templating minimal:
- Prefer plain `.env` + literal YAML/manifests.
- Use one templating layer per concern; avoid nested interpolation chains.

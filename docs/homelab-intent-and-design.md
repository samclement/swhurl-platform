# Homelab Intent and Design Direction

## Purpose

Keep this repo as the single declarative GitOps source for one homelab cluster with:
- shared infrastructure,
- shared platform services,
- separate app environments (`staging`, `prod`).

## Design Boundaries

Separate these axes and do not collapse them:

1. Cluster scope: `home`
2. Shared platform cert mode: `CERT_ISSUER` (`letsencrypt-staging|letsencrypt-prod`)
3. App environment scope: `staging|prod`
4. App certificate issuer choice: app overlay/manifests

## Repository Layering

- `clusters/home/`
  - Flux entrypoint and dependency ordering
  - cluster-level operator switches (`platform-settings` ConfigMap)
- `infrastructure/`
  - cluster-shared infra controllers and providers
  - cert-manager + issuer manifests
- `platform-services/`
  - cluster-shared services (`oauth2-proxy`, `clickstack`, `otel`)
  - runtime-input target secrets (`platform-services/runtime-inputs`)
- `tenants/`
  - app environment namespaces (`apps-staging`, `apps-prod`)
  - sample app manifests and app overlays (`tenants/apps/example/overlays/*`)

## Runtime Inputs Principle

Use environment variables only for runtime secrets that must stay out of Git.

- Source secret: `flux-system/platform-runtime-inputs` (external, synced by script)
- Targets: declarative manifests under `platform-services/runtime-inputs`
- Non-secret mode controls stay Git-tracked in manifests/templates.

## Operational Model

- `make install` / `make teardown`: default cluster lifecycle.
- `make platform-certs-*`: edits `CERT_ISSUER` in Git.
- `make app-test-*-le-*`: edits `clusters/home/app-example.yaml` path in Git.
- After any mode target: commit + push, then `make flux-reconcile`.

## Simplification Rules

1. Prefer Kustomize path composition over imperative script branching.
2. Keep script surface small; scripts orchestrate, manifests define desired state.
3. Keep ownership singular (Flux manages all cluster resources in scope).
4. Keep docs aligned with active files; remove migration history once complete.

## Success Criteria

1. A fresh setup converges with documented commands and minimal manual edits.
2. Mode changes are transparent Git diffs and reconciled through Flux.
3. Layer boundaries are clear enough that missing app secrets never block infrastructure.
4. Delete/apply loops are deterministic and verifiable.

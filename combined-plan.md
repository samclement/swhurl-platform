# Plan: Simplify swhurl-platform with strict GitOps boundaries

## Context

The repo has overlay/mode duplication, stale docs, and a mode-switch workflow that can mislead operators (local edits + reconcile without push).

Confirmed constraints:
- Flux remote Git is source of truth.
- Non-secret mode controls must stay Git-tracked (not runtime secret driven).
- `CERT_ISSUER` is a single platform switch (shared infra + platform services).
- Apps keep both `letsencrypt-staging` and `letsencrypt-prod` options via app manifests/overlays.

---

## Step 1: Make `CERT_ISSUER` a Git-tracked platform switch

**Goal:** Remove platform issuer overlay duplication while keeping control declarative and Git-tracked.

**Approach:**
- Define `CERT_ISSUER` in a Git-managed ConfigMap (for example in `clusters/home/flux-system/sources/`).
- Use `${CERT_ISSUER}` in platform-scope manifests only:
  - `infrastructure/cilium/base/helmrelease-cilium.yaml`
  - `infrastructure/storage/minio/base/helmrelease-minio.yaml`
  - `platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy.yaml`
  - `platform-services/clickstack/base/helmrelease-clickstack.yaml`
- Add `postBuild.substituteFrom` (ConfigMap) to:
  - `clusters/home/infrastructure.yaml`
  - `clusters/home/platform.yaml`
- Keep tenants/app issuer selection path-based in app overlays (no tenant `CERT_ISSUER` substitution).

**Delete after cutover:**
- `infrastructure/overlays/home-letsencrypt-prod/`
- `platform-services/overlays/home-letsencrypt-prod/`
- obsolete infra/platform mode template files tied only to staging/prod path switching.

**Do not change in this step:**
- Tenant app issuer overlays (`app-*-le-*`) stay as-is.

---

## Step 2: Fix Makefile mode workflow for strict GitOps

**Goal:** Mode targets should make Git edits only and stop implying immediate cluster changes.

**Approach:**
- Deduplicate repeated `mode_sync_file()` logic in `Makefile`.
- Update mode targets:
  - `platform-certs-staging|platform-certs-prod` edit only the Git-tracked `CERT_ISSUER` source file.
  - app mode targets continue to update `clusters/home/tenants.yaml` path.
- Remove automatic `run.sh --only ...reconcile...` from mode targets.
- Print explicit message after mode target:
  - "Local Git edits only. Commit + push, then run `make flux-reconcile`."

**Also update:**
- `.github/workflows/validate.yml` target names and dry-run coverage.
- README/Runbook examples to match new target behavior.

---

## Step 3: Move runtime-input targets out of infrastructure

**Goal:** Missing app/platform secrets should not block infra reconciliation.

**Approach:**
- Move `infrastructure/runtime-inputs/` to `platform-services/runtime-inputs/`.
- Update composition:
  - Remove runtime-inputs from `infrastructure/overlays/home/kustomization.yaml`.
  - Add runtime-inputs to `platform-services/overlays/home/kustomization.yaml`.
- Update Flux substitution boundaries:
  - `clusters/home/infrastructure.yaml`: substitute from platform settings ConfigMap only.
  - `clusters/home/platform.yaml`: substitute from platform settings ConfigMap + `platform-runtime-inputs` Secret.
  - `clusters/home/tenants.yaml`: no postBuild substitution.

---

## Step 4: Remove dead config variables and shadow contracts

**Goal:** Keep `config.env` limited to inputs that drive behavior.

**Approach:**
- Remove unused/dead vars from `config.env` (for example `RUN_HOST_LAYER`, verify-only leftovers not used by manifests).
- Keep secrets in `profiles/secrets.env` and runtime-input secret sync path.
- Update verify scripts/contracts to avoid requiring removed vars:
  - `scripts/00_verify_contract_lib.sh`
  - `scripts/91_verify_platform_state.sh`
  - `scripts/94_verify_config_inputs.sh`

---

## Step 5: Apply label-domain migration safely

**Goal:** Switch `platform.swhurl.io/*` to `platform.swhurl.com/*` consistently.

**Approach:**
- Global manifest replacement for labels/selectors.
- Update script selectors and checks that currently hardcode `.io` labels:
  - `scripts/bootstrap/sync-runtime-inputs.sh`
  - `scripts/99_execute_teardown.sh`
  - `scripts/98_verify_teardown_clean.sh`
  - any other grep/jsonpath checks using `platform.swhurl.io`

---

## Step 6: Prune stale docs and AGENTS content

**Goal:** Docs reflect current Flux-first architecture and active files only.

**Approach:**
- Rewrite/trim `AGENTS.md` to remove retired Helmfile-era instructions.
- Delete clearly historical docs that now create confusion:
  - `docs/migration-plan-local-charts.md`
  - `docs/target-tree-and-migration-checklist.md`
  - `walkthrough.previous.md`
- Update remaining docs:
  - `README.md`
  - `docs/runbook.md`
  - `docs/orchestration-api.md`
  - `docs/homelab-intent-and-design.md`
  - provider runbooks with honest "placeholder/not implemented yet" notes.

---

## Verification gates

Run after each step:

```bash
kubectl kustomize infrastructure/overlays/home >/dev/null
kubectl kustomize platform-services/overlays/home >/dev/null
kubectl kustomize tenants/overlays/app-staging-le-staging >/dev/null
kubectl kustomize tenants/overlays/app-staging-le-prod >/dev/null
kubectl kustomize tenants/overlays/app-prod-le-staging >/dev/null
kubectl kustomize tenants/overlays/app-prod-le-prod >/dev/null
kubectl kustomize clusters/home >/dev/null
./run.sh --dry-run
make verify
```

For mode-target behavior validation:

```bash
make platform-certs-staging DRY_RUN=true
make platform-certs-prod DRY_RUN=true
make app-test-staging-le-staging DRY_RUN=true
make app-test-staging-le-prod DRY_RUN=true
make app-test-prod-le-staging DRY_RUN=true
make app-test-prod-le-prod DRY_RUN=true
```

---

## Execution order

1. Step 1 (platform cert switch model)
2. Step 2 (Makefile/GitOps workflow truthfulness)
3. Step 3 (runtime-input boundary fix)
4. Step 4 (config contract cleanup)
5. Step 5 (label-domain migration, scripts + manifests together)
6. Step 6 (docs/AGENTS cleanup)

Use one commit per step for safe rollback/review.

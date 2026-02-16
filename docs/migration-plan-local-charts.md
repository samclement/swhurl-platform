# Migration Plan: Local Charts + Env-Driven Secrets

This plan migrates the repo toward a more declarative model:

- Platform glue (namespaces/issuers/apps) lives in **local charts** managed by Helmfile.
- A single script prepares **external inputs** (Secrets/ConfigMaps) from env files.
- Delete is deterministic, with explicit cleanup steps for CRDs/finalizers.

This plan is intended to be executed **step-by-step** with small commits and validation after each step.

## Principles

- One orchestrator: Helmfile drives installs/upgrades/deletes.
- Explicit ordering: dependencies are expressed with Helmfile `needs:`.
- Clear boundaries:
  - Helmfile + charts for platform services and cluster glue.
  - Bash only where Helmfile cannot help: external secrets bootstrap and CRD/finalizer cleanup.

## Step 0: Baseline Guardrails (no behavior change)

Changes
- Document contracts (env/tool/delete).
- Document this migration plan.
- Ensure local scratch notes don’t get committed.

Validation
- `./run.sh`
- `./run.sh --delete`

## Step 1: Move Managed Namespaces to a Local Chart

Changes
- Add `charts/platform-namespaces/` which templates the managed namespaces.
- Add a Helmfile release `platform-namespaces` (`labels.phase=core`).
- Make `scripts/20_reconcile_platform_namespaces.sh` a thin wrapper around Helmfile (or remove it after the cutover).

Validation
- `helmfile -l phase=core apply`
- `kubectl get ns` includes the expected managed namespaces.

## Step 2: Move ClusterIssuers to a Local Chart (Staging Default)

Changes
- Add `charts/platform-issuers/` producing:
  - `ClusterIssuer letsencrypt-staging`
  - `ClusterIssuer letsencrypt-prod`
- Helmfile release `platform-issuers` (`phase=core`) with `needs: [cert-manager]`.
- Introduce `ACME_ENV=staging|prod` (default `staging`) and keep `CLUSTER_ISSUER` aligned.

Validation
- `kubectl get clusterissuers`
- Issuer becomes Ready; no “CRD missing” races.

## Step 3: Consolidate External Inputs into a Single Secrets Script

Changes
- Replace scattered `kubectl create secret` logic with one script that:
  - sources env via `scripts/00_lib.sh`
  - applies required Secrets/ConfigMaps idempotently
  - labels them `platform.swhurl.io/managed=true`
- Helmfile releases use `existingSecret:` / existing resources only.

Validation
- Update a secret value in `profiles/secrets.env`, rerun apply, verify workloads roll and auth works.

## Step 4: Move Hello App to a Local Chart (Apps Phase)

Changes
- Add `charts/apps-hello/` for deployment/service/ingress/cert as needed.
- Helmfile release `apps-hello` with `labels.phase=apps` and `needs: [ingress-nginx, cert-manager]`
  (and optionally oauth2-proxy if enabled).
- Retire Kustomize-based hello install in `scripts/75_sample_app.sh`.

Validation
- `https://hello.${BASE_DOMAIN}` works, TLS is issued.
- OAuth annotations are correct when enabled.

## Step 5: Make Delete a First-Class Transaction (Reverse Needs)

Changes
- `./run.sh --delete` destroys in reverse phase order:
  - `apps` -> `platform` -> `core` -> teardown sweep -> `network` (cilium last)
- Keep explicit CRD/finalizer cleanup steps (Helm cannot be relied upon for this).
- Keep `scripts/98_verify_teardown_clean.sh` as the gate.

Validation
- Run delete twice; second run is a fast no-op and still passes `98`.

# Refactor Plan: Declarative Homelab Platform

This plan outlines the transition from a script-heavy orchestration model to a fully declarative, Flux-native GitOps architecture.

## Goals
- **Declarative Everything:** Eliminate manual `kubectl` or `run.sh` steps for secrets and DNS.
- **Idempotency:** Ensure the entire platform can be rebuilt from a single `flux bootstrap` command.
- **Maintainability:** Reduce custom bash logic in favor of Flux-native features (`dependsOn`, `postBuild`, `healthChecks`).
- **Observability:** Move verification into the cluster for better self-healing.

---

## Phase 1: Secrets Modernization (SOPS + Flux)
**Objective:** Replace `sync-runtime-inputs.sh` with SOPS-encrypted secrets in Git.

- [x] **Setup SOPS/Age:** Generate an Age key and store the public key in `.sops.yaml`.
- [x] **Import Key to Cluster:** Create a Kubernetes secret in `flux-system` containing the Age private key.
- [x] **Migrate Secrets:**
    - Encrypt `profiles/secrets.env` values into a new file: `clusters/home/flux-system/sources/secrets.sops.yaml`.
    - Update `clusters/home/flux-system/sources/kustomization.yaml` to include the encrypted secret.
- [x] **Configure Flux Decryption:** Update the `homelab-flux-sources` Kustomization in `clusters/home/flux-system/kustomizations.yaml` to use `spec.decryption`.
- [x] **Cleanup:** Remove `scripts/bootstrap/sync-runtime-inputs.sh`.

## Phase 2: Declarative Orchestration (Flux Dependencies)
**Objective:** Replace `run.sh` ordering with Flux `dependsOn` logic.

- [x] **Define Dependency Graph:**
    - `homelab-flux-sources` (Base)
    - `homelab-infrastructure` (Depends on: `sources`)
    - `homelab-cert-manager-issuers` (Depends on: `infrastructure`)
    - `homelab-platform` (Depends on: `cert-manager-issuers`)
    - `homelab-tenants` (Depends on: `platform`)
    - `homelab-app-example` (Depends on: `tenants`)
- [x] **Update Cluster Manifests:** Apply the `dependsOn` field to all Kustomizations in `clusters/home/`.
- [x] **Simplify `run.sh`:** Remove manual orchestration steps from `run.sh`.

## Phase 3: Automated DNS (ExternalDNS)
**Objective:** Replace `host/tasks/10_dynamic_dns.sh` with the `external-dns` controller.

- [x] **Add ExternalDNS Manifests:** Create `infrastructure/external-dns/` with a HelmRelease for Route53.
- [x] **Integrate into Infrastructure:** Add ExternalDNS to `infrastructure/overlays/home/kustomization.yaml`.
- [x] **Configure Credentials:** Add AWS keys to the encrypted secret and substitute them into the HelmRelease.
- [x] **Validate:** ExternalDNS is now managed by Flux as part of the infrastructure layer.

## Phase 4: In-Cluster Verification & Health Checks
**Objective:** Move verification logic from `scripts/91_verify_platform_state.sh` into the cluster.

- [x] **Enable Flux Health Checks:** Add `spec.healthChecks` to all Kustomizations in `clusters/home/` to wait for Deployments/DaemonSets.
- [x] **Configure Health Checks:** Added specific checks for Traefik, cert-manager, oauth2-proxy, ClickStack, and OTel.
- [x] **Update Workflow:** Reconciliation now waits for these components to be Ready.

## Phase 5: Simplification & Cleanup
**Objective:** Final removal of legacy scripts and documentation updates.

- [x] **Cleanup Scripts:** Removed redundant orchestrators and synchronization scripts.
- [x] **Unified Config:** Consolidated configuration into declarative sources.
- [x] **Update README:** Transitioned the "Quickstart" to focus on declarative GitOps.
- [x] **Update Makefile:** Simplified targets to reflect the new declarative flow.


---

## Success Criteria
1. `make install` (or a fresh `flux bootstrap`) completes without manual secret syncing.
2. DNS records for new apps are created automatically via Ingress annotations.
3. Deleting a component in Git results in a clean, automatic prune by Flux.
4. The platform state is observable directly via `kubectl get kustomizations`.

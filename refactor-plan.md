# Simplification Refactor Plan (K3s-Only, Declarative)

## Goals
1. Remove kind and Podman dependencies.
2. Standardize on Cilium for CNI.
3. Favor declarative, file-driven config over imperative flags.
4. Make run order explicit and reproducible.
5. Separate concerns so DNS is independent of Kubernetes but required before cert-manager.

## Proposed Repo Shape
1. `infra/`
2. `infra/values/`
3. `infra/manifests/`
4. `infra/manifests/issuers/`
5. `profiles/`
6. `scripts/`
7. `run.sh`

## Configuration Model
1. `config.env` remains the base.
2. `profiles/*.env` are layered, explicit, and composable.
3. A single `PROFILE` variable selects a profile stack, for example `PROFILE=homelab`.
4. A profile may `source` other profiles to avoid duplication.
5. All Kubernetes resources are expressed in `infra/` and applied declaratively.

## What We Remove
1. Legacy kind provider scripts
2. Legacy Podman provider setup scripts
3. Legacy kind sysctl helper scripts
4. `KIND_*` config in `config.env`
5. Any README/plan references to kind/Podman provider setup

## Declarative Source of Truth
1. `infra/manifests/` for raw Kubernetes YAML.
2. `infra/values/` for Helm values files.
3. `infra/manifests/issuers/` for ClusterIssuer manifests rendered from env.
4. `run.sh` only orchestrates `kubectl apply -k` and `helm upgrade --install -f`.

## Clear Separation of Concerns
1. DNS setup is outside Kubernetes and must complete before cert-manager.
2. Cluster provisioning is outside the repo (k3s install docs only).
3. Kubernetes components are applied after kubeconfig is verified.
4. Observability, logging, and storage are optional and gated by profile flags.

## Execution Order (Single, Explicit, Declarative)
1. **Prereqs**: Verify `kubectl`, `helm`, `k3s` context is reachable.
2. **DNS**: Run `scripts/12_dns_register.sh` if using `*.swhurl.com`.
3. **Namespaces**: Apply `infra/manifests/namespaces.yaml`.
4. **Helm Repos**: `scripts/25_helm_repos.sh` or `helmfile` if kept.
5. **CNI (Cilium)**: Install via Helm after k3s is running with flannel disabled.
6. **Cert-Manager**: `helm upgrade --install` with values file.
7. **ClusterIssuer**: Apply `infra/manifests/issuers/letsencrypt` or `infra/manifests/issuers/selfsigned`.
8. **Ingress**: Apply `infra/values/ingress-nginx-logging.yaml` via Helm.
9. **OAuth2 Proxy**: Apply if enabled in profile.
10. **Logging**: Apply if enabled in profile.
11. **Observability**: Apply if enabled in profile.
12. **Storage (MinIO)**: Apply if enabled in profile.
13. **Apps**: Apply sample app manifests.
14. **Smoke Tests**: Run `scripts/90_smoke_tests.sh`.

## Concrete Refactor Steps
1. Update `plan.md` to describe k3s-only and declarative flow.
2. Remove kind scripts and config references.
3. Add `infra/manifests/namespaces.yaml` and move namespace creation there.
4. Move all chart values to `infra/values/` and reference them consistently.
5. Move issuer manifests to `infra/manifests/issuers/` with envsubst or kustomize replacements.
6. Update `run.sh` to execute only declarative steps in the order above.
7. Update `README.md` to match the new flow and remove kind/Podman docs.
8. Keep `scripts/12_dns_register.sh` independent of kubeconfig or `kubectl`.

## Safety and Idempotence
1. All steps use `kubectl apply` or `helm upgrade --install`.
2. Each step is safe to re-run and converges on desired state.
3. `--delete` remains available to remove applied resources.

## Required Inputs
1. `profiles/secrets.env` for `ACME_EMAIL`, `OIDC_*`, and `MINIO_ROOT_PASSWORD`.
2. `BASE_DOMAIN`, `CLUSTER_ISSUER`, and optional `SWHURL_SUBDOMAINS` in `config.env` or profile.

## Output
1. A minimal, k3s-only platform flow.
2. A declarative resource tree in `infra/` with clear ownership and order.
3. A simplified `run.sh` that orchestrates only declarative steps.

# Local Kubernetes Cluster Script Plan

This plan describes a cohesive, step-by-step script suite to create a fully working local Kubernetes platform using Podman + kind by default (pluggable for k3d in the future). Each script is independent, idempotent, and composable so features can be added gradually. The suite also supports partial runs via profiles and per-feature flags.

## Goals

- Reproducible local cluster with sane defaults.
- Modular scripts with minimal cross-coupling.
- Idempotent: safe to re-run any step; `upgrade --install` semantics.
- Clear inputs/outputs via a shared `config.env` and `00_lib.sh` helpers.
- Optional features enabled via flags/profiles.
- Easy teardown with the inverse of each step.

## Requirements

- Podman (with Podman machine on macOS), kind, kubectl, Helm.
- Optional: jq, yq, sops/age, openssl.
- System: macOS or Linux. Windows WSL2 should work with Podman/kind.

## Repository Structure

```
scripts/
  00_lib.sh                 # common helpers (logging, checks, waits)
  01_check_prereqs.sh       # verify tools and versions
  02_podman_setup.sh        # ensure podman machine/socket running (mac/linux)

  10_cluster_kind.sh        # create kind cluster (Podman provider)
  15_kube_context.sh        # export kubeconfig, set context
  12_dns_register.sh        # optional: register dynamic DNS for host (swhurl)
  20_namespaces.sh          # create core namespaces
  25_helm_repos.sh          # add/update helm repositories

  30_cert_manager.sh        # install cert-manager (+CRDs)
  35_issuer.sh              # create ClusterIssuer (self-signed or ACME)
  40_ingress_nginx.sh       # install ingress-nginx controller
  45_oauth2_proxy.sh        # install oauth2-proxy (optional)

  50_logging_fluentbit.sh   # install fluent-bit (optional)
  55_loki.sh                # install loki (optional)

  60_prom_grafana.sh        # install kube-prometheus-stack

  70_minio.sh               # install MinIO (optional)

  75_sample_app.sh          # deploy example app + ingress + TLS

  80_mesh_linkerd.sh        # optional service mesh (Linkerd baseline)
  81_mesh_istio.sh          # optional service mesh (Istio advanced)

  90_smoke_tests.sh         # basic health checks and validations
  95_dump_context.sh        # dump info for debugging (events, logs)

  99_teardown.sh            # destroy resources/cluster

config.env                  # user-editable configuration & feature flags
values/                     # helm values overrides per component
profiles/                   # profile files enabling subsets (e.g., minimal, full)
```

## Configuration (`config.env`)

Environment-driven to avoid editing scripts. Example keys (all optional unless noted):

```
# cluster
CLUSTER_NAME=platform
K8S_PROVIDER=kind              # kind (default) | k3d (future)
KIND_EXPERIMENTAL_PROVIDER=podman
KIND_NODES=1                   # set >1 for multi-node
KIND_CONFIG=values/kind-config.yaml

# domain / TLS
# If using the homelab dynamic DNS, set subdomain and base domain accordingly.
SWHURL_SUBDOMAIN=homelab        # registers homelab.swhurl.com via systemd updater
BASE_DOMAIN=homelab.swhurl.com  # for real DNS; else use 127.0.0.1.nip.io for local-only
CLUSTER_ISSUER=selfsigned      # selfsigned | letsencrypt
ACME_EMAIL=you@example.com

# oauth2-proxy (optional)
OIDC_ISSUER=https://accounts.google.com
OIDC_CLIENT_ID=
OIDC_CLIENT_SECRET=
OAUTH_COOKIE_SECRET=
OAUTH_HOST=oauth.${BASE_DOMAIN}

# observability
GRAFANA_HOST=grafana.${BASE_DOMAIN}

# storage
MINIO_HOST=minio.${BASE_DOMAIN}
MINIO_CONSOLE_HOST=minio-console.${BASE_DOMAIN}
MINIO_ROOT_USER=minioadmin
MINIO_ROOT_PASSWORD=change-me-please

# features
FEAT_DNS_REGISTER=true
FEAT_OAUTH2_PROXY=true
FEAT_LOGGING=true
FEAT_LOKI=true
FEAT_OBS=true
FEAT_MINIO=true
FEAT_MESH_LINKERD=false
FEAT_MESH_ISTIO=false

# runtime
TIMEOUT_SECS=300
``` 

Profiles in `profiles/` can export a curated subset of these values and flags (e.g., `profiles/minimal.env`, `profiles/full.env`).

## Common Library (`00_lib.sh`)

All scripts source this library first. Responsibilities:

- `set -Eeuo pipefail` and trap for clean errors with context.
- `log_info/log_warn/log_error` with timestamps; `die` for fatal.
- `need_cmd` to assert dependencies; optional version checks.
- `kubectl_ns` helper to ensure namespaces exist.
- `wait_deploy/ns kind` waiters for Deployments, DaemonSets, Jobs, CRDs.
- `helm_upsert` wrapper for `helm upgrade --install` with values and namespace creation.
- `ensure_context` verifies a reachable kube-api before proceeding.
- `label_managed` applies `platform.swhurl.io/managed=true` for easy cleanup.
- `apply_or_delete` pattern switch for `--delete` mode symmetry.

## Script Contracts

Each script follows the same contract:

- Usage: `./scripts/<step>.sh [--delete] [--profile <file>] [--dry-run]`
- Reads: `config.env` and optional profile; requires `00_lib.sh`.
- Writes: Kubernetes objects (idempotent) and/or Helm releases.
- Idempotence: Safe to run multiple times. Check for existing releases/secrets.
- Teardown: `--delete` reverses changes owned by that step; leaves shared deps intact.
- Exit codes: Non-zero on failure; avoids partial state by waiting for readiness.

## Execution Order

Baseline order for a “full” local platform:

1. 01_check_prereqs.sh
2. 02_podman_setup.sh
3. 12_dns_register.sh (if `FEAT_DNS_REGISTER=true` on Linux with systemd)
4. 10_cluster_kind.sh
5. 15_kube_context.sh
6. 20_namespaces.sh
6. 25_helm_repos.sh
7. 30_cert_manager.sh
8. 35_issuer.sh (choose issuer based on `CLUSTER_ISSUER`)
9. 40_ingress_nginx.sh
10. 45_oauth2_proxy.sh (if `FEAT_OAUTH2_PROXY=true`)
11. 50_logging_fluentbit.sh (if `FEAT_LOGGING=true`)
12. 55_loki.sh (if `FEAT_LOKI=true`)
13. 60_prom_grafana.sh (if `FEAT_OBS=true`)
14. 70_minio.sh (if `FEAT_MINIO=true`)
15. 75_sample_app.sh
16. 80_mesh_linkerd.sh or 81_mesh_istio.sh (if enabled)
17. 90_smoke_tests.sh

Teardown sequence is reverse order plus `99_teardown.sh` for final cluster delete.

## Minimal Run Entrypoints

- Full stack: `./run.sh` (calls each step with feature gating) 
- Teardown: `./run.sh --delete` (reverse order)
- Profiled run: `./run.sh --profile profiles/minimal.env`

`run.sh` is a thin orchestrator that:

- Loads `config.env` and optional `--profile`.
- Prints a plan (enabled steps) before executing.
- Executes steps in order; stops on first failure.
- Supports `ONLY="10,30,40" ./run.sh` to run a subset by numeric prefixes.

## Helm Values and Manifests

- Store opinionated overrides under `values/<chart>/values.yaml`.
- Scripts pass `-f` appropriately; allow additional `-f` via env `EXTRA_HELM_VALUES`.
- Kubernetes manifests for simple resources (e.g., ClusterIssuer) live in `values/manifests/` and are applied with `kubectl`.

## Idempotence and Readiness

- Use `helm upgrade --install` and `kubectl apply` exclusively for create/update.
- Before creating, check if resource already exists; skip or reconcile rather than fail.
- After installing CRDs (cert-manager), wait for CRDs to be established before applying resources.
- Wait for Deployments/DaemonSets to be ready; provide timeouts (`TIMEOUT_SECS`).
- Record state with labels and annotations for traceability.

## DNS Registration (`12_dns_register.sh`)

For hosts reachable from the internet (e.g., your homelab k3s/kind node), this step installs a systemd service and timer that keeps an A record in Route53 for `<SWHURL_SUBDOMAIN>.swhurl.com` updated to the host’s public IP. It leverages the gist script maintained by @samclement.

- Behavior:
  - Linux + systemd only. On macOS, the script prints manual instructions and exits 0.
  - Downloads and installs the `aws-dns-updater.sh` helper and a systemd unit/timer.
  - Verifies the service is active and logs last run status.
  - No-op if already installed and configured for the same subdomain.
- Inputs:
  - `SWHURL_SUBDOMAIN` (e.g., `homelab`) required when `FEAT_DNS_REGISTER=true`.
  - AWS credentials/environment must be present with permissions to update the Route53 zone for `swhurl.com`.
- Outputs:
  - Active systemd service `aws-dns-updater.service` and timer `aws-dns-updater.timer`.
- Notes:
  - When using a real DNS name like `homelab.swhurl.com`, prefer `CLUSTER_ISSUER=letsencrypt` in `config.env` so cert-manager issues valid certs.
  - For local-only development with no public IP, skip this step and use `BASE_DOMAIN=127.0.0.1.nip.io` with a self-signed issuer.

## Logging and Diagnostics

- Each script logs start/end and key actions.
- On failure, print last N lines of relevant pod logs and `kubectl describe` for failed resources.
- `95_dump_context.sh` gathers cluster-info, events, and summaries for debugging.

## Smoke Tests (`90_smoke_tests.sh`)

Checks (feature-aware):

- Node readiness and core API availability.
- cert-manager pods Ready; CRDs present; sample Certificate reaches `Ready`.
- Ingress Controller Service has endpoints; test an Ingress 200 response.
- OAuth2 Proxy endpoint liveness (if enabled).
- Fluent Bit DaemonSet ready and shipping sample logs to backend (if enabled).
- Prometheus targets up; Grafana ingress reachable (HTTP 200 or protected by 302 via OAuth).
- MinIO Console reachable; bucket CRUD via `mc` if available.

## Security and Best Practices

- Namespaces and labels created early for ownership and isolation.
- Pod Security labels set to `baseline` by default; allow opting into `restricted`.
- Default deny NetworkPolicies with minimal allow rules (optional add-on script).
- Secrets provided via `kubectl create secret` or pre-encrypted SOPS files applied by scripts if present.

## Service Mesh (Optional)

- `80_mesh_linkerd.sh`: fast path mesh with mTLS; enable viz addon.
- `81_mesh_istio.sh`: advanced traffic and policy; installs minimal profile + ingress/egress gateways.
- Both scripts should detect existing mesh and skip or reconcile versions.

## Extending the Suite

To add a new feature step (e.g., external-secrets):

1. Create `scripts/65_external_secrets.sh` following the contract.
2. Add default flags in `config.env` (e.g., `FEAT_EXTERNAL_SECRETS=true`).
3. Add values under `values/external-secrets/values.yaml` as needed.
4. Insert step in `run.sh` sequence and `README`/AGENTS.md references.
5. Implement `--delete` to remove Helm release and related objects labeled `platform.swhurl.io/managed=true`.

## Makefile (Optional)

Convenience targets (optional) if using `make`:

```
make up            # full stack
make down          # teardown
make verify        # smoke tests
make observability # only monitoring components
```

## Implementation Notes

- macOS: `02_podman_setup.sh` ensures `podman machine` is initialized and started.
- Linux: enable `podman.socket` for Docker-compatible API if needed by kind.
- kind config: expose host ports for HTTP(S) if desired; multi-node cluster via `values/kind-config.yaml`.
- Prefer nip.io/sslip.io wildcard domains for frictionless local TLS via cert-manager.

## Deliverables Summary

- `scripts/` with the steps listed above.
- `config.env` with sane defaults and feature flags.
- `values/` and `profiles/` directories.
- `run.sh` orchestrator and `99_teardown.sh` cleanup.

This plan keeps steps cohesive and independent, enabling incremental adoption and easy troubleshooting while providing a turnkey local platform experience.

## Full Teardown Steps

Run everything in reverse using delete mode to fully clean the local environment. Data will be lost (PVCs, MinIO buckets, Prometheus TSDB, etc.).

Recommended order (handled by `./run.sh --delete`):

1. Sample app: Remove the demo app, its Ingress, Service, and Certificates.
2. MinIO: `helm uninstall minio -n storage` and delete PVCs in `storage` (kind nodes are ephemeral; PVCs vanish with cluster, but delete explicitly for clarity).
3. Observability: `helm uninstall monitoring -n observability` and Loki (if enabled); delete residual CRDs if desired.
4. Logging: `helm uninstall fluent-bit -n logging`; delete any extra ConfigMaps/Secrets created by the step.
5. OAuth2 Proxy: `helm uninstall oauth2-proxy -n ingress`; delete secret(s) if not managed by SOPS.
6. Ingress NGINX: `helm uninstall ingress-nginx -n ingress`.
7. Issuers/Certificates: Delete `ClusterIssuer`/`Issuer` objects created by scripts.
8. cert-manager: `helm uninstall cert-manager -n cert-manager`; optionally remove cert-manager CRDs.
9. Helm repos: No teardown required (local client state).
10. Namespaces: Delete app/platform namespaces created by the scripts (ingress, cert-manager, logging, observability, storage) if they are empty.
11. DNS updater: Run `scripts/12_dns_register.sh --delete` to stop/disable and remove the systemd service/timer (only if it matches your configured subdomain).
12. Cluster context: Unset/clean kubeconfig context if desired.
13. Cluster delete: `kind delete cluster --name "$CLUSTER_NAME"` (final step) — removes nodes and any embedded volumes.

Notes

- Use `./run.sh --delete` for an automated reverse-order teardown; it passes `--delete` to each step and then you can run `scripts/99_teardown.sh` (optional) to delete the cluster.
- Consider creating a `scripts/98_pv_cleanup.sh` if you introduce custom hostPath volumes outside the kind nodes.
- For macOS Podman, deleting the kind cluster is typically sufficient; no systemd DNS service will have been installed.

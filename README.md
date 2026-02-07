# Local Kubernetes Platform Scripts

This repository contains a modular, idempotent set of scripts to provision a fully working local Kubernetes platform using Podman + kind (default) or a remote Linux host with k3s. Components include cert-manager, ingress with OAuth2 Proxy, logging with Fluent Bit (to Loki), observability with Prometheus + Grafana, and storage with MinIO. Optional service meshes (Linkerd/Istio) are supported.

## Quick Start

- Install prerequisites: Podman, kind, kubectl, Helm (plus jq/yq optional).
- Configure DNS if desired: The machine is already registering `homelab.swhurl.com` via a systemd service. The scripts can idempotently ensure this.
- Edit `config.env` to set domain/flags (defaults work for homelab.swhurl.com). For purely local use, set `BASE_DOMAIN=127.0.0.1.nip.io` and `CLUSTER_ISSUER=selfsigned`.

Create the platform:

```
./run.sh
```

Teardown everything (reverse order) and delete the cluster:

```
./run.sh --delete
./scripts/99_teardown.sh
```

Run a subset only (by numbers or filenames):

```
./run.sh --only 01,02,10,20,25
```

Use a profile or custom overrides:

```
./run.sh --profile profiles/minimal.env
```

## Configuration

Edit `config.env` (or provide a `--profile` that overrides it):

- `CLUSTER_NAME`: Kind cluster name.
- `BASE_DOMAIN`: e.g., `homelab.swhurl.com` or `127.0.0.1.nip.io`.
- `CLUSTER_ISSUER`: `selfsigned` or `letsencrypt`.
- OAuth: `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OAUTH_COOKIE_SECRET`, `OAUTH_HOST`.
- Observability: `GRAFANA_HOST`.
- Storage: `MINIO_HOST`, `MINIO_CONSOLE_HOST`, `MINIO_ROOT_USER`, `MINIO_ROOT_PASSWORD`.
- Feature flags: `FEAT_*` booleans enable/disable components.

## Scripts Overview

Scripts live under `scripts/` and are numbered for order. Each supports idempotent runs and `--delete` where applicable.

- `00_lib.sh`: Common helpers (logging, checks, waits, helm upsert).
- `01_check_prereqs.sh`: Tool checks and warnings.
- `02_podman_setup.sh`: Podman machine/socket setup (macOS/Linux).
- `10_cluster_kind.sh`: Create/delete kind cluster (Podman provider).
- `12_dns_register.sh`: Ensure systemd DNS updater for `<subdomain>.swhurl.com` (idempotent, Linux only).
- `15_kube_context.sh`: Point kubectl to the cluster context.
- `20_namespaces.sh`: Ensure core namespaces exist.
- `25_helm_repos.sh`: Add/update Helm repositories.
- `30_cert_manager.sh`: Install cert-manager (with CRDs).
- `35_issuer.sh`: Create `ClusterIssuer` (`selfsigned` or `letsencrypt`).
- `40_ingress_nginx.sh`: Install ingress-nginx controller.
- `45_oauth2_proxy.sh`: Install OAuth2 Proxy and expose via Ingress.
- `50_logging_fluentbit.sh`: Install Fluent Bit, default to Loki backend.
- `55_loki.sh`: Install Loki (optional but expected by Fluent Bit config).
- `60_prom_grafana.sh`: Install kube-prometheus-stack; Grafana with TLS Ingress.
- `70_minio.sh`: Install MinIO with Console and TLS.
- `75_sample_app.sh`: Deploy a sample NGINX app with TLS Ingress (and OAuth if enabled).
- `80_mesh_linkerd.sh`: Optional Linkerd install (requires `linkerd` CLI).
- `81_mesh_istio.sh`: Optional Istio minimal profile (requires `istioctl`).
- `90_smoke_tests.sh`: Basic cluster/component checks.
- `95_dump_context.sh`: Dump cluster info, events, and Helm releases.
- `99_teardown.sh`: Delete the kind cluster.

## DNS and TLS

- If using real DNS (`homelab.swhurl.com`), set `CLUSTER_ISSUER=letsencrypt`. Ensure ingress is reachable from the internet for ACME HTTP-01.
- If local only, use `127.0.0.1.nip.io` and `selfsigned` issuer.
- The DNS updater script (`12_dns_register.sh`) installs a systemd service and timer using the maintained gist. It is safe to re-run and will only update when needed. Use `--delete` to remove it.

## Day-2 and Teardown

- To remove components, run `./run.sh --delete` to execute per-step teardowns (reverse order). Then run `./scripts/99_teardown.sh` to remove the cluster.
- For debugging, run `./scripts/95_dump_context.sh` to collect cluster info and events.

## Notes

- All scripts are idempotent; re-run safely.
- Helm installs use `upgrade --install` semantics.
- The suite is designed to be extended—add new scripts following the same pattern and gate them with a `FEAT_*` flag in `run.sh`.


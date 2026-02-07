# Local Kubernetes Platform Scripts

This repository contains a modular, idempotent set of scripts to provision a fully working local Kubernetes platform. The default provider is k3s (recommended for Linux), with optional support for kind as an alternative. Components include cert-manager, ingress with OAuth2 Proxy, logging with Fluent Bit (to Loki), observability with Prometheus + Grafana, and storage with MinIO. Optional service meshes (Linkerd/Istio) are supported.

## Quick Start

- Install prerequisites: kubectl, Helm (plus jq/yq optional). For kind, also install Podman or Docker.
- Configure DNS if desired: The machine can register multiple `*.swhurl.com` subdomains via a systemd service. The scripts idempotently ensure this.
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

- `CLUSTER_NAME`: Cluster name (used for kind context; not used by k3s).
- `BASE_DOMAIN`: e.g., `homelab.swhurl.com` or `127.0.0.1.nip.io`.
- `CLUSTER_ISSUER`: `selfsigned` or `letsencrypt`.
- OAuth: `OAUTH_HOST`. Put sensitive values (`OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OAUTH_COOKIE_SECRET`) in a secrets profile.
- Observability: `GRAFANA_HOST`.
- Storage: `MINIO_HOST`, `MINIO_CONSOLE_HOST`, `MINIO_ROOT_USER`, `MINIO_ROOT_PASSWORD`.
- Feature flags: `FEAT_*` booleans enable/disable components.

## Scripts Overview

Scripts live under `scripts/` and are numbered for order. Each supports idempotent runs and `--delete` where applicable.

- `00_lib.sh`: Common helpers (logging, checks, waits, helm upsert).
- `01_check_prereqs.sh`: Tool checks and warnings.
- `02_podman_setup.sh`: Podman machine/socket setup (macOS/Linux).
- `11_cluster_k3s.sh`: Validate access to k3s (no auto-install). For kind users, see `10_cluster_kind.sh`.
- `12_dns_register.sh`: Ensure systemd DNS updater for one or more `<subdomain>.swhurl.com` records (idempotent, Linux only). Now supports multiple subdomains.
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
  - Creates a cert-manager `Certificate` for `hello.${BASE_DOMAIN}` using the configured `CLUSTER_ISSUER`.
  - Uses Kustomize templates under `manifests/templates/app` rendered via env and applied with `kubectl apply -k`.
- `80_mesh_linkerd.sh`: Optional Linkerd install (requires `linkerd` CLI).
- `81_mesh_istio.sh`: Optional Istio minimal profile (requires `istioctl`).
- `90_smoke_tests.sh`: Basic cluster/component checks.
- `95_dump_context.sh`: Dump cluster info, events, and Helm releases.
- `99_teardown.sh`: Delete the cluster (kind). For k3s, prints manual uninstall instructions (optionally runs uninstall with `K3S_UNINSTALL=true`).

## Internet Ingress + OIDC (Google default)

Use this sequence to accept traffic from the internet (DNS) and enforce OIDC auth at the edge via OAuth2 Proxy.

- Dynamic DNS (Route53 under swhurl.com)
  - Ensure this host is internet-facing and has AWS credentials with Route53 change permissions for `swhurl.com`.
  - Edit `config.env`:
    - Set `BASE_DOMAIN=<env>.swhurl.com` (e.g., `homelab.swhurl.com`).
    - Set `SWHURL_SUBDOMAINS` with the subdomains to register, space or comma separated. Typical:
      - `homelab oauth.homelab grafana.homelab minio.homelab minio-console.homelab`
    - Optionally set `AWS_PROFILE` in your environment (defaults to `default`).
  - Run: `./scripts/12_dns_register.sh`
    - Installs `~/.local/scripts/aws-dns-updater.sh` and a systemd service/timer.
    - Updates all listed `*.swhurl.com` A records to this host’s public IP and keeps them fresh.
    - Optional env overrides: `AWS_PROFILE`, `AWS_ZONE_ID`, `AWS_NAMESERVER`.

- cert-manager and issuer
  - Set `CLUSTER_ISSUER=letsencrypt` and `ACME_EMAIL` in `config.env` (or use `selfsigned` for local-only).
  - Install in order: `./scripts/30_cert_manager.sh`, `./scripts/40_ingress_nginx.sh`, `./scripts/35_issuer.sh`.

- OAuth2 Proxy with Google OIDC
  - In Google Cloud Console → Credentials, create an OAuth 2.0 Web application:
    - Authorized redirect URI: `https://oauth.${BASE_DOMAIN}/oauth2/callback`
  - Set in `profiles/secrets.env` (do not commit):
    - `OIDC_ISSUER=https://accounts.google.com`
    - `OIDC_CLIENT_ID=<from Google>`
    - `OIDC_CLIENT_SECRET=<from Google>`
    - Optionally set `OAUTH_COOKIE_SECRET` (exactly 16, 24, or 32 ASCII chars). If omitted, the script will generate a safe value.
  - Install: `./scripts/45_oauth2_proxy.sh`
    - Exposes `oauth.${BASE_DOMAIN}` with TLS via cert-manager and protects apps via NGINX auth annotations.

- Protect apps behind OAuth2 Proxy
  - Ensure your Ingress has these annotations and uses `ingressClassName: nginx`:
    - `nginx.ingress.kubernetes.io/auth-url: https://oauth.${BASE_DOMAIN}/oauth2/auth`
    - `nginx.ingress.kubernetes.io/auth-signin: https://oauth.${BASE_DOMAIN}/oauth2/start?rd=$scheme://$host$request_uri`
  - Example app: `./scripts/75_sample_app.sh` deploys `hello.${BASE_DOMAIN}` with TLS and optional auth.

Tip: You can run the full flow with the orchestrator, in a safe order:

```
./run.sh --only 01,02,10,15,20,25,12,30,40,35,45,75
```

Notes:
- If you’re not internet-exposed, use `BASE_DOMAIN=127.0.0.1.nip.io` and `CLUSTER_ISSUER=selfsigned` and skip `12_dns_register.sh`.
- For remote clusters, run `12_dns_register.sh` on the internet-facing node that fronts the ingress LB IP.

## Add OIDC To Your App

Use OAuth2 Proxy at the ingress edge to require OIDC auth before your app. This works for any app reachable via an Ingress.

- Prereqs
  - Set OIDC details in `config.env` and install oauth2-proxy: `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` (and optionally `OAUTH_COOKIE_SECRET`).
  - Ensure oauth2-proxy is installed: `./scripts/45_oauth2_proxy.sh` (or `./run.sh` with features enabled).
  - In your IdP (e.g., Google), set the redirect URI to: `https://oauth.${BASE_DOMAIN}/oauth2/callback`.

- Protect your app via Ingress annotations
  - Add these annotations to your app’s Ingress to enforce auth via oauth2-proxy:
    - `nginx.ingress.kubernetes.io/auth-url: https://oauth.${BASE_DOMAIN}/oauth2/auth`
    - `nginx.ingress.kubernetes.io/auth-signin: https://oauth.${BASE_DOMAIN}/oauth2/start?rd=$scheme://$host$request_uri`
  - Optionally forward identity headers from oauth2-proxy to your app (so it can read user info):
    - `nginx.ingress.kubernetes.io/auth-response-headers: X-Auth-Request-User, X-Auth-Request-Email, Authorization`

- Example Ingress (replace `${BASE_DOMAIN}`/names as needed)

```
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: my-app
  namespace: apps
  annotations:
    kubernetes.io/ingress.class: nginx
    cert-manager.io/cluster-issuer: ${CLUSTER_ISSUER}
    nginx.ingress.kubernetes.io/ssl-redirect: "true"
    nginx.ingress.kubernetes.io/force-ssl-redirect: "true"
    nginx.ingress.kubernetes.io/auth-url: https://oauth.${BASE_DOMAIN}/oauth2/auth
    nginx.ingress.kubernetes.io/auth-signin: https://oauth.${BASE_DOMAIN}/oauth2/start?rd=$scheme://$host$request_uri
    nginx.ingress.kubernetes.io/auth-response-headers: X-Auth-Request-User, X-Auth-Request-Email, Authorization
spec:
  tls:
    - hosts: ["my-app.${BASE_DOMAIN}"]
      secretName: my-app-tls
  rules:
    - host: "my-app.${BASE_DOMAIN}"
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: my-app
                port:
                  number: 80
```

- Scaffold a new app (helper script)
  - Create a protected app quickly:
    - `./scripts/76_app_scaffold.sh --name app --host app.${BASE_DOMAIN}`
  - Make it public (no OAuth):
    - `./scripts/76_app_scaffold.sh --name public --host public.${BASE_DOMAIN} --no-auth`
  - Options: `--namespace apps`, `--image <repo:tag>`, `--issuer <ClusterIssuer>`

- Optional behaviors
  - Allow a public path (no auth): place it on a separate Ingress without the auth annotations, or use advanced NGINX snippets to exclude specific locations.
  - Pass access tokens to the app: customize oauth2-proxy to set `--set-authorization-header=true` and `--pass-access-token=true` (via a Helm override), then include `Authorization` in `auth-response-headers`.
  - App identity: read `X-Auth-Request-User` and/or `X-Auth-Request-Email` from request headers; treat them as trusted only behind the platform’s ingress.

## DNS and TLS

- If using real DNS (`*.swhurl.com`), set `CLUSTER_ISSUER=letsencrypt`. Ensure ingress is reachable from the internet for ACME HTTP-01 challenges.
- If local only, use `127.0.0.1.nip.io` and `selfsigned` issuer.
- The DNS updater script (`12_dns_register.sh`) installs a local systemd service and timer and is safe to re-run. Use `--delete` to remove it.

## Day-2 and Teardown

- To remove components, run `./run.sh --delete` to execute per-step teardowns (reverse order). Then run `./scripts/99_teardown.sh` to remove the cluster.
- For debugging, run `./scripts/95_dump_context.sh` to collect cluster info and events.

## Notes

- All scripts are idempotent; re-run safely.
- Helm installs use `upgrade --install` semantics.
- The suite is designed to be extended—add new scripts following the same pattern and gate them with a `FEAT_*` flag in `run.sh`.

## Secrets Profiles

- Keep secrets out of `config.env`. Store them in `profiles/secrets.env` (gitignored). See `profiles/secrets.example.env` for fields.
- The orchestrator loads `--profile FILE` and also auto-loads `profiles/secrets.env` when present. Scripts run directly also auto-source this file.
- Recommended sensitive keys to keep in a profile:
  - `ACME_EMAIL`, `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET`, `OAUTH_COOKIE_SECRET`, `MINIO_ROOT_PASSWORD`.

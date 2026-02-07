# Platform Infrastructure Guide (Agents)

## Agents Operating Notes

- Always update this AGENTS.md with new learnings, gotchas, and environment-specific fixes discovered while working on the repo. Keep entries concise, actionable, and tied to the relevant scripts/config.
- Prefer adding learnings in the sections below. If a learning implies a code change, also open a TODO in the relevant script and reference it here.

### Current Learnings

- Podman + kind on Linux/macOS
  - Missing rootless deps cause kind failures with Podman (errors like: `newuidmap not found`, `open /etc/containers/policy.json: no such file or directory`). Fixes:
    - Install deps (Ubuntu/Debian): `sudo apt-get install -y uidmap slirp4netns fuse-overlayfs containers-common` (and `podman` if not present).
    - Ensure user-level socket: `loginctl enable-linger "$USER" && systemctl --user daemon-reload && systemctl --user enable --now podman.socket`.
    - If system files are missing, add user-level fallbacks:
      - `~/.config/containers/policy.json` with an "insecureAcceptAnything" default policy (OK for local dev).
      - `~/.config/containers/registries.conf` with `unqualified-search-registries = ["docker.io"]`.
  - Homebrew-installed Podman (or Linux without system units): prefer `podman machine` to avoid config gaps:
    - `podman machine init --cpus 4 --memory 6144 --disk-size 40 --now`; then `podman info` and run kind with `KIND_EXPERIMENTAL_PROVIDER=podman`.
  - TODO: In `scripts/02_podman_setup.sh`, consider auto-fallback to `podman machine` on Linux when `podman.socket` is not available, and print precise next steps when `newuidmap`/`slirp4netns` are missing.
  - Kind sysctls on rootless Podman: `scripts/16_kind_sysctls.sh` cannot raise inotify limits inside kind node containers when Podman is rootless (kernel sysctls blocked by user namespaces). The script now auto-skips in this case and logs guidance. Workarounds:
    - Use Docker as the provider, or
    - Use `podman machine` (macOS) / a rootful VM for kind, or
    - Disable the step with `KIND_TUNE_INOTIFY=false`.

- Orchestrator run order
  - `scripts/00_lib.sh` is a helper and should not be executed as a step. Update `run.sh` to exclude `00_lib.sh` from selection (future improvement). For now, executing it is harmless but noisy.
  - Cert-manager Helm install: Some environments time out on the chart’s post-install API check job. `scripts/30_cert_manager.sh` disables `startupapicheck` and explicitly waits for Deployments instead. If you want the chart’s check back, set `CM_STARTUP_API_CHECK=true` and re-enable in the script.

- Domains and DNS registration
  - `SWHURL_SUBDOMAINS` accepts raw subdomain tokens and the updater appends `.swhurl.com`. Example: `oauth.homelab` becomes `oauth.homelab.swhurl.com`. Do not prepend `BASE_DOMAIN` to these tokens.
  - If `SWHURL_SUBDOMAINS` is empty and `BASE_DOMAIN` ends with `.swhurl.com`, `scripts/12_dns_register.sh` derives a sensible set: `<base> oauth.<base> grafana.<base> minio.<base> minio-console.<base>`.
  - To expose the sample app over DNS, add `hello.<base>` to `SWHURL_SUBDOMAINS`.

- OIDC for applications
  - Use oauth2-proxy at the edge and add NGINX auth annotations to your app’s Ingress:
    - `nginx.ingress.kubernetes.io/auth-url: https://oauth.${BASE_DOMAIN}/oauth2/auth`
    - `nginx.ingress.kubernetes.io/auth-signin: https://oauth.${BASE_DOMAIN}/oauth2/start?rd=$scheme://$host$request_uri`
    - Optionally: `nginx.ingress.kubernetes.io/auth-response-headers: X-Auth-Request-User, X-Auth-Request-Email, Authorization`
  - Ensure `OIDC_ISSUER`, `OIDC_CLIENT_ID`, `OIDC_CLIENT_SECRET` in `config.env` and install oauth2-proxy via `./scripts/45_oauth2_proxy.sh`.
  - See README “Add OIDC To Your App” for a complete Ingress example.
  - Chart values quirk: the oauth2-proxy chart expects `ingress.hosts` as a list of strings, not objects. Scripts set `ingress.hosts[0]="${OAUTH_HOST}"`. Do not set `ingress.hosts[0].host` for this chart.
  - Cookie secret length: oauth2-proxy requires a secret of exactly 16, 24, or 32 bytes (characters if ASCII). Avoid base64-generating 32 bytes (length becomes 44 chars). The script now generates a 32-char alphanumeric secret when `OAUTH_COOKIE_SECRET` is unset.

- Logging with Fluent Bit (chart quirk)
  - The `fluent/fluent-bit` Helm chart does not support `backend.type=loki`. It uses `config.outputs` instead and defaults to Elasticsearch. `scripts/50_logging_fluentbit.sh` now applies a values overlay to set Loki outputs explicitly. If you see `elasticsearch-master` in the rendered ConfigMap, re-run step 50 to replace values (`--reset-values` is used).
  - Structured logs for better queries:
    - ingress-nginx: `values/ingress-nginx-logging.yaml` sets JSON `log-format-upstream` and escape. Reinstall controller (step 40) to apply.
    - oauth2-proxy: `scripts/45_oauth2_proxy.sh` enables JSON for standard/auth/request logs via `extraArgs.*-format=json`.
    - Fluent Bit: sends JSON (`line_format json`) and uses a curated `labelmap.json` to keep Loki labels low-cardinality. Query with LogQL: `{namespace="ingress"} | json | status>=500` or `{app="oauth2-proxy"} | json | method="GET"`.

- Secrets hygiene
  - Do not commit secrets in `config.env`. Use `profiles/secrets.env` (gitignored) for `ACME_EMAIL`, `OIDC_*`, `OAUTH_COOKIE_SECRET`, `MINIO_ROOT_PASSWORD`.
  - `scripts/00_lib.sh` now auto-sources `$PROFILE_FILE` (exported by `run.sh`), or falls back to `profiles/secrets.env` / `profiles/local.env` if present. This ensures direct script runs get secrets too.
  - A sample `profiles/secrets.example.env` is provided. Copy to `profiles/secrets.env` and fill in.

---

This guide explains how to stand up and operate a lightweight Kubernetes platform for development and small environments. It prefers k3s by default (Linux-friendly) with kind as an alternative. It covers platform components: cert-manager, ingress with OAuth proxy, logging (Fluent Bit), observability (Prometheus + Grafana), and object storage (MinIO). It also includes best practices for secrets and RBAC.

If you already have a cluster, you can jump directly to the Bootstrap section.

## Prerequisites

- Podman: Container runtime for local builds/runs.
- kubectl: Kubernetes CLI.
- Helm: Package manager for Kubernetes.
- age + sops: Secrets encryption for GitOps.
- Optional: yq/jq for YAML/JSON processing; kustomize if desired.

Install on macOS (Homebrew):

```
brew install podman kubernetes-cli helm age sops jq yq
podman machine init --cpus 4 --memory 6144 --disk-size 40
podman machine start
```

Install on Linux (Debian/Ubuntu example):

```
sudo apt-get update
sudo apt-get install -y podman curl gnupg lsb-release jq
curl -fsSL https://get.helm.sh/helm-v3.14.4-linux-amd64.tar.gz | tar -xz && sudo mv linux-amd64/helm /usr/local/bin/helm
curl -LO "https://dl.k8s.io/release/$(curl -Ls https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" && chmod +x kubectl && sudo mv kubectl /usr/local/bin/
sudo apt-get install -y sops
curl -fsSL https://github.com/FiloSottile/age/releases/latest/download/age-v1.1.1-linux-amd64.tar.gz | sudo tar -xz -C /usr/local/bin --strip-components=1 age/age age/age-keygen
```

Notes

- For local clusters that require a Docker-compatible API (e.g., kind), Podman provides a socket. On Linux enable it with: `systemctl --user enable --now podman.socket`. On macOS, the Podman machine already exposes the socket to the VM.
- Ensure `kubectl` context points to your target cluster before running Helm installs.

## Choose a Cluster

You have two supported paths (k3s by default):

1) Local or single-node: k3s (default)

- Install k3s with Traefik disabled (we install ingress-nginx):

```
curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--disable traefik" sh -
```

- Kubeconfig path: `/etc/rancher/k3s/k3s.yaml` (copy to `~/.kube/config` or set `KUBECONFIG`). Example:

```
sudo cp /etc/rancher/k3s/k3s.yaml ~/.kube/config
sudo chown $(id -u):$(id -g) ~/.kube/config
```

- Run scripts with `K8S_PROVIDER=k3s` (default in `config.env`).

2) Alternative: Local development via kind (Podman/Docker provider)

- Install kind:
  - macOS: `brew install kind`
  - Linux: `curl -Lo ./kind https://kind.sigs.k8s.io/dl/v0.22.0/kind-linux-amd64 && chmod +x ./kind && sudo mv ./kind /usr/local/bin/`
- Use Podman as the provider:
  - `export KIND_EXPERIMENTAL_PROVIDER=podman`
- Create a cluster (single node):

```
KIND_EXPERIMENTAL_PROVIDER=podman kind create cluster --name platform
kubectl cluster-info --context kind-platform
```

Optional: Multi-node kind config (save as `kind-config.yaml`):

```
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
  - role: control-plane
  - role: worker
  - role: worker
```

Create with: `KIND_EXPERIMENTAL_PROVIDER=podman kind create cluster --name platform --config kind-config.yaml`.

## Bootstrap (Helm)

We’ll install common namespaces, add Helm repos, then deploy core components. You can adjust names and values as needed.

Create namespaces

```
kubectl create namespace platform-system --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace ingress --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace cert-manager --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace logging --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace observability --dry-run=client -o yaml | kubectl apply -f -
kubectl create namespace storage --dry-run=client -o yaml | kubectl apply -f -
```

Add Helm repos

```
helm repo add jetstack https://charts.jetstack.io
helm repo add ingress-nginx https://kubernetes.github.io/ingress-nginx
helm repo add oauth2-proxy https://oauth2-proxy.github.io/manifests
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo add grafana https://grafana.github.io/helm-charts
helm repo add fluent https://fluent.github.io/helm-charts
helm repo add minio https://charts.min.io/
helm repo update
```

### TLS: cert-manager

Install CRDs and cert-manager:

```
helm upgrade --install cert-manager jetstack/cert-manager \
  --namespace cert-manager \
  --set installCRDs=true
```

ClusterIssuer examples (choose one):

1) Self-signed (for air-gapped/dev)

```
cat <<'EOF' | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: selfsigned
spec:
  selfSigned: {}
EOF
```

2) Let’s Encrypt HTTP-01 (requires publicly reachable ingress)

```
cat <<'EOF' | kubectl apply -f -
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt
spec:
  acme:
    email: you@example.com
    server: https://acme-v02.api.letsencrypt.org/directory
    privateKeySecretRef:
      name: acme-account-key
    solvers:
    - http01:
        ingress:
          class: nginx
EOF
```

### Ingress: NGINX + OAuth2 Proxy

Install ingress-nginx:

```
helm upgrade --install ingress-nginx ingress-nginx/ingress-nginx \
  --namespace ingress \
  --set controller.replicaCount=1 \
  --set controller.ingressClassResource.default=true
```

Install OAuth2 Proxy (configure with your OIDC provider, e.g., Google, GitHub, Auth0):

```
kubectl -n ingress create secret generic oauth2-proxy-secret \
  --from-literal=client-id="YOUR_CLIENT_ID" \
  --from-literal=client-secret="YOUR_CLIENT_SECRET" \
  --from-literal=cookie-secret="$(openssl rand -base64 32)"

helm upgrade --install oauth2-proxy oauth2-proxy/oauth2-proxy \
  --namespace ingress \
  --set config.existingSecret=oauth2-proxy-secret \
  --set extraArgs.provider=oidc \
  --set extraArgs.oidc-issuer-url="https://YOUR_ISSUER" \
  --set extraArgs.redirect-url="https://oauth.YOUR_DOMAIN/oauth2/callback" \
  --set extraArgs.email-domain="*" \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.annotations."cert-manager\.io/cluster-issuer"=letsencrypt \
  --set ingress.hosts[0].host="oauth.YOUR_DOMAIN" \
  --set ingress.hosts[0].paths[0].path="/" \
  --set ingress.tls[0].hosts[0]="oauth.YOUR_DOMAIN" \
  --set ingress.tls[0].secretName=oauth2-proxy-tls
```

Protect an app behind OAuth2 Proxy by annotating its Ingress:

```
metadata:
  annotations:
    nginx.ingress.kubernetes.io/auth-url: "https://oauth.YOUR_DOMAIN/oauth2/auth"
    nginx.ingress.kubernetes.io/auth-signin: "https://oauth.YOUR_DOMAIN/oauth2/start?rd=$scheme://$host$request_uri"
```

### Logging: Fluent Bit

Deploy Fluent Bit as a DaemonSet to collect container logs. You can ship to Loki, Elasticsearch/OpenSearch, or a vendor. Example to Loki (install Loki first or point to an existing instance):

Install Fluent Bit:

```
helm upgrade --install fluent-bit fluent/fluent-bit \
  --namespace logging \
  --set tolerations[0].operator=Exists \
  --set backend.type=loki \
  --set backend.loki.host="http://loki.observability.svc.cluster.local:3100"
```

Optional: Deploy Loki for log storage (single-tenant, dev):

```
helm repo add grafana https://grafana.github.io/helm-charts && helm repo update
helm upgrade --install loki grafana/loki \
  --namespace observability \
  --set loki.auth_enabled=false
```

### Observability: Prometheus + Grafana

Install the kube-prometheus-stack:

```
helm upgrade --install monitoring prometheus-community/kube-prometheus-stack \
  --namespace observability \
  --set grafana.ingress.enabled=true \
  --set grafana.ingress.ingressClassName=nginx \
  --set grafana.ingress.annotations."cert-manager\.io/cluster-issuer"=letsencrypt \
  --set grafana.ingress.hosts[0]="grafana.YOUR_DOMAIN" \
  --set grafana.ingress.tls[0].hosts[0]="grafana.YOUR_DOMAIN" \
  --set grafana.ingress.tls[0].secretName=grafana-tls
```

Secure Grafana via OAuth2 Proxy by applying the same auth annotations to the Grafana Ingress, or by enabling Grafana’s built-in OIDC if preferred.

### Storage: MinIO (Object Storage)

For k3s, the default `local-path` StorageClass handles PVs for simple workloads. For S3-compatible object storage inside the cluster, deploy MinIO:

```
helm upgrade --install minio minio/minio \
  --namespace storage \
  --set mode=standalone \
  --set resources.requests.memory=512Mi \
  --set replicas=1 \
  --set persistence.enabled=true \
  --set persistence.size=20Gi \
  --set ingress.enabled=true \
  --set ingress.ingressClassName=nginx \
  --set ingress.annotations."cert-manager\.io/cluster-issuer"=letsencrypt \
  --set ingress.hosts[0].host="minio.YOUR_DOMAIN" \
  --set ingress.hosts[0].paths[0].path="/" \
  --set ingress.tls[0].hosts[0]="minio.YOUR_DOMAIN" \
  --set ingress.tls[0].secretName=minio-tls \
  --set consoleIngress.enabled=true \
  --set consoleIngress.ingressClassName=nginx \
  --set consoleIngress.annotations."cert-manager\.io/cluster-issuer"=letsencrypt \
  --set consoleIngress.hosts[0]="minio-console.YOUR_DOMAIN" \
  --set consoleIngress.tls[0].hosts[0]="minio-console.YOUR_DOMAIN" \
  --set consoleIngress.tls[0].secretName=minio-console-tls
```

Set access keys via a pre-created Secret (recommended) rather than Helm values. Example:

```
kubectl -n storage create secret generic minio-creds \
  --from-literal=rootUser="minioadmin" \
  --from-literal=rootPassword="CHANGE_ME_LONG_RANDOM"
```

Then set `--set existingSecret=minio-creds` in the Helm install/upgrade.

## Secrets Management Best Practices

- Prefer GitOps-friendly encryption:
  - sops + age to encrypt YAML secrets in-repo.
  - Alternatively, Sealed Secrets (Bitnami) for controller-side decryption.
  - For cloud-managed secrets, use External Secrets Operator (ESO) to sync secrets from AWS/GCP/Azure.
- Never commit plaintext secrets. Enforce pre-commit hooks guarding against accidental leaks.
- Separate secrets by namespace and purpose; rotate regularly and on role changes.
- Use distinct client IDs/secrets per environment for OAuth/OIDC.

Quick start with sops + age

```
mkdir -p .keys
age-keygen -o .keys/age.key
echo "export SOPS_AGE_KEY_FILE=$(pwd)/.keys/age.key" >> .envrc
export SOPS_AGE_KEY_FILE=$(pwd)/.keys/age.key
cat > .sops.yaml <<'EOF'
creation_rules:
  - path_regex: secrets/.*\.ya?ml
    encrypted_regex: '^(data|stringData)$'
    age: ["REPLACE_WITH_YOUR_AGE_RECIPIENT"]
EOF
```

Generate an age recipient from the key:

```
age-keygen -y .keys/age.key
```

Create and encrypt a Kubernetes Secret manifest:

```
mkdir -p secrets
cat > secrets/oauth2-proxy.yaml <<'EOF'
apiVersion: v1
kind: Secret
metadata:
  name: oauth2-proxy-secret
  namespace: ingress
type: Opaque
stringData:
  client-id: YOUR_CLIENT_ID
  client-secret: YOUR_CLIENT_SECRET
  cookie-secret: CHANGE_ME
EOF

sops -e -i secrets/oauth2-proxy.yaml
kubectl apply -f secrets/oauth2-proxy.yaml
```

External Secrets Operator (optional)

- Install ESO Helm chart and configure a SecretStore pointing to your cloud secret manager.
- Reference external secrets in namespaces using ExternalSecret resources.

## RBAC, Security, and Multi-Tenancy

- Namespaces: Isolate by domain/team; apply labels for ownership and cost tracking.
- Least privilege: Avoid `cluster-admin`. Bind narrow Roles to ServiceAccounts.
- Service accounts: One per app; mount only required secrets; use `automountServiceAccountToken: false` unless needed.
- Network policies: Default deny all; allow only necessary egress/ingress between namespaces.
- Pod Security: Enforce Kubernetes Pod Security Standards (baseline/restricted) via namespace labels.
- Supply-chain: Pin images by digest; use image pull secrets; enable admission controls as appropriate.

Example Role and RoleBinding

```
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  namespace: apps
  name: app-reader
rules:
  - apiGroups: [""]
    resources: ["configmaps", "secrets"]
    verbs: ["get", "list"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  namespace: apps
  name: app-reader-binding
subjects:
  - kind: ServiceAccount
    name: my-app
    namespace: apps
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: app-reader
```

Pod Security labels (restricted)

```
kubectl label namespace apps pod-security.kubernetes.io/enforce=restricted \
  pod-security.kubernetes.io/audit=restricted \
  pod-security.kubernetes.io/warn=restricted
```

## Service Mesh Best Practices

- Mesh choice: Start simple (Linkerd) for lightweight clusters (kind/k3s); use Istio for advanced traffic policies and multi-cluster; consider Cilium Service Mesh if you already run Cilium and want eBPF data-plane. Prefer the smallest feature set that meets requirements.
- mTLS by default: Enforce strict mTLS mesh-wide. Use short-lived certs with automatic rotation. Back trust roots by cert-manager or a private CA. Prefer SPIFFE IDs for workload identity and auditability.
- Scope and injection: Enable sidecar injection by namespace label; exclude `kube-system`, `ingress`, and control-plane namespaces. Use per-namespace PeerAuthentication/Policy (Istio) or Server/AuthorizationPolicy (Linkerd). Consider ambient/sidecarless modes only after evaluating maturity and tradeoffs.
- Traffic policy: Set sane defaults for timeouts, retries, and budgets; add circuit breaking and outlier detection to protect dependencies. Use progressive delivery (Flagger or Argo Rollouts) for canaries/blue-green with mesh traffic shifting.
- Gateways and egress: Terminate inbound TLS at ingress gateway inside the mesh; originate TLS for external calls at an egress gateway. Restrict egress with explicit allowlists and DNS-based policies where possible.
- Observability: Scrape proxy metrics with Prometheus; install mesh dashboards. Enable distributed tracing via OpenTelemetry (export to Tempo/Jaeger), propagate `traceparent`, and keep sampling/cardinality under control.
- Security policy: Default deny at the mesh policy layer; authorize by SPIFFE ID/ServiceAccount and namespace. Use JWT validation at the edge and fine-grained AuthorizationPolicy/HTTPRoute filters for internal services; avoid wildcards.
- Performance/cost: Set requests/limits for proxies; right-size concurrency and connection pools. Exclude ultra high-throughput or latency-sensitive paths from the mesh if benefits don’t outweigh overhead. Tune HTTP/2 and keep-alives; keep tracing sample rates low on hot paths.
- Upgrades: Follow control-plane/data-plane version skew guidance. Canary upgrade the control plane; roll proxies with surge/partition. Pin CRD/chart versions in Git; validate in staging.
- Multi-cluster: Use a shared root trust or mesh federation with distinct trust domains. Use east-west gateways and export/import policies intentionally; restrict cross-cluster communication to necessary namespaces/services.
- Troubleshooting: Use `istioctl x precheck`, `istioctl proxy-status`, and Envoy admin (`/config_dump`, `/clusters`) or `linkerd check`, `linkerd viz`, and `linkerd tap` to trace requests. Temporarily disable injection per-pod via annotations for isolation.

## DNS and Domains

- For local dev without DNS, use magic hosts like `127.0.0.1.nip.io` or `sslip.io` to test Ingress quickly.
- For remote clusters, create DNS A/CNAME records for `*.YOUR_DOMAIN` pointing to the ingress controller LB/IP.

## Day-2 Ops (Brief)

- Backups: Back up etcd (or for k3s, use etcd or external DB); back up MinIO buckets; export Grafana dashboards.
- Upgrades: Upgrade Helm charts one at a time; monitor with Prometheus and logs; use staged environments when possible.
- Teardown: `kind delete cluster --name platform` or `sudo /usr/local/bin/k3s-uninstall.sh`.

## Troubleshooting

- Ingress 404s: Check `ingressClassName`, controller logs, and Service/Endpoints readiness.
- Certificates pending: Inspect cert-manager `Certificate`/`Order` events; verify DNS/HTTP-01 reachability and issuer name.
- OAuth loops: Validate `redirect-url`, cookie secret length (32+ bytes base64), and time skew.
- Fluent Bit drops: Verify backend connectivity and record sizes; check DaemonSet tolerations.
- Node storage: Ensure enough disk for local PVs; adjust MinIO persistence and requests.

## Suggested Repo Structure (optional)

```
infra/
  helm-values/
    cert-manager/values.yaml
    ingress-nginx/values.yaml
    oauth2-proxy/values.yaml
    kube-prometheus-stack/values.yaml
    fluent-bit/values.yaml
    minio/values.yaml
  manifests/
    issuers/
    ingress/
    rbac/
    network-policies/
secrets/
  (sops-encrypted secrets)
```

This document is a baseline. Adjust chart values and security controls to meet your environment’s requirements.

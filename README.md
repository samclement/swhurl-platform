# Swhurl Platform (k3s-only)

This repo provides a k3s-focused, declarative platform setup: Cilium CNI, cert-manager, ingress-nginx, oauth2-proxy, logging with Fluent Bit + Loki, observability (Prometheus + Grafana), and MinIO. Scripts are thin orchestrators around Helm + manifests in `infra/`.

## Quick Start

1. Install prerequisites.
2. Install or connect to k3s.
3. Make DNS and ports reachable for ACME.
4. Configure `config.env` and `profiles/secrets.env`.
5. Run `./run.sh`.

## Assumptions and Sequence

Each step lists assumptions and outputs. Run in order.

1. **Prereqs**
   - Assumes: `kubectl`, `helm`, `curl` installed.
   - Output: tools are available locally.
   - Command: `./scripts/01_check_prereqs.sh`

2. **Install or Connect to k3s**
   - Assumes: you will install k3s on a host or use an existing cluster.
   - Output: a reachable Kubernetes API from your machine.
   - Example install (local host, Cilium-compatible):
     ```bash
     curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC="--disable traefik --flannel-backend=none --disable-network-policy" sh -
     ```
   - Kubeconfig example:
     ```bash
     sudo cp /etc/rancher/k3s/k3s.yaml ~/.kube/config
     sudo chown $(id -u):$(id -g) ~/.kube/config
     ```
   - Verify access:
     ```bash
     kubectl get nodes
     ```

3. **DNS and Network Reachability (required for cert-manager with ACME)**
   - Assumes: your cluster ingress is reachable from the public internet on ports 80 and 443.
   - Assumes: your DNS A/CNAME records point at your public IP (or upstream router).
   - Assumes: your router forwards 80/443 to the k3s node running ingress-nginx.
   - Output: `*.${BASE_DOMAIN}` resolves to your public IP and is reachable on 80/443 (including `hubble.${BASE_DOMAIN}` for the Hubble UI).
   - If using swhurl.com dynamic DNS on Linux:
     ```bash
     ./scripts/12_dns_register.sh
     ```
   - If not using swhurl.com, create your DNS records manually.

4. **Configure Environment**
   - Assumes: you can edit local files and keep secrets out of git.
   - Output: values consumed by scripts.
   - Edit `config.env` for non-secrets and domain settings.
   - Put secrets in `profiles/secrets.env` (gitignored). See `profiles/secrets.example.env`.

5. **Verify kube context**
   - Assumes: kubeconfig is pointing at the target cluster.
   - Output: the scripts will operate on the intended cluster.
   - Command: `./scripts/15_kube_context.sh`

6. **Namespaces (declarative)**
   - Assumes: kube API is reachable.
   - Output: core namespaces created.
   - Command: `./scripts/20_namespaces.sh`

7. **Helm repositories**
   - Assumes: network access to chart repos.
   - Output: required Helm repos added and updated.
   - Command: `./scripts/25_helm_repos.sh`

8. **CNI (Cilium)**
   - Assumes: k3s was installed with `--flannel-backend=none --disable-network-policy`.
   - Output: Cilium daemonset and operator running.
   - Command: `./scripts/26_cilium.sh`
   - If flannel is detected, reinstall k3s or set `CILIUM_SKIP_FLANNEL_CHECK=true` to override.

9. **cert-manager**
   - Assumes: cluster can create CRDs and deployments.
   - Output: cert-manager installed and ready.
   - Command: `./scripts/30_cert_manager.sh`

10. **ClusterIssuer (Let’s Encrypt or self-signed)**
   - Assumes: DNS + ports 80/443 are reachable for ACME HTTP-01 if using Let’s Encrypt.
   - Assumes: `ACME_EMAIL` is set in `profiles/secrets.env`.
   - Output: ClusterIssuer created.
   - Command: `./scripts/35_issuer.sh`

11. **Ingress (ingress-nginx)**
    - Assumes: NodePort is reachable from your router (80->31514, 443->30313).
    - Output: ingress-nginx installed and default IngressClass set.
    - Command: `./scripts/40_ingress_nginx.sh`

12. **OAuth2 Proxy (optional)**
    - Assumes: OIDC provider credentials are set in `profiles/secrets.env`.
    - Output: oauth2-proxy deployed with ingress + TLS.
    - Command: `./scripts/45_oauth2_proxy.sh`

13. **Logging (optional)**
    - Assumes: Loki is installed or will be installed.
    - Output: Fluent Bit running with Loki outputs.
    - Command: `./scripts/50_logging_fluentbit.sh`

14. **Loki (optional)**
    - Assumes: observability namespace exists.
    - Output: Loki single-binary deployment.
    - Command: `./scripts/55_loki.sh`

15. **Observability (optional)**
    - Assumes: ingress and cert-manager are ready.
    - Output: Prometheus + Grafana with ingress.
    - Command: `./scripts/60_prom_grafana.sh`

16. **MinIO (optional)**
    - Assumes: storage namespace exists and credentials are set in `profiles/secrets.env`.
    - Output: MinIO and console ingresses.
    - Command: `./scripts/70_minio.sh`

17. **Sample App**
    - Assumes: `BASE_DOMAIN` is set and DNS resolves.
    - Output: sample app with ingress and cert.
    - Command: `./scripts/75_sample_app.sh`

18. **Smoke Tests**
   - Assumes: core components are installed.
   - Output: basic health validation.
   - Command: `./scripts/90_smoke_tests.sh`

19. **State Validation (cluster vs local config)**
    - Assumes: components are installed and kube API is reachable.
    - Output: mismatches reported with suggested re-runs.
    - Command: `./scripts/91_validate_cluster.sh`

## Full Run

```bash
./run.sh
```

Use a profile:

```bash
./run.sh --profile profiles/minimal.env
```

## Teardown

```bash
./run.sh --delete
```

k3s uninstall is manual unless `K3S_UNINSTALL=true`:

```bash
sudo /usr/local/bin/k3s-uninstall.sh
```

## Layout

```
infra/
  manifests/
  values/
profiles/
scripts/
config.env
run.sh
```

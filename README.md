# Swhurl Platform (Declarative GitOps)

This repository manages a homelab Kubernetes platform using Flux CD and SOPS.

## Architecture

- **GitOps:** Flux CD manages the entire lifecycle (Infrastructure -> Platform -> Apps).
- **Secrets:** SOPS with Age encrypts secrets directly in Git.
- **DNS:** ExternalDNS automatically manages Route53 records from Ingress.
- **Verification:** Flux Health Checks ensure the platform is ready.

## Quickstart

1. **Prerequisites:**
   - A k3s cluster (or any K8s cluster).
   - `flux`, `sops`, `age`, `kubectl` installed locally.
   - AWS credentials for Route53.

2. **Setup Age Key:**
   ```bash
   age-keygen -o age.agekey
   # Add the public key to .sops.yaml
   # Import the key to the cluster:
   kubectl -n flux-system create secret generic sops-age --from-file=age.agekey=age.agekey
   ```

3. **Configure Secrets:**
   - Copy `profiles/secrets.example.env` to `profiles/secrets.env` and fill it out.
   - Encrypt and update the secret:
     ```bash
     # (Implicitly handled by make targets or manual sops commands)
     sops --encrypt --in-place clusters/home/flux-system/sources/secrets.sops.yaml
     ```

4. **Install Flux:**
   ```bash
   flux install --namespace flux-system
   ```

5. **Bootstrap the Platform:**
   ```bash
   make install
   ```

## Key Commands

- `make install`: Full declarative rollout.
- `make teardown`: Complete platform removal.
- `make flux-reconcile`: Trigger an immediate Git sync and reconciliation.
- `make verify`: Run local health verification scripts.

## Directory Structure

- `clusters/`: Flux cluster entrypoints and dependency definitions.
- `infrastructure/`: Shared infra (cert-manager, traefik, external-dns, minio).
- `platform-services/`: Platform services (oauth2-proxy, clickstack, otel).
- `tenants/`: Application environments and example apps.
- `scripts/`: Teardown and specialized verification logic.

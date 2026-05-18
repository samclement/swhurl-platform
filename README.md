# Swhurl Platform

Flux-managed k3s homelab platform.

The active repo layout is:

- `clusters/home`: Flux entrypoint and reconciliation chain
- `infrastructure/overlays/home`: shared cluster infrastructure
- `platform-services/overlays/home`: shared platform services
- `tenants/app-envs`: tenant landing zones
- `tenants/apps/example`: sample app deployed by its own Flux Kustomization

## Quick Start

1. Configure non-secrets in `config.env` and `clusters/home/flux-system/sources/configmap-platform-settings.yaml`.
2. Configure platform runtime Secrets (Git-managed SOPS target manifests):

```bash
SOPS_AGE_KEY_FILE=./age.agekey sops platform-services/runtime-inputs/secret-oauth2-proxy-shared.sops.yaml
SOPS_AGE_KEY_FILE=./age.agekey sops platform-services/runtime-inputs/secret-clickstack-runtime-inputs.sops.yaml
SOPS_AGE_KEY_FILE=./age.agekey sops platform-services/runtime-inputs/secret-hyperdx.sops.yaml
git add platform-services/runtime-inputs/*.sops.yaml
git commit -m "runtime-inputs: set platform secrets"
git push
```

3. Install Flux (one-time) and apply the stack:

```bash
flux check --pre
flux install --namespace flux-system
kubectl -n flux-system create secret generic sops-age \
  --from-file=age.agekey=./age.agekey \
  --dry-run=client -o yaml | kubectl apply -f -
make flux-bootstrap
make install
```

4. Optional host dynamic DNS bootstrap:

```bash
# Optional: set custom records via DYNAMIC_DNS_RECORDS in config.env
make host-dns
```

## Docs

- [`docs/INFRASTRUCTURE.md`](docs/INFRASTRUCTURE.md)
- [`docs/PLATFORM-SERVICES.md`](docs/PLATFORM-SERVICES.md)
- [`docs/TENANTS.md`](docs/TENANTS.md)
- [`docs/runbook.md`](docs/runbook.md)
- [`docs/orchestration-api.md`](docs/orchestration-api.md)
- [`docs/architecture.md`](docs/architecture.md)

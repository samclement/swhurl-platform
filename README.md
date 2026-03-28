# Swhurl Platform

Flux-managed k3s homelab platform.

The active repo layout is:

- `clusters/home`: Flux entrypoint and reconciliation chain
- `infrastructure/overlays/home`: shared cluster infrastructure
- `platform-services/overlays/home`: shared platform services
- `tenants/app-envs`: tenant landing zones
- `tenants/apps/example`: sample app deployed by its own Flux Kustomization

## Quick Start

1. Install k3s manually with packaged `traefik` and `metrics-server` enabled.
2. Install the required CLI tools: `bash`, `kubectl`, `helm`, `flux`, `sops`, and `age`.
3. Review [`config.env`](config.env) for non-secret local defaults and intent hints.
4. Edit the Git-managed runtime secret at [`clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml`](clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml).
5. Install Flux controllers and create the `flux-system/sops-age` secret from `age.agekey`.
6. Apply the bootstrap manifests and reconcile the stack:

```bash
flux check --pre
flux install --namespace flux-system
kubectl -n flux-system create secret generic sops-age \
  --from-file=age.agekey=./age.agekey \
  --dry-run=client -o yaml | kubectl apply -f -
make flux-bootstrap
make install
```

7. Optional host dynamic DNS bootstrap:

```bash
make host-dns
```

## Docs

- [`docs/INFRASTRUCTURE.md`](docs/INFRASTRUCTURE.md)
- [`docs/PLATFORM-SERVICES.md`](docs/PLATFORM-SERVICES.md)
- [`docs/TENANTS.md`](docs/TENANTS.md)
- [`docs/runbook.md`](docs/runbook.md)
- [`docs/architecture.md`](docs/architecture.md)

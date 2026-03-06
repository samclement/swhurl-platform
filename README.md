# Swhurl Platform

Flux-managed k3s homelab platform.

## Scope

This repo manages the platform stack with Flux GitOps.

- cert-manager + ClusterIssuers
- Traefik ingress controller (k3s packaged)
- metrics-server (k3s packaged)
- oauth2-proxy
- ClickStack + OTel collectors
- MinIO
- sample app (`hello-web`) via `tenants/apps/example`

![C4 Container](docs/charts/c4/rendered/container.svg)

- Architecture: `docs/architecture.md`
- C4 sources: `docs/charts/c4/*.d2`
- Render charts: `make charts-generate`

## Prereqs

- Tools: `bash`, `kubectl`, `helm`, `flux`, `curl`, `rg`, `envsubst`, `base64`, `hexdump`, `sops`, `age`
- Optional: `jq`, `yq`, `d2`
- Cluster: install k3s with packaged `traefik` and `metrics-server` enabled

## Start

1. Configure non-secrets in `config.env`.
2. Configure runtime secrets (Git-managed SOPS source):

```bash
sops clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
git add clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
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

4. Optional host bootstrap (dynamic DNS):

```bash
./host/run-host.sh
```

## Layout

- `clusters/`: Flux cluster entrypoints and bootstrap manifests
- `infrastructure/`: shared infrastructure manifests
- `platform-services/`: shared platform-service manifests
- `tenants/`: app environment manifests
- `docs/`: runbooks, ADRs, and architecture/design notes

## Docs

- Detailed operations moved to `docs/readme-ops.md`
- Runbook: `docs/runbook.md`
- Orchestration API: `docs/orchestration-api.md`

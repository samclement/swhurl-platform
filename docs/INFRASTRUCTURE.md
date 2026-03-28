# Infrastructure

## Overview

This repository manages a Flux-first platform on a manually installed k3s cluster. The active operational path is `make`-first:

- `make flux-bootstrap`
- `make install`
- `make flux-reconcile`
- `make teardown`
- `make verify`

There is no `run.sh` orchestration flow in the current repo. Cluster creation and Flux controller installation are manual prerequisites.

## Repository Layout

- [`clusters/home`](../clusters/home): Flux entrypoint for the cluster stack
- [`clusters/home/flux-system`](../clusters/home/flux-system): Flux bootstrap manifests, source definitions, and shared substitutions
- [`infrastructure/overlays/home`](../infrastructure/overlays/home): shared infrastructure composition
- [`platform-services/overlays/home`](../platform-services/overlays/home): shared platform services composition
- [`tenants/app-envs`](../tenants/app-envs): tenant landing zones
- [`tenants/apps/example`](../tenants/apps/example): sample application reconciled as its own Flux Kustomization
- [`host`](../host): optional host-level dynamic DNS automation
- [`scripts`](../scripts): Flux reconcile, verification, and chart generation helpers

## Flux Architecture

The current reconciliation chain is:

1. `homelab-flux-sources`
2. `homelab-flux-stack`
3. `homelab-infrastructure`
4. `homelab-platform`
5. `homelab-tenants`
6. `homelab-app-example`

Key files:

- [`clusters/home/flux-system/kustomizations.yaml`](../clusters/home/flux-system/kustomizations.yaml): bootstraps `homelab-flux-sources` and `homelab-flux-stack`
- [`clusters/home/infrastructure.yaml`](../clusters/home/infrastructure.yaml): points to `./infrastructure/overlays/home`
- [`clusters/home/platform.yaml`](../clusters/home/platform.yaml): points to `./platform-services/overlays/home`
- [`clusters/home/tenants.yaml`](../clusters/home/tenants.yaml): points to `./tenants/app-envs`
- [`clusters/home/app-example.yaml`](../clusters/home/app-example.yaml): points to `./tenants/apps/example`

Flux substitutions are split intentionally:

- `homelab-infrastructure` substitutes from `flux-system/platform-settings`
- `homelab-platform` substitutes from `flux-system/platform-settings` and `flux-system/platform-runtime-inputs`

## Prerequisites

Required:

- k3s cluster installed manually
- packaged k3s `traefik`
- packaged k3s `metrics-server`
- `bash`
- `kubectl`
- `helm`
- `flux`
- `sops`
- `age`

Useful but optional:

- `jq`
- `yq`
- `d2`

## Getting Started

### 1. Prepare the cluster

Install k3s manually and keep the packaged `traefik` and `metrics-server` components enabled. The repo assumes those are already present.

### 2. Review non-secret config

Review [`config.env`](../config.env). In the current repo this file mostly provides local verification inputs and host-bootstrap defaults. It is not the single source of truth for all runtime values.

### 3. Configure runtime secrets

Edit the SOPS-encrypted runtime input source:

```bash
sops clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
git add clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
git commit -m "runtime-inputs: update platform secrets"
git push
```

### 4. Install Flux and the SOPS decryption key

```bash
flux check --pre
flux install --namespace flux-system
kubectl -n flux-system create secret generic sops-age \
  --from-file=age.agekey=./age.agekey \
  --dry-run=client -o yaml | kubectl apply -f -
```

### 5. Bootstrap and reconcile

```bash
make flux-bootstrap
make install
```

### 6. Verify

```bash
make verify
```

## Operational Commands

- `make install`: verify config, reconcile Flux, then verify platform state
- `make flux-bootstrap`: apply Flux bootstrap manifests after Flux is manually installed
- `make flux-reconcile`: reconcile the Git source and stack Kustomizations
- `make teardown`: delete only `homelab-flux-stack` and `homelab-flux-sources`
- `make verify-config`: validate repo/config contracts
- `make verify-platform`: validate live platform state against expected contracts
- `make host-dns`: install or update the host dynamic DNS updater
- `make host-dns-delete`: remove the host dynamic DNS updater
- `make charts-generate`: render the D2 architecture diagrams

## Caveats

- k3s installation is manual. This repo does not provision the cluster.
- Flux controller installation is manual. `make flux-bootstrap` only applies the Git-tracked Flux manifests.
- `make teardown` is stack-only. It removes Flux stack Kustomizations and leaves Flux controllers, CRDs, and cluster services installed.
- The source GitRepository is pinned to the `main` branch and the canonical GitHub URL in [`clusters/home/flux-system/sources/gitrepositories.yaml`](../clusters/home/flux-system/sources/gitrepositories.yaml). Forked use requires updating that manifest.
- Certificate mode is controlled by [`clusters/home/flux-system/sources/configmap-platform-settings.yaml`](../clusters/home/flux-system/sources/configmap-platform-settings.yaml), not by `config.env`.
- Runtime secrets belong in the SOPS-managed source secret, not in `config.env`.
- The active ingress path depends on packaged k3s Traefik and the pinned NodePorts from [`infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml`](../infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml).
- Host dynamic DNS automation in [`host/dynamic-dns.sh`](../host/dynamic-dns.sh) is Linux and `systemd` specific.

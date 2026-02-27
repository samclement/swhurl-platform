# Flux Bootstrap

This folder contains bootstrap manifests for Flux source/controller wiring.

Typical flow:

1. Install Flux controllers on cluster (`scripts/bootstrap/install-flux.sh`).
2. Apply this directory (`kubectl apply -k cluster/flux`).
3. Reconcile `homelab-flux-sources` then `homelab-flux-stack` (`make flux-reconcile`).
4. Add component-level `Kustomization`/`HelmRelease` objects under `cluster/base` and `cluster/overlays`.

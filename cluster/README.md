# Cluster Layer (GitOps)

This directory is the target declarative cluster layer for migration from script-driven orchestration.

Structure:

- `flux/`: Flux bootstrap manifests and source definitions.
- `base/`: provider-agnostic platform components.
- `overlays/homelab/`: environment composition and provider selection.

During migration, legacy orchestration in `run.sh` remains the source of truth for applies/deletes.
Use this tree as the build-out path for phased GitOps adoption.

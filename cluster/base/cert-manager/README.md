# Base Component: cert-manager

Target location for cert-manager controller and issuer-related base resources as migration
moves from Helmfile ownership to Flux-managed manifests.

Current scaffold:

- `helmrelease-cert-manager.yaml`: suspended Flux `HelmRelease` matching legacy defaults.
- `issuers/`: separate layer for issuer ownership (kept split for explicit sequencing).

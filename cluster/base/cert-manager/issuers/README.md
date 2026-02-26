# Base Component: cert-manager issuers

Target location for ClusterIssuer/Issuer manifests after issuer ownership migrates from
`charts/platform-issuers` to Flux-managed resources.

Current pipeline source of truth remains:

- `charts/platform-issuers`
- `infra/values/platform-issuers-helmfile.yaml.gotmpl`
- `scripts/31_sync_helmfile_phase_core.sh`

Current scaffold:

- `helmrelease-platform-issuers.yaml`: suspended Flux `HelmRelease` pointing at
  `./charts/platform-issuers` in this repo.
- Values are intentionally safe defaults and should be aligned with environment contract before
  unsuspending. Current contract always renders: `selfsigned`, `letsencrypt-staging`,
  `letsencrypt-prod`, and `letsencrypt` alias.

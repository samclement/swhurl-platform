# Base Component: cert-manager issuers

Target location for ClusterIssuer/Issuer manifests after issuer ownership migrates from
`charts/platform-issuers` to Flux-managed resources.

Current pipeline source of truth remains:

- `charts/platform-issuers`
- `infra/values/platform-issuers-helmfile.yaml.gotmpl`
- `scripts/31_sync_helmfile_phase_core.sh`

# Base Component: cert-manager issuers

Active Flux-owned issuer layer using plain `ClusterIssuer` manifests.

Contract:
- Always renders `selfsigned`, `letsencrypt-staging`, and `letsencrypt-prod`.
- Issuer server/email configuration is file-managed in this directory (no runtime-input substitution).

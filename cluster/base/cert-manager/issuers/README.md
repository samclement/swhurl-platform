# Base Component: cert-manager issuers

Active Flux-owned issuer layer using plain `ClusterIssuer` manifests.

Contract:
- Always renders `selfsigned`, `letsencrypt-staging`, `letsencrypt-prod`, and `letsencrypt` alias.
- `${ACME_EMAIL}`, `${LETSENCRYPT_STAGING_SERVER}`, `${LETSENCRYPT_PROD_SERVER}`, and `${LETSENCRYPT_ALIAS_SERVER}` are substituted by Flux from `flux-system/platform-runtime-inputs`.

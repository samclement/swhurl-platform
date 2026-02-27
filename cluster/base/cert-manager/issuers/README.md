# Base Component: cert-manager issuers

Active Flux-owned issuer layer using the local `charts/platform-issuers` chart.

Contract:
- Always renders `selfsigned`, `letsencrypt-staging`, `letsencrypt-prod`, and `letsencrypt` alias.
- Base values default to staging alias issuer intent; use platform overlays for promotion.
- `letsencrypt.email` is sourced from `${ACME_EMAIL}` and substituted by Flux from `flux-system/platform-runtime-inputs`.

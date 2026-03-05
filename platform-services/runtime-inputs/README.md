# Runtime Inputs

Declarative runtime secret targets used by platform components:

- `ingress/oauth2-proxy-shared-secret`
- `logging/hyperdx-secret`
- `observability/clickstack-runtime-inputs`
- source values in `flux-system/platform-runtime-inputs` (managed by Flux SOPS)

Key mapping:

- `ingress/oauth2-proxy-shared-secret.client-id` <- `${SHARED_OIDC_CLIENT_ID}`
- `ingress/oauth2-proxy-shared-secret.client-secret` <- `${SHARED_OIDC_CLIENT_SECRET}`
- `platform-runtime-inputs.OAUTH_HOST` <- `${OAUTH_HOST}` (used by shared oauth2-proxy ingress + redirect URL)
- `observability/clickstack-runtime-inputs.CLICKSTACK_API_KEY` <- `${CLICKSTACK_API_KEY}`
- `logging/hyperdx-secret.HYPERDX_API_KEY` <- `${CLICKSTACK_INGESTION_KEY}`

`homelab-platform` in `clusters/home/platform.yaml` uses Flux
`postBuild.substituteFrom` to inject values from `flux-system/platform-runtime-inputs`
into these target secret manifests.

## Updating Secrets

Secrets are managed via SOPS. To update, edit the encrypted file:

```bash
export SOPS_AGE_KEY_FILE=age.agekey
sops clusters/home/flux-system/sources/secrets.sops.yaml
```

Commit and push to Git. Flux will automatically decrypt and apply the changes.

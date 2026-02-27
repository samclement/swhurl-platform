# Runtime Inputs

Declarative runtime secret targets used by platform components:

- `ingress/oauth2-proxy-secret`
- `logging/hyperdx-secret`
- `observability/clickstack-runtime-inputs`
- source values in `flux-system/platform-runtime-inputs` (external prerequisite)

Key mapping:

- `observability/clickstack-runtime-inputs.CLICKSTACK_API_KEY` <- `${CLICKSTACK_API_KEY}`
- `logging/hyperdx-secret.HYPERDX_API_KEY` <- `${CLICKSTACK_INGESTION_KEY}`
  - fallback behavior in `scripts/bootstrap/sync-runtime-inputs.sh`: if `CLICKSTACK_INGESTION_KEY` is unset, it uses `CLICKSTACK_API_KEY` until you update from ClickStack UI.
- Flux post-build substitutions for platform manifests also read these source keys directly:
  - `${PLATFORM_CLUSTER_ISSUER}`
  - `${APP_CLUSTER_ISSUER}`
  - `${APP_NAMESPACE}`
  - `${APP_HOST}`
  - `${LETSENCRYPT_ENV}`
  - `${LETSENCRYPT_STAGING_SERVER}`
  - `${LETSENCRYPT_PROD_SERVER}`
  - `${LETSENCRYPT_ALIAS_SERVER}` (computed from `LETSENCRYPT_ENV` by `scripts/bootstrap/sync-runtime-inputs.sh`)

`homelab-runtime-inputs` in `cluster/overlays/homelab/flux/stack-kustomizations.yaml`
uses Flux `postBuild.substituteFrom` to inject values from
`flux-system/platform-runtime-inputs` into these target manifests.

Create/update the source secret with:

```bash
make runtime-inputs-sync
```

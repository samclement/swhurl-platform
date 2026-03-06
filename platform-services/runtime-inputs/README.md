# Runtime Inputs

Declarative runtime secret targets used by platform components:

- `ingress/oauth2-proxy-shared-secret`
- `logging/hyperdx-secret`
- `observability/clickstack-runtime-inputs`
- source values in `flux-system/platform-runtime-inputs` (Git-managed via SOPS)

Key mapping:

- `ingress/oauth2-proxy-shared-secret.client-id` <- `${SHARED_OIDC_CLIENT_ID}`
- `ingress/oauth2-proxy-shared-secret.client-secret` <- `${SHARED_OIDC_CLIENT_SECRET}`
- `platform-runtime-inputs.OAUTH_HOST` <- `${OAUTH_HOST}` (used by shared oauth2-proxy ingress + redirect URL)
- `observability/clickstack-runtime-inputs.CLICKSTACK_API_KEY` <- `${CLICKSTACK_API_KEY}`
- `logging/hyperdx-secret.HYPERDX_API_KEY` <- `${CLICKSTACK_INGESTION_KEY}`
  - fallback behavior: if `CLICKSTACK_INGESTION_KEY` is unset in the source secret, set it equal to `CLICKSTACK_API_KEY` until you update from ClickStack UI.
- Flux post-build substitutions from this source secret are used only for runtime secret targets (`oauth2-proxy-shared`, `clickstack`, `otel`).

`homelab-platform` in `clusters/home/platform.yaml` uses Flux
`postBuild.substituteFrom` to inject values from `flux-system/platform-runtime-inputs`
into these target secret manifests.

Edit/update the source secret in Git:

```bash
sops clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
git add clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml
git commit -m "runtime-inputs: update platform secrets"
git push
make runtime-inputs-sync
```

For ClickStack key updates, run the full refresh flow so running `otel-k8s-*` collectors pick up the new `HYPERDX_API_KEY` env value:

```bash
make runtime-inputs-refresh-otel
```

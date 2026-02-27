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

`homelab-runtime-inputs` in `cluster/overlays/homelab/flux/stack-kustomizations.yaml`
uses Flux `postBuild.substituteFrom` to inject values from
`flux-system/platform-runtime-inputs` into these target manifests.

Create/update the source secret with:

```bash
make runtime-inputs-sync
```

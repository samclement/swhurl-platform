# Runtime Inputs

Declarative runtime secret targets used by platform components:

- `ingress/oauth2-proxy-secret`
- `logging/hyperdx-secret`
- `observability/clickstack-runtime-inputs`
- `observability/clickstack-bootstrap-inputs`
- source values in `flux-system/platform-runtime-inputs` (external prerequisite)

`homelab-runtime-inputs` in `cluster/overlays/homelab/flux/stack-kustomizations.yaml`
uses Flux `postBuild.substituteFrom` to inject values from
`flux-system/platform-runtime-inputs` into these target manifests.

Create/update the source secret with:

```bash
make runtime-inputs-sync
```

# Runtime Inputs

Declarative runtime secret targets used by platform components:

- `ingress/oauth2-proxy-secret`
- `logging/hyperdx-secret`
- `observability/clickstack-runtime-inputs`
- `observability/clickstack-bootstrap-inputs`
- source values in `flux-system/platform-runtime-inputs`

`kustomization.yaml` uses `replacements` to project source keys from
`platform-runtime-inputs` into workload target secrets.

Update `secret-platform-runtime-inputs.yaml` (or patch it from a private overlay)
to set environment-specific credentials.

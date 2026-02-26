# Runtime Inputs

Declarative runtime secret targets used by platform components:

- `ingress/oauth2-proxy-secret`
- `logging/hyperdx-secret`

Values are populated by Flux post-build substitution from:

- `flux-system/Secret platform-runtime-inputs`

Sync/update the source secret via `scripts/29_prepare_platform_runtime_inputs.sh`.

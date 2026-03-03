# Runtime Inputs

Declarative runtime secret targets used by platform components:

- `ingress/oauth2-proxy-secret`
- `ingress/oauth2-proxy-keycloak-canary-secret`
- `identity/keycloak-runtime-inputs`
- `logging/hyperdx-secret`
- `observability/clickstack-runtime-inputs`
- source values in `flux-system/platform-runtime-inputs` (external prerequisite)

Key mapping:

- `observability/clickstack-runtime-inputs.CLICKSTACK_API_KEY` <- `${CLICKSTACK_API_KEY}`
- `ingress/oauth2-proxy-keycloak-canary-secret.client-id` <- `${KEYCLOAK_CANARY_OIDC_CLIENT_ID}`
- `ingress/oauth2-proxy-keycloak-canary-secret.client-secret` <- `${KEYCLOAK_CANARY_OIDC_CLIENT_SECRET}`
- `ingress/oauth2-proxy-keycloak-canary-secret.cookie-secret` <- `${KEYCLOAK_CANARY_OAUTH_COOKIE_SECRET}`
- `identity/keycloak-runtime-inputs.admin-password` <- `${KEYCLOAK_ADMIN_PASSWORD}`
- `identity/keycloak-runtime-inputs.postgres-password` <- `${KEYCLOAK_POSTGRES_PASSWORD}`
- `logging/hyperdx-secret.HYPERDX_API_KEY` <- `${CLICKSTACK_INGESTION_KEY}`
  - fallback behavior in `scripts/bootstrap/sync-runtime-inputs.sh`: if `CLICKSTACK_INGESTION_KEY` is unset, it uses `CLICKSTACK_API_KEY` until you update from ClickStack UI.
- Flux post-build substitutions from this source secret are used only for runtime secret targets (`oauth2-proxy`, `oauth2-proxy-keycloak-canary`, `keycloak`, `clickstack`, `otel`).

`homelab-platform` in `clusters/home/platform.yaml` uses Flux
`postBuild.substituteFrom` to inject values from `flux-system/platform-runtime-inputs`
into these target secret manifests.

Create/update the source secret with:

```bash
make runtime-inputs-sync
```

For ClickStack key updates, run the full refresh flow so running `otel-k8s-*` collectors pick up the new `HYPERDX_API_KEY` env value:

```bash
make runtime-inputs-refresh-otel
```

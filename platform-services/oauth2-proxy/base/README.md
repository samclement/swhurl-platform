# Base Component: oauth2-proxy

Active Flux-owned shared oauth2-proxy release definition.

- Uses `upstream=static://202` for ForwardAuth mode with redirect responses from the auth service itself.
- Callback host/path is `https://${OAUTH_HOST}/oauth2/callback` (from `platform-settings` Flux substitution).
- Runtime credentials live in `secret-oauth2-proxy-shared.sops.yaml` and are consumed through `config.existingSecret: oauth2-proxy-shared-secret`.
- Includes shared Traefik middleware in `ingress` namespace:
  - `oauth-auth-shared` (`forwardAuth` to `/`)

# Base Component: oauth2-proxy

Active Flux-owned oauth2-proxy release definition.

- Uses `upstream=static://202` for ForwardAuth mode with redirect responses from the auth service itself.
- Includes shared Traefik middleware in `ingress` namespace:
  - `oauth-auth` (`forwardAuth` to `/`)

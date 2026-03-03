# Base Component: oauth2-proxy

Active Flux-owned oauth2-proxy release definition.

- Includes shared Traefik middlewares in `ingress` namespace:
  - `oauth-auth` (`forwardAuth` to `/oauth2/auth`)
  - `oauth-signin` (`errors` middleware mapping `401-403` to `/oauth2/start?rd={url}`)

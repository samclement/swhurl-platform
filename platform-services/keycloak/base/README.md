# Base Component: Keycloak

Optional identity provider for platform OIDC.

- Namespace: `identity`
- Host: `https://keycloak.homelab.swhurl.com`
- Ingress class: `traefik`
- TLS: cert-manager (`cert-manager.io/cluster-issuer: ${CERT_ISSUER}`)

Rollout safety:
- `HelmRelease/keycloak` is intentionally `suspend: true` by default.
- Keep oauth2-proxy on the current issuer until Keycloak realm/client setup and login flow are verified.

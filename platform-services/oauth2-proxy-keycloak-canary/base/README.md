# Base Component: oauth2-proxy Keycloak Canary

Canary oauth2-proxy instance for validating Keycloak OIDC integration without touching existing app auth routes.

- Namespace: `ingress`
- Host: `https://oauth-keycloak.homelab.swhurl.com`
- Ingress class: `traefik`
- TLS: cert-manager (`cert-manager.io/cluster-issuer: ${CERT_ISSUER}`)

Rollout safety:
- `HelmRelease/oauth2-proxy-keycloak-canary` is intentionally `suspend: true` by default.
- Existing `oauth2-proxy` remains the active auth path until canary validation is complete.

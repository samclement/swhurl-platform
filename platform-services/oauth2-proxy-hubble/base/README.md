# Base Component: oauth2-proxy-hubble

Dedicated oauth2-proxy reverse-proxy release for Hubble UI in `kube-system`.

- Uses Google OIDC and upstreams to `http://hubble-ui.kube-system.svc.cluster.local:80`.
- Hubble ingress backend should target service `oauth2-proxy-hubble` (same namespace).

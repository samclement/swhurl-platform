# Traefik settings

k3s installs and owns Traefik (chart 38, Traefik 3.6). This directory holds only the `HelmChartConfig` override, reconciled by `homelab-traefik`:

- NodePorts pinned for the router: HTTP `80 → 31514`, HTTPS `443 → 30313`.
- Permanent HTTP→HTTPS redirect via `ports.web.redirections.entryPoint`. Chart 38 silently ignores the older `redirectTo` key; `make verify-platform` checks the rendered redirect.

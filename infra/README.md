# Infrastructure

Cluster primitives with no user-facing endpoint, one directory and one Flux unit (`infra-<directory>`) each, defined in [`clusters/home/infra.yaml`](../clusters/home/infra.yaml):

- `base`: shared namespaces and the `local-path-retain` storage class
- `cert-manager`: the cert-manager release
- `issuers`: the self-signed and Let's Encrypt ClusterIssuers
- `traefik`: the override for k3s-packaged Traefik (NodePorts pinned to `31514`/`30313`)

k3s packaged networking (`flannel`), Traefik and `metrics-server` stay enabled. Platform ingresses take their issuer from `CERT_ISSUER` in [`platform-settings`](../clusters/home/flux-system/sources/configmap-platform-settings.yaml). Overview: [services](../docs/services.md).

# Base Component: Cilium Add-ons

Flux-managed Cilium-adjacent resources that are safe to apply after Cilium
bootstrap is already present (for example, Hubble UI ingress).

Core Cilium install is intentionally not Flux-owned in this repo. It is
bootstrapped pre-Flux via k3s helm-controller manifest:
- `bootstrap/k3s-manifests/cilium-helmchart.yaml`

`helmrelease-cilium.yaml` is intentionally suspended and retained as a
migration handoff placeholder for existing clusters.

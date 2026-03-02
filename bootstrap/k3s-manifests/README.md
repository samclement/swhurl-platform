# k3s Bootstrap Manifests

These manifests are applied by k3s' built-in helm-controller and are intended
for pre-Flux bootstrap dependencies.

Use this layer for components that must exist before Flux controllers can run
reliably (for example, CNI when k3s is started with `--flannel-backend=none`).

Current manifest:
- `cilium-helmchart.yaml`: pre-Flux Cilium install managed by k3s helm-controller.


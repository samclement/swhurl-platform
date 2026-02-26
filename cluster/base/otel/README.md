# Base Component: otel

Target location for Kubernetes OpenTelemetry collector resources and pipeline manifests.

Current scaffold:

- `helmrelease-otel-k8s-daemonset.yaml`: suspended Flux `HelmRelease` for daemonset collector.
- `helmrelease-otel-k8s-cluster.yaml`: suspended Flux `HelmRelease` for cluster/deployment collector.

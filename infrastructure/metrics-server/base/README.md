# Metrics Server (Flux)

Repo-managed metrics-server deployment for kube-system.

Why this exists:
- Some homelab CNI/network combinations can prevent pod-network access to kubelet on `:10250`.
- k3s packaged metrics-server can remain NotReady (`Failed to scrape node ... connect: connection refused`) in that scenario.

This release uses:
- `hostNetwork.enabled=true` to avoid pod-network path issues.
- `--secure-port=4443` to avoid host port collision with kubelet on `10250`.
- `--kubelet-insecure-tls` for kubelet cert compatibility in homelab environments.

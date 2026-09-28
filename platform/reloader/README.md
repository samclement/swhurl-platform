# Base Component: Reloader

[Stakater Reloader](https://github.com/stakater/Reloader) restarts a workload when a Secret it names changes, so rotated credentials take effect without a manual restart.

- Release `platform-system/reloader`, Flux unit `platform-reloader`.
- Scoped: watches only `reloader.namespaces` (currently `ingress`, `logging`) with a namespaced Role in each. ConfigMaps are ignored.
- Opt-in only: a workload restarts when it carries `secret.reloader.stakater.com/reload: "<secret>"` on its Deployment/DaemonSet metadata. Nothing restarts automatically otherwise.
- Current opt-ins: `ingress/oauth2-proxy-shared` → `oauth2-proxy-shared-secret`; both OTel collectors → `hyperdx-secret`.
- Restarts use the `annotations` strategy: Reloader patches a pod-template annotation, which triggers a rolling update. A later Helm upgrade may drop that annotation and roll the pods once more; this is harmless.

To opt in a workload in another namespace, add the namespace to `reloader.namespaces` first.

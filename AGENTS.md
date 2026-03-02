# Platform Infrastructure Guide (Agents)

## Agents Operating Notes

- Always update this file with concise, actionable learnings discovered while working in this repo.
- Keep guidance tied to current files only. Remove stale references when architecture/scripts change.
- If a learning implies a code change, also add/update a TODO in the relevant script or doc.

## Current Architecture (Flux-first)

- Single cluster entrypoint: `clusters/home/`.
- Shared infrastructure layer: `infrastructure/overlays/home`.
- Shared platform-services layer: `platform-services/overlays/home`.
- Tenant environment layer: `tenants/app-envs`.
- App deployment layer: `tenants/apps/example` (staging + prod overlays) reconciled by app-level Flux Kustomizations in `clusters/home/`.
- Runtime secret source (external): `flux-system/platform-runtime-inputs`.
- Runtime secret targets (declarative): `platform-services/runtime-inputs`.

Flux dependency chain:
- `homelab-flux-sources -> homelab-flux-stack`
- `homelab-infrastructure -> homelab-platform -> homelab-tenants -> homelab-app-example`

Mode boundaries:
- Platform cert issuer mode is a Git-tracked ConfigMap value:
  - `clusters/home/flux-system/sources/configmap-platform-settings.yaml`
  - `CERT_ISSUER=letsencrypt-staging|letsencrypt-prod`
- Example app path is fixed in:
  - `clusters/home/app-example.yaml`
  - `spec.path` value (`./tenants/apps/example`)

## Operator Surface

Primary commands:
- `make cilium-bootstrap`
- `make flux-bootstrap`
- `make runtime-inputs-sync`
- `make runtime-inputs-refresh-otel`
- `make flux-reconcile`
- `make install`
- `make teardown`
- `make verify`

Mode commands (edit Git files only):
- `make platform-certs-staging`
- `make platform-certs-prod`

Important contract:
- `platform-certs-*` targets only edit local files. They do not mutate cluster state directly.
- Commit + push mode edits before running `make flux-reconcile`.

## Current Learnings

- Documentation workflow
  - `showboat` is not installed globally here; use `uvx showboat ...`.
  - Run `uvx showboat verify walkthrough.md` after walkthrough edits.
  - Avoid self-referential showboat commands inside executable walkthrough blocks.
  - Keep README quickstart aligned with `Makefile` behavior.
  - Historical migration scaffolding docs were removed; keep design/operations docs focused on the active layout.
  - `scripts/bootstrap/install-flux.sh` was removed; Flux CLI/controller installation is now manual and documented in `README.md`. Keep `make flux-bootstrap` as manifest apply only.
  - `clusters/home/modes/`, `tenants/overlays/app-*-le-*`, and app-test Makefile mode targets were removed; `clusters/home/app-example.yaml` is fixed to `./tenants/apps/example`.

- Runtime inputs and substitution
  - Runtime-input targets are in `platform-services/runtime-inputs` (not infrastructure).
  - `homelab-infrastructure` substitutes from `platform-settings` only.
  - `homelab-platform` substitutes from `platform-settings` and `platform-runtime-inputs`.
  - Flux postBuild substitution will consume unescaped `${...}` tokens in HelmRelease values. For OTel collector env interpolation, use escaped literals (`"$${env:HYPERDX_API_KEY}"`) so rendered collector config does not become `authorization: null`.
  - App deployment path is fixed (`clusters/home/app-example.yaml -> ./tenants/apps/example`) and does not use runtime-input substitution.
  - `scripts/bootstrap/sync-runtime-inputs.sh` owns source secret sync and validates required secret inputs.
  - `scripts/16_verify_cilium_bootstrap.sh` enforces Cilium preflight in apply flow before Flux reconcile.

- Issuers and certificates
  - ClusterIssuers are plain manifests in `infrastructure/cert-manager/issuers`.
  - Issuer local chart (`charts/platform-issuers`) is retired.
  - Platform ingress/cert selection is driven by `${CERT_ISSUER}` substitution.
  - First-time bootstrap can race on cert-manager CRDs (`ClusterIssuer` dry-run failure) because issuers are currently in the same infrastructure layer as the cert-manager HelmRelease.
  - Apps keep issuer selection in app overlays/manifests; current example app staging/prod overlays both use `letsencrypt-prod`.
  - Example app base now includes `tenants/apps/example/base/ciliumnetworkpolicy-hello-web-l7-observe.yaml` to keep Hubble L7 HTTP visibility declarative for test app flows.
  - TODO (`docs/runbook.md`): split cert-manager issuers into a dedicated Flux Kustomization that depends on cert-manager readiness.

- DNS and host layer
  - Dynamic DNS updater is `host/scripts/aws-dns-updater.sh` and updates Route53 A records for `homelab.swhurl.com` and `*.homelab.swhurl.com`.
  - Wildcard caveat: `*.homelab.swhurl.com` matches single-label hosts only; multi-label hosts need explicit records or deeper wildcard records.
  - TODO (`host/scripts/aws-dns-updater.sh`): support an env-provided record list for additional explicit hostnames.
  - Host automation is opt-in and runs via `host/run-host.sh`.

- k3s/Cilium
  - k3s is a manual prerequisite documented in `README.md`; host automation no longer installs k3s.
  - Cilium install is pre-Flux bootstrap via k3s helm-controller manifest (`bootstrap/k3s-manifests/cilium-helmchart.yaml`) and `make cilium-bootstrap`.
  - Cilium is the standard CNI; k3s must disable flannel/network-policy before Cilium install.
  - `infrastructure/cilium/base/helmrelease-cilium.yaml` is intentionally `spec.suspend: true` as a migration handoff placeholder; active install ownership is k3s bootstrap manifest.
  - `infrastructure/cilium/base` carries post-bootstrap Cilium-adjacent resources (for example, Hubble UI ingress).
  - Keep Cilium teardown last in delete flows.
  - TODO (`docs/runbook.md`): add an explicit host-level remove flow for `/var/lib/rancher/k3s/server/manifests/cilium-helmchart.yaml` when using k3s auto-deploy mode, so teardown cannot resurrect Cilium.
  - Default `infrastructure/overlays/home` now relies on k3s-packaged `traefik` + `metrics-server` (no Flux-managed ingress-nginx/metrics-server in active composition).
  - Hubble L7 details are policy-driven. With default permissive mode (no `CiliumNetworkPolicy` selecting app endpoints), Hubble shows only `L3_L4` flows; HTTP/DNS L7 events appear only when L7-aware Cilium policy rules are applied to target workloads/ports.
  - `hubble-ui` ingress class must match the active externally-routed ingress provider. If public traffic still lands on ingress-nginx and `hubble-ui` is switched to Traefik, TLS can remain valid but requests will return 404.
  - `scripts/91_verify_platform_state.sh` now enforces ingress-class alignment (`INGRESS_PROVIDER`) for hubble, oauth2-proxy, clickstack, minio/minio-console, and example app ingresses to catch split-route states early.
  - Traefik `Middleware.spec.errors.service` cross-namespace references are rejected in this setup; use `forwardAuth.address` to `http://oauth2-proxy.ingress.svc.cluster.local/oauth2/auth` for cross-namespace auth checks.
  - Current Traefik forward-auth protects routes (`401` when unauthenticated) for `hubble-ui` and `hello-web`; it does not yet replicate nginx `auth-signin` redirect behavior.
  - During edge cutover, if router/NAT still targets legacy ingress-nginx NodePorts (`31514`/`30313`), move those NodePorts to Traefik before removing ingress-nginx or external hosts will fail.
  - Namespace-scoped `CiliumNetworkPolicy` gotcha: `fromEndpoints: [{}]` in a namespaced policy does not permit traffic from arbitrary namespaces. For cross-namespace ingress-controller traffic to app pods, use `fromEntities: [cluster]` (or explicit cross-namespace endpoint selectors).
  - TODO (`README.md`, `docs/runbook.md`): document current Traefik forward-auth behavior (`401` unauthenticated) and add explicit 401->oauth2 start redirect middleware pattern for parity with prior nginx `auth-signin` UX.
  - TODO (`docs/runbook.md`): add declarative k3s `HelmChartConfig` guidance for Traefik NodePort pinning when edge router migration cannot happen immediately.

- Observability/ClickStack
  - ClickStack first-team setup is manual in UI.
  - `CLICKSTACK_INGESTION_KEY` can initially fall back to `CLICKSTACK_API_KEY`.
  - OTel daemonset node metrics on k3s use host networking + kubelet endpoint `127.0.0.1:10250`.
  - Cold-start image pulls can exceed default Helm action wait; set `spec.timeout` in `platform-services/clickstack/base/helmrelease-clickstack.yaml` to reduce bootstrap retries.
  - If `otel-k8s-*` collector logs show `HTTP Status Code 401` with `scheme or token does not match` for `clickstack-otel-collector.observability.svc.cluster.local:4318`, exporters are dropping telemetry; refresh `CLICKSTACK_INGESTION_KEY`/`CLICKSTACK_API_KEY` in `profiles/secrets.env` and run `make runtime-inputs-sync && make flux-reconcile`.
  - `logging/hyperdx-secret` updates do not hot-reload into existing `otel-k8s-*` pods (`secretKeyRef` env values are read at container start); after ingestion key rotation, restart collector workloads to pick up the new token.
  - Use `make runtime-inputs-refresh-otel` after ClickStack key updates so runtime inputs are synced/reconciled and collector pods are restarted in one flow.
  - TODO (`docs/runbook.md`): document and/or automate collector rollout restart on `hyperdx-secret` changes (e.g., checksum annotation strategy).

- kubectl / kubeconfig behavior
  - On hosts where `/usr/local/bin/kubectl` is the `k3s` wrapper, non-interactive shells can default to `/etc/rancher/k3s/k3s.yaml`; export `KUBECONFIG=$HOME/.kube/config` explicitly for scripted checks.

- Labels and teardown ownership
  - Managed label domain is `platform.swhurl.com/managed`.
  - Teardown/verification selectors must stay aligned with that label.

- Secrets hygiene
  - Keep secrets in `profiles/secrets.env` (gitignored), not `config.env`.
  - Config layering for scripts:
    - `config.env`
    - `profiles/local.env`
    - `profiles/secrets.env`
    - optional `PROFILE_FILE` (highest precedence)
  - `PROFILE_EXCLUSIVE=true` uses only `config.env` + explicit `PROFILE_FILE`.

## Maintenance Checks

When changing orchestration/layout:
- Update `README.md`, `docs/runbook.md`, and `docs/orchestration-api.md` together.
- Run:
  - `bash -n scripts/*.sh scripts/bootstrap/*.sh host/run-host.sh host/tasks/*.sh host/lib/*.sh`
  - `kubectl kustomize clusters/home >/dev/null`
  - `kubectl kustomize infrastructure/overlays/home >/dev/null`
  - `kubectl kustomize platform-services/overlays/home >/dev/null`
  - `kubectl kustomize tenants/app-envs >/dev/null`
  - `kubectl kustomize tenants/apps/example >/dev/null`
  - `./run.sh --dry-run`

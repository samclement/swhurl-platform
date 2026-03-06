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
- Runtime secret source (Git-managed SOPS): `clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml` -> `flux-system/platform-runtime-inputs`.
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
  - Architecture docs now use C4 chart sources in `docs/charts/c4/*.d2`; use `make charts-generate` to render `docs/charts/c4/rendered/*.svg`.
  - D2 default layouts here (`dagre`/`elk`) only honor root-level `direction`; for C4 container lane/row placement, prefer `grid-rows`/`grid-columns` wrappers over nested `direction` blocks.
  - C4 container layout principle: keep inbound/request flow top-down, and use horizontal sections (lanes) to group related containers informatively.
  - C4 chart fast-path:
    - Start from a `layout_rows` skeleton with a top external row and a lower system/cluster row.
    - Define section containers first with `grid-columns`/`grid-rows`; avoid starting from free-floating nodes.
    - If a section exceeds 3-4 nodes, split it into named sub-sections (for example `control`, `telemetry`) before tuning spacing.
    - Add edges only after node placement is stable; then adjust `horizontal-gap`/`vertical-gap` for readability.
    - Treat single tall columns as a layout smell; rebalance into 2D grids unless the sequence is intentionally linear.
    - Regenerate with `make charts-generate` after each structural pass.
  - For dense C4 diagrams, reduce repeated edge labels first (especially repeated operational labels like `Apply/reconcile`) and keep labels on representative edges only.
  - Use `diagram_title` with `near: top-left` for chart titles; avoid a plain `title` node that participates in layout and consumes chart width.
  - `docs/architecture.md` is the canonical C4 architecture entrypoint; keep it aligned with Flux layering and active app path wiring.
  - C4 container view orientation convention: top-to-bottom for inbound request flow, left-to-right lanes for logical grouping (`Inbound/edge`, `Platform services`, `Apps`).
  - C4 container view should explicitly show telemetry flow (`apps -> otel-k8s-daemonset -> clickstack-otel-collector`) so observability troubleshooting paths are visible at container level.
  - When TLS automation/cert-manager is present in C4 views, include `Let's Encrypt (ACME)` as an external system with explicit ACME interaction edges.
  - For C4 context views, avoid a flat external row when relationships are hierarchical; group external systems into labeled sections (for example actors, control/delivery, identity/TLS providers).
  - Keep `docs/add-feature-checklist.md` aligned with current toggle policy: default to declarative wiring and runtime inputs; avoid introducing new `FEAT_*` switches unless strictly necessary.
  - Historical migration scaffolding docs were removed; keep design/operations docs focused on the active layout.
  - `scripts/bootstrap/install-flux.sh` was removed; Flux CLI/controller installation is now manual and documented in `README.md`. Keep `make flux-bootstrap` as manifest apply only.
  - `clusters/home/modes/`, `tenants/overlays/app-*-le-*`, and app-test Makefile mode targets were removed; `clusters/home/app-example.yaml` is fixed to `./tenants/apps/example`.
  - `run.sh` was removed; cluster orchestration is `make`-first via `make install` / `make teardown` (use `DRY_RUN=true`, `FEAT_VERIFY=...`, and optional `PROFILE_FILE=...` env overrides).
  - `make teardown` is now stack-only by default: it deletes `homelab-flux-stack` and `homelab-flux-sources` and leaves Flux controllers/cert-manager/CRDs installed.
  - Keep `.github/workflows/validate.yml` aligned with active make targets and existing kustomize paths; remove deleted path checks (`run.sh`, `infrastructure/cilium/base`, and retired app-test overlays).
  - Runtime service feature flags were removed from active config (`FEAT_OAUTH2_PROXY`, `FEAT_CLICKSTACK`, `FEAT_OTEL_K8S`, `FEAT_MINIO`); keep `FEAT_VERIFY` only and treat oauth2-proxy/clickstack/otel/minio inputs as always required by active composition.
  - Dead orchestration scripts were removed (`scripts/15_verify_cluster_access.sh`, `scripts/20_reconcile_platform_namespaces.sh`); `make install` now delegates sync+reconcile through `make flux-reconcile`, and unused helper functions were pruned from `scripts/00_lib.sh` / `scripts/00_verify_contract_lib.sh`.
  - Legacy hard-delete scripts were removed (`scripts/30_manage_cert_manager_cleanup.sh`, `scripts/98_verify_teardown_clean.sh`, `scripts/99_execute_teardown.sh`); default teardown remains stack-only via `scripts/32_reconcile_flux_stack.sh --delete`.
  - TODO (`.github/workflows/validate.yml`): add optional C4 chart render validation when `d2` is available so `docs/charts/c4/*.d2` syntax drift is caught in CI.
  - TODO (`docs/architecture.md`): add short per-view scope notes (what intentionally belongs in context/container/component) to reduce ambiguity when updating chart relationships.

- Runtime inputs and substitution
  - Runtime-input targets are in `platform-services/runtime-inputs` (not infrastructure).
  - `homelab-infrastructure` substitutes from `platform-settings` only.
  - `homelab-platform` substitutes from `platform-settings` and `platform-runtime-inputs`.
  - Runtime-input source secret is Git-managed in `clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml` and decrypted by `homelab-flux-sources` (`spec.decryption.secretRef.name=sops-age`).
  - Keep app-specific secrets with each app (`tenants/apps/<app>/.../secret-*.sops.yaml`); when app paths include encrypted manifests, set `spec.decryption` on that app-level Flux Kustomization (for example `clusters/home/app-*.yaml`) to use `sops-age`.
  - `.sops.yaml` creation rules currently cover `clusters/home/flux-system/sources`; add an app-path creation rule before onboarding app-local `*.sops.yaml` files so `sops --encrypt --in-place` uses the expected recipient automatically.
  - `scripts/bootstrap/sync-runtime-inputs.sh` was removed; `make runtime-inputs-sync` now reconciles `homelab-flux-sources` from pushed Git state.
  - Flux postBuild substitution will consume unescaped `${...}` tokens in HelmRelease values. For OTel collector env interpolation, use escaped literals (`"$${env:HYPERDX_API_KEY}"`) so rendered collector config does not become `authorization: null`.
  - App deployment path is fixed (`clusters/home/app-example.yaml -> ./tenants/apps/example`) and does not use runtime-input substitution.
  - oauth2-proxy client secret/config secret updates do not trigger automatic rollout restart; after runtime input credential changes, restart `ingress/oauth2-proxy-shared` (or automate via checksum strategy) to load new client credentials.
  - `redirect_uri_mismatch` during login means the running oauth2-proxy `--redirect-url` does not match the Google OAuth client's allowed callback URI. Keep `platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml` redirect host/path aligned with the active client credentials wired from `platform-runtime-inputs`.
  - TODO (`Makefile`, `docs/runbook.md`): add an oauth2-proxy refresh target (or checksum rollout mechanism) after runtime credential updates so shared oauth client-id/client-secret changes are picked up without manual deployment restarts.
  - `platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml` uses OIDC with Google issuer (`provider: oidc`, `oidc-issuer-url: https://accounts.google.com`); release name is `oauth2-proxy-shared`, callback host/path is `https://${OAUTH_HOST}/oauth2/callback`, and runtime secret wiring uses `SHARED_OIDC_CLIENT_ID` / `SHARED_OIDC_CLIENT_SECRET`.

- Repo structure
  - `tenants/kustomization.yaml` was removed; cluster Kustomizations point directly to `tenants/app-envs` and `tenants/apps/example`.
  - Legacy-only provider/migration manifests are isolated under `legacy/`:
    - `legacy/infrastructure/ingress-nginx/base`
    - `legacy/infrastructure/metrics-server/base`
    - `legacy/infrastructure/storage/ceph/base`
    - `legacy/bootstrap/k3s-manifests`
  - Active Flux composition and NodePort ownership remain in `infrastructure/overlays/home -> ../../ingress-traefik/base` and `infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml` (`31514`/`30313`).

- Issuers and certificates
  - ClusterIssuers are plain manifests in `infrastructure/cert-manager/issuers`.
  - Issuer local chart (`charts/platform-issuers`) is retired.
  - Platform ingress/cert selection is driven by `${CERT_ISSUER}` substitution.
  - First-time bootstrap can race on cert-manager CRDs (`ClusterIssuer` dry-run failure) because issuers are currently in the same infrastructure layer as the cert-manager HelmRelease.
  - Apps keep issuer selection in app overlays/manifests; current example app staging/prod overlays both use `letsencrypt-prod`.
  - TODO (`docs/runbook.md`): split cert-manager issuers into a dedicated Flux Kustomization that depends on cert-manager readiness.

- DNS and host layer
  - Dynamic DNS updater is `host/scripts/aws-dns-updater.sh` and updates Route53 A records for `homelab.swhurl.com` and `*.homelab.swhurl.com`.
  - Wildcard caveat: `*.homelab.swhurl.com` matches single-label hosts only; multi-label hosts need explicit records or deeper wildcard records.
  - TODO (`host/scripts/aws-dns-updater.sh`): support an env-provided record list for additional explicit hostnames.
  - Host automation is opt-in and runs via `host/run-host.sh`.

- k3s defaults
  - k3s is a manual prerequisite documented in `README.md`; host automation no longer installs k3s.
  - Active default stack uses k3s defaults: flannel CNI + packaged `traefik` + packaged `metrics-server`.
  - Traefik NodePorts are pinned declaratively via k3s `HelmChartConfig` at `infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml`; `infrastructure/overlays/home` includes `../../ingress-traefik/base` so Flux reconciles the override (`80 -> 31514`, `443 -> 30313`).
  - `scripts/91_verify_platform_state.sh` validates live Traefik service NodePorts (`web=31514`, `websecure=30313`) when `INGRESS_PROVIDER=traefik`.
  - Cilium lifecycle scripts were removed (`scripts/16_verify_cilium_bootstrap.sh`, `scripts/26_manage_cilium_lifecycle.sh`, `scripts/bootstrap/patch-hubble-relay-hostnetwork.sh`).
  - Cilium/Hubble manifests were removed from active and legacy composition (`infrastructure/cilium/base`, `platform-services/oauth2-proxy-hubble/base`, and legacy bootstrap Cilium HelmChart manifests).
  - `clusters/home/flux-system/sources/helmrepositories.yaml` no longer includes the `cilium` HelmRepository.
  - `config.env` no longer carries `FEAT_CILIUM`, `HUBBLE_HOST`, or `HUBBLE_OIDC_*`.
  - Shared oauth2-proxy edge-auth middleware lives in `platform-services/oauth2-proxy/base` (`oauth-auth-shared` in namespace `ingress`) and app ingresses reference `ingress-oauth-auth-shared@kubernetescrd`.
  - For Traefik edge-auth redirect behavior, set oauth2-proxy to `upstream=static://202` + `skip-provider-button=true`, and point Traefik `ForwardAuth` to `http://oauth2-proxy-shared.ingress.svc.cluster.local/` (not `/oauth2/auth`) so unauthenticated requests return browser-followable `302` redirects.
  - During edge cutover, if router/NAT still targets legacy ingress-nginx NodePorts (`31514`/`30313`), move those NodePorts to Traefik before removing ingress-nginx or external hosts will fail.
  - TODO (`scripts/91_verify_platform_state.sh`): verify app ingresses reference `ingress-oauth-auth-shared@kubernetescrd` where edge auth is expected.

- Observability/ClickStack
  - ClickStack first-team setup is manual in UI.
  - `CLICKSTACK_INGESTION_KEY` can initially fall back to `CLICKSTACK_API_KEY`.
  - OTel daemonset node metrics on k3s use host networking + kubelet endpoint `127.0.0.1:10250`.
  - Cold-start image pulls can exceed default Helm action wait; set `spec.timeout` in `platform-services/clickstack/base/helmrelease-clickstack.yaml` to reduce bootstrap retries.
  - On ClickHouse `25.7.x`, `system.query_log` does not expose `query_parameters`; for parameterized failures, match `query_id` against `observability/clickstack-app` logs and inspect `/clickhouse-proxy?...&param_HYPERDX_PARAM_*=` values.
  - `BAD_QUERY_PARAMETER (457)` with `Value nan cannot be parsed as Int64` can be confirmed in `clickstack-app` logs as `param_HYPERDX_PARAM_*=nan`; recent failures also carried `TraceId='undefined'` in the generated SQL from search row-side-panel flows.
  - If `otel-k8s-*` collector logs show `HTTP Status Code 401` with `scheme or token does not match` for `clickstack-otel-collector.observability.svc.cluster.local:4318`, exporters are dropping telemetry; update `CLICKSTACK_INGESTION_KEY`/`CLICKSTACK_API_KEY` in `clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml` and run `make runtime-inputs-refresh-otel`.
  - `logging/hyperdx-secret` updates do not hot-reload into existing `otel-k8s-*` pods (`secretKeyRef` env values are read at container start); after ingestion key rotation, restart collector workloads to pick up the new token.
  - Use `make runtime-inputs-refresh-otel` after ClickStack key updates so runtime inputs are synced/reconciled and collector pods are restarted in one flow.
  - `make runtime-inputs-refresh-otel` reconciles `homelab-platform` and waits for `logging/hyperdx-secret` to match `flux-system/platform-runtime-inputs.CLICKSTACK_INGESTION_KEY` before restarting collectors; this avoids stale-token restarts after key rotation.

- kubectl / kubeconfig behavior
  - On hosts where `/usr/local/bin/kubectl` is the `k3s` wrapper, non-interactive shells can default to `/etc/rancher/k3s/k3s.yaml`; export `KUBECONFIG=$HOME/.kube/config` explicitly for scripted checks.

- Labels and teardown ownership
  - Managed label domain is `platform.swhurl.com/managed`.
  - Teardown/verification selectors must stay aligned with that label.
  - `scripts/91_verify_platform_state.sh` now centralizes repeated checks with helpers (`check_flux_kustomization_path`, `check_ingress_contract`, `check_certificate_contract`, and resource-presence helpers); extend those helpers instead of adding new ad-hoc blocks.

- Flux reconcile behavior
  - `flux reconcile kustomization ... --with-source` does not preempt an already running `wait: true` reconciliation. If a prior revision is in `Running health checks ... timeout 20m`, new `requestedAt` values queue but the old in-flight revision continues until timeout/failure.
  - During this window, `flux get kustomizations` can show stale `lastAttemptedRevision` (older sha) even when `homelab-flux-sources` already applied a newer source revision.
  - TODO (`scripts/32_reconcile_flux_stack.sh`, `docs/runbook.md`): add a preflight/notice for long-running in-progress stack reconciliations (include expected timeout and optional suspend/resume workaround) so `make flux-reconcile` doesn’t appear silently hung.

- Secrets hygiene
  - Keep shared platform runtime secrets in `clusters/home/flux-system/sources/secret-platform-runtime-inputs.sops.yaml` (SOPS-encrypted), not `config.env`.
  - Keep app-only secrets in app directories (`tenants/apps/<app>/.../secret-*.sops.yaml`) and decrypt via the app Flux Kustomization.
  - Ensure Flux decryption key secret exists in-cluster as `flux-system/sops-age` (`age.agekey`).
  - Keep local age private key material (`age.agekey`) gitignored.
  - `config.env` no longer carries secret placeholders for `SHARED_OIDC_CLIENT_ID`, `SHARED_OIDC_CLIENT_SECRET`, `OAUTH_COOKIE_SECRET`, `CLICKSTACK_API_KEY`, or `MINIO_ROOT_PASSWORD`.
  - TODO (`docs/contracts.md`, `docs/orchestration-api.md`): document shared-vs-app secret boundaries and app-level Flux decryption requirements alongside the central runtime-input source flow.
  - Config layering for scripts:
    - `config.env`
    - `profiles/local.env`
    - optional `PROFILE_FILE` (highest precedence)

- Local process hygiene
  - Use `scripts/cleanup-hanging-mosh.sh` to prune stale `mosh-server`/`mosh-client` processes; defaults are conservative (age >= 3600s, detached/no TTY, and detached server `ppid=1`).
  - Run `./scripts/cleanup-hanging-mosh.sh --dry-run` first to inspect matches before termination.

## Maintenance Checks

When changing orchestration/layout:
- Update `README.md`, `docs/runbook.md`, and `docs/orchestration-api.md` together.
- Run:
  - `bash -n scripts/*.sh host/run-host.sh host/tasks/*.sh host/lib/*.sh`
  - `kubectl kustomize clusters/home >/dev/null`
  - `kubectl kustomize infrastructure/overlays/home >/dev/null`
  - `kubectl kustomize platform-services/overlays/home >/dev/null`
  - `kubectl kustomize tenants/app-envs >/dev/null`
  - `kubectl kustomize tenants/apps/example >/dev/null`
  - `make install DRY_RUN=true`
  - `make teardown DRY_RUN=true`

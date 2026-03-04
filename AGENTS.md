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
  - oauth2-proxy client secret/config secret updates do not trigger automatic rollout restart; after runtime input credential changes, restart `ingress/oauth2-proxy-shared` (or automate via checksum strategy) to load new client credentials.
  - `redirect_uri_mismatch` during login means the running oauth2-proxy `--redirect-url` does not match the Google OAuth client's allowed callback URI. Keep `platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml` redirect host/path aligned with the active client credentials wired from `platform-runtime-inputs`.
  - TODO (`Makefile`, `docs/runbook.md`): add an oauth2-proxy refresh target (or checksum rollout mechanism) after runtime credential updates so shared oauth client-id/client-secret changes are picked up without manual deployment restarts.
  - `platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml` uses OIDC with Google issuer (`provider: oidc`, `oidc-issuer-url: https://accounts.google.com`); release name is `oauth2-proxy-shared`, callback host/path is `https://${OAUTH_HOST}/oauth2/callback`, and runtime secret wiring uses `SHARED_OIDC_CLIENT_ID` / `SHARED_OIDC_CLIENT_SECRET`.
  - `scripts/bootstrap/sync-runtime-inputs.sh` supports legacy `HELLO_OIDC_*` and `OIDC_CLIENT_*` fallback values, but warns they are deprecated in favor of `SHARED_OIDC_*`.

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
  - Cilium lifecycle scripts were removed (`scripts/16_verify_cilium_bootstrap.sh`, `scripts/26_manage_cilium_lifecycle.sh`, `scripts/bootstrap/patch-hubble-relay-hostnetwork.sh`).
  - Cilium/Hubble manifests were removed from active and legacy composition (`infrastructure/cilium/base`, `platform-services/oauth2-proxy-hubble/base`, `bootstrap/k3s-manifests/cilium-helmchart.yaml`).
  - `clusters/home/flux-system/sources/helmrepositories.yaml` no longer includes the `cilium` HelmRepository.
  - `config.env` and `profiles/secrets.example.env` no longer carry `FEAT_CILIUM`, `HUBBLE_HOST`, or `HUBBLE_OIDC_*`.
  - Shared oauth2-proxy edge-auth middleware lives in `platform-services/oauth2-proxy/base` (`oauth-auth-shared` in namespace `ingress`) and app ingresses reference `ingress-oauth-auth-shared@kubernetescrd`.
  - For Traefik edge-auth redirect behavior, set oauth2-proxy to `upstream=static://202` + `skip-provider-button=true`, and point Traefik `ForwardAuth` to `http://oauth2-proxy-shared.ingress.svc.cluster.local/` (not `/oauth2/auth`) so unauthenticated requests return browser-followable `302` redirects.
  - During edge cutover, if router/NAT still targets legacy ingress-nginx NodePorts (`31514`/`30313`), move those NodePorts to Traefik before removing ingress-nginx or external hosts will fail.
  - TODO (`scripts/91_verify_platform_state.sh`): verify app ingresses reference `ingress-oauth-auth-shared@kubernetescrd` where edge auth is expected.

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

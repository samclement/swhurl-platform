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

- Issuers and certificates
  - ClusterIssuers are plain manifests in `infrastructure/cert-manager/issuers`.
  - Issuer local chart (`charts/platform-issuers`) is retired.
  - Platform ingress/cert selection is driven by `${CERT_ISSUER}` substitution.
  - Apps keep issuer selection in app overlays/manifests; current example app staging/prod overlays both use `letsencrypt-prod`.
  - Example app base now includes `tenants/apps/example/base/ciliumnetworkpolicy-hello-web-l7-observe.yaml` to keep Hubble L7 HTTP visibility declarative for test app flows.

- DNS and host layer
  - Dynamic DNS updater is `host/scripts/aws-dns-updater.sh` and updates Route53 wildcard `*.homelab.swhurl.com`.
  - Wildcard caveat: `*.homelab.swhurl.com` matches single-label hosts only; multi-label hosts need explicit records or deeper wildcard records.
  - Host automation is opt-in and runs via `host/run-host.sh`.

- k3s/Cilium
  - k3s is a manual prerequisite path (`host/run-host.sh --only 20_install_k3s.sh`).
  - Cilium is the standard CNI; k3s must disable flannel/network-policy before Cilium install.
  - Keep Cilium teardown last in delete flows.
  - Native k3s `metrics-server`/Traefik mode requires removing Flux-managed `infrastructure/metrics-server/base` and `infrastructure/ingress-nginx/base` from `infrastructure/overlays/home`, plus migrating issuer solver class and ingress annotations/class from NGINX conventions to Traefik conventions.
  - Hubble L7 details are policy-driven. With default permissive mode (no `CiliumNetworkPolicy` selecting app endpoints), Hubble shows only `L3_L4` flows; HTTP/DNS L7 events appear only when L7-aware Cilium policy rules are applied to target workloads/ports.
  - Namespace-scoped `CiliumNetworkPolicy` gotcha: `fromEndpoints: [{}]` in a namespaced policy does not permit traffic from arbitrary namespaces. For cross-namespace ingress-controller traffic to app pods, use `fromEntities: [cluster]` (or explicit cross-namespace endpoint selectors).

- Observability/ClickStack
  - ClickStack first-team setup is manual in UI.
  - `CLICKSTACK_INGESTION_KEY` can initially fall back to `CLICKSTACK_API_KEY`.
  - OTel daemonset node metrics on k3s use host networking + kubelet endpoint `127.0.0.1:10250`.

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

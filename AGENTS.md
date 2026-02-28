# Platform Infrastructure Guide (Agents)

## Agents Operating Notes

- Always update this file with concise, actionable learnings discovered while working in this repo.
- Keep guidance tied to current files only. Remove stale references when architecture/scripts change.
- If a learning implies a code change, also add/update a TODO in the relevant script or doc.

## Current Architecture (Flux-first)

- Single cluster entrypoint: `clusters/home/`.
- Shared infrastructure layer: `infrastructure/overlays/home`.
- Shared platform-services layer: `platform-services/overlays/home`.
- Tenant/app environment layer: `tenants/overlays/app-*-le-*`.
- Runtime secret source (external): `flux-system/platform-runtime-inputs`.
- Runtime secret targets (declarative): `platform-services/runtime-inputs`.

Flux dependency chain:
- `homelab-flux-sources -> homelab-flux-stack`
- `homelab-infrastructure -> homelab-platform -> homelab-tenants`

Mode boundaries:
- Platform cert issuer mode is a Git-tracked ConfigMap value:
  - `clusters/home/flux-system/sources/configmap-platform-settings.yaml`
  - `CERT_ISSUER=letsencrypt-staging|letsencrypt-prod`
- App URL/issuer mode is path-selected in:
  - `clusters/home/tenants.yaml`
  - `spec.path` value (`./tenants/overlays/app-*-le-*`)

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
- `make app-test-staging-le-staging`
- `make app-test-staging-le-prod`
- `make app-test-prod-le-staging`
- `make app-test-prod-le-prod`

Important contract:
- Mode targets only edit local files. They do not mutate cluster state directly.
- Commit + push mode edits before running `make flux-reconcile`.

## Current Learnings

- Documentation workflow
  - `showboat` is not installed globally here; use `uvx showboat ...`.
  - Run `uvx showboat verify walkthrough.md` after walkthrough edits.
  - Avoid self-referential showboat commands inside executable walkthrough blocks.
  - Keep README quickstart aligned with `Makefile` behavior.
  - Historical migration scaffolding docs were removed; keep design/operations docs focused on the active layout.
  - `scripts/bootstrap/install-flux.sh` was removed; Flux CLI/controller installation is now manual and documented in `README.md`. Keep `make flux-bootstrap` as manifest apply only.
  - `clusters/home/modes/` was removed; app-test mode targets now patch only `clusters/home/tenants.yaml` `spec.path` to avoid duplicate template drift.

- Runtime inputs and substitution
  - Runtime-input targets are in `platform-services/runtime-inputs` (not infrastructure).
  - `homelab-infrastructure` substitutes from `platform-settings` only.
  - `homelab-platform` substitutes from `platform-settings` and `platform-runtime-inputs`.
  - `scripts/bootstrap/sync-runtime-inputs.sh` owns source secret sync and validates required secret inputs.

- Issuers and certificates
  - ClusterIssuers are plain manifests in `infrastructure/cert-manager/issuers`.
  - Issuer local chart (`charts/platform-issuers`) is retired.
  - Platform ingress/cert selection is driven by `${CERT_ISSUER}` substitution.
  - Apps keep issuer selection in app overlays/manifests.
  - Example app base now includes `tenants/apps/example/base/ciliumnetworkpolicy-hello-web-l7-observe.yaml` to keep Hubble L7 HTTP visibility declarative for test app flows.

- DNS and host layer
  - Dynamic DNS updater is `host/scripts/aws-dns-updater.sh` and updates Route53 wildcard `*.homelab.swhurl.com`.
  - Wildcard caveat: `*.homelab.swhurl.com` matches single-label hosts only; multi-label hosts need explicit records or deeper wildcard records.
  - Host automation is opt-in and runs via `host/run-host.sh`.

- k3s/Cilium
  - k3s is a manual prerequisite path (`host/run-host.sh --only 20_install_k3s.sh`).
  - Cilium is the standard CNI; k3s must disable flannel/network-policy before Cilium install.
  - Keep Cilium teardown last in delete flows.
  - Hubble L7 details are policy-driven. With default permissive mode (no `CiliumNetworkPolicy` selecting app endpoints), Hubble shows only `L3_L4` flows; HTTP/DNS L7 events appear only when L7-aware Cilium policy rules are applied to target workloads/ports.

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
  - `kubectl kustomize tenants/overlays/app-staging-le-staging >/dev/null`
  - `./run.sh --dry-run`

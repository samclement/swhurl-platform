# ADR 0001: Ingress Provider Strategy

- Status: accepted
- Date: 2026-02-26

## Context

The platform historically used `ingress-nginx` as the default ingress controller.
Homelab direction is to prefer k3s default Traefik while keeping a controlled rollback
path to nginx during migration.

Without an explicit provider strategy, ingress behavior leaks across scripts, values,
and verification logic, making migrations risky and hard to reason about.

## Decision

Adopt explicit ingress provider intent via `INGRESS_PROVIDER` with allowed values:

- `nginx`
- `traefik`

Use this as a single contract that drives:

- Helmfile release installation gating (`ingress-nginx` installs only for `nginx`)
- ingress class templating (`computed.ingressClass`)
- provider-specific annotations/verification expectations
- runbook and overlay composition for migration

## Consequences

- Provider switching is declarative and profile-driven instead of ad-hoc script edits.
- Verification noise is reduced by skipping NGINX-specific checks under Traefik.
- Migration is safer, but requires keeping provider-aware logic synchronized in:
  - `environments/common.yaml.gotmpl`
  - `helmfile.yaml.gotmpl`
  - `infra/values/*`
  - `scripts/90_verify_runtime_smoke.sh`
  - `scripts/91_verify_platform_state.sh`

## Follow-ups

1. Implement Traefik-specific resources in `infrastructure/ingress-traefik/base` and switch composition in `infrastructure/overlays/home/kustomization.yaml`.
2. Keep `docs/runbooks/migrate-ingress-nginx-to-traefik.md` aligned with real behavior.

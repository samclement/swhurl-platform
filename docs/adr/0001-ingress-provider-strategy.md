# ADR 0001: Ingress Provider Strategy

- Status: accepted
- Date: 2026-02-26

## Context

The homelab cluster needs one active ingress provider at a time, with the ability to switch
between `ingress-nginx` and `traefik` without restructuring repository layers.

## Decision

Use composition-driven provider selection in:
- `infrastructure/overlays/home/kustomization.yaml`

Current default is `ingress-nginx` (`../../ingress-nginx/base`).
Optional Traefik composition path is `../../ingress-traefik/base`.

Keep `INGRESS_PROVIDER` in `config.env` as an operator intent hint for verification and
operational checks, not as the source of deployment truth.

## Consequences

- Provider changes are explicit Git diffs in overlay composition.
- Flux reconciliation remains the only deployment mechanism.
- Verification can still assert expected behavior from `INGRESS_PROVIDER` intent.
- Switching providers requires matching ingress class/annotation behavior across platform and app manifests.

## Follow-ups

1. Keep `docs/runbooks/migrate-ingress-nginx-to-traefik.md` aligned with actual manifests.
2. When Traefik manifests become fully implemented, update default composition intentionally.

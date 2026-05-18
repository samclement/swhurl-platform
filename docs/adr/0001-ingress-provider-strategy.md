# ADR 0001: Ingress Provider Strategy

- Status: accepted
- Date: 2026-02-26

## Context

The homelab cluster needs one active ingress provider at a time.
Native k3s `traefik` is the default provider.

## Decision

Use composition-driven provider selection in:
- `infrastructure/overlays/home/kustomization.yaml`

Current default relies on k3s-packaged `traefik` (no Flux ingress controller release in `home`).
Legacy `ingress-nginx` provider manifests are no longer retained in this repo.

Verification checks traefik directly (hardcoded); there is no config-level provider switch.

## Consequences

- Provider changes are explicit Git diffs in overlay composition.
- Flux reconciliation remains the only deployment mechanism.
- Switching providers requires matching ingress class/annotation behavior across platform and app manifests.

## Follow-ups

1. If Flux-managed Traefik ownership is introduced, add explicit manifests under `infrastructure/ingress-traefik/base` and update this ADR.

# AGENTS.md

Keep documentation current.

Any change to repository behavior, reconcile flow, manifests, runtime inputs, scripts, defaults, caveats, or operating procedures must update the relevant documentation in the same change.

Use the docs in `docs/` as the detailed source of truth:

- `docs/INFRASTRUCTURE.md`: cluster bootstrap, Flux layering, repository layout, prerequisites, and getting started.
- `docs/PLATFORM-SERVICES.md`: shared platform services, runtime inputs, service architecture, and operational caveats.
- `docs/TENANTS.md`: tenant landing zones, app overlays, onboarding patterns, and current limitations.
- `docs/runbook.md` and `docs/architecture.md`: operational workflows and design views.

Keep `README.md` short. It should describe the repo at a high level, explain the supported quick start, and link to the detailed docs.

Do not leave aspirational or stale documentation in the repo. Validate the current state from the active `Makefile`, `clusters/home`, overlay manifests, and scripts before updating docs.

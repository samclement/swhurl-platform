# Walkthrough (Current State)

The historical executable walkthrough was retired because script/layout refactors made it drift quickly.

For the current repo behavior, use:

1. `README.md` for operator flows and common use cases.
2. `docs/orchestration-api.md` for CLI/step contracts.
3. `docs/runbook.md` for Flux-first operations.
4. `make install DRY_RUN=true` and `make teardown DRY_RUN=true` for exact orchestrator plans.

If a full executable transcript is needed, regenerate a fresh walkthrough with Showboat:

```bash
uvx showboat init walkthrough.md --title "Swhurl Platform Walkthrough"
```

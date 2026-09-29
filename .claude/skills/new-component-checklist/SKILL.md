---
name: new-component-checklist
description: Checklist for adding or replacing a platform component, host timer or app-facing service on the swhurl homelab — retention, default credentials, logs reaching ClickStack, verify-platform coverage, backups, docs and operator test steps. Use when planning or finishing a change that adds something that runs, stores data or holds a credential.
---

# New component checklist

> Draft (29 September 2026). Each item exists because the operator had to raise it after the change was proposed. Revise as steering shows gaps.

Run through this when you plan the change, and again before reporting it done. In the plan, state each item as covered, not applicable (with a reason), or deferred (with where it is tracked in `docs/plan.md`).

| Item | Question | Where it usually lands |
| --- | --- | --- |
| Data retention | What does it store, how fast does it grow, and what limit or TTL applies by default? | HelmRelease values, ClickHouse TTL, backup retention |
| Default credentials | Is there an admin login or key? How is it set without a manual UI step, and where is it stored? | SOPS Secret, `docs/services.md` |
| Credential shape | Is each key encoded once (`stringData`) and used for one purpose only? | `make check-secrets` |
| Logs and metrics | Do its logs reach ClickStack, including host timers and one-shot jobs? | `platform/otel`, `docs/services.md` |
| Health check | Does `make verify-platform` fail when it is broken or stale? | `tools/swhurl/verify.py` |
| Backups | Is its data in a backup, and has a restore been exercised? | `docs/operations.md#backups-and-recovery`, `docs/current-state.md` "Not exercised" |
| Restarts on change | Does a Secret or config change restart it (Reloader namespace, annotation)? | `platform/reloader` |
| Failure behaviour | Do scripts stop on the first failure, including `sudo` with no terminal? Is that tested? | `host/*.sh`, `tests/` |
| Upgrades | Is the chart or image pinned where Renovate finds it? Is a major-version path known? | `docs/operations.md#chart-updates` |
| Docs | Which canonical page describes it, and which make targets changed? | map in `AGENTS.md` |
| Operator test | What can the operator do in a browser or terminal to see it working? | the report's "Test it yourself" block |

Only add a row when a real change missed it. Remove a row that never applies.

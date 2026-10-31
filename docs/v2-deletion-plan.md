# Deleting v1: the plan for 1b's second half

**Status: draft, not started (31 October 2026).** The migrations are done: nine staging and four production
instances are v2-owned, `apps/` holds no instance record, and the live checks in
[current state](current-state.md#the-production-cutover-four-instances-31-october-2026) pass. What remains is the
operator's write surface, the console's reader, and the v1-only branches in `verify-platform` and `resolve`.

This page is the working plan, not a status page. The contract stays in
[iteration 2, section 8](iteration-2-plan.md#8-the-cutover-finish-the-migration-and-delete-v1-approved-31-october-2026);
the decision record is on [v2 status](v2-status.md#decisions). The operator chose **option 2**: delete every
instance-writing command v2 replaces, port the console to v2 records in the same slice, and keep only read-only
operations plus the capability commands v2 has no replacement for. Execute it in the phases below; each phase is a
green commit.

## 1. The rule this slice enforces

**One writer per fact.** A fact v2 models is written only by v2; a fact v2 does not model has v1 as its only
writer until v2 models it. There is never a fact with two writers. That is why keeping `app-egress` or
`app-schedule` is not a compatibility layer: v2 renders neither fact, so v1 remains its single writer. The
moment v2 models one of them, its v1 command is deleted in that v2 change.

The acceptance is therefore **no fact has two writers**, not "no v1 code exists". `apps/` stays as the home of
the unmodeled facts and the shared team registry; it holds no instance record.

## 2. Inventory

### 2.1 Commands

The canonical list is `tools/swhurl/commands.py`; the Makefile and [commands](commands.md) are checked against
it. The writer functions are in `tools/swhurl/apps/cli.py`, `new.py`, `offers.py`, `keys.py`, `teams.py`,
`repo.py` and `hooks.py`.

| Command | Fact it writes | v2 replacement | Disposition |
| --- | --- | --- | --- |
| `app-scale` | replicas, memory | `swhurl-v2 edit --set` | **delete** |
| `app-expose` | exposure, host | `edit --set` | **delete** |
| `app-calls` | calls | `edit --set calls=...` | **delete** |
| `app-allows` | allows | `edit --set allows=...` | **delete** |
| `app-events` | subscriptions | `edit --set events=...` | **delete** |
| `app-team` | app team, quota, policy | `edit --set team=...` (quota/policy are rendered) | **delete** |
| `app-remove` | an instance | `swhurl-v2 remove` | **delete** |
| `app-deploy` | staging release | `swhurl-v2 deploy` | **delete** |
| `app-promote` | production | `swhurl-v2 promote` | **delete** |
| `app-sync` | derived instance files | `auth`, `users`, `reloader`, `egress`, `proxy`, `compile` | **delete** |
| `app-check` | v1 render + policy | `compile` and `observe` | **delete** |
| `platform-signing-keys` | `apps/signing-keys.json` | `auth` (profile-owned) | **delete** |
| `app-offers` | offered documents | v2 records/offers and `deploy` | **delete** |
| `app-egress` | egress grants | none (v2 has no egress-grant field) | **keep** (v1-only fact) |
| `app-alerts` | alert settings | none (v2 renders no alert) | **keep** (v1-only) |
| `app-schedule` | job schedule | none (no `job` kind) | **keep** (v1-only) |
| `app-secret` | secret names/values | none (secrets are not modelled) | **keep** (v1-only) |
| `app-new` | a new staging instance | none yet (create is a later v2 capability) | **keep** (v1-only) |
| `app-repo` | a GitHub repository from a template | none yet | **keep** |
| `app-hooks` | image webhooks | none yet | **keep** |
| `team-*`, `channel-*` | `apps/teams.yaml` | none (v2 reads the team string, not the registry) | **keep** |
| `app-status`, `app-logs`, `app-reconcile` | reads only | n/a | **keep** |

**Dependencies that move with the table:**

- `check-apps` (the make target and `tools/swhurl/apps/checks.py`) loses its instance writer. Replace it in
  phase 4 with a v2 check (compile every `v2-records` instance, compare with `v2-apps`, and validate the
  platform documents), or delete it if `make check-v2` already covers every assertion. The plan is to replace,
  because `check-apps` also checks the team registry and app policy for the v1-only facts.
- `check-templates` calls `app-new` in a temporary directory; it stays while `app-new` stays. If `app-new` is
  later deleted, `check-templates` is ported or retired in that change.
- `live-test-*` commands that use the writer (`live-test-lifecycle`, `live-test-app-template`,
  `live-test-call-identity`, `live-test-network-policy`, `live-test-team-scenario`, the restore tests) are
  inventoried in phase 3; each is ported to v2 or deleted with the command it tests. None is left pointing at a
  deleted function.

### 2.2 Console

`tools/swhurl/console/` is the larger part: **15 modules import `swhurl.apps`**. They split into two jobs.

**Read / render** (must move to v2 records):

| Module | Today | v2 source |
| --- | --- | --- |
| `catalogue.py` | `apps.offers`, v1 catalogue | `v2-records/*/app.json`, `offers/` |
| `context.py`, `server.py` | `apps.contract`, `apps.repo` | `v2-records/*/app.json`, app policy |
| `cluster.py` | `apps.calling`, `egress`, `events`, `helmrelease`, `ops`, `state` | compiled `v2-apps` + live kubectl |
| `stages.py` | `apps.model.render`, `store`, `Settings` | `v2-records` + `swhurl-v2 compile`/preview |
| `pages.py` | `apps.contract`, `apps.instance` | v2 app/instance records |
| `checks.py` | `apps.helmrelease` | compiled `v2-apps` |
| `flagstore.py` | `apps.events`, `nkeys` | v2 events records |
| `repos.py` | `apps.hooks`, `repo` | kept (v1-only facts) |

**Write / propose** (must move to v2 or be disabled):

| Module | Today | v2 path |
| --- | --- | --- |
| `merge.py` | applies v1 `Settings`/`Release` edits | `swhurl-v2 edit` |
| `proposal.py` | builds a v1 proposal | v2 record pair + compile |
| `promotion.py` | `apps.promotion` | `swhurl-v2 promote` |
| `routes/instance.py`, `routes/new_app.py` | v1 edit/new | v2 `edit` / create |
| `routes/teams.py` | `apps.teams` | kept (v1-only team registry) |

The console image (`images/console/Dockerfile`) is Python. The cleanest write adapter is to build the
`swhurl-v2` binary in that image and have a thin Python `V2` module call it (through the existing `Runner`
conventions) for `edit`, `remove` and `promote`. That keeps one compiler and one model. The alternative — a
Python port of v2's edit — duplicates the contract and is not recommended. A read-only console (change/new
buttons disabled with "use `swhurl-v2 edit`") is the fallback if the binary cannot be shipped in the image.

## 3. Phases

Each phase ends with a commit on `main`, `make check`, and the focused v2 checks. Nothing under `flux-system` is
touched. No live cluster change is expected after phase 1's console deploy, except the normal console image
rollout; phase 2's first live edit is confirmed with the operator.

**Phase 0 — freeze the surface (half a day).**
- `make v2-snapshot REVISION=HEAD OUT=/tmp/v2work/snapshot.json` and keep it with the plan.
- Record the exact command list, the console routes, and the test files that exercise them.
- Add a test that fails if a deleted command reappears or a console module imports a v1 writer module after
  phase 3.

**Phase 1 — the console reads v2 (1–2 days).**
- Add a small Python reader for `v2-records/<app>/{app,instance,release}.json` and
  `v2-apps/instances/<app>/<env>/*.json`; do not parse SOPS files.
- Switch `catalogue`, `context`, `pages`, `cluster`, `checks`, `flagstore` and `stages` reads to it. Keep the
  templates' view models stable.
- Disable the change/new/promotion **write** buttons for v2 instances with a clear message until phase 2.
- Tests: a fixture repo with one v2 app/instance; every page renders for all 13 live instances; signed in and
  signed out.
- Deploy the console; check `make verify-platform`'s console image check.

**Phase 2 — the console writes v2 (2–3 days).**
- Build `swhurl-v2` in `images/console/Dockerfile`; add `tools/swhurl/console/v2.py` as the only adapter
  (argv, records root, profile, expected revision; no model in Python).
- Port `merge.py`/`proposal.py`/`promotion.py` and `routes/instance.py` to it for `edit`, `remove`, `promote`;
  keep the proposal/PR flow (write the v2 records in a temp checkout, run the binary, commit the diff).
- Port `routes/new_app.py` only if v2 has a create path; otherwise disable new-app with a pointer to
  `app-repo` + `app-new` (the v1-only create) and state the gap.
- Tests: edit replicas, exposure, calls, allows, events, team; remove; promote; a refused edit carries v2's
  refusal; the console cannot write a v1 record.
- Live: one change to a throwaway staging instance, then revert; a promotion of a staging release that already
  exists in production (no new image), then confirm `observe`.

**Phase 3 — delete the v1 writer commands and modules (1–2 days).**
- Remove the **delete** rows from `commands.py`, the Makefile `COMMANDS`, [commands](commands.md), their tests
  and fixtures, and the functions/modules only they use.
- Delete `resolve`'s v1-shaped reading; `swhurl-v2 resolve` reads v2 records only. `verify-platform`'s v1
  branch is phase 4, not this phase.
- Keep `apps/checks.py` until phase 4 replaces `check-apps`.
- Keep the v1-only modules used by the **keep** rows: `new.py`, `repo.py`, `hooks.py`, `teams.py`, `offers.py`
  (only if `app-offers` is kept, which this plan does not), `keys.py` (only if `platform-signing-keys` is kept,
  which this plan does not), and the `model`/`store`/`render` paths those commands need. Mark each kept path in
  the plan's table.
- Acceptance: `rg "from swhurl\.apps" tools/swhurl/console` has no read/write hit for a deleted feature;
  `make check`, `make check-v2`, `make check-apps` (or its replacement), `make check-repo`.

**Phase 4 — the v2 checks (1 day).**
- Add a `make check-apps` replacement (or extend `check-v2`) that compiles every `v2-records` instance and
  compares it with `v2-apps`, checks `apps/teams.yaml`, and validates the platform documents. The Makefile's
  `check` keeps the same shape.
- `make verify-platform` reads v2 records for the namespace/team/quota/port checks; delete the v1 branch and
  its `0 instance(s)` path. Then delete `apps/checks.py` if the new `check-apps` no longer uses it.
- `swhurl-v2 resolve` test: one record shape, no v1 fallback.

**Phase 5 — external repositories (separate confirmation).**
- The eight app repositories' v1 `deploy.yml` is a change **outside this repository**. It needs the operator's
  explicit confirmation and the GitHub tokens; it is not part of phases 1–4.
- Until then, an app repository's v1 Deploy workflow can still write a v1 release for an app whose instance is
  v2-owned. The plan is to remove that workflow in phase 5, or to make it call the v2 Deploy workflow. This is
  the only remaining second writer after phase 3, and it is external.

## 4. Acceptance for the whole slice

- `make check` passes; `make check-v2` (and `REQUIRE_HELM=1 make check-v2`) passes; `make check-repo` and
  `make check-apps` (or its replacement) pass.
- `rg "from swhurl\.apps" tools/swhurl/console` has no import of a **deleted** writer; the allowed imports are
  listed in §2.2/§3.
- `rg "app-(scale|expose|calls|allows|events|team|remove|deploy|promote|sync|check|offers)" Makefile
  tools/swhurl tests docs` has no live command entry (only historical records).
- `verify-platform` has no v1 branch and still checks the four production namespaces' team, quota, policy and
  port from v2 records.
- `swhurl-v2 resolve` reads one record shape.
- The console, signed in and out, renders the catalogue, every instance page and the platform page; a change to
  a throwaway v2 instance succeeds; a promotion is proposed and applied; a v1-only fact (egress, alerts,
  schedule, secret) still points at its v1 command and says so.
- No `clusters/home/*.yaml` app unit; `apps/` has no instance record.
- Live evidence goes in [current state](current-state.md), with the same "what changed / checked / not
  exercised" shape as the cutover section.

## 5. Decisions to confirm before phase 2

1. **Keep the v1-only capabilities?** The plan keeps `app-egress`, `app-alerts`, `app-schedule`, `app-secret`,
   `app-new`, `app-repo`, `app-hooks`, `team-*` and `channel-*` because v2 has no replacement. If the operator
   prefers a strict "no v1 writer at all", they are deleted in phase 3 and the capabilities are listed as lost
   until v2 models them. **Recommendation: keep**, because none of them is a fact v2 also writes.
2. **Console write path:** build `swhurl-v2` into the console image and call it (recommended), or keep the
   console read-only until a v2 API exists. A Python port of v2 edit is refused: it creates a second model.
3. **External `deploy.yml`:** defer to phase 5 with explicit confirmation (recommended), or stop before phase 5
   and leave the external workflow as the only second writer, recorded in the gap list.

## 6. The reconcile delay, for the record

`app-swhurl-try-10-prod` took 788s in `make flux-reconcile` at `52f257bc`. The Helm upgrade itself took 26s
(14:02:33–14:02:59). The delay was before it, and then in a health-check retry:

- The new folder applied at **14:02:32** (CronJob and HelmRelease configured).
- The Kustomization's health check waited for `Ingress/swhurl-try-10-prod/swhurl-try-10` and timed out at
  **14:05:32** (`NotFound`).
- The Ingress was created at **14:15:33**, after the retry; the helm-controller's old release (which owned the
  same Ingress name inside its chart) had been upgraded at 14:02:35, removing the chart's Ingress. The likely
  cause is the one-time ownership move from a Helm-owned Ingress to the separate `ingress.json` of the same
  name: the first apply cannot create it while the chart owns it, and the health check's backoff makes the
  retry look like a 13-minute reconcile.
- It is not a recurring delay: the next reconciles are under a second. A future move can delete the chart's
  Ingress first, or apply the separate Ingress after the Helm upgrade, if the retry matters.

This is the only unexplained live observation from the migration; it is recorded here so the deletion does not
inherit it as a mystery.

## 7. Starting a new session

Paste this prompt:

> Read `docs/v2-deletion-plan.md` and `docs/iteration-2-plan.md` §8. The migrations are done; this is the deletion
> slice. Confirm decisions 1–3 in §5 with me before editing. Start at phase 0 and phase 1. Do not delete v1
> modules or commands until phase 3. Run `make check` after each commit (commit first, then check on the clean
> tree) and `make check-v2` for every v2 change. Do not touch `flux-system` or the eight app repositories; the
> external `deploy.yml` is phase 5 and needs separate confirmation. Keep one implementation writer at a time.

The first commands to run in that session:

```bash
git status -sb
sed -n '1,220p' docs/v2-deletion-plan.md
sed -n '1895,1925p' docs/iteration-2-plan.md
rg -n "from swhurl\.apps" tools/swhurl/console
rg -n "_edit\(|Command\('app-" tools/swhurl/commands.py
```

The plan is intentionally a checklist, not a substitute for reading the current code: re-check the command list,
the console imports and the working tree before deleting anything. Another session may have changed them.

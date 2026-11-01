# Deleting v1: the plan for 1b's second half

**Status: in progress (31 October 2026). Phases 0, 1, 2 and 3 are done — the console reads v2's records and writes
them through the `swhurl-v2` binary, live-tested by a memory change and its revert; v1's writer commands and modules
are deleted, `resolve` reads one record shape, and Activity reads v2's tree. Phase 4 (the v2-only checks) is next. The
four decisions in section 5 were confirmed 31 October 2026.** The migrations are done: nine staging and four production
instances are v2-owned, `apps/` holds no instance record, and the live checks in
[current state](current-state.md#the-production-cutover-four-instances-31-october-2026) pass. What remains is the
deletions and the v1-only branches in `verify-platform` and `resolve`.

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
| `app-egress` | egress grants | none (v2 has no egress-grant field) | **delete** (inert, below) |
| `app-alerts` | alert settings | none (v2 renders no alert) | **delete** (inert, below) |
| `app-schedule` | job schedule | none (no `job` kind) | **delete** (inert, below) |
| `app-secret` | secret names/values | none (secrets are not modelled) | **delete** (inert, below) |
| `app-new` | a new staging instance | none yet (create is a later v2 capability) | **keep** (the create path, below) |
| `app-repo` | a GitHub repository from a template | none yet | **keep** |
| `app-hooks` | image webhooks | none yet | **keep** |
| `team-*`, `channel-*` | `apps/teams.yaml` | none (v2 reads the team string, not the registry) | **keep** |
| `app-status`, `app-logs`, `app-reconcile` | reads only | n/a | **keep** |

The four **keep** rows the plan proposed as v1-only facts were measured before phase 0 on a clean archive of
`dfe4dafa`: each refuses before writing, because it reads v1's world (`apps/` records, through `write.read`) and no
instance has one. `app-egress`, `app-alerts`, `app-secret` and `app-schedule` all stop with `no instance
demo-shop/staging (missing apps/demo-shop/staging/release.yaml)` and exit 2. Keeping them would keep dead code whose
first act is to write a v1 record for an instance v2 owns, so **decision 1a deletes them** and the four facts (egress
grants, alert thresholds, job schedule, secret values) are recorded as lost until v2 models them. `app-new`,
`app-repo`, `app-hooks`, `team-*` and `channel-*` are not in that position: they need no existing record — they are
the create path, the GitHub side and the shared team registry, none of which v2 writes. **Decision 1b keeps them.**
The create flow after phase 3 is `app-repo` → `app-new` → `swhurl-v2 migrate` → delete the v1 record and switch the
unit, the sequence all 13 migrations took: a v1-created instance is a staging post, not a state the platform stays
in.

`platform-signing-keys` stays a **delete** row, but `apps/signing-keys.json` outlives it until phase 4: `verify`'s
`signing-keys` check and `check-apps`' policy still read the file, whose keys now reach the instances as
`platform/auth/` objects — a v1 copy waiting for its last two readers.

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

### 2.3 The surface, frozen (phase 0, 31 October 2026)

`make v2-snapshot REVISION=HEAD OUT=/tmp/v2work/snapshot.json` at `dfe4dafa` wrote **13 instances, 0 units without
records**; its SHA-256 is `778b60a82a7be41051e200426afb6af387ed4bf74c70cb877d1966889d536c63`. It is not committed: the
records change from here, and the pin is the commit, so a later comparison regenerates it with
`make v2-snapshot REVISION=dfe4dafa OUT=…`.

**Commands:** 79 in `tools/swhurl/commands.py`, plus 11 internal ones (`make`, `console`, `console-merge`,
`bot-render`, `bot-deploy`, `bot-deploy-v2`, `image-declaration`, `notify-serve`, `flux-wait`,
`backup-job-sqlite`, `backup-job-mongodb`). `docs/commands.md` and the Makefile's `COMMANDS` are checked against that
list, so a deletion that misses one of the three fails `make check` rather than sitting unnoticed.

**Console routes** (`tools/swhurl/console/routes/`): `/`, `/apps`, `/catalogue`, `/catalogue/{env}/use`, `/platform`,
`/egress`, `/units/{unit}`, `/units/{unit}/{action}`, `/healthz`, `/apps/{app}/{env}`,
`/apps/{app}/staging/promotion`, `/apps/{app}/{env}/flags/{flag}`, `/apps/{app}/{env}/{change}`, `/new`, `/new/repo`,
`/jobs/{id}/resume`, `/activity`, `/jobs`, `/jobs/{id}`, `/jobs/{id}/events`, `/teams`, `/teams/{action}`,
`/team-channel/{action}`, with `/services` and `/units` as redirects.

**Tests that exercise the surface**, by `def test_` count at this commit:

| Test file | Tests | The phase that rewrites it |
| --- | --- | --- |
| `tests/test_console_changes.py` | 93 | phase 2: the console's write and propose paths |
| `tests/test_console_merge.py` | 44 | phase 2: PR validation and merge |
| `tests/test_console_stages.py` | 14 | phase 1: `stages.py` |
| `tests/test_console_reading.py` | 11 | phase 1: the Git reader every page shares |
| `tests/test_console_jobs.py` | 9 | untouched |
| `tests/test_console_logging.py` | 4 | untouched |
| `tests/test_app_edit.py` | 60 | phase 3: the v1 writer every deleted command calls |
| `tests/test_app_ops.py` | 24 | phase 3 (`app-check`) and 4 (`verify-platform`) |
| `tests/test_app_manifest.py` | 19 | phase 3: v1's render |
| `tests/test_commands.py` | 17 | phase 3: the command table against the Makefile |
| `tests/test_verify.py` | 47 | phase 4: `verify-platform`'s two branches |
| `tests/test_manifest_policy.py` | 15 | phase 4: the app policy |
| `tests/test_command_safety.py` | 7 | phase 3: dry-run and live separations |

**The ratchet test lands with phase 3, not here.** A test that fails when a deleted command reappears can only assert
the list it is handed: written now it would either fail on today's tree or assert nothing, and a permanently failing
test is not a guard. It is phase 3's first step, in the same commit as the first deletion.

## 3. Phases

Each phase ends with a commit on `main`, `make check`, and the focused v2 checks. Nothing under `flux-system` is
touched. No live cluster change is expected after phase 1's console deploy, except the normal console image
rollout; phase 2's first live edit is confirmed with the operator.

**Phase 0 — freeze the surface. Done 31 October 2026.** The snapshot pin, the command list, the console routes and
the test inventory are in §2.3; the decision the inventory changed is in §2.1 and §5. The ratchet test moved to
phase 3 (§2.3).

**Phase 1 — the console reads v2. Done 31 October 2026.**
- **`tools/swhurl/console/records.py`** is the one reader: `Declared` (the instance's declared facts and the
  `problem` that says which part Git does not hold) from `v2-records/<app>/{app.json,<env>/instance.json,release.json}`,
  `of(app, env, read)` trying v2 first and v1 second, and `rows(...)` composing the declared connections (with why
  each does not connect yet) with the live ones. No SOPS file is read, and no v1 shape is translated: the page shows
  v2's facts, and says so where v2 has none (`minimum` is v1's only; a v2 release states no floor).
- **Reading the cluster, not Git, is where v2 moved facts.** The recon expected four v1-shaped HelmRelease reads.
  Measured on the live cluster: **the Ingress** is a separate object, so `hosts`, the web address and the API URL
  were empty for all 13 instances — `cluster.apps` lists ingresses now (`INGRESSES`) and reads the hosts from them
  when the release's values carry none. The **allow-list** moved into a `<app>-callers` ConfigMap, but it need not
  be read at all: v2 renders a *resolved* pair, so the caller's own variables (`SWHURL_SERVICE_<APP>_URL`) name the
  apps it may call, and inverting those gives exactly the rendered allow-list (checked against the live ConfigMaps:
  `demo-shop/staging` is called by `demo-shopper`, `demo-orders` by `demo-shop`). That keeps `platform/console/rbac.yaml`
  as it is — the console still reads no ConfigMap outside its own namespace. The API annotation, the exposure
  annotation, the offer commit and the digest are rendered, so they needed nothing.
- **The page's write forms are gone for a v2 instance**, replaced by one line naming `swhurl-v2 edit <app>/<env>
  --set <field>=<value>`, `swhurl-v2 promote <app>` and `swhurl-v2 remove <app>/<env>`; the promotion row and the
  Uninstall card say the same, and `change_app` and `review_promotion` refuse with the same words (409) rather than
  relying on a hidden form. `routes/new_app.py` is untouched: it creates a v1 record, which phase 2 replaces.
- **Two facts v2 does not model lose their forms now, not in phase 3**: **Reach a host outside** (no grant field in
  the records, so v1's command is inert) and **Schedule** (no `job` kind in v2). The pages still show what is
  granted and what schedule runs, read from the cluster. [apps.md](apps.md#reaching-outside-the-cluster) and
  [console.md](console.md) say so.
- **Tests**: `tests/test_console_v2.py` (6) renders the v2 instance page — records read, Ingress host published,
  declared rows with their pending reasons, no form, the three commands named by the page and by both POST paths.
  The v1-shaped console tests keep their fixtures and pass unchanged where they still describe something: they are
  the `app-new` path of decision 1b.
- **Verified live 31 October 2026** (image `src-ef5da49e447a3c90`): every page answers `200`, and a v2 instance's page
  carries its host, its grants, its declared connections and the three commands, with no form. The first deploy broke
  every instance page on a ConfigMap permission the console does not hold; the derivation above replaced that read.
  [Current state](current-state.md#the-console-reads-v2-and-writes-nothing-31-october-2026) has the evidence.

**Phase 2 — the console writes v2. Done 31 October 2026.**
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
- **Found before the port, 31 October 2026: the three commands are not enough.** `v2-records/calls.json` (the
  call and event resolution) came only from `resolve --snapshot`, and the snapshot is v1's YAML records: nothing
  derived it from `v2-records/`, so a `calls`/`allows`/`subscriptions` edit could not be completed — `edit` says so
  itself — and `remove` reports the platform documents it will not write. The per-instance files, the units and the
  platform documents were a mix of `make` targets, hand-pasted snapshots and `v2/check-rendered.sh` (which checked
  neither `v2-apps/` nor the units). A console that ran only `edit`/`remove`/`promote` would therefore commit a tree
  whose change does not take effect, or re-implement that pipeline in Python — the second model decision 2 refuses.
- **Done 31 October 2026, offline: `swhurl-v2 render`.** One command renders the whole tree from the records —
  `calls.json`, every instance's compiled folder and unit, the egress allocation and the proxy's two files, the
  auth folder, NATS's users and Reloader's values — and `--check` compares all of it instead of writing. It reuses
  the existing resolvers, renderers and `orchestration.CompileInto` (no new semantics, no new package), and it is
  what phase 4's check needs: `v2/check-rendered.sh` is now one `render --check`, and `make check-v2` compares 208
  files, of which `v2-apps/` and `clusters/home/app-*.json` were previously unchecked. Verified on this tree: 208
  checked, 0 differ, nothing written; a one-field drift in a record names the file it moves. `images/console/Dockerfile`
  now builds the binary from a pinned `golang:1.26.0-bookworm` stage, and `v2/` is in the console's content hash
  (`platform.CONSOLE_IMAGE_INPUTS` and `validate.yml`, which `v2/coexistence_test.go` now holds in step — the
  assertion that the image must *not* consume v2 was the contract decision 2 replaces).
- **Done 31 October 2026, offline: the console's write surface.** `tools/swhurl/console/v2.py` is the only adapter
  (argv, binary, records root, profile, the `@base` revision filled at the PR head, and v2's refusal verbatim).
  `merge.py` gained the seven v2 actions (scale, expose, connect, disconnect, allow, remove, promote), each one
  `edit`/`remove`/`promote` followed by `render`, each `gated=False`; `proposal.py` runs them on the PR head and
  never auto-merges one; `routes/instance.py` builds them from the forms (replicas and memory only, whole-list
  connections and allowed callers, no host), and `review_promotion` shows the v2 review. The app page offers those
  forms again; the banner that said the records were v1's is gone. `routes/new_app.py` is disabled with the four-step
  pointer (`app-repo` → `app-new` → `migrate` → delete the v1 records), while its resume route still finishes a job
  an earlier creation started. A console image without the binary refuses with the command to run, rather than
  failing a job. **The one open choice was the operator's, and option 1 was taken:** every v2 PR is held and the
  gate's v1 read/judge/`app-sync` stays untouched until phase 3 deletes it. Nothing was applied.
- **Done live 31 October 2026: the change and its revert.** PR #101 took `swhurl-try-10/staging` from 128 to
  192 MiB and PR #102 returned it to 128; the operator merged each held PR, Flux applied it, the Deployment stated
  the new memory (192Mi then 128Mi) with a ready pod, the instance reached Healthy, and `make verify-platform`
  passed. The first attempt, PR #100, was closed unmerged after Validate exposed the migration test's live-vs-frozen
  HelmRelease input; the test now reads the frozen fixtures. A promotion through the console has not been exercised;
  it waits for a staging release that already exists in production and the operator's request.
  [Current state](current-state.md#the-first-live-console-edit-and-the-frozen-fixture-it-found-31-october-2026) has
  the evidence.

**Phase 3 — delete the v1 writer commands and modules (done 31 October 2026).**
- Removed the **delete** rows from `commands.py`, the Makefile `COMMANDS`, [commands](commands.md), their tests
  and fixtures, and the functions/modules only they used, with the ratchet test in the same commit.
- `swhurl-v2 resolve` reads v2 records only: a snapshot instance still in a predecessor's shape is refused.
  Activity's `render_targets` reads `v2-records/` and `v2-apps/`, not v1's `apps/`. `verify-platform`'s v1
  branch is phase 4, not this phase.
- Keep `apps/checks.py` until phase 4 replaces `check-apps`.
- Keep the v1-only modules used by the **keep** rows: `new.py`, `repo.py`, `hooks.py`, `teams.py`, `offers.py`
  (only if `app-offers` is kept, which this plan does not), `keys.py` (only if `platform-signing-keys` is kept,
  which this plan does not), and the `model`/`store`/`render` paths those commands need. Mark each kept path in
  the plan's table.
- Acceptance (met): `rg "from swhurl\.apps" tools/swhurl/console` has no read/write hit for a deleted feature;
  `make check`, `make check-v2`, `make check-apps` (or its replacement), `make check-repo`.

**Phase 4 — the v2 checks (1 day).**
- Add a `make check-apps` replacement (or extend `check-v2`) that compiles every `v2-records` instance and
  compares it with `v2-apps`, checks `apps/teams.yaml`, and validates the platform documents. The Makefile's
  `check` keeps the same shape.
- `make verify-platform` reads v2 records for the namespace/team/quota/port checks; delete the v1 branch and
  its `0 instance(s)` path. Then delete `apps/checks.py` if the new `check-apps` no longer uses it.
- `swhurl-v2 resolve` test (done in phase 3): one record shape, a predecessor's refused.

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
- `rg "app-(scale|expose|calls|allows|events|team|remove|deploy|promote|sync|check|offers|egress|alerts|schedule|secret)"
  Makefile tools/swhurl tests docs` has no live command entry (only historical records).
- `verify-platform` has no v1 branch and still checks the four production namespaces' team, quota, policy and
  port from v2 records.
- `swhurl-v2 resolve` reads one record shape.
- The console, signed in and out, renders the catalogue, every instance page and the platform page; a change to
  a throwaway v2 instance succeeds; a promotion is proposed and applied; a v1-only fact (egress, alerts,
  schedule, secret) is named on the page as not modelled by v2, with no command left that pretends to set it.
- No `clusters/home/*.yaml` app unit; `apps/` has no instance record.
- Live evidence goes in [current state](current-state.md), with the same "what changed / checked / not
  exercised" shape as the cutover section.

## 5. Decisions (confirmed 31 October 2026)

The operator took every recommendation, with one qualification that governs the rest of the slice: **v1
compatibility is not required.** A phase may leave a service not working rather than buy it with a second model;
what is refused is a design chosen because the answer was not known yet.

1. **The four inert editors are deleted; the rest stay.** `app-egress`, `app-alerts`, `app-schedule` and
   `app-secret` refuse on every instance (§2.1), so keeping them preserves dead code, not a capability: they go in
   phase 3, and their four facts are listed as lost until v2 models them. `app-new`, `app-repo`, `app-hooks`,
   `team-*` and `channel-*` stay: create, the GitHub side and the shared team registry are facts v2 does not write.
   After phase 3 a new app arrives as `app-repo` → `app-new` → `swhurl-v2 migrate` → the v1 record deleted and the
   unit switched.
2. **The console writes v2 by running v2's binary.** Phase 2 builds `swhurl-v2` in `images/console/Dockerfile`
   (pinned `golang:1.26.0-bookworm`, the precedent `images/backup/Dockerfile` set) and routes `edit`, `remove` and
   `promote` through one `console/v2.py`; a Python port of v2's edit is refused because it is a second model. A
   read-only console stays the fallback if the binary cannot ship. The image's CI content hash gains `v2/`, so a v2
   change rebuilds the console.
3. **The eight app repositories' `deploy.yml` waits for phase 5**, with its own confirmation and the GitHub tokens.
   Until then it is the only second writer left, and it is external.
4. **The freeze is a pin, not a file.** The snapshot is regenerated from `dfe4dafa`, so §2.3 records the command,
   the revision and the hash rather than a 49 KB copy the next phase invalidates.

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
> slice. The decisions in §5 are confirmed; do not reopen them. Start at the next phase the status line names. Do
> not delete v1 modules or commands before phase 3. Run `make check` after each commit (commit first, then check on
> the clean tree) and `make check-v2` for every v2 change. Do not touch `flux-system` or the eight app
> repositories; the external `deploy.yml` is phase 5 and needs separate confirmation. Keep one implementation writer
> at a time.

The first commands to run in that session:

```bash
git status -sb
sed -n '1,60p' docs/v2-deletion-plan.md
sed -n '1895,1925p' docs/iteration-2-plan.md
rg -n "from swhurl\.apps" tools/swhurl/console
rg -n "_edit\(|Command\('app-" tools/swhurl/commands.py
```

The plan is intentionally a checklist, not a substitute for reading the current code: re-check the command list,
the console imports and the working tree before deleting anything. Another session may have changed them.

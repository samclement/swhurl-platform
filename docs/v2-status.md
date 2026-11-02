# Go replacement (v2): progress, deferred work and the next gate

Status: in progress
Gate: the post-cutover leftovers (M14, re-scoped), then the ownership milestone
Uncommitted: none

**The goal is one Go implementation and no Python.** v2 becomes the platform's only implementation —
kernel, compiler, CLI, console, checks and recovery, built from this repository — and `tools/swhurl`
(Python) is retired. Bash stays for host glue. v2 already owns every instance and the live loop; the
Python surfaces below are the rest of the road. Each retirement is its own approved slice, and the
console port is not approved.

This page is the one place v2's progress, remaining work and deferred features live. Contracts are in
[iteration 2](iteration-2-plan.md); dated evidence in [current state](current-state.md); the registry in
[workstreams](workstreams.md). Rows are overwritten in place; history is Git and current-state.

## Where it stands

| Capability | State | Evidence |
| --- | --- | --- |
| Release selection and identity | done | [contract](iteration-2-plan.md#2-phase-1-model-and-contract) |
| Deterministic compiler | done | [kernel](../v2/kernel) |
| Git delivery and units | done | [record](current-state.md#first-v2-instance-live-25-october-2026) |
| Observation | done | [record](current-state.md#first-v2-observation-of-a-live-instance-25-october-2026) |
| Shadow bridge | done | [shadow](../v2/shadow) |
| Routes | done | [record](current-state.md#first-v2-route-live-signed-in-exposure-25-october-2026) |
| Calls between apps | done | [record](current-state.md#the-first-call-between-two-v2-owned-instances-26-october-2026) |
| SQLite storage | done | [record](current-state.md#sqlite-storage-proved-live-25-october-2026) |
| Backup and restore | done | [record](current-state.md#the-first-content-changing-restore-swhurl-try-10-staging-moved-back-a-day-27-october-2026) |
| Promotion and rollback | done | [record](current-state.md#a-rollback-exercised-live-2-november-2026) |
| Offers and documents | done | [record](current-state.md#every-image-states-its-documents-and-the-productions-follow-1-november-2026) |
| Cutover: one writer | done | [record](current-state.md#the-production-cutover-four-instances-31-october-2026) |
| Checks read v2's tree | done | [record](current-state.md#the-checks-read-v2s-tree-31-october-2026) |
| One render from the records | done | [record](current-state.md#the-derived-tree-comes-from-v2s-own-records-31-october-2026) |
| Egress and the proxy | done | [record](current-state.md#the-egress-cutover-v2-renders-the-proxy-and-grants-are-enforced-26-october-2026) |
| Always-on instance surface | done | [record](current-state.md#the-always-on-instance-surface-is-live-29-october-2026) |
| Team grants | done | [record](current-state.md#team-grants-carry-from-a-predecessors-records-30-october-2026) |
| Reloader watch list | done | [record](current-state.md#v2-renders-reloaders-watch-list-and-a-migrated-instance-is-watched-again-29-october-2026) |
| Worker kind | done | [kernel](../v2/kernel) |
| Events | done | [record](current-state.md#v2-owns-natss-user-list-and-its-first-event-subscriber-is-live-29-october-2026) |
| Broker identity minting | done | [record](current-state.md#v2-minted-a-broker-identity-for-both-keyless-instances-29-october-2026) |
| Image automation and deploy | done | [record](current-state.md#the-first-image-delivered-by-v2-30-october-2026) |
| Create | done | [record](current-state.md#a-new-app-created-from-its-own-image-live-1-november-2026) |
| Console writes v2's records | done | [record](current-state.md#the-consoles-new-app-is-v2s-live-1-november-2026) |
| Interface catalogue: templates and v2's tree | done | [record](current-state.md#the-interface-catalogue-checks-the-templates-and-v2s-tree-2-november-2026) |

## Remaining Python

What still runs Python, what replaces it, and where that port stands. This table is the distance to the
goal; each row is its own approved slice.

| Python surface | What replaces it | State |
| --- | --- | --- |
| `console/` (Starlette and its templates) | a Go server that keeps the same HTMX templates | not started; not approved |
| Operator commands (`commands.py`, the `make` targets) | `swhurl-v2` subcommands or bash | partial: the instance and delivery commands are Go (`edit`, `remove`, `promote`, `create`, `deploy`, `render`, `observe`, `mint`, `restore`); the reads, backups and one-off operations remain |
| Platform checks (`check-apps`'s team, notify and fixture rules, `verify-platform`, `check-templates`, `check-docs`, `check-repo`) | v2 checks in Go | partial: `render --check` and `check-v2` are Go; the rest is Python |
| The image-declaration producer (`image_declaration.py`) | a Go producer the app builds call | not started |
| The v1 records model and its fixture oracle (`apps/model/*`) | delete; v2 judges the fixtures | not started: it survives for the fixtures' policy and the team/channel edits |
| Recovery and backup tooling (`recovery.py`, `sqlite_backup.py`, `sqlite_restore.py`, `backupset.py`) | `swhurl-v2 restore` covers an instance; operator recovery remains | partial |
| Notification, heartbeat, ClickStack and dashboard tooling | `swhurl-v2` or bash | not started |
| `app-repo` and `app-hooks` (Copier and the GitHub API) | v2, or a kept GitHub step in bash | not started |

## Remaining work

Ordered; each item is small enough to land and verify alone.

1. **One S3 location, one source.** Read `BACKUP_S3_URI` from `platform/backup/mongodb.yaml` (its only
   reader, `recovery.py`, is a host command, so the tree is there) and either read `SQLITE_S3_URI` from the
   profile or check that the two agree.
2. **The team commands see v2 apps.** `team-remove` and `team-edit` plan against the v1 model, which finds
   no instance now, so their documented refusal cannot fire. Make them read `v2-records`, or refuse while a
   v2 app names the team.
3. **Renovate, recorded rather than changed.** Verified 2 November 2026: no app-chart PR ever existed (PR
   #47's base had no `apps/` HelmRelease), and the app chart version is the profile's, which Renovate cannot
   see in `v2-apps/*.json`. Take #47 as the platform's three HelmReleases, keep apps on 5.2.1, and drop
   `apps/` from `managerFilePatterns` only if a later bump shows it reads something there.
4. **The v1 Python model.** Decide whether it stays bounded (a ratchet test) or goes with the fixtures'
   policy. It reads no v2 record and is the last Python that touches an app's rendered files; the v1
   renderer's interface tests in `tests/test_interface.py` go or stay with it.
5. **The ownership milestone.** Record that v2 renders, writes and checks every instance, commit it, and set
   the next gate: a decision brief for the console port, the largest Python surface.
6. **The Python retirements.** Console, operator commands, checks, producer and host tooling, each its own
   approved slice; the retirement order and the first brief are the next gate after the milestone.

## Deferred features

The one list of features v2 does not model. A row leaves this table when its capability lands, or when the
operator decides against it.

| Feature | v2 today | Reopen when |
| --- | --- | --- |
| Egress grants declared by an instance record | Only the platform's backup host; `edit --set egress=` is refused by name | An instance must reach an outside host of its own |
| An instance that must not reach outside | Every instance that needs one gets a proxy port; nothing withholds one | A kind that must not egress |
| The egress namespace's own egress | Not restricted (the rendered policy states Ingress only) | Default-deny for the platform's own namespaces |
| Routes: `public`, a host override, route observation | `authenticated-web` and `private` only; no host is stored; observation checks no route | An instance needs a public route or an unusual host |
| App kinds: `job` and a command override | Refused (`unsupported`); `worker` is modelled | An instance needs a schedule or a command override |
| Secrets (`envFrom`, Reloader) and bindings | Non-empty `bindings` and `secrets` are refused | An app declares one |
| Alerts | Not generated; `edit --set alerts=` is refused by name | An app needs an alert |
| A per-instance memory or CPU choice | CPU from the profile; one memory value for request and limit | An app needs more |
| Quota headroom for other pods | The backup pod is counted; no other second workload exists | A feature adds pods |
| Image automation: auto-merge and a production path | Staging follows its images; production follows a promotion | An unattended production update is wanted |
| A first production (creating it) | `promote` refuses an environment with no record | A first promotion must not need a person |
| Restore breadth | One live restore over the newest archive, and one that changes content; another instance's archive, a point in time and the failure paths are unexercised | A restore beyond the newest archive over itself |
| The tag an instance was selected with | Not recorded; `verify-platform` resolves the tag through the index | A record-shape change carries it |
| Complete release, source and build verification | Claims are stated, not verified; a migrated release's artifact is checked | Before claiming verified release semantics |
| Observation breadth | Four kinds; no pod-level facts, no foreign ownership, no other kind | A consumer needs them |
| A credential source other than a kubeconfig in JSON | Kubeconfig JSON only; no in-cluster token, no YAML | v2 runs in the cluster, or YAML is approved |
| Live ownership of foreign objects | The bridge reads the repository's own claims; another owner's objects are not read | A migration needs the wider inventory |
| Console: repository creation, seed images, historical Activity | None | Later |
| Live feature flags | None | Later |

## Durable decisions

Rules that constrain the next change, one line each. The reasoning stays in Git and current-state.

- One writer per fact, no compatibility layers, records are v2 JSON only, unused features stay on the
  backlog, promotion is one-way, and `edit` is the one settings primitive (31 October 2026).
- A rollback is a promotion of an older release. A release from before declaration label version 2 is
  converted once as data, never read by a new decoder (2 November 2026).
- A team grant is the callee's rule, matched against the caller's own record: no member list, no registry
  read (30 October 2026).
- The backup host is the platform's grant from the profile; an instance record gains no `egress` field until
  one needs an outside host (28 October 2026).
- Restore: v2 orchestrates and a pod decrypts, with a presigned URL for one archive and the age key in a
  Secret v2 creates and deletes (26 October 2026).
- The bucket is read with a hand-written SigV4 signer and the operator's credentials; the region comes from
  the bucket, not the profile (26 October 2026).
- The egress proxy renders only its generated objects; its deployment stays hand-written and grants are
  enforced (25 October 2026).
- Delivery and observation use the operator's credentials from the host, not a new Secret or control plane
  (25 October 2026).
- The declaration is label version 2: the image states its whole declaration, version 1 is refused, and a
  digest determines a release (1 November 2026).
- `create` writes records only; the whole-tree render allocates the port and writes the files
  (1 November 2026).

## How this page is kept

- One row per capability. Overwrite it; never append. Its evidence is one dated link.
- No slice records and no dated narrative: history is `git log -p -- docs/v2-status.md` and
  [current state](current-state.md)'s dated entries.
- A change that moves a capability updates its row and adds one dated `current-state.md` entry, in the same
  commit.
- A change deletes what it makes false, here and in every page it links. The independent review asks: "what
  did this change delete from this page, and what else did it make false?"
- `make check-docs` enforces the shape: these sections, the state vocabulary, a resolving evidence link per
  row, a non-empty deferred table, no slice records, and the line cap (250).

## Where the rest lives

- **Contract:** schemas, identity, selection, refusal codes, layers and guardrails —
  [iteration 2](iteration-2-plan.md).
- **Dated evidence:** [current state](current-state.md).
- **Workstream registry:** [workstreams](workstreams.md).
- **Removal inventory:** [deleting v1](v2-deletion-plan.md).
- **Commands and app lifecycle:** [commands](commands.md), [apps](apps.md).

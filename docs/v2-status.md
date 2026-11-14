# Go replacement (v2): progress, deferred work and the next gate

Status: parked
Gate: choose the next slice from remaining work or deferred features
Uncommitted: none

**The goal is one Go platform implementation.** v2 becomes the platform's only implementation —
kernel, compiler, CLI, console, checks and recovery, built from this repository — and `tools/swhurl`
(Python) is retired. Bash stays for host glue. v2 already owns every instance and the live loop; Copier and the separate
codegraph development aid are retained exceptions; the Go chart tests also call upstream PyYAML. Each retirement is its own approved slice; the console
left by the [approved strangling](iteration-2-plan.md#11-the-console-port-strangled-from-inside-approved-3-november-2026).

This page is the one place v2's progress, remaining work and deferred features live. Contracts are in
[iteration 2](iteration-2-plan.md); dated evidence in [current state](current-state.md); the registry in
[workstreams](workstreams.md). Rows are overwritten in place; history is Git and current-state.

## Where it stands

| Capability | State | Evidence |
| --- | --- | --- |
| Release selection and identity | done | [contract](iteration-2-plan.md#2-phase-1-model-and-contract) |
| Deterministic compiler | done | [kernel](../v2/kernel) |
| App policy: Go decisions, legacy checker/API retired | done | [record](current-state.md#legacy-app-policy-retirement-4-november-2026) |
| The v1 records model, its fixture oracle and the Python nothing ran | done | [record](current-state.md#the-unreachable-python-leaves-and-the-console-is-signed-off-12-november-2026) |
| Workstream check (`check-docs`) in Go | done | [record](current-state.md#the-workstream-check-leaves-python-5-november-2026) |
| Python-free `check-apps` and explicit chart acquisition | done | [record](current-state.md#check-apps-leaves-python-5-november-2026) |
| Notify service in Go, Python implementation retired | done | [retirement](current-state.md#the-python-notify-service-is-retired-5-november-2026), [live cutover](current-state.md#the-notify-service-switches-to-go-5-november-2026) |
| Notification checker in Go, Python implementation retired | done | [lifecycle acceptance and retirement](current-state.md#notification-checker-lifecycle-acceptance-and-python-retirement-7-november-2026) |
| Dashboard reconciler in Go (`swhurl-v2 clickstack-dashboards`), Python implementation retired; it also keeps the `Platform: <service>` dashboards of the platform's own services | done | [retirement](current-state.md#the-dashboard-reconcilers-python-is-retired-7-november-2026), [live cutover](current-state.md#the-dashboard-reconciler-switches-to-go-7-november-2026), [platform family](current-state.md#platform-dashboards-13-november-2026) |
| Host heartbeat in Go (`swhurl-v2 notifications-heartbeat`), Python implementation retired | done | [cutover and retirement](current-state.md#the-host-heartbeat-runs-go-and-its-python-is-retired-7-november-2026), [offline](current-state.md#the-host-heartbeat-in-go-offline-7-november-2026) |
| ClickStack bootstrap in Go (`swhurl-v2 clickstack-bootstrap`), Python implementation retired | done | [record](current-state.md#clickstack-bootstrap-runs-go-7-november-2026) |
| `check-repo` in Go (`swhurl-v2 check-repo` decides; `v2/check-repo.sh` renders, substitutes, parses shell and, with `SCHEMA=1`, validates schemas), Python implementation retired | done | [record](current-state.md#check-repo-runs-go-and-validatepy-is-retired-8-november-2026) |
| Git delivery and units | done | [record](current-state.md#first-v2-instance-live-25-october-2026) |
| Observation | done | [record](current-state.md#first-v2-observation-of-a-live-instance-25-october-2026) |
| Routes | done | [record](current-state.md#first-v2-route-live-signed-in-exposure-25-october-2026) |
| Calls between apps | done | [record](current-state.md#the-first-call-between-two-v2-owned-instances-26-october-2026) |
| SQLite storage | done | [record](current-state.md#sqlite-storage-proved-live-25-october-2026) |
| Backup and restore | done | [record](current-state.md#the-first-content-changing-restore-swhurl-try-10-staging-moved-back-a-day-27-october-2026) |
| Promotion and rollback | done | [record](current-state.md#a-rollback-exercised-live-2-november-2026) |
| Image declaration producer in Go | done | [workflow rollout](current-state.md#go-producer-adoption-by-template-workflows-5-november-2026) |
| Feature flags in Go: the signed NATS key-value adapter, the card's rows, `swhurl-v2 flags` and the console asking it | done | [record](current-state.md#feature-flags-are-gos-and-the-consoles-card-asks-it-8-november-2026) |
| The console server in Go: `swhurl-v2 console` serves Overview, Platform, Deployments (list and map), Egress, a Flux unit, Teams, the Catalogue (with each app's own OpenAPI and AsyncAPI documents), Activity, a job's page (each change followed past its pull request to the cluster), an app instance's page and the page that waits for it, a promotion's review and the New app form — byte-identically from embedded templates and assets, with the oauth2 identity, `/healthz` and `--dev` | done | [signed off](current-state.md#the-unreachable-python-leaves-and-the-console-is-signed-off-12-november-2026), [pages](current-state.md#the-console-server-serves-its-pages-9-november-2026), [catalogue](current-state.md#the-catalogue-is-the-consoles-documents-and-all-9-november-2026), [activity](current-state.md#activity-and-a-jobs-page-are-the-consoles-9-november-2026), [app, pending, promotion and New app](current-state.md#the-app-page-pending-promotion-and-new-app-are-the-consoles-9-november-2026), [Activity keeps a change it saw](current-state.md#activity-keeps-a-change-it-saw-and-reads-history-one-archive-at-a-time-10-november-2026), [and reads a merge commit's four files](current-state.md#activity-reads-a-merge-commits-four-files-not-its-archive-10-november-2026), [a waiting stage says so on screen](current-state.md#a-new-worker-from-the-go-console-test-01-11-november-2026) |
| The console's and notify's own telemetry in Go: one structured line per request and per job state change, and the `Platform: <service>` dashboards' request and job tiles that count them | done | [record](current-state.md#console-and-notify-telemetry-14-november-2026) |
| The console's own traces and metrics: the pure `telemetry` model and OTLP/JSON encoding, the `adapters/otlp` exporter, a span per request with its cluster, GitHub, pinned-tree and render reads inside, and request and job metrics | partial | [contract](platform-telemetry-status.md#remaining-work) |
| Platform verification in Go (`swhurl-v2 verify`: all twenty-four groups, six of them the console's cluster scope) | done | [record](current-state.md#every-platform-check-is-go-7-november-2026); the egress check asks a port only of an instance that reaches the proxy, and the events check warns when JetStream cannot fit another app stream ([record](current-state.md#a-new-worker-from-the-go-console-test-01-11-november-2026)) |
| `check-otel` as bash in the collector's own image (`platform/otel/check.sh`: `helm template`, then `validate` and the log fixtures in the rendered image), `otel.py` and the Python fixture harness retired | done | [record](current-state.md#check-otel-runs-in-the-collectors-own-image-11-november-2026) |
| `check-secrets` in Go (`swhurl-v2 check-secrets`: the pure `secretcheck` decisions over a local SOPS decrypt), `secrets_check.py` retired | done | [record](current-state.md#check-secrets-runs-go-13-november-2026) |
| `platform-certs-*` in Go (`swhurl-v2 platform-certs`: the pure `certissuer` edit of `CERT_ISSUER`), `settings.py` retired | done | [record](current-state.md#platform-certs-runs-go-13-november-2026) |
| `destroy-data` in Go (`swhurl-v2 destroy-data`: the pure `datadestroy` guards over the API server, Helm's records read by name only), `lifecycle.py` retired | done | [record](current-state.md#destroy-data-runs-go-13-november-2026) |
| `live-test-team-scenario` in Go (`swhurl-v2 team-scenario`: the pure `teamscenario` chain and verdicts, the chain from the records alone, ClickHouse by API-server exec), `team_scenario.py` retired | done | [record](current-state.md#the-team-scenario-runs-go-13-november-2026) |
| `live-test-lifecycle` in Go (`swhurl-v2 lifecycle-test`: the pure `lifecycletest` verdicts, v2's unit actions and destroy-data in process, the marker by API-server exec), `livetests/lifecycle.py` retired | done | [record](current-state.md#the-lifecycle-test-runs-go-13-november-2026) |
| Host suspend and resume through Go for units and HelmReleases | done | [contract](iteration-2-plan.md#32-make-suspend-and-make-resume-use-go-approved-13-november-2026), [record](current-state.md#host-suspend-and-resume-use-go-13-november-2026) |
| The command catalogue without Python: each target one `##` line in the Makefile, `make help` an `awk` over them, `commands.py` and its dispatch deleted | done | [contract](iteration-2-plan.md#34-the-command-catalogue-leaves-python-make-help-over-the-makefiles-own-lines-approved-13-november-2026), [record](current-state.md#the-command-catalogue-leaves-python-13-november-2026) |
| The last of `tools/swhurl` (`charts.py`, `run.py`, `platform.py`, `yamlio.py`) and Validate's classifier (`.github/records-only.sh`, the dead app-only path dropped) | done | [contract](iteration-2-plan.md#35-the-last-of-tools-the-clickstack-render-test-pulls-its-own-chart-and-validates-classifier-is-bash-approved-13-november-2026), [record](current-state.md#the-last-of-tools-leaves-13-november-2026) |
| Top-level Python regression suite and host probe dropped; Copier-only runtime dependencies | done | [delivered console image and host removal verified](current-state.md#python-tests-and-the-host-probe-retired-offline-13-november-2026) |
| Live log coverage as a saved SQL diagnostic, Python command retired | done | [record](current-state.md#log-coverage-is-a-saved-query-11-november-2026) |
| Template check without Python but Copier | done | [record](current-state.md#the-template-check-leaves-python-5-november-2026) |
| Offers and documents | done | [record](current-state.md#every-image-states-its-documents-and-the-productions-follow-1-november-2026) |
| Bulk status and revision evidence | done | [record](current-state.md#bulk-status-and-revision-evidence-in-go-3-november-2026) |
| Status with the pod's ServiceAccount | done | [record](current-state.md#status-with-the-pods-serviceaccount-3-november-2026) |
| Status from the records | done | [record](current-state.md#swhurl-v2-status-replaces-make-app-status-3-november-2026) |
| Console reads Go status | done | [record](current-state.md#bulk-status-and-revision-evidence-in-go-3-november-2026) |
| Team registry and notification routes | done | [record](current-state.md#the-team-registry-leaves-python-4-november-2026) |
| Backup and restore commands without Python | done | [record](current-state.md#the-backup-and-restore-commands-leave-python-4-november-2026) |
| Every instance v2-owned | done | [record](current-state.md#the-ownership-milestone-every-instance-is-v2s-3-november-2026) |
| Cutover: one writer | done | [record](current-state.md#the-production-cutover-four-instances-31-october-2026) |
| Checks read v2's tree | done | [record](current-state.md#the-checks-read-v2s-tree-31-october-2026) |
| One render from the records | done | [record](current-state.md#the-derived-tree-comes-from-v2s-own-records-31-october-2026) |
| One render is the only producer of the platform files; the snapshot export and migration tools are retired | done | [record](current-state.md#the-snapshot-export-leaves-python-and-v2s-migration-tools-are-retired-8-november-2026) |
| Egress and the proxy | done | [record](current-state.md#the-egress-cutover-v2-renders-the-proxy-and-grants-are-enforced-26-october-2026) |
| Always-on instance surface | done | [record](current-state.md#the-always-on-instance-surface-is-live-29-october-2026) |
| Team grants | done | [record](current-state.md#team-grants-carry-from-a-predecessors-records-30-october-2026) |
| Reloader watch list | done | [record](current-state.md#v2-renders-reloaders-watch-list-and-a-migrated-instance-is-watched-again-29-october-2026) |
| Worker kind | done | [kernel](../v2/kernel) |
| Events | done | [record](current-state.md#v2-owns-natss-user-list-and-its-first-event-subscriber-is-live-29-october-2026) |
| Broker identity minting | done | [record](current-state.md#v2-minted-a-broker-identity-for-both-keyless-instances-29-october-2026) |
| Image automation and deploy | done | [record](current-state.md#the-first-image-delivered-by-v2-30-october-2026) |
| Create | done | [record](current-state.md#a-new-app-created-from-its-own-image-live-1-november-2026); the render registers the unit ([record](current-state.md#a-new-worker-from-the-go-console-test-01-11-november-2026)) |
| The publish path in Go (`swhurl-v2 change`): one kind table, the pinned tree, the owned paths, the five GitHub writes and the pull request's words | done | [contract](iteration-2-plan.md#24-the-publish-path-in-go-step-2a-proposed-9-november-2026), [record](current-state.md#the-console-relays-every-change-to-swhurl-v2-change-9-november-2026) — the deployed console relays every change to it and its Python publication is deleted; a scale from the deployed console opened a real pull request whose body, author, commit and files match the one the Python console opened |
| The Flux unit actions in Go (`swhurl-v2 unit`: reconcile, suspend, resume, the source before the unit, and the wait on `lastHandledReconcileAt`, `observedGeneration` and `Ready`) with the console relaying its three POSTs to it | done | [contract](iteration-2-plan.md#25-the-flux-unit-actions-in-go-step-2b-proposed-9-november-2026), [record](current-state.md#the-consoles-flux-actions-run-swhurl-v2-unit-9-november-2026) — the deployed console's suspend, refused reconcile, resume and reconcile changed a throwaway unit and reverted it, and `actions.py`'s `kubectl` patches are deleted; the job lifecycle is ported to `orchestration` and gets its store, pages and stream at the cutover |
| New app's repository in Go (`swhurl-v2 app-repo`): the stack catalogue's questions, one pinned Copier run into a scratch folder, the public repository with its first commit and image webhook through the GitHub write port, the app's own first image through the registry adapter (the template's seed is a copied build, so it is not a release) and the ordinary staging pull request; the console's New app relays it, and `apps/repo.py` is deleted | done | [record](current-state.md#the-new-app-path-takes-the-apps-own-image-9-november-2026) — the deployed console resumed a job, found the repository's `1-<sha>` image, opened the staging pull request with the app's own description and documents, and the instance then ran, was removed, and its repository and package deleted |
| The Flux commands without Python: `swhurl-v2 flux-wait` (every unit Ready at the fetched revision, through the API server), `make reconcile` over `swhurl-v2 unit`, and `make flux-install` as bash over `flux` and `kubectl` (`host/flux-install.sh`), so Go gains no install effect | done | [record](current-state.md#the-flux-commands-leave-python-12-november-2026) |
| CI's bot pushes and the console image pin without Python: [`host/push-main.sh`](../host/push-main.sh) (`render`, `deploy`, `console-image --expect`) over Git and `make`, and [`images/pin-console.sh`](../images/pin-console.sh) behind `make console-image`, with `bots.py` and `images.py` deleted | done | [record](current-state.md#the-bots-and-the-console-image-pin-leave-python-12-november-2026) |
| Console writes v2's records | done | [record](current-state.md#the-consoles-new-app-is-v2s-live-1-november-2026) |
| Interface catalogue: templates and v2's tree | done | [record](current-state.md#the-interface-catalogue-checks-the-templates-and-v2s-tree-2-november-2026) |
| Egress grants an instance declares (`edit --set egress=`, rendered to the proxy) | done | [record](current-state.md#an-instances-egress-grants-are-its-records-8-november-2026) |

## Remaining Python

The platform's own Python implementation and the top-level Python test suite are retired. No replacement
suite was added: those manifest, workflow, image, script and make-safety regression checks are lost,
including the ClickStack chart-secret render check and automatic plan-only dry runs. `tests/fixtures/`
stays for Go, collector and live checks; Go, backup and codegraph tests stay intact.

| Python surface | What replaces it | State |
| --- | --- | --- |
| Copier in the console image and template checks | Kept upstream Python tool | Pinned alone in `pyproject.toml`/`uv.lock`; Jinja2 and PyYAML remain transitive dependencies, not platform runtime libraries. No Starlette, uvicorn, httpx, check or backup group remains |
| Codegraph and its own tests (`devtools/codegraph/`) | Kept read-only, standard-library development aid | Separate from the platform and console image; `check-codegraph` and Ruff remain in `make check` and CI |
| Go chart tests' YAML reader (`python3` importing `yaml`) | Kept upstream test-only dependency | Validate and Deploy v2 explicitly install `PyYAML==6.0.3` with Python 3.12, outside the Copier-only runtime groups |
| Host connectivity probe | Dropped, no replacement | Source, systemd templates, make targets and installer support removed; installed units removed and no timer scheduled ([record](current-state.md#python-tests-and-the-host-probe-retired-offline-13-november-2026)) |

## Remaining work

Ordered; each item is small enough to land and verify alone. The order follows what a real app needs and
what Python each slice deletes, not feature parity: v2 changes eight things after creation (`replicas`,
`memoryMiB`, `exposure`, `calls`, `allows`, `subscriptions`, `egress`, `team`), and the deferred list is capability,
not migration.

**A. The record.** One open item, and it changes no behaviour: **Renovate's app-template bumps take a shorter
hand step** — one Renovate PR bumps the profile and the three platform releases together, and the v2 tests read the
version from the profile, but the render and the goldens (compiled, profile identity, `chart-prod.json`) are still a
person's on the PR's branch ([record](current-state.md#one-renovate-pr-bumps-app-template-everywhere-11-november-2026),
[steps](operations.md#chart-updates)). Open: a CI job that finishes the branch, if bumps become frequent.

**B. The rest of the road.** What still runs Python, in the order it leaves; each is its own slice. Each surface is
shrunk, retired or ported (the rule is under [durable decisions](#durable-decisions)):

1. **Kept checks** (done 13 November 2026). `check-secrets`, `platform-certs-*`, `destroy-data`,
   `live-test-team-scenario` and `live-test-lifecycle` were ported with the same behaviour, and
   `live-test-app-template` was rebuilt over instances v2 renders, without its two Secret checks
   (§[27](iteration-2-plan.md#27-check-secrets-in-go-approved-13-november-2026),
   [28](iteration-2-plan.md#28-platform-certs-in-go-approved-13-november-2026),
   [29](iteration-2-plan.md#29-destroy-data-in-go-approved-13-november-2026),
   [30](iteration-2-plan.md#30-live-test-team-scenario-in-go-approved-13-november-2026),
   [31](iteration-2-plan.md#31-live-test-lifecycle-in-go-approved-13-november-2026),
   [33](iteration-2-plan.md#33-live-test-app-template-in-go-over-instances-v2-renders-approved-13-november-2026)).
2. **The catalogue** (done 13 November 2026). Each target is one `##` line at the top of the Makefile and
   `make help` prints them with `awk`; `commands.py`, its dispatch, `report.py` and `kube.py` are deleted
   ([§34](iteration-2-plan.md#34-the-command-catalogue-leaves-python-make-help-over-the-makefiles-own-lines-approved-13-november-2026)).
3. **The last of `tools/`** (done 13 November 2026). The ClickStack render test pulls its own chart, so
   `charts.py`, `run.py`, `platform.py` and `yamlio.py` are deleted; Validate's records-only rule is
   `.github/records-only.sh` over v2's inputs, and the app-only fast path, which matched only v1's layout, is gone;
   v2's coexistence guard no longer expects `tools` among the console image's inputs
   ([§35](iteration-2-plan.md#35-the-last-of-tools-the-clickstack-render-test-pulls-its-own-chart-and-validates-classifier-is-bash-approved-13-november-2026)).
4. **Approved cleanup delivered** (13 November 2026). Top-level `tests/*.py`, `make test`, the host
   probe and its installation support are dropped, not ported. Copier is the only runtime Python
   dependency group; Go chart tests retain explicitly pinned test-only PyYAML. CI passed and the
   deployed console/notify image contains Copier and none of the retired packages; all 11 instances
   are healthy. The operator removed the installed probe units; both are not found and no timer is
   scheduled. Cleanup is complete. Genuine signed-in New app creation on this image remains unexercised ([record](current-state.md#python-tests-and-the-host-probe-retired-offline-13-november-2026)).

Copier in the console image stays Python: it renders a new app's repository
([§23](iteration-2-plan.md#23-the-console-server-in-go-the-pages-the-jobs-and-the-consoles-last-python-proposed-9-november-2026)).

## Deferred features

The one list of features v2 does not model. A row leaves this table when its capability lands, or when the
operator decides against it.

| Feature | v2 today | Reopen when |
| --- | --- | --- |
| An instance that must not reach outside | Every instance that needs one gets a proxy port; nothing withholds one | A kind that must not egress |
| The egress namespace's own egress | Not restricted (the rendered policy states Ingress only) | Default-deny for the platform's own namespaces |
| Routes: `public`, a host override | `authenticated-web` and `private` only; no host is stored; status reads the expected route and certificates by name | An instance needs a public route or an unusual host |
| App kinds: `job` and a command override | Refused (`unsupported`); `worker` is modelled, and no app type is a job | An instance needs a schedule or a command override |
| The kind or type of an existing app | Fixed by the image at creation; `deploy` refuses a changed `runtime.kind` and says "create a new app for another kind" | An instance must change kind without a new app |
| A connection at creation | `create` takes the team and the exposure; calls and subscriptions are `edit`s, which the console's New app runs in the same pull request, and `allows` is always an edit afterwards | One command must create a connected app, or a new app must allow callers at creation |
| A team rename relabelling its apps | `edit --set team=` (the app page's **Change team**) moves one app; `swhurl-v2 team --action rename` and `remove` refuse while a v2 record names the team or allows it | A team must be renamed across its apps in one change |
| Secrets (`envFrom`, Reloader) and bindings | Non-empty `bindings` and `secrets` are refused | An app declares one; `live-test-app-template` gains back its two Secret checks with it |
| Alerts | Not generated; `edit --set alerts=` is refused by name | An app needs an alert |
| A per-instance memory or CPU choice | CPU from the profile; one memory value for request and limit | An app needs more |
| Quota reserve for transient platform pods | Rendered workloads, surge/init and backup are counted; no profile reserve for HTTP-01 solver or restore pods. The legacy fixture reserve checker is retired | An explicit v2 reserve contract is approved |
| Image automation: auto-merge and a production path | Staging follows its images; production follows a promotion | An unattended production update is wanted |
| A first production (creating it) | `promote` refuses an environment with no record | A first promotion must not need a person |
| Restore breadth | One live restore over the newest archive, and one that changes content; another instance's archive, a point in time and the failure paths are unexercised, and no live test repeats a restore | A restore beyond the newest archive over itself |
| Restoring a MongoDB | Nothing restores or test-restores ClickStack's MongoDB: its nightly archive is kept for 90 days, `swhurl-v2 restore` is SQLite only, and the Python restore test is deleted | A MongoDB holds data nothing rebuilds |
| The tag an instance was selected with | Not recorded; `verify-platform` resolves the tag through the index | A record-shape change carries it |
| Complete release, source and build verification | Claims are stated, not verified; a migrated release's artifact is checked | Before claiming verified release semantics |
| Observation breadth | The unit, release, workload, pods (readiness and failing containers), the expected Ingress and Certificates by name, and the backup; no foreign ownership, no other kind | A consumer needs them |
| A YAML kubeconfig | Kubeconfig JSON, or an explicit `status --in-cluster` using the pod's mounted ServiceAccount; no YAML | A consumer needs YAML |
| Live ownership of foreign objects | The bridge reads the repository's own claims; another owner's objects are not read | A migration needs the wider inventory |
| Console: repository creation, seed images, historical Activity | None | Later |
| Feature-flag state in the records | Overrides only: the console writes one to NATS with an audit line, and no record holds a flag's state or renders it | A record must own a flag's state |

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
- Delivery uses the operator's credentials from the host; status uses either an explicit kubeconfig or the
  pod's mounted ServiceAccount, without fallback or a new Secret (3 November 2026).
- The declaration is label version 2: the image states its whole declaration, version 1 is refused, and a
  digest determines a release (1 November 2026).
- `create` writes records only; the whole-tree render allocates the port and writes the files
  (1 November 2026).
- Each backup location is stated once, where its job reads it (MongoDB's in `platform/backup/mongodb.yaml`,
  the apps' in the profile), and no environment variable overrides it (3 November 2026).
- The console leaves by strangling: one `swhurl-v2` subcommand per slice, called through `console/v2.py` and
  deleting that Python; its server and templates go last (3 November 2026). That last part is two slices, the
  feature-flag card and its NATS key-value adapter first, so the server adds no external effect (8 November 2026).
- The console's server keeps the markup, the URLs and its two auth rules, computes every page's facts in Go and
  carries no trusted HTML in page data; Copier stays as one pinned subprocess for a new app's repository, the fourth
  after SOPS, Helm and Git (9 November 2026).
- The binary is built, never committed: make targets build it into a throwaway folder, and a local build in
  `v2/` is ignored — so the change report reads the working tree as Git carries it, not as the disk holds it,
  and an artifact left there is not a change to v2 (3 November 2026; 8 November 2026).
- The team registry is a v2 record (`v2-records/teams.json`): a team has at most one channel and a channel one
  team, `swhurl-v2 team` writes it and its channel Secrets, and `render` refuses a record or grant naming a team it
  does not hold (4 November 2026).
- `helm template` is the second approved subprocess, beside SOPS: one chart-rendering adapter for app-policy
  checks. Explicit pinned `helm pull` into the check cache is separately approved; rendering stays
  local-only, with no cluster reads or install/upgrade (5 November 2026).
- Read-only `git` is the third: four fixed reads of one checkout for the workstream check, with no write,
  fetch or general command runner (5 November 2026).
- The only third-party v2 dependency is `go.yaml.in/yaml/v3` v3.0.6, confined to exactly `adapters/helm`, `adapters/appsource` and `adapters/manifests`;
  `make v2-deps` bootstraps with checksums and builds/checks stay offline (4 November 2026).
- The stack and name catalogues are v2's files (`v2/contract/stacks.json`, `names.json`), read by Go and shell;
  the console reads them from the pinned tree, not files copied into its image.
- A test earns a Go port only if it keeps catching regressions in code this repository owns. A one-time proof of
  k3s or a third-party component is retired, and its evidence stays in current-state (11 November 2026).
- The v1 Python model and the top-level Python regression suite are deleted, not ported. Go keeps tests of
  its own decisions; Copier, the read-only codegraph aid and PyYAML for Go chart tests remain Python exceptions.

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

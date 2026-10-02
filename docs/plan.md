# Swhurl Platform — implementation plan

27 September 2026 · Home cluster implementation plan · **paused 28 September 2026**

## 0. Where this paused and what is left

Work paused on 28 September 2026 after PR07b. Everything in the delivery table (section 3) is done except **PR06**, the remainder of **PR08a**, and the **final operator exercise**. Live evidence for each step is in `docs/current-state.md`. Before resuming: pull `main`, run `make check-repo`, `make test`, `make verify-platform` and `flux get kustomizations` (14 units, all Ready), and re-read that file's "Not exercised" notes.

**Remaining plan work**

1. **PR06 — GHCR publishing and Renovate** (section 4). The first app repository is live (`samclement/hello-ts`, public images, from the template; item 10). Chart update PRs are live ([operations](operations.md#chart-updates)); the first, ClickStack 1.1.2, was merged and verified on 28 September 2026. ClickStack 3.x was installed fresh rather than upgraded on 29 September 2026 ([current state](current-state.md)). The GHCR half starts with the console image (section 7, phase 3), published **public** from this repo; still to decide: which app repository goes first, and public or private for app images (private needs pull credentials per namespace). Image digest updates stay disabled in `renovate.json` until then: Renovate reads the `hello` image's `tag` but not its separate `digest` field, and would bump both environments at once. **Decided 30 September 2026 (item 10): Flux image automation deploys template apps to staging.** Earlier options considered: The console's route (its publish run commits the pin with its own `GITHUB_TOKEN`) cannot push across repositories; the options are Renovate digest PRs (section 4's plan: reviewable, no app repo writes here; needs the separate `digest` field handled), Flux image automation (one policy per app, updating staging only, one write key for this repo; the console could move to it too) or a cross-repo credential in each app repo. Production stays behind **Promote to prod**.
2. **PR08a remainder** (section 5): off-host copies (S3) and a daily timer are live since 29 September 2026 ([operations](operations.md#backups-and-recovery)). The k3s datastore is SQLite and reconstructible from Git. Left: the gate: restore on a separate machine from S3 using only the docs. Follow-ups: a write-only IAM user for backups instead of `sam`, and then possibly a Kubernetes CronJob with a published backup image (after PR06's GHCR half).
3. **Final operator exercise** (section 6), using only the docs.
4. ~~**Console**~~ done 29 September 2026 (section 7): a signed-in web console for apps and Flux units; changes go through PRs. Decided 29 September 2026. Phases 1 (`e180ff2`), 2 (`884f050`, `make console-dev`) 3 (`b63d2af`, public image on GHCR) and 4 (`79c07f3`, deployed read-only at `console.<BASE_DOMAIN>`; signed-in browser check passed after `edd4f44`) 5 (`bf63344`, reconcile/suspend/resume with audit lines; buttons checked by the operator), 6 (`9364c77`, new-app PRs; PR #8 merged, deployed and removed) and 7 (`0acb567` promote/scale/uninstall PRs, each merged and applied live; `af2b11d` `make console-image`) done 29 September 2026. Open: the operator confirms the token is limited to this repository. Reloader for `console` moves to phase 6, when it first has a Secret.
10. ~~**Simpler New app and automatic staging deploys**~~ done 30 September 2026: `app-new` presets and the console's preset form, the template repository `samclement/swhurl-app-template-typescript`, and Flux image automation that deploys template apps to staging on every push (production through a promote). First app: `hello-ts` ([current state](current-state.md#presets-template-and-automatic-staging-deploys-30-september-2026)).
9. **App scaffold** (decided 30 September 2026): new app *code* starts from a GitHub template repository per language, outside this repo; this repo keeps the cluster wiring (`make app-new` and [`contract.py`](../tools/swhurl/apps/contract.py), for example `--otlp`), so it gains no language dependencies and platform deploys stay fast. The manifest injects every cluster-specific value, so app code only follows conventions (OpenTelemetry SDK reading `OTEL_*`, a readiness path, port 8080, a non-root user, a publish workflow that prints the image digest). Next: turn `samclement/swhurl-platform-typescript-app-example` into that template, then build PR06's first app from it. Revisit with Copier (`copier update`) if existing apps should receive template changes.
8. ~~**Faster delivery**~~ done 29 September 2026: parallel checks and a faster CI (`4bd61a4`), `cluster-stack` without `wait` plus `swhurl flux-wait` (`9bce4a7`), `make flux-install` with a 5s dependency retry (`33fb42e`), the GitHub push webhook (`cfb5daf`) and the publish run deploying the console (`0503987`). A push reaches Flux in about 2s; a full `make flux-reconcile` takes about 17s; a console tooling change runs about 90s after the push ([current state](current-state.md)).
11. ~~**Console redesign**~~ done 1 October 2026 (review the same day; one commit per step, each checked live):
    1. ~~Structure~~ done 1 October 2026: navigation Overview · Apps · Platform · Activity with **+ New app**; Overview; Platform merging units and checks with a page per unit for its actions; Activity with open console PRs.
    2. ~~Apps~~ done 1 October 2026: one row per app with Staging and Prod columns and Promote; the app page with a verdict, main actions, a staging/prod switch, Details collapsed, Scale and Who can reach it as disclosures, Uninstall apart; app instances named `<app>/<env>` on every page.
    3. ~~Language and states~~ done 1 October 2026: one vocabulary (Healthy ✓, Updating ↻, Failing ✕, Suspended ‖) on every page; a dependency wait after a push is Updating, not a problem; purpose lines on every page; empty states and a more helpful error page.
    4. ~~Polish~~ done 1 October 2026: commit SHAs and digests shortened to 12 characters with the full value on hover; breadcrumbs on every page below the top level; "Updated 19:46 · ↻ Refresh" in the header.
12. **New app from a catalogue** (proposed 1 October 2026, widened 2 October 2026 to include creating the app's GitHub repository): plan in [section 8](#8-new-app-from-a-catalogue). Done so far: the `sqlite` capability (`--database sqlite`, 1 October 2026) and its nightly backups (`make backup-sqlite`, 2 October 2026). Decided 2 October 2026: Copier, a second token, Kotlin on Micronaut (JVM). Phases 1 (SQLite restore), 2 (`swhurl.yaml`, `app-new --from-repo`), 3 (Copier template, `make app-repo`), 4 (the console's New app creates the repository) and 5 (worker and SQLite features) and 6 (Kotlin stack) done 2 October 2026. Next: phase 7, template updates reaching existing apps.
13. ~~**Observability fixes**~~ done 2 October 2026 (agreed the same day, before section 8 phase 7; [current state](current-state.md#observability-fixes-2-october-2026)). A review found ten compromises in how telemetry works (logs read from stdout, auto-instrumentation, one shared node collector, best effort, alerts on deploy failures only). Fixing the three cheapest with the most value:
    1. **Link logs to traces:** the node collector reads each JSON log line's `trace_id` and `span_id` (top level, or under `mdc` as logback writes them) into the record, so ClickStack's `TraceId` column fills and a trace opens its logs. Today it is empty for every app.
    2. **Drop health-check traces:** spans from Kubernetes probes (user agent `kube-probe/…`) are dropped in the node collector; they were all 34,507 of `hello-ts`'s traces in a day. The Kotlin template's health check stops querying the database, so no child span is left without its parent.
    3. **Silence the cloud check in TypeScript apps:** the template (and `hello-ts`) limit the SDK's resource detectors to the local ones, removing the `MetadataLookupWarning` logged at every start.

    Left as known: log severity guessed from text (a `hello-ts` request log is labelled "trace" because it mentions `traceId`), attribute names that change with OpenTelemetry updates, the Java agent's start-up cost and missing `jvm.memory.*`/`jvm.thread.*` metrics, no backup of telemetry, no alerts from app behaviour (error rates, latency, a stuck worker), and a collector that trusts every pod.
14. **Console follow-ups** (agreed 2 October 2026, from the operator's notes), in this order:
    1. ~~**New app links**~~ done 2 October 2026 (`8e77604`): after a new-app job, its links led to "No such app instance" until the PR was merged and applied. The app page now says it is waiting for the pull request instead.
    2. ~~**Secret names**~~ done 2 October 2026: app Secret keys reach the container through `envFrom`, where the platform's own `env` silently wins. `contract.py`, `app-new`, `swhurl.yaml` and the form refuse `HOST_IP`, `DATABASE_PATH` and names starting `OTEL_`, `SWHURL_` (future platform variables) or `KUBERNETES_`; the app page lists "Provided by the platform" apart from "Your secrets" ([apps](apps.md#secrets)).
    3. ~~**New app page by scenario**~~ done 2 October 2026: first choose "Start a new app" (generate the repository) or "Deploy an existing image" (the platform conventions, the repository's `swhurl.yaml`, newly on the form, or Custom), each saying what it produces ([console](console.md#use-it)).
    4. ~~**Auto-merge console PRs**~~ done 2 October 2026 ([console](console.md#auto-merge)): the console labels a new staging app without a Secret, Scale, and Promote when ticked; a workflow merges a labelled `console/*` PR once Validate passes. Chosen over GitHub's own auto-merge, which needs a required check that would also block the direct pushes to `main` (the operator's, the console publish run's and Flux image automation's).
    5. **Test strategy review:** template CI renders every feature combination (2 stacks × 2 kinds × 2 databases today); propose pairwise combinations, a platform-side contract test rendering each template's `swhurl.yaml` through `app-new` and the policy, and build caching, before more features or stacks.
    6. **ClickStack dashboards per app** (decision brief first): a dashboard per `kind` from `swhurl.yaml`, definitions in Git, created through HyperDX's API by an idempotent sync; where the sync runs is the decision.
5. ~~Documentation restructure~~ done 28 September 2026: task-based pages in `docs/` with one canonical page per topic (map in `docs/contributing.md#documentation`), `AGENTS.md` trimmed. The `document-repo` skill used for it is committed at [`.claude/skills/document-repo/SKILL.md`](../.claude/skills/document-repo/SKILL.md).

6. ~~Operator tooling in the right language~~ done 28 September 2026: move logic (parsing, safety decisions, Secret handling, polling, live-test assertions) from bash into a tested Python package; keep short glue, streaming host scripts and systemd units as linted bash. The `make` interface does not change. The rule for choosing is in [contributing](contributing.md#operator-tooling); the finished sub-plan was removed and is in Git history (`97faeeb:Swhurl-platform-tooling-plan.md`).

7. ~~Cleanup~~ done 28 September 2026 (steps 1 to 4): the repository review below, recorded 28 September 2026. Item numbers follow the review.

**Cleanup plan**

A review of boundaries, technology choices, bloat and responsibilities, made during tooling phase 3 and checked against the code after the tooling work finished. Work top to bottom: tooling-only items first, because tests cover them and nothing deployed changes; then the decisions that change what runs; then all renames and moves together in one planned Flux migration.

Done since the review: #7 (the runner no longer checks for a test-only attribute) and #10 (the phase 3 bash scripts are deleted).

*Step 1: tooling only* — **done** 28 September 2026 (commits `6f406b6`, `ebfa7a6` and the env-drift commit; `db8b68d` alone left `main` broken for one CI run):

- #2 one contract module, [`tools/swhurl/apps/contract.py`](../tools/swhurl/apps/contract.py); `app-new` checks its own output against the policy.
- #3 [`tools/swhurl/platform.py`](../tools/swhurl/platform.py) holds shared paths, names and queries; `check-config` derives required Secrets from each unit's `decryption`.
- #6 `check-repo` and the app policy take an injected runner; `check-repo` reports every failure before exiting.
- #8 fake-executable tests cover only the `make` interface.
- #9 `make check-apps` fails when an app's environments differ beyond namespace, hosts, image tag/digest, replicas, resources and issuer (`env-drift`; exceptions need a reason).
- #13 `help` generated from `## ` comments; `SKIP_VERIFY=1`; defaults in Python; `DYNAMIC_DNS_RECORDS` in `host/dns.env`; `config.env` deleted.
- #19 `swhurl/apps/{contract,new,ops,policy}.py`, `retention.py`; tests split by subject; no `sys.path` edits in tests.
- #4 (checks by service) deliberately skipped: `verify.py` is one readable file at the current size. Revisit if it passes about 400 lines or a second cluster needs different checks.

*Step 2: stale records* — **done** 28 September 2026:

- #11 the local `todo.md` was reviewed: its open items are done, superseded (Go by the Python tooling; "all options in one configuration" by #1) or moved below (certificate reuse when rebuilding); the file was then deleted. The `document-repo` skill is committed.
- `docs/current-state.md` now starts with the current cluster facts, folds the first inventory into the P0 evidence, and drops the obsolete finding classification; the PR05 section records the signed-in check over HTTPS.
- This plan keeps only live material: section 0, goal, design decisions, the delivery summary, and the open PR06, PR08a and final-exercise sections. Completed PR sections, the 27 September baseline and the tooling sub-plan are in Git history at `97faeeb`.

*Step 3: decisions that change what runs* — **done** 28 September 2026:

- #1 **done** 28 September 2026 (option A): `BASE_DOMAIN` in `platform-settings` is the one source of platform hostnames, the cookie domain and the redirect allowlist; `OAUTH_HOST` is gone and `swhurl` reads the same value for the app policy. `make test` fails on a literal platform hostname. The ACME account email and approved sign-in addresses stay literal on purpose. What `CERT_ISSUER` switches is in `docs/services.md#settings`.
- #12 **done** 28 September 2026: MinIO is removed (it held no buckets). Commit `62f0e7c` emptied its unit so Flux uninstalled the release and deleted its volume; the next commit removed the unit, the `minio` HelmRepository, the `storage` namespace and the remaining references.
- #5 **done** with #12: `docs/architecture.md` defines foundation as cluster primitives with no user-facing endpoint and shared services as services apps use or people visit. Nothing needed to move.

*Step 4: names and layout* — **done** 28 September 2026. Stages, each validated, pushed and verified live:

1. **Done:** #18 verbs: `check-*` offline (`check` runs everything CI runs), `test` for unit tests, `verify-*` live, `live-test-*` throwaway cluster tests, `backup-mongodb`; Python command names match. #20: this plan is `docs/plan.md`, the evidence file `docs/current-state.md`, and the PyYAML pin is the `check` dependency group in `pyproject.toml`.
2. **Done:** canary `homelab-reloader` became `platform-reloader` at `platform/reloader` (`9d9c7bb` suspend, `81b6ccf` cutover). Suspending it through Git blocked `homelab-flux-stack` for its 20-minute health-check timeout: the suspend commit is a new revision, the unit was suspended while still waiting on a dependency for it, and its status froze as not Ready. The next stage made the old units `Orphan` instead.
3. **Done:** the other non-root units (`44ddd85` made the two app units `Orphan`; the cutover commit followed), with #16 (one directory per unit under `infra/`, `platform/`, `apps/`; no `base/` levels) and #17 (`<kind>.yaml`, `-<name>` only to tell two of a kind apart): `infra-base`, `infra-cert-manager`, `infra-issuers`, `infra-traefik`, `platform-oauth2-proxy`, `platform-clickstack`, `platform-otel`, `app-<app>-<env>`.
4. **Done:** the roots `homelab-flux-sources` and `homelab-flux-stack` became `cluster-sources` and `cluster-stack` (new units applied with `make flux-bootstrap`, then the old `Orphan` roots deleted).

#14, #15: a unit is named `<area>-<component>` after its directory; namespaces, HelmReleases, Secrets and hostnames keep their names, so no workload changes. Each rename is a handover: suspend the old unit, then replace it (all units are `Orphan`), after proving offline that the new unit renders byte-identical objects, and live that Helm revisions and pod and volume UIDs are unchanged.

*Follow-up simplification* — **done** 28 September 2026: Mermaid diagrams replaced D2 (no render step), the component READMEs were folded into `docs/services.md` and `docs/operations.md`, the ADRs retired; `make reconcile UNIT=<name>` replaced the four OTel refresh targets, and the old-name aliases, `verify`, `teardown` and `reinstall` were removed.

*Leave alone* (judged sound by the review): `Runner` and `Report`; the `Orphan`/`MirrorPrune` deletion split and its tests; the explicit repetition in Flux unit definitions (guarded by `make test`); keeping the host scripts as bash; explicit per-environment app copies, once #9 exists.

**Known issues, deliberately not fixed yet**

- OTel collector 0.161.0 warns that the `hostmetrics`, `kubeletstats` and `k8sobjects` component names and the inline `service.telemetry.resource` map are deprecated. `platform/otel/helmrelease-daemonset.yaml` uses `kubeletstats` (its receiver settings and pipeline) and the chart's presets generate all three names, so renaming only ours could duplicate receivers. Rename each together with a chart version whose preset uses the new name (chart issue #2183, one preset at a time); `make check-otel` (in CI) lists the deprecated names on every chart update and fails if a rename leaves a pipeline naming a missing component. Our values already use the names the chart otherwise rewrites (`k8s_attributes`, `otlp_http`, `file_log`), so dropping those rewrites (chart PR #2429 for `k8sattributes`) cannot break them. The telemetry format warning goes when chart PR #2343 ships.
- Staging and production `hello` differ only in namespace and host (same digest, issuer and sign-in). `make check-apps` now fails if they drift further. No per-instance quotas, NetworkPolicies or RBAC.
- Everything under `homelab.swhurl.com` shares the sign-in cookie. Public or untrusted apps must use another parent domain; none exist yet.

**Not yet exercised live** (each is correct by construction or test, but unproven on the cluster)

- A fresh bootstrap on a real new host: DNS, router forwarding and Let's Encrypt (the in-cluster sequence was rehearsed on k3d; see `docs/current-state.md`).
- A real credential rotation through Reloader (only dummy-key and disposable rotations were tested), and Reloader restarting a generated app.
- A `public` app instance on a domain outside `homelab.swhurl.com`.
- A rejected sign-in reaching oauth2-proxy's email list (the test account was refused by Google first).

**Open question from the old todo list:** how to avoid Let's Encrypt rate limits when rebuilding the cluster (for example backing up and restoring certificate Secrets, or using `letsencrypt-staging` while iterating). Relevant to the fresh-bootstrap test above.

**Extension points kept out of scope:** a second cluster (EC2), tailnet-only private routes, DNS-01 and wildcard certificates, directory renaming, and Hermes.

## 1. Goal and scope

Make an app instance a small, reviewable definition: image digest, resources, probes, exposure, configuration, secrets, and persistence. A generator supplies the namespace, Flux Kustomization, HelmRelease, and optional encrypted Secret. Git review and Flux remain the deployment path.

Keep k3s, Flux, packaged Traefik, cert-manager, and SOPS/age. Fix current safety, identity, and telemetry faults first; then separate reconciliation, prove recovery, and build the app workflow. Work on `clusters/home/` only. Preserve clean capability boundaries for a possible future `clusters/aws/`, but do not build the EC2 cluster or move host automation now. Hermes and Mac model access are a separate follow-on project.

### Completion criteria

- Onboard a second app from an existing image in under 15 minutes, excluding DNS and certificate delays, without hand-written Deployment, Service, or Ingress.
- Each app instance has its own namespace and Flux unit; staging and production reconcile independently.
- Image and chart updates are reviewable and pinned. Promote and roll back the same image digest.
- A ClickStack failure does not block an unrelated app update.
- Exposure modes enforce their route and cookie boundaries; only approved identities can sign in.
- Secret rotation restarts only the referencing workload without printing the value.
- Suspend, uninstall, and data destruction have distinct effects. Restore one stateful workload and the age key from an independent backup.
- One documented command shows desired revision, running image, readiness, address, and failure reason.

## 2. Design decisions

| Concern | Home decision | Future extension |
| --- | --- | --- |
| Cluster | Keep `clusters/home/` the sole entrypoint; choose independent capability units. | A later `clusters/aws/` may select shared bases and use separate settings and age key. |
| Host | Keep host/, dynamic DNS, NodePorts, and router mapping home-specific. | Design EC2 bootstrap and Route53 ownership when that move is committed. |
| App chart | Use pinned bjw-s app-template, initially version 5.2.1 after rendering fixtures. Share an HTTP HelmRepository; pin version per HelmRelease. | Write a custom chart only if the values and policy model prove inadequate. |
| App isolation | One namespace and one Flux unit per instance, with SOPS decryption on that unit when needed. | Reuse the generator/policy for another cluster later. |
| Browser exposure | private has no Ingress; authenticated-web is for trusted apps under the shared-cookie domain; public uses a domain outside that scope. | Private browser routes need a proven tailnet or internal entrypoint. A public IP allowlist is not the default private boundary. |
| TLS | Keep working HTTP-01 for public home routes. | Consider Route53 DNS-01 for private-host certificates. Wildcard TLS is optional and has a larger key blast radius. |
| Updates | Pilot Renovate for chart and digest PRs; retain manual digest PRs. | Avoid custom cross-repository PR machinery unless needed. |
| Data | Retain irreplaceable state on uninstall; explicit separate destruction. | Prove a Retain StorageClass and restore before stateful migration. |

A public app on public.homelab.swhurl.com is still in the .homelab.swhurl.com cookie scope. Use a sibling domain such as public.swhurl.com or a separate host-only proxy. HttpOnly does not stop the destination server receiving the cookie. Browser sign-in establishes identity; each app owns authorization. Machine APIs use token auth, not browser redirects.

How far hostnames and the cookie domain should be configurable is cleanup item #1 in section 0.

## 3. Delivery order and gates

Prove restore before deletion, ownership handover, or stateful migration. Evidence for each completed row is in `docs/current-state.md`.

| Order | Deliverable | Live gate |
| --- | --- | --- |
| PR01 | Inventory, validator, CI, documentation | Complete at 2bae8d0 |
| P0a | Guard destructive teardown/reinstall and correct operator docs | Complete in Git |
| P0b | Back up age key off-host and test recovery | Complete: encrypted USB copy decrypted all three Secrets |
| P0c | Fix verifier and double-encoded ingestion Secret; restart and verify collectors | Complete: live on 27 Sep 2026 after collector restart; no 401s, fresh logs/metrics in ClickHouse, verifier passes |
| P0d | Restrict sign-in to approved identities; test accepted/rejected accounts | Complete: live (`sam@swhurl.com` only); approved sign-in verified and non-approved account refused by operator test, 27 Sep 2026 |
| PR08a | Independent backup and tested restore of one stateful workload | Partial: data classified; encrypted backup and disposable restore proven 27 Sep 2026; live restore, S3 copies and daily timer 29 Sep 2026; restore on a separate machine pending |
| PR02a | Retention defaults: telemetry 30d, ClickHouse system logs 7d, backup pruning 7 daily + 4 weekly, MongoDB PV Retain + PVC keep, `local-path-retain` class | Complete 27 Sep 2026; backups stay manual until an off-host destination is chosen |
| PR02b | Lifecycle commands (suspend/resume/destroy-data), `Orphan` shared units, prune protection | Complete 27 Sep 2026; proven by `make live-test-lifecycle` |
| PR03 | Capability split and cert-manager/issuer ordering | Complete 27 Sep 2026: 10 units, 22 resources handed over with no recreation |
| PR07a | Narrow Secret rollout controller pilot | Complete 28 Sep 2026: scoped opt-in Reloader for oauth2-proxy and OTel; manual refresh kept as fallback |
| PR04 | App-template contract, generator, rendered policy | Complete 28 Sep 2026: generator, `make check-apps`, fixtures proven live by `make live-test-app-template` |
| PR05 | Split and migrate example staging/production; operator commands | Complete 28 Sep 2026: `hello-staging`/`hello-prod` on app-template; routes cut over with seconds of default-cert gap |
| PR06 | GHCR publishing and Renovate pilot | PR04, app repository access |
| PR07b | App Secret conventions and shared settings | Complete 28 Sep 2026: conventions documented, `make check-secrets`, `BASE_DOMAIN` removed, duplicate refresh target retired |
| Final | Operator exercise and documentation | All core deliverables |

## 4. PR06 — registry and reviewable updates

Use GHCR for first-party images. Each app repo tests, builds, publishes a source-revision-tagged image, and captures its digest. Use package-write credentials only in publishing. Private packages need read-only pull credentials in consuming namespaces; test an uncached pull. Promote one digest between staging and production.

Pilot Renovate for app-template versions and GHCR image digests. Configure Flux manager file patterns for this repo's clusters/ and apps/ paths; the default does not cover them. Verify source resolution and that each digest PR touches the intended instance; keep chart bumps separate. Configure private registry auth if needed. Hosted Renovate still needs its own GitHub integration/token, but avoids custom cross-repo app-to-platform PR code. Manual digest PRs remain supported. CI renders updates; merge triggers Flux.

Build x86-64 for home; add ARM64 when a consumer needs it. Revisit before any Graviton move. Retain deployed digests for rollback.

## 5. PR08a — independent recovery gate

Classify data as reconstructible, expendable telemetry, or irreplaceable. Inspect datastore, local-path placement, reclaim, and existing host backups. Choose off-host destination and application-consistent method for each irreplaceable service. Record recovery point/time targets, retention, capacity, and required credentials.

The age key is backed up (P0b); integrate it into the full recovery sequence. Rebuild a clean test scope, restore an encrypted Secret and one persistent workload, and verify app behavior. Record dated evidence before stateful migration.

## 6. Final operator exercise

Using only current docs: onboard web and worker; publish/deploy, promote, and roll back a digest; diagnose bad image and probe; rotate a Secret without exposing it; deploy during ClickStack failure; suspend/resume; uninstall a disposable persistent app with retained state; restore workload and age key. 
After the core exercise, consider tailnet private browser access, DNS-01, wildcard certs, a second cluster, or directory renaming. Hermes remains a separate project requiring model-network design and stronger command-execution isolation than a namespace alone.

## 7. Console

A web console at `console.homelab.swhurl.com`, behind the shared sign-in, that shows app instances, Flux units and cluster health, runs reconcile and suspend/resume, and makes every other change by opening a PR against this repo. Flux still applies only what is merged.

**Decisions** (29 September 2026)

- **Code lives in this repo** (`tools/swhurl/console/`), so tooling changes and the console that uses them are tested together. The image is published from here.
- **Reads use the image's own code; writes use the clone's CLI.** Status, units and health import `swhurl` from the image and read the cluster (units come from the cluster, not Git). A write downloads `main` through GitHub's API, runs `python -m swhurl app-new …` and `check-apps` inside that tree, commits the changed files to a new `console/*` branch through the API and opens a PR, so every change is generated by the rules CI checks it with. The only cross-version interface is `app-new`'s flags.
- **A platform unit, not an app:** `platform-console` needs a service-account token and RBAC, which the app policy forbids. Read access excludes Secrets and `pods/exec`; the only cluster writes are the reconcile annotation and `spec.suspend` (RBAC grants `patch`; the code limits the fields and refuses `cluster-sources`, `cluster-stack` and `destroy-data`). A NetworkPolicy admits only Traefik, so the sign-in header cannot be forged from inside the cluster.
- **GitHub access: a fine-grained personal token** (this repo; Contents and Pull requests read/write), stored in SOPS, sent only in the API's `Authorization` header and redacted. Chosen over a GitHub App for simplicity; the security impact is low for a homelab. PRs appear as the operator, marked by the `console/` branch, a `[console]` title and a `Requested-by:` trailer. `verify-platform` warns 14 days before the token expires. `main` stays unprotected (the commit-to-`main` loop is unchanged); a test ensures the console creates only `console/*` branches.
- **Image: public on GHCR**, pinned by its `src-<hash of inputs>` tag and digest with `make console-image`. Renovate digest updates were dropped (29 September 2026): the image's tags never move, so Renovate would never propose one.
- `verify-platform` checks are labelled by what they need (`cluster`, `secret`, `exec`, `host`); the console runs only the `cluster` ones and links to HyperDX for host timer logs.

**Phases** (each ends in `make check`; phases 4 to 7 also reconcile, verify live and record evidence in `docs/current-state.md`)

1. **Tooling, offline:** `app status` split into gathering (`gather_status`) and printing; `verify-platform` checks labelled by need, selectable in code; `Runner.stream()` (line by line, redacted, stops the command when closed); `Report` keeps structured entries. CLI output unchanged.
2. **Read-only console, local:** apps, unit graph, cluster checks and `/healthz`; `make console-dev` on `127.0.0.1` with a fixed identity (refused on any other address).
3. **Image and GHCR publishing:** Dockerfile with pinned Python, `kubectl`, `flux`, `helm`, `sops` and `git`, labelled with the source commit; a publish workflow tagging by commit SHA.
4. **Deploy read-only:** `console` namespace in `infra-base`, `platform/console/` (read-only RBAC, NetworkPolicy, HelmRelease), the `platform-console` unit (after `infra-base` and `platform-oauth2-proxy`), Reloader namespace, `verify-platform` checks that the console is Ready and not built from older tooling than `main`. Live: redirect, signed-out 302, signed-in pages, forged header from a throwaway pod blocked, `kubectl auth can-i` denies Secrets, exec and patch.
5. **Flux operations:** patch RBAC; reconcile and suspend/resume as background jobs with streamed progress and one audit log line each (reaching ClickStack). Live: on `hello-staging`.
6. **Changes through Git:** the token Secret (operator creates the token), clone, commit, push and PR; the new-app wizard. Live: a throwaway app PR through CI, merged only after asking, then an uninstall PR.
7. **Promote, scale, uninstall PRs** (`make app-promote`, `app-scale`, `app-remove`, also offered by the console) and `make console-image` in place of Renovate digest updates.

**Out of scope:** `destroy-data`, Secret values, credential rotation, `flux-system` root units, host timers, triggering live tests, users other than the operator.

## 8. New app from a catalogue

Agreed 2 October 2026. Replaces the five sub-steps of item 12 in section 0.

**Goal.** On the console's **New app** page you choose a language and framework, tick the features you want and give a name. The console then creates the app's GitHub repository with working code, waits for the first image and opens the platform pull request. Once you merge it, the app runs in staging, and every push to its `main` deploys it there. Production stays behind **Promote to prod**.

Example: *TypeScript · features: SQLite* with the name `notes` produces the repository `samclement/notes`, the image `ghcr.io/samclement/notes:1-<sha>` and a PR `[console] new app notes/staging`, which serves `staging-notes.homelab.swhurl.com` once merged.

```mermaid
flowchart LR
  form["New app form<br/>stack, features, name"] --> job["console job"]
  job -->|"render template<br/>(Copier)"| repo["new GitHub repo<br/>code + swhurl.yaml"]
  repo -->|"CI: checks, build"| ghcr["GHCR image<br/>1-&lt;sha&gt;@digest"]
  job -->|"waits for the image,<br/>reads swhurl.yaml"| pr["platform PR<br/>app-new --from-repo"]
  pr -->|"merge"| flux["Flux: staging<br/>+ image automation"]
```

**How it fits together.**

- **A stack** is one template repository per language and framework, for example `swhurl-app-template-typescript` (exists) or a Micronaut one. It holds the conventions every app already follows (port 8080, `/healthz`, UID 65532, writes only to `/tmp`, the OpenTelemetry SDK, the shared publish workflow).
- **A feature** is optional code inside a stack: SQLite access with migrations, or a worker loop instead of an HTTP server, the shared libraries. The template renders it only when it is ticked. A feature that the cluster also has to provide (a volume, a Secret, a route) declares that in `swhurl.yaml`.
- **`swhurl.yaml`** is written into the app repository by the template. It is the app's contract with the platform: `kind`, `database`, `secrets`, `telemetry`, resource defaults and `stack`. `app-new --from-repo` reads it, so a stack's needs (a JVM wants more than 128Mi) live in the stack, not in the console.
- **A capability** is the platform side of a feature, as item 12 proposed: one module here owns its manifests, policy rules, backup hook, live test and docs. `sqlite` is the first one and is done.

**Decisions** (2 October 2026):

1. **Copier renders a stack's features.** There is one template repository per stack, with features as Copier questions, and each app commits `.copier-answers.yml` so that `copier update` can bring later template changes to existing apps (the open point from item 9). Rejected:
   - one template repository per combination: the number of repositories multiplies;
   - **Use this template** followed by our own feature patches: a second template system to maintain;
   - a generator in `tools/swhurl`: language-specific code in this repo, which item 9 ruled out.

   Cost: the console image gains `copier` and `git`, and every template's CI renders each feature combination and runs its checks.
2. **A second token creates repositories.** It is fine-grained and covers all repositories: Administration, Contents and Workflows read/write, plus Actions read so the console can follow the first run. It is held only by the console, separate from today's token, which stays limited to this repository. Such a token could delete any repository, so the console's code may only create a new repository, push its first commit to a repository it just created, and read runs (tested, like the `console/*` branch rule). `verify-platform` warns before it expires, as it does for the first token. Rejected:
   - a GitHub organisation with a GitHub App: images, Renovate and the template would all move;
   - creating the repository by hand: New app would not be complete.
3. **The second stack is Kotlin on Micronaut, running on the JVM, with a small image:** a `jlink` runtime holding only the modules the app needs, on a distroless base, with the image size reported in CI. No GraalVM native image.

**Deferred:** sign-in roles in apps (authn/authz) and the shared libraries feature. Traefik already passes `X-Auth-Request-Email` to signed-in apps (`platform/oauth2-proxy/middleware.yaml`), but an app can trust it only once its namespace has a NetworkPolicy admitting just Traefik.

**Phases.** Each phase ends in `make check`. Phases that change the cluster or GitHub also reconcile, verify live and record evidence in `docs/current-state.md`. Throwaway repositories are deleted only after asking.

1. ~~**Finish the SQLite capability**~~ done 2 October 2026: `make restore-sqlite`, proven by `make live-test-restore-sqlite` ([current state](current-state.md#app-sqlite-restore-2-october-2026)); backups now record the plaintext size and SHA-256.
2. ~~**The `swhurl.yaml` contract**~~ done 2 October 2026 ([apps](apps.md#swhurlyaml)): the schema in `contract.py` (version 1; each of today's options is a field, and the presets are the template's `swhurl.yaml`), `app-new --from-repo OWNER/REPO[@REF]` and `--manifest PATH`; the template and `hello-ts` carry the file, and `hello-ts` regenerates byte-identical from it. Splitting each capability into its own module waits for a capability that needs more than generator defaults.
3. ~~**The TypeScript stack on Copier, with no features yet**~~ done 2 October 2026 ([apps](apps.md#start-a-new-app)). Convert the template to Copier. Its CI renders the template and runs the rendered app's checks, build and smoke test. Add `make app-repo NAME= STACK=` (Python, through `Runner`, using your own `gh` login): it renders the template, creates the public repository, pushes it, waits for the first publish run and prints the image with its digest, ready for `app-new --from-repo`. Live: a throwaway app from `make app-repo` reaches staging.
4. ~~**New app creates the repository**~~ done 2 October 2026 ([console](console.md#github-tokens)): the tab **New app and repository** (the default) runs one job: render with Copier (now in the console image, with `git`), create the repository and its first commit through the API with `APP_REPOS_TOKEN` (writes only to a repository created in the same job), wait for the first build, open the platform PR with `app-new --from-repo`. The preset tabs stay for images that already exist (production, or a retry after a failed first build).
5. ~~**Features in the TypeScript stack**~~ done 2 October 2026 ([apps](apps.md#start-a-new-app)): Copier questions `kind` (web, worker) and `database` (none, sqlite on `node:sqlite` with migrations at startup); the template's CI renders every combination and `app.yml`'s smoke test reads `swhurl.yaml`; the console and `make app-repo ANSWERS=` read the questions from the template's `copier.yml`, so a stack's features need no platform code. The preset tabs stay for images that already exist.
6. ~~**The Kotlin Micronaut stack**~~ done 2 October 2026 ([apps](apps.md#start-a-new-app)): `samclement/swhurl-app-template-kotlin` with the same questions; Java 25 in a `jlink` runtime on `distroless/cc` with the OpenTelemetry agent (about 150 MB); `-XX:TieredStopAtLevel=1` (the agent made start-up 50 s on half a CPU, 16 s with the flag); a new optional `startupSeconds` in `swhurl.yaml` (`app-new --startup-seconds`) writes a startup probe so liveness waits for a slow starter. Follow-ups found live: the agent's `jvm.memory.*` and `jvm.thread.*` metrics do not reach ClickStack (only `jvm.gc.duration`); the log pipeline does not set `TraceId` from a JSON log's trace id for any app; the first Kotlin build takes about 6.5 of the job's 10 minutes.
7. **Template updates.** Apps receive template changes as pull requests: Renovate's Copier manager, or a scheduled `copier update` workflow, decided by trying Renovate first. The shared publish workflow and base images stay pinned and bumped by Renovate (item 9).
8. **The app page shows the stack, its features and each capability's health**: for example the last SQLite backup, or "roles: 3 users".

**Operator actions:** create the second token before phase 4 (decision 2); in phases 3, 4 and 6, approve deletion of each throwaway repository; check the New app form signed in, in phase 4.

**In scope:**
- creating the app repository;
- stacks: TypeScript and Kotlin on Micronaut;
- features: worker and SQLite;
- `swhurl.yaml` and `app-new --from-repo`;
- template CI across feature combinations;
- updates to existing apps from their template;
- the SQLite restore.

**Out of scope** (pull back in if wanted):
- private repositories and images (PR06 decides public or private; everything stays public until then);
- databases other than SQLite;
- sign-in roles in apps and shared libraries (deferred, above); apps running their own OIDC login;
- deleting or archiving the repository on **Uninstall** (the platform side only; you delete the repository on GitHub);
- creating production from the form (production is still created once with `make app-new … --env prod`, then promoted);
- a `kind: App` operator;
- more than two stacks;
- users other than you.

## 9. References

- [Flux pruning and deletion](https://fluxcd.io/flux/components/kustomize/kustomizations/)
- [Flux HelmChart reconcile strategies](https://fluxcd.io/flux/components/source/helmcharts/)
- [bjw-s app-template values reference](https://bjw-s-labs.github.io/helm-charts/docs/app-template/reference/)
- [Renovate Flux manager](https://docs.renovatebot.com/modules/manager/flux/)
- [Stakater Reloader](https://github.com/stakater/Reloader/blob/master/README.md)
- [oauth2-proxy provider configuration](https://oauth2-proxy.github.io/oauth2-proxy/7.6.x/configuration/providers/)
- [Cookie Domain scope, RFC 6265](https://www.rfc-editor.org/rfc/rfc6265)
- [cert-manager Route53 DNS-01](https://cert-manager.io/docs/configuration/acme/dns01/route53/)

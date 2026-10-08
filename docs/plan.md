# Swhurl Platform — plan

Started 27 September 2026 · last tidied 5 October 2026. Older work is summarised with its decisions and commits; what was run live is in [current state](current-state.md), and the earlier full text of this file is in Git history (for example `d4566c9`). Recent work (sections 9, 11, 12 and 14) keeps its full contract.

## 0. Where this paused and what is left

The platform is live. Every deliverable in section 3 is done except PR06's remainder, PR08a's gate and the final operator exercise. Before resuming, pull `main` and run `make check-repo`, `make test` and `make verify-platform`, and re-read the "Not exercised" notes in `docs/current-state.md`.

### Open work

1. **PR06 — GHCR publishing and Renovate** (section 4). Chart update PRs and the console image are done. Left: public or private images for apps (everything is public meanwhile) and which app repository goes first.
2. **PR08a gate** (section 5; drill plan in [section 13](#13-recovery-drill-pr08a-gate-and-fresh-bootstrap--designed-3-october-2026-not-started)): restore on a separate machine from S3 using only the docs. Follow-ups: a write-only IAM user for backups instead of `sam`; possibly a Kubernetes CronJob with a published backup image once PR06's GHCR half is done.
3. **Final operator exercise** (section 6), using only the docs.
4. **Operator browser checks:** a real Google-signed-in session for (a) reviewing and submitting a promotion (automated UI proof used the local dev identity) and (b) the live job-output stream, including that Traefik and ForwardAuth do not buffer it ([section 12](#12-live-job-output-on-the-console-deployed-3-october-2026)). Also confirm the console's `GITHUB_TOKEN` is limited to this repository.
5. **Notification gaps** ([contract](services.md#notification-expectations)): live fixture uninstall/rollback and timed unhealthy/recovery delivery exercises; alerts for failed or stale backups, external availability, certificate expiry or renewal failure and disk pressure.
6. **Decision needed — ClickHouse CPU** (delivered item 17): merge write amplification sets the load, not data volume. The lever is `async_insert` or bigger batches in the ClickStack HelmRelease, which trades a few seconds of data on a crash. Unexplained: since HyperDX restarted at 21:44 on 2 October the histogram table merges every new part on its own (about +100 s of merge time an hour). Revert `008c5d5` and `6cdfe3f` (coarser metric intervals, no CPU gain) if the graphs bother you.
7. **App page stack panel** (section 8, phase 8): show the stack, its features and each capability's health (last SQLite backup, later roles).
8. **Optional cleanup** (deleting needs confirmation; repository and package deletion is yours): `samclement/swhurl-try-6` (repository, package, staging and prod instances, retained volumes; restoring those volumes is unexercised, though the same SQLite restore shape has live evidence); retiring `hello` or migrating `hello-ts`.
9. **AI incident review and fix PRs** ([section 14](#14-ai-incident-review-and-fix-prs--in-progress-stage-2a-planning-6-october-2026)). Done: Stage 0 approvals, offline Stage 1, and Stage 2a tasks 1 to 9; all eight-item decisions needed for 2a are taken. The reviewer runs hourly in **collect-only mode** since 8 October 2026 (task 10): sweeps, pre-filter and state are live and no model is called. Left, in order: **operator**: create the OpenAI project ($20 cap, training disabled), set its key with `sops platform/incident-review/secret.sops.yaml`, subscribe to the diagnoses topic, and sign off a real bundle from `make incident-review-bundle`; then a week of collect-only sweeps with the CPU comparison; then **task 11**: remove `INCIDENT_REVIEW_MODE` to enable analysis and exercise a provider outage and a stale job. Human review and merge remain required for anything the reviewer proposes.

### Delivered

Numbers are kept because other docs and code comments cite them. Evidence for each is in `docs/current-state.md`.

| # | Done | What, and where documented |
| --- | --- | --- |
| 20 | 3 Oct | Live job output over SSE ([section 12](#12-live-job-output-on-the-console-deployed-3-october-2026)) |
| 19 | 3 Oct | Notification refactors R1–R4 and the host heartbeat H1–H4 ([section 11](#11-notification-boundary-refactors-and-heartbeat-3-october-2026)) |
| 18 | 3 Oct | Predictable deployment and promotion ([section 9](#9-predictable-app-deployment-and-promotion)). Web promotion #34 and private SQLite worker promotions #35–36 passed; duplicate submissions reused PRs; failed gates wrote and merged nothing; both worker databases kept independent claims through an image update |
| 16 | 2 Oct | Automatic app dashboards (`d5c0f87`; [apps](apps.md#dashboards)) |
| 15 | 2 Oct | Structured logs across the cluster (`cf3df0c`, `498310b`; [formats](services.md#structured-logs)); existing streams only, original lines and schema preserved |
| — | 2 Oct | Console correctness (`b3dd939`) and app validation / YAML editing (`6bac215`): validation fails when tools fail; scale, expose and promote keep handwritten YAML ([boundaries](apps.md#operate-an-instance)) |
| 17 | 2 Oct | ClickHouse load and telemetry noise: log noise filtered (`1fa822d`, 26,352 → 8,780 lines/h); HyperDX self-traces sampled at 10% (`4074d6a`, 12,866 → 1,362 spans/h); metric intervals raised (`008c5d5`, `6cdfe3f`; 752,944 → 420,234 rows/h). ClickStack's own scrape of ClickHouse (half the rows) cannot change from Git: the chart's `customConfig` loses to the config HyperDX pushes over OpAMP (`cc8e497`, `e8140d8` reverted). Server CPU did not drop (0.21–0.22 cores); see open work 6 |
| 14 | 2 Oct | Console follow-ups: new-app links wait for the PR (`8e77604`); reserved Secret names (`HOST_IP`, `DATABASE_PATH`, `OTEL_*`, `SWHURL_*`, `KUBERNETES_*`, because `envFrom` loses to the platform's `env`); New app page by scenario; auto-merge of console PRs ([policy](console.md#auto-merge)); `make check-templates`; `make clickstack-dashboards` |
| 13 | 2 Oct | Observability fixes: logs carry `trace_id`/`span_id`, kube-probe spans dropped (34,507 of `hello-ts`'s daily traces), TypeScript resource detectors limited to local ones |
| 12 | 2–3 Oct | New app from a catalogue ([section 8](#8-new-app-from-a-catalogue)), phases 1–7 |
| 11 | 1 Oct | Console redesign: Overview · Apps · Platform · Activity; one state vocabulary (Healthy ✓, Updating ↻, Failing ✕, Suspended ‖); instances named `<app>/<env>`; hashes shortened to 12 characters with the full value on hover; breadcrumbs; "Updated … ↻ Refresh" |
| 10 | 30 Sep | `app-new` presets, the TypeScript template repository, Flux image automation deploying template apps to staging on every push (production through a promote); first app `hello-ts` |
| 9 | 30 Sep | App code starts from a GitHub template repository per language, outside this repo; this repo keeps the cluster wiring (`app-new`, `contract.py`). The manifest injects every cluster-specific value, so app code only follows conventions (OpenTelemetry SDK reading `OTEL_*`, a readiness path, port 8080, non-root user, a publish workflow printing the image digest). Copier was added later (item 12) |
| 8 | 29 Sep | Faster delivery: parallel checks and CI (`4bd61a4`), `cluster-stack` without `wait` plus `swhurl flux-wait` (`9bce4a7`), `flux-install` with a 5 s dependency retry (`33fb42e`), GitHub push webhook (`cfb5daf`), publish run deploying the console (`0503987`). A push reaches Flux in about 2 s, a full `make flux-reconcile` takes about 17 s, a console tooling change deploys about 90 s after the push |
| 7 | 28 Sep | Foundation cleanup in four steps (section 3) |
| 6 | 28 Sep | Operator logic moved from bash to the tested `tools/swhurl` package; short glue and host scripts stay bash ([rule](contributing.md#operator-tooling)) |
| 5 | 28 Sep | Documentation restructure: one canonical page per topic ([map](contributing.md#documentation)); the `document-repo` skill is in `.claude/skills/` |
| 4 | 29 Sep | Console, phases 1–7 ([section 7](#7-console)) |

### Known issues, deliberately not fixed

- **OTel deprecations:** collector 0.161.0 warns that `hostmetrics`, `kubeletstats`, `k8sobjects` and the inline `service.telemetry.resource` map are deprecated. The chart's presets generate all three names, so renaming only ours could duplicate receivers; rename each with a chart version whose preset uses the new name (chart issue #2183). `make check-otel` lists deprecated names on every chart update and fails if a rename leaves a pipeline naming a missing component. The telemetry format warning goes with chart PR #2343.
- **Kotlin telemetry:** the agent's `jvm.memory.*` and `jvm.thread.*` metrics do not reach ClickStack (only `jvm.gc.duration`), so those dashboard panels wait. Log severity is guessed from text (a request log mentioning `traceId` reads as "trace").
- **Not covered by observability:** no backup of telemetry, no alerts on app behaviour (error rates, latency, a stuck worker), and a collector that trusts every pod.
- **Staging and production `hello`** differ only in namespace and host; `make check-apps` fails if they drift further. No per-instance quotas, NetworkPolicies or RBAC.
- **Shared sign-in cookie:** everything under `homelab.swhurl.com` shares it, so public or untrusted apps must use another parent domain (none exist).

### Not yet exercised live

- A fresh bootstrap on a real new host (DNS, router forwarding, Let's Encrypt; the in-cluster sequence was rehearsed on k3d).
- A real credential rotation through Reloader (only dummy-key and disposable rotations ran), and Reloader restarting a generated app.
- A `public` app on a domain outside `homelab.swhurl.com`.
- A rejected sign-in reaching oauth2-proxy's email list (Google refused the test account first).
- Open question: avoiding Let's Encrypt rate limits when rebuilding (back up and restore certificate Secrets, or use `letsencrypt-staging` while iterating); relevant to the fresh bootstrap above.

**Kept out of scope:** a second cluster (EC2), tailnet-only private routes, DNS-01 and wildcard certificates, directory renaming, and Hermes (a separate project needing model-network design and stronger command isolation than a namespace).

## 1. Goal and scope

Make an app instance a small, reviewable definition: image digest, resources, probes, exposure, configuration, secrets and persistence. A generator supplies the namespace, Flux Kustomization, HelmRelease and optional encrypted Secret. Git review and Flux remain the deployment path.

Keep k3s, Flux, packaged Traefik, cert-manager and SOPS/age. Work on `clusters/home/` only, preserving capability boundaries for a possible `clusters/aws/` without building it.

### Completion criteria

- Onboard a second app from an existing image in under 15 minutes (excluding DNS and certificate delays) without hand-written Deployment, Service or Ingress.
- Each app instance has its own namespace and Flux unit; staging and production reconcile independently.
- Image and chart updates are reviewable and pinned. Promote and roll back the same image digest.
- A ClickStack failure does not block an unrelated app update.
- Exposure modes enforce their route and cookie boundaries; only approved identities sign in.
- Secret rotation restarts only the referencing workload without printing the value.
- Suspend, uninstall and data destruction have distinct effects. Restore one stateful workload and the age key from an independent backup.
- One documented command shows desired revision, running image, readiness, address and failure reason.

## 2. Design decisions

| Concern | Home decision | Future extension |
| --- | --- | --- |
| Cluster | `clusters/home/` is the sole entrypoint; independent capability units. | A later `clusters/aws/` may share bases with separate settings and age key. |
| Host | host/, dynamic DNS, NodePorts and router mapping stay home-specific. | Design EC2 bootstrap and Route 53 ownership when that move is committed. |
| App chart | Pinned bjw-s app-template (started at 5.2.1) from a shared HTTP HelmRepository; version pinned per HelmRelease. | A custom chart only if the values and policy model prove inadequate. |
| App isolation | One namespace and one Flux unit per instance, with SOPS decryption on that unit when needed. | Reuse the generator and policy for another cluster. |
| Browser exposure | `private` has no Ingress; `authenticated-web` is for trusted apps under the shared-cookie domain; `public` uses a domain outside that scope. | Private browser routes need a proven tailnet or internal entrypoint; a public IP allowlist is not the default private boundary. |
| TLS | HTTP-01 for public home routes. | Route 53 DNS-01 for private-host certificates; wildcard TLS has a larger key blast radius. |
| Updates | Renovate for chart PRs; Flux image automation for template apps in staging. | No custom cross-repository PR machinery. |
| Data | Retain irreplaceable state on uninstall; destruction is explicit and separate. | Prove a Retain StorageClass and restore before stateful migration. |

A public app on `public.homelab.swhurl.com` is still in the `.homelab.swhurl.com` cookie scope: use a sibling domain such as `public.swhurl.com`. HttpOnly does not stop the destination server receiving the cookie. Browser sign-in establishes identity; each app owns authorization. Machine APIs use token auth, not browser redirects.

## 3. Delivery order and gates

Rule kept: prove restore before deletion, ownership handover or stateful migration. Evidence for each row is in `docs/current-state.md`.

| Order | Deliverable | Outcome |
| --- | --- | --- |
| PR01 | Inventory, validator, CI, documentation | Complete (`2bae8d0`) |
| P0a | Guard destructive teardown/reinstall; correct operator docs | Complete |
| P0b | Age key backed up off-host | Complete: an encrypted USB copy decrypted all three Secrets |
| P0c | Fix verifier and the double-encoded ingestion Secret; restart collectors | Complete 27 Sep: no 401s, fresh logs and metrics in ClickHouse |
| P0d | Restrict sign-in to approved identities | Complete 27 Sep: `sam@swhurl.com` only; a non-approved account was refused |
| PR08a | Independent backup and tested restore | Partial: data classified; encrypted backup and disposable restore proven 27 Sep; live restore, S3 copies and daily timer 29 Sep; **restore on a separate machine pending** |
| PR02a | Retention: telemetry 30d, ClickHouse system logs 7d, backup pruning 7 daily + 4 weekly, MongoDB PV Retain + PVC keep, `local-path-retain` class | Complete 27 Sep |
| PR02b | Lifecycle commands (suspend/resume/destroy-data), `Orphan` shared units, prune protection | Complete 27 Sep; `make live-test-lifecycle` |
| PR03 | Capability split; cert-manager/issuer ordering | Complete 27 Sep: 10 units, 22 resources handed over with no recreation |
| PR07a | Scoped opt-in Reloader for oauth2-proxy and OTel | Complete 28 Sep; manual refresh kept as fallback |
| PR04 | App-template contract, generator, rendered policy | Complete 28 Sep: `make check-apps`, `make live-test-app-template` |
| PR05 | Split and migrate example staging/production | Complete 28 Sep: `hello-staging`/`hello-prod`; routes cut over with seconds of default-cert gap |
| PR07b | App Secret conventions and shared settings | Complete 28 Sep: `make check-secrets` |
| PR06 | GHCR publishing and Renovate | **Open** (section 4) |
| Final | Operator exercise | **Open** (section 6) |

### Foundation cleanup (28 September 2026)

A review of boundaries, technology choices and responsibilities, executed in four steps. Numbers follow the review and are cited by evidence in `current-state.md`.

- **Tooling only:** one contract module, `apps/contract.py` (#2); `platform.py` holds shared paths, names and queries, and `check-config` derives required Secrets from each unit's `decryption` (#3); `check-repo` and the app policy take an injected runner and report every failure (#6); fake-executable tests cover only the `make` interface (#8); `make check-apps` fails when environments differ beyond namespace, hosts, image, replicas, resources and issuer (#9); `help` is generated from `## ` comments, defaults live in Python, `config.env` is deleted (#13); `apps/{contract,new,ops,policy}.py` and `retention.py`, tests split by subject (#19). #4 (split `verify.py` by service) was skipped on purpose: revisit past about 400 lines or with a second cluster.
- **Stale records:** `todo.md` was reviewed and deleted; `current-state.md` starts with current cluster facts.
- **Decisions that change what runs:** `BASE_DOMAIN` in `platform-settings` is the one source of platform hostnames, cookie domain and redirect allowlist (#1; `make test` fails on a literal platform hostname; the ACME email and approved sign-in addresses stay literal on purpose). MinIO was removed (#12; it held no buckets): an emptied unit let Flux uninstall the release and delete its volume, then the unit and references went. Foundation means cluster primitives with no user-facing endpoint; shared services are what apps use or people visit (#5).
- **Names and layout:** verbs `check-*` (offline; `check` runs what CI runs), `test`, `verify-*` (live), `live-test-*` (throwaway cluster) and `backup-mongodb` (#18); this file is `docs/plan.md` and the evidence file `docs/current-state.md` (#20); units are `<area>-<component>` named after their directory, one directory per unit under `infra/`, `platform/`, `apps/`, files `<kind>.yaml` (#14–17): `infra-base`, `infra-cert-manager`, `infra-issuers`, `infra-traefik`, `platform-oauth2-proxy`, `platform-clickstack`, `platform-otel`, `app-<app>-<env>`, with roots `cluster-sources` and `cluster-stack`. Namespaces, HelmReleases, Secrets and hostnames kept their names, so no workload changed. Each rename was a handover: make the old unit `Orphan`, create the replacement, after proving offline that it renders byte-identical objects and live that Helm revisions and pod and volume UIDs are unchanged. **Lesson:** suspending a unit through Git blocked `homelab-flux-stack` for its 20-minute health-check timeout (the suspend commit is a new revision and the unit froze as not Ready), so later stages used `Orphan` instead.
- **Simplification:** Mermaid replaced D2 (no render step); component READMEs folded into `services.md` and `operations.md`; ADRs retired; `make reconcile UNIT=<name>` replaced the four OTel refresh targets; old aliases, `verify`, `teardown` and `reinstall` removed.
- **Judged sound, left alone:** `Runner` and `Report`; the `Orphan`/`MirrorPrune` deletion split and its tests; explicit repetition in Flux unit definitions (guarded by `make test`); host scripts as bash; explicit per-environment app copies.

## 4. PR06 — registry and reviewable updates

First-party images go to GHCR. Each app repo tests, builds, publishes a source-revision-tagged image and prints its digest; package-write credentials are used only when publishing. One digest is promoted between staging and production, and deployed digests are retained for rollback. Build x86-64 for home; add ARM64 when a consumer needs it.

**Decided:** chart updates come from Renovate (live; [operations](operations.md#chart-updates); the first, ClickStack 1.1.2, merged 28 September, and ClickStack 3.x was installed fresh on 29 September). The console image is public on GHCR and pinned by `make console-image` (its tags never move, so Renovate digest PRs were dropped). Template apps deploy to staging by Flux image automation (30 September): one policy per app, staging only, one write key for this repo. Chosen over Renovate digest PRs (Renovate cannot read the HelmRelease's separate `digest` field and would bump both environments at once, so digest updates stay disabled in `renovate.json`) and a cross-repository credential in each app repo. Production stays behind **Promote to production**.

**Still to decide:** public or private app images (private needs read-only pull credentials in consuming namespaces and an uncached-pull test), and which app repository goes first.

## 5. PR08a — independent recovery gate

Data is classified as reconstructible, expendable telemetry or irreplaceable. The k3s datastore is SQLite and reconstructible from Git; MongoDB and app SQLite databases are irreplaceable and backed up daily to S3 (since 29 September; [operations](operations.md#backups-and-recovery)). The age key backup (P0b) is part of the recovery sequence. **Left:** rebuild a clean scope on a separate machine, restore an encrypted Secret and one persistent workload from S3 using only the docs, verify app behaviour, and record dated evidence before any stateful migration.

## 6. Final operator exercise

Using only current docs: onboard a web app and a worker; publish, promote and roll back a digest; diagnose a bad image and probe; rotate a Secret without exposing it; deploy during a ClickStack failure; suspend and resume; uninstall a disposable persistent app with retained state; restore a workload and the age key. Afterwards consider tailnet private browser access, DNS-01, wildcard certificates, a second cluster or directory renaming.

## 7. Console

A web console at `console.homelab.swhurl.com`, behind the shared sign-in, showing app instances, Flux units and cluster health, running reconcile and suspend/resume, and making every other change by opening a PR against this repo. Flux still applies only what is merged. Done 29 September 2026; behaviour is in [console](console.md).

**Decisions** (29 September 2026)

- **Code lives in this repo** (`tools/swhurl/console/`), so tooling and the console that uses it are tested together; the image is published from here.
- **Reads use the image's own code; writes use the clone's CLI.** Status, units and health import `swhurl` from the image and read the cluster. A write downloads `main` through GitHub's API, runs `python -m swhurl app-new …` and `check-apps` inside that tree, commits to a new `console/*` branch through the API and opens a PR, so every change is generated by the rules CI checks it with. The only cross-version interface is `app-new`'s flags.
- **A platform unit, not an app:** `platform-console` needs a service-account token and RBAC, which the app policy forbids. Read access excludes Secrets and `pods/exec`; the only cluster writes are the reconcile annotation and `spec.suspend` (RBAC grants `patch`; the code limits the fields and refuses `cluster-sources`, `cluster-stack` and `destroy-data`). A NetworkPolicy admits only Traefik, so the sign-in header cannot be forged from inside the cluster.
- **GitHub access: a fine-grained personal token** (this repo; Contents and Pull requests read/write) in SOPS, sent only in the `Authorization` header and redacted. Chosen over a GitHub App for simplicity. PRs appear as the operator, marked by the `console/` branch, a `[console]` title and a `Requested-by:` trailer. `verify-platform` warns 14 days before expiry. `main` stays unprotected (the commit-to-`main` loop is unchanged); a test ensures the console creates only `console/*` branches.
- **Image: public on GHCR**, pinned by `src-<hash of inputs>` tag and digest.
- `verify-platform` checks are labelled by what they need (`cluster`, `secret`, `exec`, `host`); the console runs only the `cluster` ones and links to HyperDX for host timer logs.

**Phases** (commits): 1 tooling split and `Runner.stream()` (`e180ff2`); 2 read-only local console, `make console-dev` (`884f050`); 3 image and GHCR publishing (`b63d2af`); 4 deployed read-only, with a forged header from a throwaway pod blocked and `kubectl auth can-i` denying Secrets, exec and patch (`79c07f3`, fix `edd4f44`); 5 reconcile and suspend/resume with audit lines (`bf63344`); 6 new-app PRs (`9364c77`; Reloader for `console` was added here, when it first had a Secret); 7 promote, scale and uninstall PRs and `make console-image` (`0acb567`, `af2b11d`).

**Out of scope:** `destroy-data`, Secret values, credential rotation, `flux-system` root units, host timers, triggering live tests, users other than the operator.

## 8. New app from a catalogue

Agreed 2 October 2026; phases 1–7 done by 3 October. On the console's **New app** page you choose a language and framework, tick features and give a name. The console creates the app's GitHub repository with working code, waits for the first image and opens the platform PR. After you merge it the app runs in staging, and every push to its `main` deploys there.

Example: *TypeScript · features: SQLite* named `notes` produces the repository `samclement/notes`, the image `ghcr.io/samclement/notes:1-<sha>` and a PR `[console] new app notes/staging`, serving `staging-notes.homelab.swhurl.com` once merged.

```mermaid
flowchart LR
  form["New app form<br/>stack, features, name"] --> job["console job"]
  job -->|"render template<br/>(Copier)"| repo["new GitHub repo<br/>code + swhurl.yaml"]
  repo -->|"CI: checks, build"| ghcr["GHCR image<br/>1-&lt;sha&gt;@digest"]
  job -->|"waits for the image,<br/>reads swhurl.yaml"| pr["platform PR<br/>app-new --from-repo"]
  pr -->|"merge"| flux["Flux: staging<br/>+ image automation"]
```

**Concepts.** A *stack* is one template repository per language and framework (`swhurl-app-template-typescript`, `swhurl-app-template-kotlin`) holding the conventions every app follows (port 8080, `/healthz`, UID 65532, writes only to `/tmp`, OpenTelemetry SDK, the shared publish workflow). A *feature* is optional code in a stack (SQLite with migrations, a worker loop instead of an HTTP server), rendered only when ticked; a feature the cluster must also provide declares it in `swhurl.yaml`. `swhurl.yaml` is the app's contract with the platform (`kind`, `database`, `secrets`, `telemetry`, resource defaults, `stack`, optional `startupSeconds`), read by `app-new --from-repo`, so a stack's needs live in the stack, not the console. A *capability* is the platform side of a feature; `sqlite` is the first, and splitting each into its own module waits for one that needs more than generator defaults.

**Decisions** (2 October 2026)

1. **Copier renders a stack's features**: one template repository per stack, features as questions, and each app commits `.copier-answers.yml` so `copier update` brings later template changes. Rejected: a repository per combination (they multiply), GitHub's **Use this template** plus our own patches (a second template system), a generator in `tools/swhurl` (language-specific code here, ruled out by delivered item 9). Cost: the console image gains `copier` and `git`, and each template's CI renders every feature combination.
2. **A second token creates repositories** (`APP_REPOS_TOKEN`): fine-grained, all repositories, Administration, Contents and Workflows read/write plus Actions read. Held only by the console, separate from the first token. Such a token could delete any repository, so the code may only create a new repository, push its first commit to one it just created, and read runs (tested like the `console/*` rule). `verify-platform` warns before expiry. Rejected: a GitHub organisation with a GitHub App (images, Renovate and the template would all move) and creating the repository by hand (New app would not be complete).
3. **The second stack is Kotlin on Micronaut on the JVM** with a small image: a `jlink` runtime on `distroless/cc`, size reported in CI, no GraalVM native image.

**Deferred:** sign-in roles in apps and shared libraries. Traefik already passes `X-Auth-Request-Email` (`platform/oauth2-proxy/middleware.yaml`), but an app can trust it only once its namespace has a NetworkPolicy admitting just Traefik.

**Phases**

1. SQLite capability finished: `make restore-sqlite`, `make live-test-restore-sqlite`; backups record plaintext size and SHA-256.
2. `swhurl.yaml` contract (version 1, `contract.py`) and `app-new --from-repo OWNER/REPO[@REF]` / `--manifest PATH`; `hello-ts` regenerates byte-identical from it.
3. TypeScript stack on Copier; `make app-repo NAME= STACK=` (Python, through `Runner`, using your `gh` login) renders, creates the public repository, pushes, waits for the first publish run and prints the image and digest.
4. New app creates the repository: the **New app and repository** tab renders with Copier, creates the repository through the API (writes only to one created in the same job), waits for the first build and opens the PR. The preset tabs stay for existing images (production, or a retry after a failed first build).
5. Features: Copier questions `kind` (web, worker) and `database` (none, sqlite on `node:sqlite` with migrations at startup); the console and `make app-repo ANSWERS=` read the questions from the template's `copier.yml`, so a stack's features need no platform code.
6. Kotlin Micronaut stack with the same questions; Java 25, about 150 MB, the OpenTelemetry agent. `-XX:TieredStopAtLevel=1` cut start-up from 50 s to 16 s on half a CPU, and `startupSeconds` writes a startup probe so liveness waits for a slow starter. The first Kotlin build takes about 6.5 of the job's 10 minutes.
7. Template updates (3 October): native Renovate Copier PRs from versioned releases; independent app edits are preserved, overlapping edits give conflict markers, app checks pass before main publication; workflow versions are pinned ([template updates](apps.md#template-updates)).
8. **Open:** the app page shows the stack, its features and each capability's health.

**Out of scope:** private repositories and images (PR06), databases other than SQLite, roles and shared libraries, an app's own OIDC login, deleting or archiving the repository on **Uninstall** (you delete it on GitHub), direct production creation (section 9), a `kind: App` operator, a third stack, users other than you.

## 9. Predictable app deployment and promotion

Approved 3 October 2026 and implemented (phases 1–5) with live proof; evidence: [current state](current-state.md#successful-reviewed-promotion-and-fixed-template-checks-3-october-2026).

### Operator experience

For example, `weather-api` deploys automatically to staging. Open its staging page, try the app, then press **Promote to production**. The confirmation shows the exact image, the production address and whether this creates production or updates it. The platform opens a PR, merges it when checks pass, and shows production becoming Ready. The next staging image uses the same button.

Both **Start a new app** and **Deploy an existing image** create staging only: `app-new --env prod` is refused, and `app-promote` always means staging → production. Existing production instances remain supported; production-only apps need staging before promotion. Direct Git edits remain a reviewed route for custom deployments, but promotion provenance is not enforced against manual commits.

```mermaid
flowchart LR
  source["New app or existing image"] --> staging["Staging deployed and reviewed"]
  staging --> button["Promote to production"]
  button --> pr["PR: create production or update its image"]
  pr --> checks["Validate current PR commit"]
  checks --> merge["Eligible PR merges automatically"]
  merge --> git["Platform main"]
  git --> flux["Flux applies production"]
  flux --> ready["Console shows production Ready"]
```

### Design choice

The deployment generator and app policy serve standard web apps and workers whatever the language or origin of the image; app-code templates are optional; custom deployment files remain a reviewed route for shapes the generator cannot express; shared infrastructure keeps its Flux units. Assumed: a single operator, public app images, existing GitHub credentials. No new controller, credential or cluster service.

| Approach | Predictability | Cost |
| --- | --- | --- |
| Fetch the app repository's latest `swhurl.yaml` for first promotion | Familiar, but defaults can differ from reviewed staging and some images have no source manifest | Small; needs source discovery, pinning and a second route for existing images |
| **Derive first production from validated staging files (chosen)** | Uses the settings actually reviewed; works for template apps and existing images | An environment-conversion layer with clear refusals for unsupported shapes |
| Store an app definition and regenerate both environments | One source for creation, editing and promotion | Larger migration; must resolve manual edits and per-environment overrides |

Git deployment files stay authoritative, with no second stored definition. Revisit a stored definition if repeated read/edit/generate conversions become the main maintenance cost.

### Operating rules

- **Supported shape:** one main controller and container; standard generator web or private worker deployments; compatible handwritten resources, probes, security, command and telemetry; platform routing and persistence; app Secret references. Extra controllers, sidecars, resources, external volume bindings and ambiguous YAML anchors are refused with an explanation. Later image-only promotions need only an unambiguous main image field and passing policy.
- **Configuration:** first promotion keeps reviewed staging runtime settings and resources and generates production routing and independent storage. Later promotions change only the image; other production changes are explicit Git edits. Conversion maps namespace and environment labels, generated hosts and environment-specific references by meaning, not by text replacement, and removes staging image automation from production.
- **Setup:** a distinct production public host and production Secret setup are collected on first promotion; those PRs stay manual. Staging credentials, data and PV bindings are never copied. First production gets its own retained volume and empty database (migrations run at normal startup); secret stubs are encrypted for the destination and need manual review, including Reloader registration; private workers need no route; a custom public hostname must be given explicitly.
- **Pending PR:** its reviewed image is frozen, so new staging builds do not invalidate it. Relevant destination or configuration changes need fresh review; first-production source changes beyond the image need regeneration. Unrelated `main` changes continue after validating the updated merge result. Relevant generator, policy, chart or platform setting changes require regeneration or revalidation before eligibility returns.
- **Merge control:** eligible promotions merge automatically after Validate passes on the current head (eligibility is checked from the actual diff) unless **Hold for manual review** was chosen before submission. **Hold PR** removes `auto-merge`; resuming rechecks eligibility. A merge already accepted cannot be stopped by removing the label. The merge workflow's branch, repository, file and head checks stay, and it is not widened to infrastructure. Encrypted-file changes or required setup keep a PR manual.
- **Rollback:** an older staging image may be promoted and is labelled a rollback when ordering is known. It does not reverse database migrations.
- **State recovery:** duplicate clicks, an open PR, a merge conflict and a console restart recover from GitHub and Flux, not in-memory jobs. Target existence is decided from the downloaded Git tree, and a refusal writes no partial instance and opens no PR.

### Behaviour by scenario

| Situation | Console action and result |
| --- | --- |
| Healthy, digest-pinned staging; no production | **Promote to production** creates production from reviewed staging settings |
| Healthy staging; production has a different digest | The same button updates production's image, preserving its settings |
| Both environments have the same digest | **Same image in both**; no promotion needed |
| Staging tag changes but the digest stays the same | Same deployed image; no redundant rollout |
| Production has a newer build number, or tags cannot be ordered | Same promotion when digests differ; the confirmation says rollback when ordering is known, otherwise shows both images |
| Staging unhealthy, suspended, unpinned or still applying Git | Shows why promotion is unavailable and what to do |
| Production still reconciling a previous promotion | Shows deployment progress; no second promotion of the same image |
| A promotion PR is already open | Links to it; repeated clicks reuse it; a different reviewed image needs an explicit replacement |
| Production in Git but absent from the cluster | Treated as pending or failed; never regenerated from live absence |
| Production without staging | Shown normally; promotion explains it needs staging |
| First production needs secrets or a custom host | Collects or links the setup, keeps the reviewed image, explains why auto-merge waits |

The per-click auto-merge opt-in checkbox was removed. A created PR is never reported as a completed deployment; CI failure, conflict or missing setup leaves the PR visible with its reason, and Activity and app pages distinguish creating PR, checks running, awaiting setup or review, merging, deploying, Ready and failed.

### Delivered phases

1. **Shared model:** `tools/swhurl/apps/` modules for reading instance configuration, deciding the promotion outcome and preparing Git edits, shared by the CLI and console, whose templates render a view model. Table-driven tests cover legacy nginx, template web and worker apps, SQLite, secret references and unsupported YAML.
2. **First promotion and image updates** (`3455292`, console pin `6aa39a6`): `app-new` creates staging only; `app-promote` creates or updates production.
3. **One button and auto-merge:** production selection removed from creation forms and forged submissions refused; live promotions #34–36.
4. **Reproducible checks and bounded test cost:** catalogue templates pinned to explicit revisions used for questions, rendering and contract checks (changing a pin is reviewable); platform checks free of language builds; exhaustive platform rendering (`make check-templates`) while cheap; template CI runs defaults, each non-default choice alone and all enabled, with targeted interactions; SQLite tests show writes, migrations and persistence; a passing sample does not claim every combination was tested. A third stack needs catalogue entries, its own template and build checks and conformance evidence, with no language build dependencies in platform tooling.
5. **Template updates** (section 8, phase 7). `hello` stays as the existing-image example and nginx fixtures as a compatibility case; if `hello` is retired, audit storage, links, probes and fixtures first and ask before uninstalling live namespaces. `hello-ts` is not migrated merely because it predates Copier.

**Proof conventions:** browser proof needs a real signed-in session (hand-set identity headers do not prove sign-in); throwaway repositories are named `swhurl-try-<n>` and listed for the operator to delete; the confirm-first actions in `AGENTS.md` apply. `swhurl-try-6` (a TypeScript private worker with SQLite, one event a minute with no TTL, a separate retained 1Gi claim per environment) proved first production, an image update and independent databases; its removal is open work 8.

**Not prerequisites:** a new language, a general app controller, new databases, cluster infrastructure through the app generator, deleting live apps.

## 10. References

- [Flux pruning and deletion](https://fluxcd.io/flux/components/kustomize/kustomizations/)
- [Flux HelmChart reconcile strategies](https://fluxcd.io/flux/components/source/helmcharts/)
- [bjw-s app-template values reference](https://bjw-s-labs.github.io/helm-charts/docs/app-template/reference/)
- [Renovate Flux manager](https://docs.renovatebot.com/modules/manager/flux/)
- [Stakater Reloader](https://github.com/stakater/Reloader/blob/master/README.md)
- [oauth2-proxy provider configuration](https://oauth2-proxy.github.io/oauth2-proxy/7.6.x/configuration/providers/)
- [Cookie Domain scope, RFC 6265](https://www.rfc-editor.org/rfc/rfc6265)
- [cert-manager Route53 DNS-01](https://cert-manager.io/docs/configuration/acme/dns01/route53/)

## 11. Notification boundary refactors and heartbeat (3 October 2026)

Follow-ups from the review of the notification commits (`10d8bac`..`3810e4c`). All tasks are done; behaviour is in [services](services.md#alerts) and [operations](operations.md#notification-checker-heartbeat), evidence in [current state](current-state.md#notification-boundaries-and-heartbeat-3-october-2026).

### Decisions (approved by the operator, 3 October 2026)

| Topic | Decision | Reason |
| --- | --- | --- |
| Who notifies about what | The checker owns app and console lifecycle and health. Flux's `failures` Alert owns infrastructure units and sources. ClickStack rules own app-specific signals. No event is sent by two of them. | Duplicate incident messages were the reason the app Alerts were removed. |
| Failure Alert sources | Selected by label `platform.swhurl.com/alert: failures` on each infrastructure/platform Flux unit, not by a list in `alerts.yaml`. App units and `platform-console` are never labelled. | A new unit is covered where it is defined; new apps are excluded by default. |
| Checker placement | Stays in the `platform-console` HelmRelease and shares the console image. | Moving it would not remove the image coupling and risks losing incident state. |
| Checker self-monitoring | An independent host timer, the **heartbeat**, watches the CronJob and sends to the failures ntfy topic. | It still runs when the console image, the publish pipeline or the in-cluster scheduler is broken. |
| ntfy destinations | Stay in two Secrets (Flux provider, checker), kept equal by `make check-secrets`. The heartbeat reads the checker's Secret at run time and keeps no copy. | Merging the Secrets is a separate decision; a third copy is avoided. |

### Delivered tasks

- **R1** docs record the console-image coupling and the two ntfy Secrets.
- **R2** (`c695daa`) split `notifications.py` into a package: `state.py`, `evaluate.py` and `delivery.py`, with `__init__.py` keeping `check` and `main`. Code was moved, not rewritten, and every test stayed unchanged.
- **R3** (`61346e6`) drives the ntfy destination checks in `secrets_check.py` from one `NTFY_DESTINATIONS` table and a `destination_hash` helper (query and fragment removed before hashing).
- **R4** (`953bfcd`, evidence `cb0afea`) replaced the 14 explicit `kind: Kustomization` Alert sources with one `matchLabels` selector and labelled the units in `infra.yaml`, `platform.yaml` and the root `flux-system/kustomizations.yaml` (applied with `make flux-bootstrap`); a wiring test fails if `platform-console` or any app unit carries the label.
- **H1–H4** (`862b360`, `d809189`, `700e065`, evidence `d6b8075`): the command, the host timer, installation and exercise, and the `verify-platform` host check. Lesson from H3: the service first failed because systemd's fixed PATH omitted this host's `~/.local/bin/uv`; the template was fixed and `make host-heartbeat` reinstalled. The operator confirmed receipt of both the stale and recovery notifications.

### Heartbeat design

```mermaid
flowchart LR
    timer[systemd timer, every 5 min] --> hb[make notifications-heartbeat]
    hb -->|get cronjob console-notifications| k8s[Kubernetes API]
    hb -->|get Secret notification-ntfy| k8s
    hb -->|stale, reminder or recovery| ntfy[ntfy failures topic]
    hb --> flag[(~/.local/state/swhurl-platform/notification-heartbeat.json)]
```

- **Stale** means the CronJob is missing, suspended, or `status.lastSuccessfulTime` is older than 10 minutes (`verify-platform` uses 5; the longer limit avoids two messages for one blip). The pure function `heartbeat_action(cronjob, state, now, max_age)` returns `none`, `alert`, `remind` (hourly while stale) or `recover`; recovery is sent only if an alert was sent.
- **Messages:** `notification checker stale` (high priority) with the reason and `make verify-platform`; `notification checker recovered` (normal). No Secret values.
- **State:** `{"alerting_since": <epoch or null>, "last_alert": <epoch or null>}`; losing the file repeats at most one alert.
- **Cluster unreachable:** log `cannot read notification checker`, leave state unchanged, exit 1; no alert is possible.
- **Credentials:** `NTFY_FAILURES_URL` is read from `console/notification-ntfy` as JSON, decoded in memory, registered with `runner.add_secret` and never logged; sending reuses the checker's validated `delivery.publish`.
- **Schedule:** timer `OnBootSec=5min`, `OnUnitActiveSec=5min`, `AccuracySec=1s`; the log is `/var/log/swhurl-platform/swhurl-notification-heartbeat.log` (5 MiB rotation, read by the OTel DaemonSet). `verify-platform` requires the timer active and a successful service run within 15 minutes.
- **Limits** (also in services.md): it cannot report when the node or the Kubernetes API is down; a checker failure silences the console's own lifecycle messages for up to 10 minutes plus one five-minute timer interval and one second.

One commit per boundary for future notification work: checker code, alert wiring, app-tooling cleanup, docs. Write the contract before implementing; record live evidence only for what is deployed.

## 12. Live job output on the console (deployed 3 October 2026)

The job page (`/jobs/<id>`) streams output with Server-Sent Events (SSE) while a job runs; without JavaScript it reloads every 2 seconds. Scope is the job page only: the pending-app page (10 s meta refresh) and the cluster pages are unchanged. User-facing behaviour is in [console](console.md); this section keeps the design and contract.

### Decisions

| Topic | Decision | Reason |
| --- | --- | --- |
| Transport | SSE (`EventSource`) over a plain GET | Data flows one way; the browser reconnects and resumes by itself; no new dependency; passes oauth2-proxy ForwardAuth as an ordinary GET. WebSocket (two-way channel nobody needs), htmx (new dependency) and `fetch` polling (still a poll) were not chosen |
| Producer side | `actions.py` is unchanged; the stream reads `job.lines` and `job.finished` every `STREAM_POLL` with `asyncio.sleep` | `job.lines.append` is called from `actions.py`, `changes.py`, `repos.py` and `server.py`, so a notify hook would touch all of them; an async sleep holds no thread per open page |
| End of stream | `job.finished is not None`, read before the lines | `Jobs._run` sets it after the final state and lines, so nothing is missed |
| After the end | The client updates the state mark, finish time and PR link from `done`, then closes the stream | Keeps scroll and text selection without duplicating page rendering |
| Line rendering | The server escapes each line with `short_hashes` and sends HTML; the client uses `insertAdjacentHTML` | Same output as the first render; safe because `short_hashes` escapes first |

### Contract: `GET /jobs/{id:int}/events`

- Auth is the `RequireIdentity` middleware (a GET needs no Origin check). An unknown job gets `404` as `text/plain`, so `EventSource` fails instead of retrying forever.
- Success is `200` with `Content-Type: text/event-stream; charset=utf-8`, `Cache-Control: no-cache` and `X-Accel-Buffering: no`.
- **Start position** is the number of lines the client already has: `Last-Event-ID` if present (a non-negative integer, else `0`), otherwise the `after` query parameter (invalid or absent means `0`), clamped to `len(job.lines)`.
- **Events** end with a blank line; `data` is one line of JSON so a line containing `\n` cannot break the framing:

```
id: 3
event: line
data: {"html": "[INFO] reconciling <span class=\"hash\" title=\"…\">1edf37052ebd…</span>"}

event: done
data: {"state": "succeeded", "finished": "12:34:56", "link": ""}

: keep-alive
```

- `id` is the number of lines delivered so far, so `Last-Event-ID: 3` resumes at line 4. `done` is last and carries `state` (`succeeded` or `failed`), the `finished` time and the optional final PR `link`; the stream then ends. `: keep-alive` is sent after `KEEPALIVE` seconds with nothing to send.
- `STREAM_POLL = 0.25` and `KEEPALIVE = 15.0` are module constants in `server.py`; tests patch them.
- Each loop reads `finished` before `job.lines[sent:]`; do not reorder them. Starlette cancels the generator on disconnect, so `CancelledError` is not caught.
- The page ([`job.html`](../tools/swhurl/console/templates/job.html)) opens `EventSource('/jobs/<id>/events?after=<lines rendered>')` only for a running job and removes the `waiting for output…` placeholder on the first line. On `done` it updates the status mark, state, finish time and optional PR link in place and closes the stream without reloading. EventSource reconnects automatically after a transient error. A `<noscript>` meta refresh keeps the old behaviour.

### Status

Route, page, tests (`06f552a`, final-state update `62fd518`) and docs are done; deployment and cluster evidence are in [current-state.md](current-state.md). **Open:** confirm in a signed-in browser that lines arrive one by one (a burst at the end would point at Traefik or ForwardAuth response buffering), scroll and selection are kept, the final status and finish time update without a reload, a finished job opens without a stream, and a mid-job reload resumes without duplicate lines. Then append the result to `current-state.md`.

Out of scope: live updates on cluster pages (needs a Kubernetes watch), the pending-app page, streaming across console replicas or restarts, and a notify hook in `actions.py`.

## 13. Recovery drill (PR08a gate and fresh bootstrap) — designed 3 October 2026, not started

**Goal.** Prove that a stranger holding only the repository, the age key's off-host copy and read access to the S3 bucket can rebuild the platform on a **different machine** from the docs, and get the data back. This closes PR08a, the "fresh bootstrap on a real new host" item and the Let's Encrypt rebuild question in section 0. Success is a dated record in `current-state.md` plus docs fixed wherever the drill stumbled.

**Why it is risky, and the rule that follows.** A cluster built from `main` is a second copy of the live platform. Left alone it would: push commits to `main` (`platform-image-automation` holds the write key), open PRs and post to the real ntfy topic (`platform-console`, `platform-alerts`), and start the console's dashboard and notification jobs with real tokens. **The drill cluster must therefore never follow `main`.** It follows a branch `drill/recovery` in which those units are removed (the 28 September k3d rehearsal did the same with `rehearsal/bootstrap`; [evidence](current-state.md#bootstrap-rehearsal)). The host timers (`make host-dns`, `host-backup`, `host-heartbeat`) are never installed on the drill machine; DNS and the router are never touched.

### Decisions to take first (operator; ask, do not assume)

| Question | Options | Recommendation |
| --- | --- | --- |
| Where does the drill run? | (a) a throwaway cloud VM; (b) a VM or spare machine on the LAN; (c) another k3d on this host (already done, and not a separate machine, so it cannot count) | (a) or (b), whichever you can delete afterwards; a fresh OS install is what makes "using only the docs" honest |
| How does it read S3? | A read-only IAM user limited to `s3://swhurl-platform-backups-110927251694/` for the drill, deleted after; or copy two backup files by hand | A temporary read-only user; never copy the `sam` credentials to another machine |
| Which certificates? | `selfsigned` only. Let's Encrypt needs public DNS pointing at the drill machine, which would disturb live routing | `selfsigned`; the Let's Encrypt rate-limit question is answered on paper (task D1), not by issuing |

Record the answers at the top of the evidence entry. Stop and ask if any is unanswered.

### Tasks (run in order; one commit per task; D2 and D4 involve the operator)

**D1. Walk the docs as a stranger (offline).**
1. Read `docs/bootstrap.md` and `docs/operations.md` (Backups and recovery). List every command, tool, version, file and credential a reader needs, in order. Check each exists: `make` targets (`grep -n '^name:' Makefile`), scripts, file paths, tools named without an install line (`aws`, `age`, `sops`, `flux`, `helm`, `uv`, `kubectl`), and the Python version.
2. Check what a new machine lacks: the bucket name and region (hard-coded in operations.md), where the age key's off-host copy lives (the docs say "location kept outside Git"; say how to ask yourself for it, not where it is), `age.agekey` placement, and the `sops-age` Secret step.
3. Check the SQLite path: a fresh cluster creates empty claims, so `make restore-sqlite` needs the app running first and the backup files copied to `BACKUP_DIR/sqlite/<app>-<env>/`. Confirm the docs say so in order and that `restore-sqlite --dry-run` works against a copied file.
4. Answer the Let's Encrypt rebuild question in `operations.md`: the limits that matter (duplicate certificate: 5 per week for the same hostnames; 50 certificates per registered domain per week), what a rebuild issues (the four platform and app hostnames), and the rule: use `selfsigned` or `letsencrypt-staging` while iterating, and switch to `letsencrypt-prod` once. Check the numbers against Let's Encrypt's current rate-limit page before writing them.
5. Fix every gap in the canonical page (bootstrap.md for building, operations.md for data), not in a new page. Keep a list of what you could not verify offline.
6. Validate: `make check`. Commit: `Close recovery documentation gaps found by a dry read`.

**D2. Prepare the drill branch and machine (operator + model).**
1. Model: create `drill/recovery` from `main`. In it, remove from `clusters/home/kustomization.yaml` and the unit files: `platform-image-automation`, `platform-console`, `platform-alerts`, `platform-flux-webhook`, and every `app-*` unit except one SQLite app chosen for the data check. Set the Git source in `clusters/home/flux-system/sources/` to the branch. Set every certificate to `selfsigned` (`make platform-certs-*` edits files only) and remove the Let's Encrypt issuers if they would be requested. Run `make check-repo` on the branch; fix only what the removals break. **Push only the branch, never `main`.**
2. Operator (own terminal): provision the machine, install k3s per `docs/bootstrap.md` step 1, create the temporary read-only IAM user, bring the age key from its off-host copy, and install the tools the docs list. Note anything the docs did not tell you.
3. Hand the operator the list of what to copy over (nothing else): the repository clone URL and branch, the age key, and the temporary AWS profile.

**D3. Rebuild from the docs (operator, model watching and noting).** Follow `bootstrap.md` literally on the drill machine, steps 1, 3 (no new Secrets), 4, 5 and 6, with these differences: no DNS step, no router step, `drill/recovery` as the source. Time each step. At every stumble, write down the exact command and message, then fix the docs afterwards (D5). Expected: units Ready in dependency order within about 10 minutes of the first image pulls; `make install` fails on the ingestion key until the restore (as documented).

**D4. Restore the data (operator + model).**
1. MongoDB: copy the newest `clickstack-mongodb/` archive and its `.json` from S3, then follow the restore commands in `operations.md`. The restored document count must equal the metadata's. Run `make clickstack-bootstrap` and `make verify-platform`.
2. SQLite: copy one app's newest backup from `app-sqlite/<app>-<env>/`, run `make restore-sqlite APP= ENV= DRY_RUN=true`, then with `CONFIRM=<app>/<env>`. Check the row counts against the backup's metadata; the app must start on the restored data.
3. Pass criteria: MongoDB count and ingestion key match; SQLite checksum, table count and `integrity_check` pass; `verify-platform` passes except checks that need public DNS or the units removed from the branch (list which ones failed and why); Traefik answers on the drill machine with the self-signed certificate; HyperDX shows current logs after the collectors reconnect.
4. Stop conditions: any step wants a real token, the live `main`, the live DNS or the live ntfy topic; any command would delete data on the live cluster. Stop and report. The drill never uses the live `KUBECONFIG`: check `kubectl config current-context` before every command.

**D5. Record, fix, tear down.**
1. Fix every doc gap D3 and D4 found, again in the canonical page. Commit: `Fix recovery documentation after the drill`.
2. Record dated evidence in `current-state.md`: the decisions above, timings per step, the pass-criteria results, and what was **not** exercised (Let's Encrypt issuance, public DNS and router forwarding, a first login without a backup, the Google OAuth redirect on a new hostname).
3. Update section 0: mark PR08a and the fresh-bootstrap item done or partial exactly as proven, and update the delivery table in section 3. If something failed, leave the item open and list what is left.
4. Operator: delete the drill machine, the IAM user and the `drill/recovery` branch. List them in the evidence entry so nothing is left running.
5. Commit: `Record the recovery drill`.

### Track A: what can be proven on this host (do this before the separate machine)

A throwaway k3d cluster on this host, built by following the docs from a clean shell, proves the docs and the data restore. It does **not** prove a bare-host install, independent credentials or the public side (DNS, router, Let's Encrypt), so when Track A passes, mark PR08a **partial** in section 0 and keep D2 to D5 above as the remaining separate-machine gate. D1 is shared by both tracks and runs first.

**Guardrails (check at the start of every task and before every `kubectl`/`flux`/`helm` command).**
- The drill uses its own kubeconfig, `export KUBECONFIG=$DRILL/kubeconfig` where `DRILL=$CLAUDE_JOB_DIR/tmp/drill` (create it). `kubectl config current-context` must print `k3d-drill`, never the live k3s context. If it does not, stop.
- Never run `make flux-bootstrap`, `make flux-install`, `make host-*`, `make backup-*`, `make destroy-data` or any `make` target that reads `$HOME/.kube/config` unless `KUBECONFIG` is exported to the drill file in the same command. `git push` only the `drill/recovery` branch; never `main`.
- The drill must not follow `main` (see the rule above): confirm in the drill Git source that the branch is `drill/recovery`.
- Read S3 only with `aws s3 cp` (and `ls`); never `rm`, `sync --delete` or `mv`. Copy backups to `$DRILL/backups/`, never into `~/.local/state/swhurl-platform/backups`.
- The age key is read from the repo copy (`age.agekey`, git-ignored). Never print it or any Secret value; compare by counts and checksums.
- Resources first: the live stack already uses about 11 GiB of 31 GiB RAM and the drill adds ClickStack. Run `free -g` and `df -h /`; stop if available memory is under 12 GiB or free disk under 40 GiB.

**A1. D1 (shared).** Do D1 as written. Commit as described.

**A2. Drill branch (offline).** Do D2 step 1 on `drill/recovery`, keeping one SQLite app (pick from `ls apps/`; prefer the one with the most recent backup in S3: `aws s3 ls s3://swhurl-platform-backups-110927251694/app-sqlite/ --recursive | tail`). Verify before pushing the branch: `grep -rn "platform-image-automation\|platform-console\|platform-alerts\|platform-flux-webhook" clusters/` shows nothing on the branch; `make check-repo` passes; `git diff main --stat` lists only deletions and the source/issuer edits. Push with `git push -u origin drill/recovery`.

**A3. Build the cluster (operator terminal for `sudo`).** Follow `docs/bootstrap.md` step 1 and 4 with k3d in place of the k3s installer, as the 28 September rehearsal did ([evidence](current-state.md#bootstrap-rehearsal)): rootful Podman, API bound to `127.0.0.1`, cluster name `drill`, k3s image at the live version (`kubectl version` on the live cluster shows it; do not guess). Hand the operator the exact commands, labelled "run in your own terminal" where they need `sudo`. Then, in the drill kubeconfig only: `make flux-install`, create the `sops-age` Secret from `age.agekey`, `make flux-bootstrap`, `make install`. Expected: all remaining units Ready in dependency order (about 3 minutes in the rehearsal, longer for cold image pulls); `make install` ends with the documented failures on the ingestion key and the MongoDB volume's reclaim policy. Record any other failure verbatim.

**A4. Restore MongoDB.** `aws s3 cp` the newest archive and its `.json` metadata from `clickstack-mongodb/` into `$DRILL/backups/`. Follow the restore commands in `operations.md` against the drill cluster. Pass: `mongorestore`'s document count equals the metadata's; `make clickstack-bootstrap` succeeds; `make verify-platform` passes except for checks the branch cannot satisfy (list each failed check with the reason: removed units, no public DNS, no S3 backup timer on the drill host, host-only checks).

**A5. Restore one SQLite app.** Copy that app's newest backup and metadata from `app-sqlite/<app>-<env>/` to `$DRILL/backups/sqlite/<app>-<env>/` and run with `BACKUP_DIR=$DRILL/backups`: `make restore-sqlite APP=<app> ENV=<env> DRY_RUN=true`, then with `CONFIRM=<app>/<env>`. Pass: checksum, table count and `integrity_check` match the metadata, and the app's pod is Ready on the restored data (compare a row count with the metadata).

**A6. Record and tear down.**
1. `k3d cluster delete drill` (names the drill cluster only; check the name first with `k3d cluster list`), delete `$DRILL`, and delete the remote branch (`git push origin --delete drill/recovery`) after confirming with the operator.
2. Evidence in `current-state.md`: date, k3s and Flux versions, per-step timings, pass results from A3 to A5, the failed `verify-platform` checks with reasons, the doc fixes made, and the **not exercised** list (bare-host install, independent credentials, Let's Encrypt, DNS and router, first login without a backup, Google OAuth redirect on a new host).
3. Section 0: PR08a stays **partial** (docs and data restore proven on this host; separate-machine gate open); update the delivery row in section 3 and the "not yet exercised" list to match. Commit: `Record the recovery drill on this host`.

**Verification the cheaper model must run before reporting:** `kubectl --kubeconfig $DRILL/kubeconfig config current-context` is `k3d-drill`; the live cluster still shows all Flux units Ready (`KUBECONFIG=$HOME/.kube/config flux get kustomizations`) and `git log origin/main` has no commit from this work except the documentation commits; `aws s3 ls` of the bucket shows no new or removed objects; `k3d cluster list` shows no leftover drill cluster.

### Out of scope

Issuing real Let's Encrypt certificates, DNS and router cut-over, restoring the live cluster, a write-only backup IAM user (separate follow-up in section 0), automating the drill, and a second permanent cluster.

## 14. AI incident review and fix PRs — in progress (Stage 2a planning, 6 October 2026)

**Goal.** Periodically identify actionable application failures from logs, metrics and traces; send a concise, evidence-linked root-cause analysis; and, when the evidence supports a code change, open a tested draft pull request against that app's repository. The system never merges or deploys its own fix. GitHub review, CI and the existing GitOps path remain the gates to production.

**Starting choice.** Use the Codex CLI (`codex exec`) in a disposable worker for the first coding pilot. Keep the analyzer behind a small internal interface (`ModelAdapter`) so a later phase can use direct provider API calls or replace the coding worker without changing evidence collection, incident state or PR policy. Do not build a general-purpose agent framework or allow model-selected arbitrary tools in the first version.

**Trigger decision (5 October 2026, operator-directed).** A scheduled sweep is the primary trigger; ClickStack alerts are added later as a low-latency accelerator and never replace the sweep. Reason: an alert exists only for failures someone predicted, silent failures emit nothing to alert on, new apps start with no rules, and a broken alert path is invisible. See [Trigger strategy](#trigger-strategy-and-ai-boundary).

**Existing boundaries to preserve.** The notification checker in `console-notifications` remains a deterministic lifecycle/health checker with no application log access or GitHub credential. The reviewer is a separate capability with its own Flux unit, namespace, service account, state and SOPS Secrets. It has no Kubernetes write access except `patch` on its own state ConfigMap. The analysis worker and the verifier hold no Kubernetes, ClickHouse, ntfy or GitHub credential. A separate PR broker (Stage 4) is the only component allowed to write a branch or open a pull request, and only in explicitly enabled app repositories. No reviewer-created PR receives auto-merge eligibility.

**How to use this section.** Work the [Stage 2a tasks](#stage-2a-tasks) in order, one commit per task. Every name, path, limit and schema an implementer needs is in [Runtime layout](#runtime-layout-stage-2a), [Data contracts](#data-contracts) and [Component design contracts](#component-design-contracts); where this section and the Stage 1 code disagree, this section is the target and the task list says which task closes the gap. Do not choose a value this section leaves to the operator: stop and ask ([open decisions](#open-decisions-operator-ask-do-not-assume)).

### Flow

```mermaid
flowchart LR
    signal["Trigger: scheduled sweep (primary) or ClickStack alert (stage 2c)"] --> collect["Collector + deterministic pre-filter"]
    collect -->|collect report| gate[Orchestrator: schema, policy, budgets, state]
    collect -->|redacted bundle, only when the pre-filter fires| review[Analysis worker: Codex CLI]
    review -->|diagnosis and, in stage 3, candidate patch| gate
    gate -->|patch, stage 3| verify[Verifier: clean checkout, no model key]
    verify -->|check report| gate
    gate -->|validated summary| notify[Notifier: ntfy]
    gate -->|validated patch artifact, stage 4| broker[PR broker]
    broker -->|draft PR only| repo[Enabled app repository]
    repo -->|human review and CI| merge[Existing GitOps deployment path]
```

The collector, not the model, defines the incident scope and query limits. A run covers a bounded recent window and proceeds to the model only if the deterministic pre-filter fires. Logs and trace fields are untrusted data, never instructions. The model cannot widen the app or time scope, access credentials, choose a GitHub destination, merge, deploy, or alter platform or cluster configuration. No-change and low-confidence results are valid outcomes.

### Runtime layout (Stage 2a)

Confirmed by the operator on 6 October 2026: **one pod, three containers run in sequence.** Isolation between containers is by credential and volume mount, not by network: service-account tokens and NetworkPolicy apply to a whole pod, so the pod's egress is the union of what its containers need.

| Name | Value |
| --- | --- |
| Flux unit | `platform-incident-review` in `clusters/home/platform.yaml`, path `platform/incident-review`, labelled `platform.swhurl.com/alert: failures`, no `postBuild` substitution unless a value needs it |
| Namespace | `incident-review` |
| Workload | CronJob `incident-review` from the bjw-s `app-template` chart, as `platform/console/helmrelease.yaml` uses; hourly at minute 17, `concurrencyPolicy: Forbid`, `backoffLimit: 0`, `activeDeadlineSeconds: 600`, one successful and one failed Job kept |
| Service account | `incident-review` (created by the chart), `automountServiceAccountToken: false` on the pod; a projected token volume is mounted only into `collect` and `decide` |
| State | ConfigMap `incident-review-state`, key `state.json`, annotation `kustomize.toolkit.fluxcd.io/ssa: Merge` (same shape as `platform/console/notification-state.yaml`) |
| Secrets (all SOPS, in `platform/incident-review/`) | `incident-review-openai` (`OPENAI_API_KEY`, file `secret.sops.yaml`); `incident-review-clickhouse` (`CLICKHOUSE_USER`, `CLICKHOUSE_PASSWORD`, file `secret-clickhouse.sops.yaml`); `incident-review-ntfy` (`NTFY_REVIEW_URL`, file `secret-ntfy.sops.yaml`). Each is encoded once with `stringData`. |

| Container (order) | Image | Command | Mounts and credentials | Time limit |
| --- | --- | --- | --- | --- |
| `collect` (init 1) | console operator image | `timeout 120 python -m swhurl incident-review-collect` | Kubernetes token (read state, read HelmReleases); `incident-review-clickhouse`; writes `/work/collect` and `/work/bundle` | 120 s |
| `analyse` (init 2) | new worker image `images/incident-review-worker` | `/opt/review/analyse.sh`, a wrapper around `timeout 300 codex exec` (environment: `CODEX_MODEL`, optional `INCIDENT_REVIEW_MODE=collect-only`, `ANALYSE_SECONDS`) | `incident-review-openai` as the file `/run/secrets/openai/OPENAI_API_KEY`; `/work/bundle` read-only; writes `/work/diagnosis`; no token, no other Secret | 300 s (the approved per-run cap) |
| `decide` (main) | console operator image | `timeout 60 python -m swhurl incident-review-decide` | Kubernetes token (get and patch state); `incident-review-ntfy`; `/work/collect`, `/work/bundle`, `/work/diagnosis` read-only | 60 s |

- `/work/collect`, `/work/bundle` and `/work/diagnosis` are three separate `emptyDir` volumes with a size limit. Nothing else is shared.
- All containers: non-root, `readOnlyRootFilesystem: true`, `allowPrivilegeEscalation: false`, all capabilities dropped, resource requests and a memory limit. `analyse` gets a bounded `emptyDir` for `CODEX_HOME` and scratch.
- **Exit codes.** `collect` and `analyse` exit 0 for every handled outcome and record it in their status file; they exit non-zero only on a crash. `decide` is the only container that turns an outcome into a notification, a state change and the Job's exit code. A quiet sweep is exit 0.
- **RBAC** (pattern: `platform/console/notification-rbac.yaml`): a namespaced Role with `get` and `patch` on ConfigMap `incident-review-state` only; a ClusterRole with `list` on `helmreleases.helm.toolkit.fluxcd.io` only (image tag and digest of the allowlisted app). No Secrets, no `pods/exec`, no workload verbs.
- **NetworkPolicy** selects the reviewer's pods only (never `podSelector: {}`). Egress: cluster DNS; `clickstack-clickhouse-clickhouse-headless.observability.svc` port 8123; the Kubernetes API; TCP 443 to addresses outside the cluster (OpenAI and ntfy). No ingress. Proven live on 8 October 2026 from a pod carrying the reviewer's labels: ClickHouse 8123, external 443 and the API were reachable; ClickHouse 9000, HyperDX 3000, an app Service and external port 80 were not, while an unlabelled control pod reached all of them. The policy takes effect a few seconds after a pod starts (the first connection of a new pod got through to a port that was blocked 20 seconds later), so `collect`, which holds no model key, can run inside that gap; `analyse` starts after it. The API server rule is TCP 6443 to any address outside the pod and Service ranges, because the node's address differs per host. Hostname filtering is not possible on k3s; the open 443 egress is approved for 2a (decision 5).
- **Images.** `collect` and `decide` reuse the console operator image, as the dashboard and notification jobs do. `tools/swhurl/images.py` pins only `platform/console/helmrelease.yaml` today; extend its pin step to the reviewer manifest so both units move together. The worker image contains only the pinned Codex CLI, the wrapper script, the prompt template and `diagnosis.schema.json`; it has no `kubectl`, no `swhurl` package and no Git credentials.

### Open decisions (operator; ask, do not assume)

Decisions 1, 2, 4 and 5 (6 October 2026), 3 (7 October) and 7 (8 October) were taken by the operator and are binding. Decision 6 only matters from Stage 2c and decision 8 from Stage 4.

| # | Decision | Decided value or recommended default | Notes |
| --- | --- | --- | --- |
| 1 | ClickHouse account for the collector | **Decided: reuse.** The existing `app` user (live grants on 6 October 2026: `SHOW` on everything, `SELECT` on `default.*` and `system.*`; password is `CLICKHOUSE_APP_PASSWORD` in `observability/clickstack-runtime-inputs`), copied into `incident-review-clickhouse` and kept equal by `make check-secrets` | A dedicated user is narrower (no `system.*`) but needs a ClickStack chart change that is not yet investigated; the copy is a second place to rotate |
| 2 | ntfy destination | **Decided: a new topic** for diagnoses and reviewer failure messages, in `incident-review-ntfy` only | No third copy of the failures destination (section 11), so `NTFY_DESTINATIONS` is unchanged; the operator creates the topic and subscribes in task 8 |
| 3 | Pilot incident class | **Decided 7 October 2026: a planted, caught failure in `hello-ts`.** `GET /repeat?times=-1` throws `RangeError`, which the handler catches, logs at `error` and answers with 500; the pod does not restart. Draft PR [hello-ts#11](https://github.com/samclement/hello-ts/pull/11); merging it deploys to staging | Replaces the approved class (service name falling back to `swhurl-app`), which cannot occur on the cluster: the platform always injects `OTEL_SERVICE_NAME`, and no `swhurl-app` service reached ClickHouse in the 7 days to 6 October. The fallback in `src/server.ts` only applies off the platform and is left alone. The bug stays unfixed by hand so Stage 3 has a real fix to attempt |
| 4 | Extra state fields | **Approved:** rolling hourly counts, cooldown timestamps, coverage tags and a monthly spend counter | Extends the 5 October retention approval, which lists fingerprint, timestamps, model/CLI version, status, links and check result only. The additions are numbers and enums, never log text |
| 5 | Model-endpoint egress in 2a | **Approved:** TCP 443 to any external address for the pod | The bundle is redacted and no source code is present in 2a. A hostname-allowlisting proxy is still required before Stage 3 |
| 6 | Diagnosis alongside a ClickStack alert (from 2c) | Allow both: the rule reports detection, the reviewer sends one `diagnosis` follow-up | Section 11 says no event is sent by two senders; this needs an explicit exception or the reviewer must stay silent for rule-covered incidents |
| 7 | Thresholds and model | **Decided 8 October 2026:** the thresholds in [Allowlist](#allowlist) as proposed, and model `gpt-6-luna` (the pinned CLI's catalogue calls it the fast, affordable model for easier tasks) | They set cost and noise. The model's prices are not recorded here; spend is counted at the flat per-run cap |
| 8 | Broker credential (Stage 4, not needed for 2a) | A fine-grained token limited to the pilot repository (Contents and Pull requests: write), reusing the console's `GitHubAPI` client and the `verify-platform` expiry check | A GitHub App is narrower to revoke but adds token-minting code and a new credential type |

### Trigger strategy and AI boundary

**Trigger staging.** Stage 2 is split so coverage is measured before alerts are trusted:

| Sub-stage | Trigger | Model call | Purpose |
| --- | --- | --- | --- |
| 2a | Scheduled sweep over allowlisted apps, hourly | Only when the pre-filter fires; at most one per run | Discovery of known and unknown failures |
| 2b | Operator review of sweep output, weekly | None | Tag each finding "covered" or "alert gap"; the gap list is the backlog of ClickStack rules |
| 2c | A ClickStack rule fires and the next collector run is scoped to that rule | Yes (same path) | Low latency for failure classes that have proven rules |
| Permanent | Sweep keeps running, at a lower cadence once 2c is live | Only when the pre-filter fires | Backstop for gaps and for a broken alert path |

The sweep may be retired only by a later explicit decision after several weeks of 2b show no uncovered findings; the default is to keep it. Cadence is a cost lever, not a correctness one: the pre-filter, not frequency, decides whether anything is analysed.

**Deterministic versus AI steps.** The AI is used in exactly two places: the diagnosis (stage 2) and the patch (stage 3). Everything else, including deciding whether to look at all, is deterministic code.

| Step | Owner |
| --- | --- |
| Sweep schedule, `Forbid` concurrency, timeouts | Deterministic (CronJob) |
| Telemetry queries (fixed, parameterized, time/row/byte limits) | Deterministic (collector) |
| Pre-filter: new fingerprint, error-rate change against baseline, missing expected signal, cooldown, dedupe | Deterministic (collector, reading state) |
| Fingerprinting, redaction, bundle assembly | Deterministic (collector) |
| **Diagnosis** (summary, likely cause, confidence, evidence references, or no-change) | **AI** (Codex, read-only sandbox, bundle only) |
| Diagnosis validation (schema, every cited reference exists, confidence threshold) | Deterministic (orchestrator) |
| Notification text, delivery, dedupe and rate limit | Deterministic (notifier, from validated fields) |
| Coverage (2b): "sweep finding with no matching alert rule" | Deterministic report; writing a rule is the operator's decision |
| Alert rule evaluation and webhook (2c) | Deterministic (ClickStack) |
| **Patch authoring** (stage 3) | **AI** (Codex, workspace-write on a fresh checkout) |
| Which checks run, exit-code interpretation, verification on a clean checkout | Deterministic (orchestrator, verifier) |
| Patch policy, base SHA and digest checks, draft PR creation | Deterministic (orchestrator, broker) |
| Budgets, stop conditions, corrupt-state handling, state writes | Deterministic (orchestrator) |
| Review, merge, deploy | Human, CI, Flux; never the AI |
| Post-merge recurrence check | Deterministic (same query and fingerprint) |

Consequences to preserve: a quiet sweep never calls the model; the model never chooses scope, window, repository, branch name, checks or destination; malformed, uncited or low-confidence output is suppressed by validation. Other AI uses (batch triage, drafting a candidate ClickStack rule) are out of scope until an eval set exists (stage 5).

### Data contracts

All files are UTF-8 JSON with a top-level `"version": 1`. A reader that sees another version, a missing required field or an unknown `status` refuses the run with reason `contract`.

#### Allowlist

One checked-in file, `tools/swhurl/incident_review/allowlist.yaml`, replaces the constants in `incident_review/__init__.py` (`ALLOWED_REPOSITORIES`, `ALLOWED_PATHS`, the limits). It ships in the operator image. Changing it is a reviewed commit; nothing at run time can extend it.

```yaml
version: 1
model: gpt-6-luna             # decision 7; recorded with the CLI version in every run
defaults:                     # decision 7, confirmed as proposed
  confidence_min: 0.7         # below this: suppress, no notification
  cooldown_hours: 24          # no second model call for the same fingerprint inside this
  max_analyses_per_day: 4     # across all apps
  baseline_hours: 24          # rolling window of hourly counts kept per app/env/signal
  rate_ratio: 2               # fires when count > ratio * mean(baseline) ...
  rate_min_count: 5           # ... and count >= this
  incident_retention_days: 30 # incidents not seen for this long are pruned from state
  run_cost_cents: 50          # charged per model call when the CLI reports no usage
  monthly_refuse_cents: 1600  # approved: refuse new model calls at $16 of the $20 cap
apps:
  - app: hello-ts
    repository: samclement/hello-ts
    environments: [staging]   # namespace is <app>-<env>
    expected_service: hello-ts
    signals: [error-logs, error-spans, unexpected-service, missing-telemetry]
    severity: {error-logs: default, error-spans: default, unexpected-service: low, missing-telemetry: high}
    patch_paths: [src/, test/, README.md]    # stage 3; hello-ts keeps its tests in test/
    checks: []                               # stage 3: the only commands the verifier may run
alert_rules: []               # 2b: {app, signal, rule} entries the operator has written in ClickStack
```

#### Signals and queries

Queries go to ClickHouse over HTTP (`httpx`, already a dependency) as the account in `incident-review-clickhouse`, with every caller value passed as a query parameter, `max_result_rows` 20, `max_result_bytes` 64,000 and the streamed 64,000-byte reader from Stage 1 as the hard cap. The window is the previous full UTC hour. Scope is always the namespace, because a wrong service name is itself a failure. Column names below were read from the live schema on 6 October 2026; the Stage 1 `telemetry_query` uses columns (`app`, `service`, `exception_type`) that do not exist and is replaced in task 3.

| Signal | Table and predicate (all also filter `ResourceAttributes['k8s.namespace.name'] = {namespace}` and the window) | Count | Fingerprint key |
| --- | --- | --- | --- |
| `error-logs` | `default.otel_logs` where `lower(SeverityText) IN ('error','fatal') OR SeverityNumber >= 17` (live rows carry `error` with number 0 as well as 17) | rows | `ServiceName` plus `LogAttributes['exception.type']` when present, else the first 80 characters of `Body` with digits and hex runs replaced by `#`. `hello-ts` logs with pino, whose `err` object reaches ClickHouse as `LogAttributes['err.type']` and `['err.stack']` (read from a live row on 7 October); the collector falls back to those when the OpenTelemetry `exception.*` attributes are empty |
| `error-spans` | `default.otel_traces` where `StatusCode = 'Error'` | rows | `ServiceName` plus `SpanName` |
| `unexpected-service` | `default.otel_logs` and `default.otel_traces` where `ServiceName` is non-empty and differs from `expected_service` (pod logs without a service name are normal and ignored) | rows | the unexpected `ServiceName` |
| `missing-telemetry` | `default.otel_metrics_sum` (`TimeUnix`, `ServiceName = expected_service`) | points | the literal `no-metrics` |

Pre-filter rules, evaluated per finding in this order; the first match is the recorded reason:

1. `repeat-inside-cooldown`: the fingerprint exists in state and `now < cooldown_until`. Does not fire.
2. `missing-expected-signal`: signal `missing-telemetry` counts 0 and the baseline mean is above 0. Fires. This signal counts healthy points, so it is the only rule (after cooldown) that applies to it; any other `missing-telemetry` result is `quiet-window`.
3. `error-rate-change`: `count > rate_ratio * mean(baseline)` and `count >= rate_min_count`. Fires.
4. `new-fingerprint`: the fingerprint is not in state and `count > 0`. Fires.
5. `quiet-window`: anything else. Does not fire.

If several findings fire, the collector builds a bundle for one: highest severity, then highest count, then the signal's position in the app's `signals` list, then lowest fingerprint. The others stay `fired-deferred` in the report and are reconsidered next sweep. If `max_analyses_per_day` or the monthly refusal threshold is reached, no bundle is written and the report says `budget` with `budget` set to `daily` or `monthly`; only `monthly` produces a message.

**Fingerprint.** The first 24 hex characters of SHA-256 over the compact, key-sorted JSON of `{repository, app, env, signal, key}`. It never includes the time window: the Stage 1 `fingerprint()` hashes `window_start` and `window_end`, which makes every sweep a new incident, and is corrected in task 2.

#### Handoff files

| File | Writer | Fields |
| --- | --- | --- |
| `/work/collect/report.json` | `collect` | `status` (`quiet`, `fired`, `budget`, `failed`), `reason` (a [failure reason](#failure-reasons) or null), `trigger` (`sweep`, later `rule:<id>`), `window` (`start`, `end`, ISO-8601 with zone), `findings` (list of `fingerprint`, `app`, `env`, `signal`, `key`, `count`, `baseline_mean`, `decision`, `reason`, `coverage` as `covered` or `alert-gap`), `counts` (per `app/env/signal`, this window's count for the baseline) |
| `/work/bundle/bundle.json` | `collect`, only when `status` is `fired` | `fingerprint`, `repository`, `app`, `env`, `signal`, `window`, `image` (`tag`, `digest` from the HelmRelease), `logs`, `metrics`, `traces` (each at most 20 redacted records restricted to the approved fields in Stage 0), `links` (ClickStack query links). At most 64,000 bytes after redaction. `base_revision` is not part of the 2a bundle: no source is touched before Stage 3, where the verifier resolves the image tag's short SHA to a full commit. |
| `/work/diagnosis/status.json` | `analyse` | `status` (`skipped` when there is no bundle, `disabled` in collect-only mode, `ok`, `timeout`, `error`), `cli`, `model`, `seconds`, `usage` (token counts if the CLI reports them, else null) |
| `/work/diagnosis/diagnosis.json` | `analyse`, only when `status` is `ok` | The Codex final message, constrained by `diagnosis.schema.json`; at most 64,000 bytes. Evidence references are strings of the form `logs[0]`, `metrics[2]`, `traces[1]` indexing the bundle lists. |

#### State

`incident-review-state`, key `state.json`, at most 64,000 bytes (Kubernetes allows more; the ceiling keeps the state compact by design):

```json
{"version": 1,
 "last_sweep": 0, "last_result": "quiet",
 "incidents": {"<fingerprint>": {"app": "", "env": "", "signal": "", "first_seen": 0, "last_seen": 0,
   "last_analysis": 0, "cooldown_until": 0, "status": "", "coverage": "alert-gap",
   "cli": "", "model": "", "notified": 0, "pr": null, "check": null}},
 "baselines": {"<app>/<env>/<signal>": [0]},
 "spend": {"month": "2026-10", "cents": 0, "analyses": 0, "day": "2026-10-06", "analyses_today": 0},
 "seen": {}, "pending": []}
```

- `status` is one of `suppressed-low-confidence`, `suppressed-uncited`, `no-change`, `no-diagnosis`, `notified` (`seen` and `analysed` are reserved). An incident enters state only when a model call was started for it, so a finding that was deferred, blocked by budget or seen in collect-only mode fires again on the next sweep. `last_result` is `quiet`, `collect-only`, `analysed`, `budget` or `failed`.
- `baselines` keep the last `baseline_hours` counts; `seen` and `pending` are the dedupe map and durable outbox with the same meaning as in `notifications/state.py` and `delivery.py`.
- Before each save: drop incidents whose `last_seen` is older than `incident_retention_days`, reset `spend` when the month or day changes, then check the size. Over the ceiling after pruning is `state-size`, a failure.
- **Single writer:** only `decide` writes. `collect` reads state for the pre-filter and never patches it. `Forbid` concurrency makes a write conflict impossible in normal operation, so writes use the same merge patch as the notification checker (field manager `incident-review`), with no resource-version check.
- An invalid `state.json` is `state-corrupt`: stop, do not reset. A ConfigMap that cannot be read or patched is `state-unavailable`. An absent key on first run means empty state.
- Never holds prompts, bundles, log text or Secret values.

#### Notification

Sent with `notifications.delivery.publish` through the outbox, to `NTFY_REVIEW_URL`. At most one diagnosis message per fingerprint per cooldown.

| Field | Source |
| --- | --- |
| Title | `<app>/<env> diagnosis` |
| Priority and tags | `severity` for the signal in the allowlist; tag `incident-review` |
| Message | Validated `summary` and `likely_cause`, `confidence` as a percentage, signal, count against baseline, window, trigger, up to three cited evidence lines (already redacted, 200 characters each), "code fix attempted: no" in Stage 2 |
| Click | The first ClickStack link in the bundle |

The ClickStack link format is not yet known: task 3 records one hand-made HyperDX search URL for a namespace and window and derives the template from it; until then the message carries the signal, namespace and window as text and no `click`.

#### Failure reasons

A fixed vocabulary; no free text from telemetry, the provider or exceptions reaches a notification or a log line.

| Reason | Raised by | Meaning |
| --- | --- | --- |
| `query` | collect | ClickHouse unreachable, error, or a limit was hit, or the HelmRelease list could not be read; no partial bundle |
| `redaction` | collect | A record could not be reduced to the approved fields |
| `state-unavailable`, `state-corrupt`, `state-size` | collect, decide | The ConfigMap cannot be read or patched; its content is invalid; it is over the ceiling after pruning. See State |
| `contract` | any | A handoff file is missing, malformed or the wrong version |
| `provider` | analyse | `codex` exited non-zero or produced no final message |
| `timeout` | analyse | The 300-second cap was reached |
| `schema` | decide | The diagnosis failed `validate_diagnosis` (shape, bounds or an uncited reference) |
| `delivery` | decide | ntfy rejected the message; it stays in the outbox |

**One failure path.** For any reason above, `decide` records `last_result: failed`, queues one `incident review failed` message naming the reason (high priority, deduplicated per reason for 24 hours, no evidence) and exits 1. The exception is a state failure in `decide` itself: with no state there is nothing to deduplicate against, so it sends nothing, exits 1 and leaves the report to the heartbeat. `schema` and `provider` also set the incident's status and cooldown so a bad response is not retried every hour. Budget refusal is not a failure: one `incident review paused: budget` message per month, exit 0. When `decide` cannot run at all (image pull, crash in an earlier container, API down), nothing is sent from the pod and the **heartbeat** reports the stale CronJob. This replaces the earlier wording that sent failures only through the heartbeat.

### Stages and acceptance gates

**Stage 0 — Provider and data handling (approved).**

Approved by the operator on 5 October 2026: option A (OpenAI API key and Codex CLI), as a two-step approval, with the `hello-ts` pilot, the spend limits and compact-metadata retention. On 6 October they selected ConfigMap state, authorized provisioning a dedicated OpenAI project credential and confirmed the one-pod layout. No reviewer Secret exists in Git or the cluster.

| Item | Approved value |
| --- | --- |
| Provider and agent | OpenAI API project with Codex CLI (`codex exec`); alternatives considered: provider-neutral API (B), local model (C), no provider (D) |
| Step 1 (stage 2) | Redacted evidence bundles only. No source code leaves the host. |
| Step 2 (stage 3) | Sharing source with the provider needs a separate operator approval after the stage 2 gate passes. |
| Per-run cap | $0.50 and 5 minutes |
| Monthly cap | $20 hard cap on a dedicated OpenAI project; the job refuses new runs at 80% ($16) |
| Model and CLI | Codex CLI 0.160.1 (pinned in `images/incident-review-worker/Dockerfile`); the model is `gpt-6-luna` (decision 7), set in `allowlist.yaml` and as `CODEX_MODEL` on the `analyse` container; both are recorded with every analysis |
| Fields sent | message, level, timestamp, service, exception type, top 10 stack frames, route path (no query string), image revision. Excluded: request and response bodies, headers, user identifiers, query strings, anything redaction flags. |
| Provider data use | API terms with training disabled, confirmed in the project settings before the first run |
| Retention | Incident fingerprint, timestamps, model/CLI version, status, notification/PR links and check result; no prompts or raw evidence; extended on 6 October by the numeric fields in decision 4 |

- **Spend enforcement.** The $20 hard cap is set on the OpenAI project by the operator. The reviewer's own counter charges every started model call `run_cost_cents` (the full per-run cap, so it over-counts) and refuses at `monthly_refuse_cents`: 32 calls a month. Token-based cost can replace the flat charge once a model and its prices are pinned. The 5-minute cap is the `timeout` on `analyse`.
- **Key handling.** The worker reads the key file, passes it to `codex login --with-api-key` on stdin, runs `codex exec` with `CODEX_HOME` on its scratch volume and exits; the pod's volumes are discarded with it. Never a developer's interactive login, never in the image, never in argv or the environment of another container. The agent can read its own inference credential, which is why that container holds nothing else. See [Codex authentication](https://developers.openai.com/codex/auth).
- **Claims to verify, not facts.** (1) That the pre-filter and field limits keep bundles small is measured at the 2a gate. (2) Option A is the lowest-effort route to a repo-aware coding agent, not the only one; whether Codex can use a non-OpenAI endpoint is unchecked. (3) Redaction is tested only on synthetic fixtures until the 2a privacy review. (4) The Codex flags the wrapper uses (`exec --sandbox read-only --skip-git-repo-check --ephemeral --ignore-user-config --model --cd --output-schema --output-last-message`, the prompt on stdin as `-`, and `login --with-api-key`) were confirmed against the help of CLI 0.160.1 on 7 October; none has been exercised with a real key. (5) The CLI's sandbox for model-requested shell commands needs bubblewrap and the right to mount `/proc`; in a container with no capabilities it fails (`bwrap: Can't mount proc`), so in the reviewer pod the model cannot run commands at all (confirmed in the cluster on 8 October: `codex sandbox` exits at once with `error building bubblewrap command: Read-only file system`; it does not hang). Stage 2 needs none, but whether `codex exec` still returns its final message when a command is refused is unverified until task 11, and Stage 3 (workspace-write) needs a different isolation design. (6) Whether the provider accepts every keyword in `diagnosis.schema.json` (`minItems`, `maxItems`, `minimum`) as an output schema is unverified; `validate_diagnosis` enforces them regardless.

**Gate:** passed for data, spend and retention. Still required before the first provider request: the real key in `incident-review-openai` and the project's data-use setting verified. Any change to provider, unredacted data or repository scope is a separate approval.

**Stage 1 — Offline evidence and policy prototype (complete; evidence in [current state](current-state.md#offline-ai-incident-review-prototype-5-october-2026)).**

Delivered in `tools/swhurl/incident_review/`: redaction, bounded query-response reading, Codex output decoding, `validate_diagnosis`, `validate_patch` (repository, paths, five files, 200 changed lines, no manifests, lockfiles, symlinks or binaries), `check_patch_applies` against the exact base revision, the injectable `ModelAdapter` and `GitHubAdapter` protocols with validation wrappers, fixtures under `tests/fixtures/incident-review/` and `make incident-review-dry-run` (no external calls). The gate passed offline on 5 October 2026.

Gaps between that prototype and this contract, each closed by a 2a task: the fingerprint includes the window (task 2); the pre-filter takes precomputed `inside_cooldown` and `baseline_count` flags instead of reading state (task 2); queries use non-existent columns and there is no metrics query (task 3); the allowlist is Python constants (task 1); the bundle has no version, links or image fields and requires `base_revision` (task 3); `ALLOWED_PATHS` and the path pattern in `validate_patch` say `tests/` where `hello-ts` uses `test/` (task 1).

**Stage 2 — Read-only signal collection and analysis notification.**

#### Stage 2a tasks

One commit per task, each with its documentation, `make check` before the commit, and no live credential or cluster change before task 8. Tasks 1 to 7 are offline and need no operator action beyond the open decisions they name.

| # | Task | Touches | Done when |
| --- | --- | --- | --- |
| 1 | **Done 7 October 2026.** Allowlist file and loader with strict validation (unknown keys, apps or signals are refused); remove the constants it replaces (byte, file and line limits stay in code) | `incident_review/allowlist.yaml`, new `allowlist.py`, tests | Loader tests cover every refusal; `make incident-review-dry-run` output is unchanged apart from the source of its limits |
| 2 | **Done 7 October 2026** (`tools/swhurl/statestore.py` is the shared helper). State module and pre-filter: the [State](#state) schema, pruning, size ceiling, corrupt-state refusal; fingerprint without the window; pre-filter reading state and the allowlist defaults. Generalise the ConfigMap read and merge-patch in `notifications/state.py` into a helper taking name, namespace, key, size and field manager, and use it from both | `incident_review/state.py`, `notifications/state.py`, fixtures, tests | A fixture per pre-filter rule, including two sweeps of one failure producing one fingerprint and one model call; existing notification tests pass unchanged |
| 3 | **Done 7 October 2026**, except the link template (bundles carry `links: []` until one real HyperDX search URL is recorded here). Collector: the four signal queries, a ClickHouse HTTP adapter behind a protocol with a fake, bundle and report writers, HelmRelease image lookup through `Runner`, link template | `incident_review/collect.py`, fixtures, tests | Fake-backed tests for each signal, each failure reason the collector raises, the 64,000-byte and 20-row limits, redaction to the approved fields and "quiet writes no bundle" |
| 4 | **Done 7 October 2026.** Decide step: decision function, message rendering, outbox delivery through `notifications.delivery`, failure path, budget accounting | `incident_review/decide.py`, tests | A table-driven test maps every combination of report status, diagnosis status and validation result to one decision, one state change and one exit code |
| 5 | **Done 7 October 2026.** Commands and dry run: `incident-review-collect` and `incident-review-decide` in `tools/swhurl/__main__.py`; `make incident-review-dry-run` runs collect, a fake analyse and decide over fixtures in a temporary `/work` with no external calls | `__main__.py`, `Makefile`, `docs/commands.md` | The dry run prints the report, decision and rendered message and still reports zero external calls |
| 6 | **Done 7 October 2026**, built in CI but not published: publishing creates a public GHCR package only the operator can delete, so the publish job and the `images.py` pin move to task 9 and need the operator's confirmation. Worker image: Codex CLI 0.160.1 pinned by SHA-256 (like the `tools` stage of `images/console/Dockerfile`), wrapper script (bash, `set -euo pipefail`), prompt template | `images/incident-review-worker/`, `.github/workflows/validate.yml` | The image builds in CI; the wrapper, given no bundle, writes `skipped` and never starts `codex`; given a bundle and a stub `codex` on `PATH`, it writes `ok`, `timeout` or `error` correctly and exits 0 |
| 7 | **Done 7 October 2026.** Heartbeat: the single hard-coded CronJob in `notifications/heartbeat.py` is now the `WATCHED` table, with per-job maximum age, messages and incident flag (the first job's flag stays at the top level of the existing file, others go under `others`). The reviewer's row is **not** added yet: the host timer runs this checkout, and a missing or suspended CronJob is reported as stale. The row (`incident-review/incident-review`, stale after 130 minutes) and the `verify-platform` check are part of task 10 | `heartbeat.py`, tests, `docs/operations.md` | `heartbeat_action` stays pure; existing heartbeat tests pass unchanged and new ones cover a second job; the host timer needs no reinstall because only Python changed |
| 8 | **Operator, partly done 8 October 2026.** Done: decision 7; `incident-review-clickhouse` (a copy of the `app` password, kept equal by `make check-secrets`) and `incident-review-ntfy` (a new topic `swhurl-diagnoses-<random>`) created without printing values; `incident-review-openai` committed with the `REPLACE_ME` placeholder. Left for the operator: create the OpenAI project with the $20 cap and training disabled, put its key in with `sops platform/incident-review/secret.sops.yaml`, and subscribe to the topic (read it with `sops decrypt platform/incident-review/secret-ntfy.sops.yaml`) | `platform/incident-review/*.sops.yaml`, `secrets_check.py` | `make check-secrets` passes (it fails on the placeholder until the key is set); the plan records the project's data-use setting and date |
| 9 | **Done 8 October 2026.** Worker image published as public package `ghcr.io/samclement/swhurl-incident-review-worker` (operator-confirmed) by `publish-incident-review-worker.yml`; `tools/swhurl/images.py` pins both of the reviewer's images from lines marked `# image: console` and `# image: worker`; namespace, unit `platform-incident-review`, CronJob, RBAC, egress NetworkPolicy, state ConfigMap | `platform/incident-review/`, `clusters/home/platform.yaml`, `infra/base/namespaces.yaml`, `images.py`, `tests/test_incident_review_manifests.py`, docs | Live: both publish runs pinned the manifest themselves; a manual Job completed (`collect` quiet, `analyse` skipped, `decide` saved state); the egress policy and the per-container mounts were proven from the running pod (evidence in current state) |
| 10 | **Started 8 October 2026.** Collect-only week: the CronJob is unsuspended with `INCIDENT_REVIEW_MODE=collect-only` on `analyse` (it reports `disabled`; no model call). `make incident-review-status` and `make incident-review-bundle` exist. After the first scheduled success, add the reviewer's row to the heartbeat's `WATCHED` table and a `verify-platform` check. Left: the operator's privacy sign-off on a real bundle (`make incident-review-bundle`), a week of sweeps, and the ClickHouse and node CPU comparison against the days before 8 October | manifests, `inspect.py`, `Makefile`, `heartbeat.py`, `verify.py`, docs | Sweeps record `quiet`; an induced failure (`kubectl -n hello-ts-staging port-forward` to the app, then six or more requests to `/repeat?times=-1`, enough to pass `rate_min_count`) records `fired` with the expected fingerprint on two consecutive sweeps; the operator signs off the fields in a real bundle |
| 11 | Enable analysis: remove the switch; induce the pilot failure; exercise a provider outage (wrong endpoint or revoked key) and a stale job | manifests, `docs/current-state.md` | One diagnosis notification with working evidence; `incident review failed: provider` received once; the heartbeat's stale and recovery messages received |

**Gate (2a):** a signed-off privacy review of the fields actually sent; repeated synthetic incidents group to one fingerprint; a quiet window makes no model call; model output cannot trigger writes; notifications link to matching evidence; provider outage and stale-job behaviour are visible; cost and latency measured for at least one week; ClickHouse and node CPU compared over at least a day with a pre-change baseline (section 0, open work 6); the reviewer runs with resource limits. A PriorityClass is not required: none exists in this repository and the Job's requests are small.

*2b — coverage review.* Each sweep's findings carry `coverage`, computed from `alert_rules` in the allowlist. The operator reads them weekly with `make incident-review-status` and, for each `alert-gap`, either writes a ClickStack rule and adds it to `alert_rules` (a commit) or records "not worth alerting" in `docs/current-state.md`. The reviewer never reads ClickStack's rule store and nobody edits the state ConfigMap by hand.

**Gate (2b):** at least four weekly reviews completed; the alert-gap count and its disposition recorded in `docs/current-state.md`.

*2c — alert trigger (starts only after the 2a gate and at least one proven rule).* First verify, and record here, that HyperDX webhooks can reach an in-cluster receiver and what they carry. The reviewer has no right to create Jobs, so a webhook cannot start a run directly. Design to confirm at that point, with a `decision-brief`: a small alert-intake receiver (Secret and pod-selected NetworkPolicy as in `platform/flux-webhook`) that records only an allowlisted rule identifier and a time window as a pending request, and a more frequent collector schedule that is a no-op unless a request is pending; or no receiver at all and the rule's saved search evaluated by the sweep. Payload text is never passed to the model or used to choose scope. Alert-triggered runs use the same pre-filter, validation, notifier and state. Add a dead-man check: the sweep reports when errors exist but no alert-triggered run happened for N days.

**Gate (2c):** an alert-triggered run for a proven rule matches the sweep's diagnosis for the same incident; the dead-man check is exercised; the sweep still runs.

**Stage 3 — Isolated patch and test worker; no PR creation.** Needs the separate source-sharing approval and its own task list, written when 2a passes.

1. For a high-confidence diagnosis in the chosen incident class, fetch the exact allowlisted repository at the commit of the running image into a fresh workspace. Pin dependencies or use the app's locked build environment. No writable host path, no workspace reuse.
2. Run `codex exec` with a task containing the diagnosis, evidence references and app-specific instructions, with workspace-write access to the checkout only; bound CPU, memory, time, file changes, tool calls and output. No Kubernetes credentials, provider administration keys, broker token or live application Secret. Restrict outbound network by hostname through an allowlisting proxy (model endpoint and package registries) or pre-fetch locked dependencies and give the build no network. Choose the mechanism at the start of this stage; do not start without one.
3. The worker's own check runs are advisory, because they execute model-authored code beside the model key. The orchestrator decides which checks run (`checks` in the allowlist). The **verifier**, a separate pod with no model key and only dependency-fetch network, reapplies the patch to a clean checkout and runs them; only its report counts.
4. Send the analysis and a diff summary in the notification; publish no branch or PR. A human applies and reviews a few candidates.

The patch artifact is the Stage 1 shape: `{repository, base_revision, files: [{path, diff}]}`, validated by `validate_patch`. Its **digest** is the SHA-256 of the compact, key-sorted JSON of that object; the verifier's report and the broker both carry and compare it.

**Gate:** at least five consecutive candidate runs for the pilot class with no out-of-policy file change or credential access, and the human reviewer judges evidence and patch quality acceptable. Track false diagnoses, useful fixes, check pass rate, runtime and cost; stop if the worker repeatedly makes broad or speculative edits.

**Stage 4 — Draft PR broker for one repository.**

1. A separate broker job accepts only a validated patch artifact plus repository, base SHA, incident fingerprint and check report. It revalidates the allowlist, path and size policy, base SHA and patch digest; it accepts no model-authored repository URL, branch name or API call.
2. The broker's credential is limited to the single pilot repository with branch-content write and pull-request creation only (open decision 8). The worker never receives it. Do not reuse the console's `GITHUB_TOKEN` or `APP_REPOS_TOKEN`. Reuse the console's `GitHubAPI` client and `open_pr` flow in `tools/swhurl/console/changes.py`; GitHub's API takes file contents, not diffs, so the broker applies the patch to a clean checkout at the base SHA and submits the resulting files. `GitHubAdapter.create_draft_pull_request` keeps its signature and the implementation does the apply.
3. The broker creates a uniquely named branch and a **draft** PR; it never pushes `main`, enables auto-merge or edits the platform repository. The PR body carries the incident and window, diagnosis confidence, evidence links, exact base image and revision, change summary and the verifier's checks, with an `ai-generated` marker and the fingerprint.
4. If an open PR already exists for that fingerprint and repository, do not update it: attach the new evidence to the incident record and notify with the existing PR link.
5. Required CI and human review remain the merge gates; Flux deploys after merge. Observe the same signal afterwards and send a recovery or recurrence update; no automatic revert.

**Gate:** one repository only; the draft PR passes normal CI and is human-reviewed; there is no path from model output to merge or deployment; the post-merge signal is checked and linked to the PR.

**Stage 5 — Expand by evidence, not by default.** Add repositories and incident types one at a time, each with its allowlist entry, check commands, failure fixtures and rollback approach. Consider API-based triage only after a representative eval set exists; provider choice must not alter the deterministic gates. Auto-merge stays out of scope unless a later explicit decision defines a narrow class and independent safeguards.

### Component design contracts

Each component has one job, a narrow interface and a stated failure behaviour. Nothing below may be widened without a fresh review. "Credentials" lists everything the component can read; anything not listed is denied. In Stage 2a the first four rows are the three containers of one pod (orchestrator and notifier are both `decide`), so "Network" is what the component uses, while the enforced limit is the pod's policy in [Runtime layout](#runtime-layout-stage-2a).

| Component | Runs as | Job | Credentials | Network it uses | Writes |
| --- | --- | --- | --- | --- | --- |
| Evidence collector | `collect` | Run the pre-filter; build the bounded, redacted bundle | ClickHouse read-only account; Kubernetes token (get state, list HelmReleases) | ClickHouse, Kubernetes API | `/work/collect`, `/work/bundle` |
| Analysis worker | `analyse` | Diagnose from the bundle; in Stage 3, propose a patch | Model key only | Model endpoint | `/work/diagnosis`, its scratch |
| Orchestrator | `decide` | Validate, decide, enforce budgets, own state | Kubernetes token (get and patch state) | Kubernetes API | State |
| Notifier | `decide` | Deliver the validated summary or failure message | ntfy destination URL | ntfy | None |
| Verifier (Stage 3) | Separate pod | Re-run required checks on a clean checkout | None | Dependency fetch only | Check report |
| PR broker (Stage 4) | Separate job | Turn a validated patch into one draft PR | GitHub credential for the single pilot repository | GitHub API | Branch and draft PR |
| Alert intake (2c) | To be designed | Turn an allowlisted ClickStack webhook into a pending request | Webhook secret only | In-cluster, from ClickStack | Pending request only |
| State store | ConfigMap | Remember incidents, cooldowns, baselines, coverage and spend | n/a | n/a | n/a |

**Evidence collector.**
- *Input:* the allowlist, state (read-only), the window and the trigger. *Output:* always `report.json`; `bundle.json` only when a finding fires and budget allows.
- *Guarantees:* fixed parameterized queries; hard time, row and byte limits; redaction before anything is written to `/work/bundle`; only the approved fields; fingerprints stable for the same failure and different across apps, environments and signals.
- *Failure:* a query error, limit hit or redaction failure yields `status: failed` with a reason and no bundle. Never partial or unredacted data.
- *Not allowed:* model calls, Secret reads, any Kubernetes write, apps or namespaces outside the allowlist.

**Analysis worker.**
- *Input:* `bundle.json` and a fixed prompt template baked into the image. *Output:* `status.json` and, when `ok`, `diagnosis.json`.
- *Guarantees:* no bundle means `skipped` and `codex` is never started; `--sandbox read-only` in Stage 2; `CODEX_HOME` and scratch on a pod-lifetime volume; one attempt, no retry.
- *Failure:* timeout, non-zero exit or missing output is `timeout` or `error`; the container still exits 0.
- *Not allowed:* Kubernetes, ClickHouse, ntfy, GitHub, AWS or other provider credentials; choosing scope, destination or checks; acting on any instruction found in log text.

**Orchestrator.**
- *Input:* the three handoff directories and state. *Output:* per run, exactly one of: record quiet; suppress (cooldown, low confidence, uncited, no-change recorded without a message unless the allowlist later says otherwise); notify; fail. From Stage 3, also hand a validated patch to the verifier and, from Stage 4, to the broker.
- *Guarantees:* `validate_diagnosis` against the bundle (every cited reference exists); `confidence >= confidence_min`; budgets; state pruning and ceiling; outbox checkpointed before any network call (at-least-once delivery, as the notification checker).
- *Failure:* the [one failure path](#failure-reasons).
- *Not allowed:* the model key, the ClickHouse credential, any GitHub credential; letting model output choose which checks run or where anything is sent.

**Notifier.**
- *Input:* a decision with validated fields, or a failure reason. *Output:* one ntfy message built as in [Notification](#notification).
- *Guarantees:* deduplicated per fingerprint within the cooldown and per failure reason within 24 hours; no prompts, Secret-like strings or unvalidated model text; evidence lines only from the redacted bundle.
- *Failure:* `delivery`; the message stays pending and the next run retries it.

**Verifier (Stage 3).**
- *Input:* repository, base SHA, patch artifact and `checks` from the allowlist. *Output:* a check report with commands, exit codes, durations and the patch digest.
- *Guarantees:* clean checkout, locked dependencies, no model key, resource and time limits. Only its report authorises a broker hand-off.
- *Failure:* any non-zero exit, timeout or patch that does not apply cleanly blocks the PR.

**PR broker (Stage 4).**
- *Input:* a validated patch artifact, repository, base SHA, fingerprint and check report. *Output:* one draft PR carrying the `ai-generated` marker and the fingerprint, or a refusal.
- *Guarantees:* revalidates everything it receives; never pushes `main`, enables auto-merge or touches the platform repository; one open PR per fingerprint and repository.
- *Failure:* a GitHub or policy error leaves no branch behind and records the refusal.

**Alert intake (2c; not built before the 2a gate).**
- *Input:* a ClickStack webhook. *Output:* one pending request carrying only an allowlisted rule identifier and a time window.
- *Guarantees:* authenticates the webhook; rejects unknown rule identifiers; discards the payload body; rate-limited.
- *Failure:* malformed or unknown requests are dropped and counted; the sweep still covers the incident.
- *Not allowed:* model calls, ClickHouse access, starting workloads.

**State store.** As specified in [State](#state): one writer, a size ceiling, pruning, and a stop on corrupt content.

**Contract tests.** A fixture for each pre-filter rule (quiet window means no model call); fake-backed tests for each component's guarantees and failures before any live wiring; manifest-policy tests that the service-account token, each Secret and each `/work` volume are mounted only where the layout table says; a live check in task 11 that the `analyse` container has no token file and cannot read the ClickHouse or ntfy Secret.

### Component checklist and documentation updates

- Work through the `new-component-checklist` skill in task 9 (retention, credentials, logs reaching ClickStack, `verify-platform` coverage, backups: the state ConfigMap is disposable and not backed up).
- SOPS rules already cover `platform/**/*.sops.yaml`. Secret values are never printed or included in evidence, notifications, prompts, artifacts or PRs. Provisioning or rotating real credentials follows the confirm-first rule.
- Reloader does not watch `incident-review`; a CronJob reads its Secrets afresh each run, so none is needed.
- Make targets: `incident-review-dry-run` (offline, exists), `incident-review-status` and `incident-review-bundle` (task 10). A controlled live test uses the pilot app's staging environment; it creates no repository.
- Documentation, each in the task that changes the behaviour: `docs/services.md` (alert ownership, data flow, the new sender), `docs/architecture.md` (unit, credentials), `docs/operations.md` (key rotation, disable by `suspend`, failure reasons, state reset), `docs/commands.md` (each target), and `docs/current-state.md` only after dated live evidence exists.
- Keep this section as the implementation contract. Update section 0 and add dated evidence only as each stage passes; do not describe an unexercised stage as live behaviour.

### Stop conditions

A quiet pre-filter is not a stop condition: it ends the run normally with no model call. Stop the run, send nothing derived from model output and create no PR if: evidence cannot be reduced to the approved fields; the diagnosis fails schema, citation or confidence validation; state is corrupt or over its ceiling; a budget is exhausted; the provider is unavailable or times out; and, from Stage 3, the repository or base revision differs from the allowlist, the patch touches forbidden paths or exceeds its size limits, required checks fail, or the broker is unavailable. Each maps to one [failure reason](#failure-reasons) or a recorded suppression, and no raw evidence outlives the pod.

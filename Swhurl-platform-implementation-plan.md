# Swhurl Platform — implementation plan

27 September 2026 · Home cluster implementation plan · **paused 28 September 2026; cleanup planned**

## 0. Where this paused and what is left

Work paused on 28 September 2026 after PR07b. Everything in the delivery table (section 4) is done except **PR06**, the remainder of **PR08a**, and the **final operator exercise**. Live evidence for each step is in `docs/operations/current-state.md`. Before resuming: pull `main`, run `make validate-repo`, `make test-safety`, `make verify-platform` and `flux get kustomizations` (13 units, all Ready at pause), and re-read that file's "Not exercised" notes.

**Remaining plan work**

1. **PR06 — GHCR publishing and Renovate** (section 11). Needs decisions only you can make: which app repository goes first; hosted Renovate (GitHub App) or self-hosted; public or private GHCR images (private needs pull credentials per namespace). A smaller first step needs no app repo: point Renovate at this repo's pinned charts (app-template, reloader, cert-manager, ClickStack, OTel, oauth2-proxy, MinIO) and the `hello` image digest, with Flux manager file patterns for `clusters/` and `tenants/`.
2. **PR08a remainder** (section 13): choose an off-host backup destination, then schedule `make backup-clickstack-mongodb` (deliberately manual until then; copy `~/.local/state/swhurl-platform/backups` to the USB meanwhile). Also restore on a separate machine, and bring HyperDX up against restored data.
3. **Final operator exercise** (section 14), using only the docs.
4. ~~Documentation restructure~~ done 28 September 2026: task-based pages in `docs/` with one canonical page per topic (map in `docs/contributing.md#documentation`), `AGENTS.md` trimmed. The `document-repo` skill used for it is at `.claude/skills/document-repo/SKILL.md` (untracked): decide whether to commit it.

5. ~~Operator tooling in the right language~~ done 28 September 2026: move logic (parsing, safety decisions, Secret handling, polling, live-test assertions) from bash into a tested Python package; keep short glue, streaming host scripts and systemd units as linted bash. The `make` interface does not change. Sub-plan: [Swhurl-platform-tooling-plan.md](Swhurl-platform-tooling-plan.md).

6. **Cleanup** (not started): the repository review below, recorded 28 September 2026. Item numbers follow the review.

**Cleanup plan**

A review of boundaries, technology choices, bloat and responsibilities, made during tooling phase 3 and checked against the code after the tooling sub-plan finished. Work top to bottom: tooling-only items first, because tests cover them and nothing deployed changes; then the decisions that change what runs; then all renames and moves together in one planned Flux migration.

Done since the review: #7 (the runner no longer checks for a test-only attribute) and #10 (the phase 3 bash scripts are deleted).

*Step 1: tooling only (low risk: covered by tests, no deployed change)*

| # | Item | Fix |
| --- | --- | --- |
| 2 | `COOKIE_DOMAIN`, `AUTH_MIDDLEWARE` and the cookie-domain check are defined in both `app_new.py` and `app_policy.py` | One contract module (for example `swhurl/apps/contract.py`); `app-new` runs the policy on what it generated before exiting |
| 3 | Platform knowledge duplicated: the `platform-settings` path in three modules; `verify_config` finds `OAUTH_HOST` by regex; the team-key MongoDB script differs between `verify.py` and `recovery.py`; `REQUIRED_SECRETS` listed by hand | A small `swhurl/platform.py` for paths, names and shared queries; derive required Secrets from each unit's `decryption` |
| 6 | `validate.py` and `app_policy.py` create a `Runner` at import, so tests cannot inject `FakeRunner`; `validate.py` stops at its first error | Pass the runner in, report through `Report`, collect every failure before exiting |
| 8 | The fake-`kubectl` scenarios in `test_operator_safety.py` duplicate branches `test_verify.py` covers | Keep fake executables only for the `make` interface (exit codes, arguments, dry runs making no cluster calls) |
| 13 | Makefile: the nested `INSTALL_STEPS` expression; `help` repeats `docs/commands.md`; `TIMEOUT_SECS` default in the Makefile | `SKIP_VERIFY=1` in place of `FEAT_VERIFY`; defaults in Python; generate `help` from `## ` comments on targets. Moving `DYNAMIC_DNS_RECORDS` to host config is part of the same change |
| 19 | Modules and tests grouped by accident: `backups.py` only prunes while `recovery.py` backs up and restores; `app_new`/`app_ops`/`app_policy`; `test_operator_safety.py` mixes manifest policy with command safety; `test_swhurl_core.py` | `swhurl/recovery/` (or rename `backups.py` to `retention.py`); `swhurl/apps/{new,ops,policy}.py`; split tests into `test_manifest_policy.py` and `test_command_safety.py`; `test_runner.py` |
| + | Every test file repeats `sys.path.insert(0, ROOT / 'tools')` | One shared test helper, or `PYTHONPATH` from the Makefile only |
| 4 | Service-specific checks (ClickStack keys, ClickHouse TTLs, Traefik args, MongoDB PV) all live in `verify.py` | Group checks by service (`swhurl/checks/<service>.py`); `verify.py` runs them in order. Optional at the current size |
| 9 | App environments are full copies with nothing checking they stay equivalent | A policy check that two environments of one app differ only in namespace, host, image tag/digest, replicas and resources |

*Step 2: stale records (no risk)*

| # | Item | Fix |
| --- | --- | --- |
| 11 | Local `todo.md` (git-ignored) proposes helmfile, Go and Cilium-era work that contradicts current decisions | Delete it, or move any live item into this plan |
| + | `docs/operations/current-state.md`: the PR01 "Finding classification" section lists finished work as future; the PR05 section says a signed-in session was not tried (it has been, over HTTPS) | Correct both; consider trimming the file to current facts plus dated evidence |
| + | This plan's PR sections below section 0 are mostly history | Trim or archive once section 0 is the only live part |
| + | `.claude/skills/document-repo/` is untracked | Decide whether to commit it |

*Step 3: decisions that change what runs (medium risk: live resources change)*

| # | Item | Decision needed |
| --- | --- | --- |
| 1 | The domain is only partly configurable: `OAUTH_HOST` is substituted, but the cookie and whitelist domains, ClickStack and MinIO hosts, issuer emails and `COOKIE_DOMAIN` are literal; `CERT_ISSUER` switches only platform hosts | Either one `BASE_DOMAIN` read by both Flux and `swhurl`, or accept a single domain and drop the `OAUTH_HOST` substitution. Either way, state exactly what the certificate switch affects |
| 12 | MinIO has a unit, chart upgrades, two public hosts and certificates, but no buckets and no users | Remove it until something needs it, or record why it stays |
| 5 | No written rule separates `infrastructure/` from `platform-services/` (MinIO has an ingress yet lives in `infrastructure/storage`; namespaces are central) | One sentence in `docs/architecture.md` (for example "infrastructure = cluster primitives with no user-facing endpoint"), then move MinIO to match or remove it |

*Step 4: names and layout (high risk: Flux unit and path changes are migrations; batch them into one planned change using the PR03 procedure)*

| # | Item | Proposal |
| --- | --- | --- |
| 14 | One system has several names: `swhurl` (repo, source, package), `homelab-*` (units), `home` (cluster), `platform.swhurl.com` (labels) | Pick one prefix; at least document the mapping |
| 15 | Directories are named after products, namespaces after functions, units a mix (`oauth2-proxy` / `ingress` / `homelab-auth`) | Name units after their directories (`homelab-oauth2-proxy`) so one name leads to the others |
| 16 | Leftover levels: `platform-services/<svc>/base` and `ingress-traefik/base` have no overlays; `infrastructure/` mixes depths; `tenants/` holds only `apps/`; `docs/operations.md` sits beside `docs/operations/` | Flatten to `<area>/<component>/`; keep a subdirectory only where it is its own unit (`cert-manager/issuers`) |
| 17 | Two file-name styles: `helmrelease-oauth2-proxy-shared.yaml` vs generated `helmrelease.yaml` | Pick one (the shorter generated style reads fine inside a named directory) |
| 18 | Overlapping verbs: `verify-*`, `validate-repo`, `app-check`, `app-policy`, `secrets-check`; `test-safety` runs all unit tests; `*-test` are live tests; Python names drift from make targets (`backup-mongodb` vs `backup-clickstack-mongodb`) | `check-*` offline, `verify-*` live, `test` for unit tests, `live-test-*` for cluster tests; keep aliases for current names |
| 20 | Root plan files are capitalised and outside `docs/`; `requirements-validation.txt` duplicates dependency information | `docs/plans/implementation.md` and `docs/plans/tooling.md`; dependencies in `pyproject.toml` |

*Leave alone* (judged sound by the review): `Runner` and `Report`; the `Orphan`/`MirrorPrune` deletion split and its tests; the explicit repetition in Flux unit definitions (guarded by `make test-safety`); keeping the host scripts as bash; explicit per-environment app copies, once #9 exists.

Decisions only you can make before steps 3 and 4: the domain approach (#1), whether MinIO stays (#12), and the naming convention (#14, #15, #18).

**Known issues, deliberately not fixed yet**

- `CLICKSTACK_API_KEY` is stored double base64-encoded; the ClickStack app runs with the 48-character once-decoded text. Harmless today (it is not the team ingestion key), but fixing it restarts ClickStack with a different `HYPERDX_API_KEY`. Plan and test it; `make secrets-check` warns until then.
- The MongoDB PV's `Retain` policy is a live patch, not in Git (dynamic PV). `make verify-platform` fails if a recreated claim loses it.
- Staging and production `hello` differ only in namespace and host (same digest, issuer and sign-in). No per-instance quotas, NetworkPolicies or RBAC.
- Everything under `homelab.swhurl.com` shares the sign-in cookie. Public or untrusted apps must use another parent domain; none exist yet.

**Not yet exercised live** (each is correct by construction or test, but unproven on the cluster)

- A fresh bootstrap on a new cluster (issuer ordering, `make flux-bootstrap` from nothing).
- A real credential rotation through Reloader (only dummy-key and disposable rotations were tested), and Reloader restarting a generated app.
- A `public` app instance on a domain outside `homelab.swhurl.com`.
- Deleting a real shared Flux unit (only a disposable `Orphan` unit was deleted).
- A rejected sign-in reaching oauth2-proxy's email list (the test account was refused by Google first).

**Extension points kept out of scope:** a second cluster (EC2), tailnet-only private routes, DNS-01 and wildcard certificates, directory renaming, and Hermes.

---

PR01 is complete at `2bae8d0`. This plan is based on repository review and read-only checks of the live, single-node k3s host. Router settings, external backups, the Mac model service, and app repositories remain uninspected. Recheck live state before migrations.

## 1. Goal and scope

Make an app instance a small, reviewable definition: image digest, resources, probes, exposure, configuration, secrets, and persistence. A generator supplies the namespace, Flux Kustomization, HelmRelease, and optional encrypted Secret. Git review and Flux remain the deployment path.

Keep k3s, Flux, packaged Traefik, cert-manager, and SOPS/age. Fix current safety, identity, and telemetry faults first; then separate reconciliation, prove recovery, and build the app workflow. Work on `clusters/home/` only. Preserve clean capability boundaries for a possible future `clusters/aws/`, but do not build the EC2 cluster or move host automation now. Hermes and Mac model access are a separate follow-on project.

### Completion criteria

- Onboard a second app from an existing image in under 15 minutes, excluding DNS and certificate delays, without hand-written Deployment, Service, or Ingress.
- Each app instance has its own namespace and Flux unit; staging and production reconcile independently.
- Image and chart updates are reviewable and pinned. Promote and roll back the same image digest.
- ClickStack or MinIO failure does not block an unrelated app update.
- Exposure modes enforce their route and cookie boundaries; only approved identities can sign in.
- Secret rotation restarts only the referencing workload without printing the value.
- Suspend, uninstall, and data destruction have distinct effects. Restore one stateful workload and the age key from an independent backup.
- One documented command shows desired revision, running image, readiness, address, and failure reason.

## 2. Observed baseline and urgent faults

Use KUBECONFIG=$HOME/.kube/config for scripted kubectl/Flux checks here; the k3s wrapper may otherwise select the root-owned kubeconfig.

| Area | Observed on 27 September 2026 | Consequence |
| --- | --- | --- |
| Host | Arch Linux x86-64; one Ready node at 192.168.1.200; k3s v1.34.4+k3s1; 31 GiB RAM | This is an in-place change. Inventory datastore, server config, and disk layout before recovery work. |
| Flux | Six Kustomizations and current releases were Ready at the previously inspected revision 91e88935; PR01 is now on main at 2bae8d0 and CI passed | Recheck applied revision and inventories before moving ownership. |
| Edge | Packaged Traefik NodePorts 31514/30313; current issuers and certificates Ready | Preserve routes; verify router/DNS during cutover. |
| Storage | Four Bound local-path claims/PVs for ClickHouse data/logs, MongoDB, and MinIO; reclaim policy Delete | Namespace or claim deletion and pruning can destroy data. |
| Identity | oauth2-proxy permits email-domain: "*" and uses cookie domain .homelab.swhurl.com | Restrict sign-in. All sibling hosts under this domain receive the shared cookie. |
| Lifecycle | make teardown deletes the two root Flux Kustomizations; they and children prune. make reinstall invokes teardown, then install | Deletion can cascade through namespaces/PVs and removes the GitRepository needed by the next reconcile. |
| Telemetry | The live logging/hyperdx-secret ingestion key is 48 bytes after Kubernetes data decoding; decoding again yields 36 bytes matching ClickStack's team ingestion key. Recent collector logs contain repeated HTTP 401 token/scheme errors | The collectors receive encoded text and drop telemetry. Fix the source Secret and verifier, restart both collectors, and verify fresh telemetry. |
| Validation | PR01 removed the stale CI render path and fixed the shell check; CI passed | Extend CI to rendered Helm chart resources in PR04. |

The key comparison and log check were read-only and did not print values. They prove a live mismatch; successful telemetry after repair remains to be checked. The current verifier double-decodes and may print secrets on mismatch, so avoid normal make verify-platform or make install until corrected. Do not run live teardown/reinstall under current semantics. The dated baseline in `docs/operations/current-state.md` is observation, not restore evidence.

Other live checks: router/DNS, approved user list, Flux/Helm owners, backup destination, datastore, private GHCR access, and Mac model endpoint.

## 3. Design decisions

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

BASE_DOMAIN currently has only a non-empty check while hostnames are hardcoded. In the settings phase, make it authoritative where it owns hosts or remove the misleading setting. Keep config.env for host/local inputs and the Git-tracked platform-settings ConfigMap for cluster non-secrets.

## 4. Delivery order and gates

PR01 is complete and green. Prepare full recovery in parallel with the immediate repairs. Prove restore before deletion, ownership handover, or stateful migration.

| Order | Deliverable | Live gate |
| --- | --- | --- |
| PR01 | Inventory, validator, CI, documentation | Complete at 2bae8d0 |
| P0a | Guard destructive teardown/reinstall and correct operator docs | Complete in Git |
| P0b | Back up age key off-host and test recovery | Complete: encrypted USB copy decrypted all three Secrets |
| P0c | Fix verifier and double-encoded ingestion Secret; restart and verify collectors | Complete: live on 27 Sep 2026 after collector restart; no 401s, fresh logs/metrics in ClickHouse, verifier passes |
| P0d | Restrict sign-in to approved identities; test accepted/rejected accounts | Complete: live (`sam@swhurl.com` only); approved sign-in verified and non-approved account refused by operator test, 27 Sep 2026 |
| PR08a | Independent backup and tested restore of one stateful workload | Partial: data classified; encrypted ClickStack MongoDB backup and disposable restore proven 27 Sep 2026; off-host destination, schedule and app-level restore pending |
| PR02a | Retention defaults: telemetry 30d, ClickHouse system logs 7d, backup pruning 7 daily + 4 weekly, MongoDB PV Retain + PVC keep, `local-path-retain` class | Complete 27 Sep 2026; backups stay manual until an off-host destination is chosen |
| PR02b | Lifecycle commands (suspend/resume/destroy-data), `Orphan` shared units, prune protection | Complete 27 Sep 2026; proven by `make lifecycle-test` |
| PR03 | Capability split and cert-manager/issuer ordering | Complete 27 Sep 2026: 10 units, 22 resources handed over with no recreation |
| PR07a | Narrow Secret rollout controller pilot | Complete 28 Sep 2026: scoped opt-in Reloader for oauth2-proxy and OTel; manual refresh kept as fallback |
| PR04 | App-template contract, generator, rendered policy | Complete 28 Sep 2026: generator, `make app-policy`, fixtures proven live by `make app-template-test` |
| PR05 | Split and migrate example staging/production; operator commands | Complete 28 Sep 2026: `hello-staging`/`hello-prod` on app-template; routes cut over with seconds of default-cert gap |
| PR06 | GHCR publishing and Renovate pilot | PR04, app repository access |
| PR07b | App Secret conventions and shared settings | Complete 28 Sep 2026: conventions documented, `make secrets-check`, `BASE_DOMAIN` removed, duplicate refresh target retired |
| Final | Operator exercise and documentation | All core deliverables |

Planning range: roughly 8–14 hands-on days for the core home workflow, plus immediate fixes, discovery, and restore time. Hermes is excluded.

### Immediate repair acceptance

1. teardown/reinstall cannot silently prune a live stack. Disabling them until PR02 gives explicit semantics is preferable. A confirmation must describe namespace/PV loss and the broken reinstall path. Update `docs/INFRASTRUCTURE.md`, `docs/runbook.md`, and `docs/orchestration-api.md` alongside any command change. Dry-run remains available.
2. An age private-key copy exists outside the host and passes a controlled recovery check. Keep its location and access process in a private recovery record, never Git.
3. The verifier decodes Kubernetes data once, compares bytes without printing values, and fails safely. Correct the encrypted source Secret so the Kubernetes Secret contains the actual ingestion token. Reconcile, restart both OTel collectors, verify 401 errors stop and new telemetry arrives. Keep the ClickStack bootstrap key concept separate.
4. Replace email-domain: "*" with provider-supported approved identities. Verify approved and unapproved accounts. Document existing-session impact and review current protected routes.

## 5. PR01 — complete

PR01 at 2bae8d0 inventories the home cluster, discovers active Flux render paths for local and CI checks, checks each tracked shell script, validates Kustomize/Flux manifests, and removes stale runtime-input references. CI passed. It does not render future Helm chart internals. Live verifier, teardown, sign-in, and ingestion faults remain.

## 6. PR02 — lifecycle and retention

PR02a (retention defaults) is complete: telemetry keeps its 30-day TTL (now verified), ClickHouse system logs expire after 7 days via a Git-managed `config.d` override, backups prune to 7 daily + 4 weekly, the MongoDB PV is `Retain` and all ClickStack PVCs carry `helm.sh/resource-policy: keep`, and `local-path-retain` exists for new irreplaceable data. Backups remain manual until an off-host destination is chosen. PR02b is complete: `make suspend|resume|destroy-data`, `deletionPolicy: Orphan` on shared units (app units keep `MirrorPrune` so Git uninstall works), `observability` namespace prune protection, and `make lifecycle-test` proving the acceptance below on a disposable app. Per-instance uninstall is only as fine-grained as the units: it becomes per-app after PR03/PR05.

Replace teardown/reinstall as a normal deploy path with explicit suspend, resume, uninstall-one-instance, and destroy-named-data operations. Recreate removed Flux parents/sources through bootstrap; reconciling an absent object is not reinstall. Capture current Flux inventory and Helm owners; test deletion effects in a disposable scope. Parent/child prune: true can cascade.

Give new app instances dedicated namespaces. Protect retained namespaces and PVCs from ordinary Flux pruning and use a named local-path-retain StorageClass with reclaimPolicy Retain for new irreplaceable data after verifying local-path behavior. A PVC prune-disabled annotation alone cannot protect against namespace deletion. Inventory the four existing Delete PVs and handle any reclaim/ownership change as a backed-up migration. Do not recreate existing data for the new default.

Acceptance: suspend leaves workloads/data; a disposable uninstall affects only its instance and retains declared data; destroy requires an explicit named target; removed roots can be bootstrapped again. Git revert does not restore volume data.

## 7. PR03 — independent reconciliation

Complete. The units, dependencies and handover evidence are in `docs/architecture.md` and `docs/operations/current-state.md`. Fresh-bootstrap ordering is by construction (issuers wait for cert-manager) and was not exercised on a new cluster.


Split `clusters/home/` infrastructure, platform, and tenants into capability units: sources, namespaces, cert-manager controller, issuers, Traefik configuration, shared sign-in, ClickStack, OTel, MinIO, and each app instance. Install cert-manager CRDs before issuers. Apps depend only on services they need. Each unit declares substitutions and SOPS decryption explicitly.

Preserve resource/HelmRelease names, claims, and routes. For an ownership transfer, record inventories, suspend owners, prevent old-owner pruning, reconcile and verify the new owner, then retire old ownership. Test on a disposable resource; avoid a blind revert with pruning active. Keep capability bases reusable for a future cluster, but do not add `clusters/aws/` or move host/ now.

Acceptance: fresh bootstrap installs issuers without CRD race; ClickStack/MinIO failures do not block unrelated app updates; no duplicate ownership, Helm uninstall, route break, or PV recreation.

## 8. PR07a — Secret rollout pilot

Install a pinned, opt-in Reloader release after telemetry repair. Pilot Secret-specific annotations on oauth2-proxy and OTel collectors, with scoped watch/RBAC. Rotate a test Secret; verify intended workloads restart and unrelated ones do not. Keep manual runtime-inputs-refresh-otel until rotation and telemetry pass, then update/retire it and docs together. Do not enable global restart-all.

## 9. PR04 — app contract and generator

Use bjw-s app-template rather than maintaining charts/swhurl-app. Add one HTTP HelmRepository source at https://bjw-s-labs.github.io/helm-charts/ and pin exact app-template version in each HelmRelease. A shared OCIRepository pinned to one tag prevents independent per-app chart promotion. For a chart from GitRepository, Flux defaults to ChartVersion: source edits do not deploy until Chart.yaml's version changes, unless Revision is selected. The released chart avoids this local-chart lifecycle.

The contract has three layers:

| Layer | Responsibility |
| --- | --- |
| Generator (`tools/swhurl/app_new.py`, `make app-new`) | Creates instance namespace, Flux Kustomization, HelmRelease, Kustomize wiring, optional encrypted Secret stub; refuses overwrite/plaintext credentials. |
| Explicit instance values | Non-root where supported, no service-account token, dropped capabilities, small resources, app-specific probes, opt-in Secret reload. Defaults change by reviewable instance diff. |
| CI on rendered resources | Enforces production digest, security/resources or reviewed exceptions, ingress/cookie boundaries, named storage class, and no hostNetwork/hostPath/token mount by default. |

The HelmRelease is the main app definition; no second custom language. Add a .sops.yaml creation rule for app paths before creating encrypted app Secrets. Each app Flux unit containing them needs spec.decryption.secretRef.name: sops-age. Test without logging values.

| Input | Rule |
| --- | --- |
| Image | Immutable digest for promoted production releases; chart version pinned separately. |
| Workload | Deployment by default; worker may omit Service/Ingress; special cases may use upstream chart/raw manifests. |
| Health | App-specific readiness/startup probes, no invented /health. |
| Exposure | private: no Ingress. authenticated-web: trusted cookie domain with Traefik auth middleware. public: separate cookie-excluded domain. Machine APIs use own auth. |
| Persistence | Explicit claim/mount/size/storage class/retention; none by default. |
| Secrets | Encrypted instance-local Secret and chart references; automatic rollout after pilot. |

Add worker, authenticated-web, and persistent fixtures. Render the actual pinned chart with Helm in CI and run policy on those Kubernetes resources, as well as PR01 Flux/Kustomize checks. Verify illustrative values with helm template; they are not a tested manifest. The upstream values surface is broad, so supported use is defined by generator defaults and policy.

Acceptance: generator output renders; worker has no public route; authenticated web has middleware; public cannot use .homelab.swhurl.com; missing production digest and unreviewed privileges fail policy; SOPS secrets decrypt in their app Flux unit.

## 10. PR05 — example migration and operation

Complete. Evidence in `docs/operations/current-state.md`.


Current homelab-app-example reconciles both staging and production. Split it into two Flux units and dedicated instance namespaces. Both current overlays use letsencrypt-prod and shared sign-in, so staging presently isolates a namespace only; document that unless deliberately changed.

Current nginx:1.25-alpine runs as root on port 80. Use an unprivileged image on 8080 with compatible writable paths or approve a narrow exception. Render and compare generated resources. Test route, TLS, sign-in, readiness; switch staging first, then production. Prefer fresh resources and controlled route cutover to untested Helm adoption of raw resources. Preserve old workloads/routes until verified and prevent old Flux pruning of new ownership.

Add app-check, app-status, app-reconcile, and app-logs commands. Status shows desired/applied revision, desired/running digest, replicas, route, failure reason. Keep make install for bootstrap/full-platform use. Verify a bad image or probe in one instance does not block the other.

## 11. PR06 — registry and reviewable updates

Use GHCR for first-party images. Each app repo tests, builds, publishes a source-revision-tagged image, and captures its digest. Use package-write credentials only in publishing. Private packages need read-only pull credentials in consuming namespaces; test an uncached pull. Promote one digest between staging and production.

Pilot Renovate for app-template versions and GHCR image digests. Configure Flux manager file patterns for this repo's clusters/ and tenants/ paths; the default does not cover them. Verify source resolution and that each digest PR touches the intended instance; keep chart bumps separate. Configure private registry auth if needed. Hosted Renovate still needs its own GitHub integration/token, but avoids custom cross-repo app-to-platform PR code. Manual digest PRs remain supported. CI renders updates; merge triggers Flux.

Build x86-64 for home; add ARM64 when a consumer needs it. Revisit before any Graviton move. Retain deployed digests for rollback.

## 12. PR07b — settings and app Secret conventions

Complete. App-path SOPS rules landed with PR04. `stringData` was verified live (PR04 fixture) and is the convention for new values; conventions are in `docs/operations.md#secrets`. `make secrets-check` decrypts locally and flags placeholders and probable double encoding without printing values; it found `CLICKSTACK_API_KEY` double-encoded (recorded as a known issue, not changed live). `BASE_DOMAIN` (unused) was removed; `runtime-inputs-refresh-clickstack-otel` was retired after the Reloader rotation tests, leaving `runtime-inputs-refresh-otel` as the fallback.


Finish app-path SOPS rules and Secret authoring docs. Prefer encrypted stringData for human-authored values only after verifying SOPS/Flux apply behavior; otherwise encode once into Kubernetes data and compare decoded bytes without printing. Keep ClickStack bootstrap and OTel ingestion keys distinct. Remove stale duplicated host settings. Test disposable rotation before retiring manual restarts.

## 13. PR08a — independent recovery gate

Classify data as reconstructible, expendable telemetry, or irreplaceable. Inspect datastore, local-path placement, reclaim, and existing host backups. Choose off-host destination and application-consistent method for each irreplaceable service. In-cluster MinIO on the same disk is not the only backup. Record recovery point/time targets, retention, capacity, and required credentials.

P0b backs up the age key immediately. Integrate it into a full recovery sequence here. Rebuild a clean test scope, restore an encrypted Secret and one persistent workload, and verify app behavior. Record dated evidence before current-state deletion, Flux ownership handover, or stateful migration.

## 14. Final operator exercise

Using only current docs: onboard web and worker; publish/deploy, promote, and roll back a digest; diagnose bad image and probe; rotate a Secret without exposing it; deploy during ClickStack failure; suspend/resume; uninstall a disposable persistent app with retained state; restore workload and age key. Update relevant docs alongside every behavior change under AGENTS.md; keep README short.

After the core exercise, consider tailnet private browser access, DNS-01, wildcard certs, a second cluster, or directory renaming. Hermes remains a separate project requiring model-network design and stronger command-execution isolation than a namespace alone.

## 15. Evidence

- Repository: `docs/operations/current-state.md`, Makefile, `clusters/home/`, .github/workflows/validate.yml, scripts/validate-repo.py, scripts/verify-platform.sh; PR01 commit 2bae8d0.
- [Flux pruning and deletion](https://fluxcd.io/flux/components/kustomize/kustomizations/)
- [Flux HelmChart reconcile strategies](https://fluxcd.io/flux/components/source/helmcharts/)
- [bjw-s app-template values reference](https://bjw-s-labs.github.io/helm-charts/docs/app-template/reference/)
- [Renovate Flux manager](https://docs.renovatebot.com/modules/manager/flux/)
- [Stakater Reloader](https://github.com/stakater/Reloader/blob/master/README.md)
- [oauth2-proxy provider configuration](https://oauth2-proxy.github.io/oauth2-proxy/7.6.x/configuration/providers/)
- [Cookie Domain scope, RFC 6265](https://www.rfc-editor.org/rfc/rfc6265)
- [cert-manager Route53 DNS-01](https://cert-manager.io/docs/configuration/acme/dns01/route53/)

The live findings do not prove recovery, external reachability, or post-repair telemetry. Acceptance items are future work unless marked complete.

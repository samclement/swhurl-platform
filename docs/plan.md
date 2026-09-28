# Swhurl Platform — implementation plan

27 September 2026 · Home cluster implementation plan · **paused 28 September 2026; cleanup in progress**

## 0. Where this paused and what is left

Work paused on 28 September 2026 after PR07b. Everything in the delivery table (section 3) is done except **PR06**, the remainder of **PR08a**, and the **final operator exercise**. Live evidence for each step is in `docs/current-state.md`. Before resuming: pull `main`, run `make check-repo`, `make test`, `make verify-platform` and `flux get kustomizations` (12 units, all Ready), and re-read that file's "Not exercised" notes.

**Remaining plan work**

1. **PR06 — GHCR publishing and Renovate** (section 4). Needs decisions only you can make: which app repository goes first; hosted Renovate (GitHub App) or self-hosted; public or private GHCR images (private needs pull credentials per namespace). A smaller first step needs no app repo: point Renovate at this repo's pinned charts (app-template, reloader, cert-manager, ClickStack, OTel, oauth2-proxy) and the `hello` image digest, with Flux manager file patterns for `clusters/` and `tenants/`.
2. **PR08a remainder** (section 5): choose an off-host backup destination, then schedule `make backup-mongodb` (deliberately manual until then; copy `~/.local/state/swhurl-platform/backups` to the USB meanwhile). Also restore on a separate machine, and bring HyperDX up against restored data.
3. **Final operator exercise** (section 6), using only the docs.
4. ~~Documentation restructure~~ done 28 September 2026: task-based pages in `docs/` with one canonical page per topic (map in `docs/contributing.md#documentation`), `AGENTS.md` trimmed. The `document-repo` skill used for it is committed at [`.claude/skills/document-repo/SKILL.md`](../.claude/skills/document-repo/SKILL.md).

5. ~~Operator tooling in the right language~~ done 28 September 2026: move logic (parsing, safety decisions, Secret handling, polling, live-test assertions) from bash into a tested Python package; keep short glue, streaming host scripts and systemd units as linted bash. The `make` interface does not change. The rule for choosing is in [contributing](contributing.md#operator-tooling); the finished sub-plan was removed and is in Git history (`97faeeb:Swhurl-platform-tooling-plan.md`).

6. **Cleanup** (steps 1 and 2 done; step 3 partly done): the repository review below, recorded 28 September 2026. Item numbers follow the review.

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

*Step 4: names and layout* — decided 28 September 2026; in progress. Stages, each validated, pushed and verified live:

1. **Done:** #18 verbs: `check-*` offline (`check` runs everything CI runs), `test` for unit tests, `verify-*` live, `live-test-*` throwaway cluster tests, `backup-mongodb`; old names stay as aliases ([commands](commands.md#old-names)); Python command names match. #20: this plan is `docs/plan.md`, the evidence file `docs/current-state.md`, and the PyYAML pin is the `check` dependency group in `pyproject.toml`.
2. **Done:** canary `homelab-reloader` became `platform-reloader` at `platform/reloader` (`9d9c7bb` suspend, `81b6ccf` cutover). Suspending through Git blocks `homelab-flux-stack` for its 20-minute health-check timeout (a suspended unit never reports its new generation), so the next stage makes the old units `Orphan` instead of suspending them.
3. **Done:** the other non-root units (`44ddd85` made the two app units `Orphan`; the cutover commit followed), with #16 (one directory per unit under `infra/`, `platform/`, `apps/`; no `base/` levels) and #17 (`<kind>.yaml`, `-<name>` only to tell two of a kind apart): `infra-base`, `infra-cert-manager`, `infra-issuers`, `infra-traefik`, `platform-oauth2-proxy`, `platform-clickstack`, `platform-otel`, `app-<app>-<env>`.
4. The roots under `flux-system` (`cluster-sources`, `cluster-stack`), after a separate confirmation.

#14, #15: a unit is named `<area>-<component>` after its directory; namespaces, HelmReleases, Secrets and hostnames keep their names, so no workload changes. Each rename is a handover: suspend the old unit, then replace it (all units are `Orphan`), after proving offline that the new unit renders byte-identical objects, and live that Helm revisions and pod and volume UIDs are unchanged.

*Leave alone* (judged sound by the review): `Runner` and `Report`; the `Orphan`/`MirrorPrune` deletion split and its tests; the explicit repetition in Flux unit definitions (guarded by `make test`); keeping the host scripts as bash; explicit per-environment app copies, once #9 exists.

**Known issues, deliberately not fixed yet**

- `CLICKSTACK_API_KEY` is stored double base64-encoded; the ClickStack app runs with the 48-character once-decoded text. Harmless today (it is not the team ingestion key), but fixing it restarts ClickStack with a different `HYPERDX_API_KEY`. Plan and test it; `make check-secrets` warns until then.
- The MongoDB PV's `Retain` policy is a live patch, not in Git (dynamic PV). `make verify-platform` fails if a recreated claim loses it.
- Staging and production `hello` differ only in namespace and host (same digest, issuer and sign-in). `make check-apps` now fails if they drift further. No per-instance quotas, NetworkPolicies or RBAC.
- Everything under `homelab.swhurl.com` shares the sign-in cookie. Public or untrusted apps must use another parent domain; none exist yet.

**Not yet exercised live** (each is correct by construction or test, but unproven on the cluster)

- A fresh bootstrap on a new cluster (issuer ordering, `make flux-bootstrap` from nothing).
- A real credential rotation through Reloader (only dummy-key and disposable rotations were tested), and Reloader restarting a generated app.
- A `public` app instance on a domain outside `homelab.swhurl.com`.
- Deleting a real shared Flux unit (only a disposable `Orphan` unit was deleted).
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
| PR08a | Independent backup and tested restore of one stateful workload | Partial: data classified; encrypted ClickStack MongoDB backup and disposable restore proven 27 Sep 2026; off-host destination, schedule and app-level restore pending |
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

## 7. References

- [Flux pruning and deletion](https://fluxcd.io/flux/components/kustomize/kustomizations/)
- [Flux HelmChart reconcile strategies](https://fluxcd.io/flux/components/source/helmcharts/)
- [bjw-s app-template values reference](https://bjw-s-labs.github.io/helm-charts/docs/app-template/reference/)
- [Renovate Flux manager](https://docs.renovatebot.com/modules/manager/flux/)
- [Stakater Reloader](https://github.com/stakater/Reloader/blob/master/README.md)
- [oauth2-proxy provider configuration](https://oauth2-proxy.github.io/oauth2-proxy/7.6.x/configuration/providers/)
- [Cookie Domain scope, RFC 6265](https://www.rfc-editor.org/rfc/rfc6265)
- [cert-manager Route53 DNS-01](https://cert-manager.io/docs/configuration/acme/dns01/route53/)

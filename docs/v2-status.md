# Offline Go v2: status and handoff

Status: approved
Gate: the operator's choice of the next slice: phase 3 slice 2 (snapshot acquisition, or legacy translation) or phase 5 observation
Uncommitted: none

Where v2 stands and how to resume it. The [iteration 2 plan](iteration-2-plan.md) owns the **contracts** (records, identity, selection, the compiler, the phase-3 brief and [the gap list](iteration-2-plan.md#6-deliberately-deferred)); [workstreams](workstreams.md) indexes the workstreams; [current state](current-state.md) holds dated live evidence. This page does not restate a contract: link it or change it there.

## Authority

**Approved:** the operator said yes to the concrete phase-1 proposal: root-level `v2/`, Go 1.26.0, JSON-only descriptors, positional canonical encoding, manifest-only `linux/amd64` artifact claims, empty-only bindings and the narrow policy exception. Phase 2's compiler and its review decisions are approved and implemented. The operator's [design principles](iteration-2-plan.md#design-principles) and the revised [phase-3 brief](iteration-2-plan.md#phase-3-shadow-not-synchronise) are approved as direction; only the [first pure HTTP slice](iteration-2-plan.md#first-slice-proposal-pure-http-preview-and-comparison) is additionally approved for implementation: v2 is not bound to v1 contracts, responsibilities and data have explicit owners, and transition may have incomplete semantics. **Order approved 25 October 2026:** retire risk, not phase numbers — one disposable instance through delivery, observation and removal (phases 4–6) comes before the rest of the bridge, which is still required before any existing app is migrated ([why](iteration-2-plan.md#5-incremental-delivery)).

**Not approved:** v1 behaviour changes, a platform console UI, a general framework, bridge integration beyond that slice, adapters, credentials, v2 publication, live migration or feature removal. The [code graph](visual-change-tool.md) is a separate workstream; do not build it in this thread.

**Implementing more needs the operator's approval of a concrete slice** — design approval and permission to push are separate decisions.

## Where it stands

| Phase | State | Evidence |
| --- | --- | --- |
| 1. Release-selection kernel | done, reviewed, committed 24 October 2026 | [contract](iteration-2-plan.md#2-phase-1-model-and-contract), [evidence](iteration-2-plan.md#phase-1-verification-evidence) |
| 2. Deterministic compiler | done, reviewed, committed 25 October 2026 | [contract](iteration-2-plan.md#phase-2-establish-compiler-ownership) |
| 4. Git delivery | done: offline half and one live instance, 25 October 2026 | [slice](iteration-2-plan.md#first-delivery-slice-implemented-offline-25-october-2026-operator-run) |
| 5. Observation | **a candidate next slice** (needs approval): the first real adapter | [gate](iteration-2-plan.md#phase-5-explain-current-state-honestly) |
| 6. First live instance | done 25 October 2026: applied, verified and removed, see the record above | [evidence](current-state.md#first-v2-instance-live-25-october-2026) |
| 3. Shadow bridge | slice 1 done; **3.2–3.5 are the other candidate next slice** (need approval) | [slice 1](iteration-2-plan.md#first-slice-proposal-pure-http-preview-and-comparison) |
| 7. Selective replacement | proposed, not approved; needs the bridge first | [phases](iteration-2-plan.md#5-incremental-delivery) |

Of the phase-1 review's findings, the coexistence check's blind spots are deliberately left. Nothing in v2 is published, and no v2 intent or resource reaches an active Flux path.

## Decisions

**25 October 2026 — egress and credentials for the first instance (decided, in the operator's words, by accepting both recommendations):** all egress is denied for the first disposable instance (`policyTypes: [Ingress, Egress]`, no rules) rather than routed through the proxy or left open, and delivery and observation use the operator's own credentials from the host rather than a new Secret, runner or control plane. [The record](iteration-2-plan.md#decision-egress-and-credentials-for-the-first-instance-decided-25-october-2026) states what each answer changes, why the alternatives lost, and what reopens them.

**25 October 2026 — the first delivery slice approved and implemented offline:** the [delivery slice](iteration-2-plan.md#first-delivery-slice-implemented-offline-25-october-2026-operator-run) — deny-all egress, a bounded `compile --out DIR`, and the unit written where kustomize can see it. Its contract, acceptance per invariant and the costs taken are on that page. **The live half has not run:** no instance has been compiled into the repository, and nothing has reached the cluster.

## Slice record: the delivery slice, offline half

- **Outcome and the refusal it must produce:** a compiled instance can be written into the repository, with all egress denied and the Flux unit where kustomize can list it. It refuses to write into the checkout's `apps/` — however the destination is spelled, including through a symlink — outside the destination, or at all if any file's path is unsound.
- **Owners and boundaries:** the compiler decides the paths and their content; `adapters/files` decides only whether a destination is safe and what each path needs; `cmd/swhurl-v2` owns the `apps/` refusal and the exit codes. The profile owns both roots.
- **What crosses each seam:** `[]kernel.File` into the writer; `[]files.Outcome` (`written`/`unchanged`, in compilation order) back, including the outcomes completed before a failure; an error carrying a code, a path and a fixed reason, never content.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** v1 unchanged (nothing under `apps/`, `clusters/` or `platform/` touched; `make check` passes). Pure — `kernel` and `shadow` still take no effects; the filesystem lives in a layer-1 adapter. The golden diff is exactly the intended change — compilation ID, profile ID, the network policy's `egress` and `policyTypes`, the unit's path and its registration — and the other four instance documents and the unit document are byte-identical; the profile identity was recomputed independently in Python. Refusal codes gained `refused_destination` and no existing code or precedence moved. Failure paths tested: every refusal, a symlink at or below the destination, a refused path writing nothing (proved by a clean write afterwards), an unnamed file left alone, both filesystem-failure branches, and the outcomes completed before a failure. Each file is written to a temporary name and renamed, so an interrupted run cannot leave a truncated file. Assessed scope: the write contract above; no claim about anything else. Offline only — nothing was applied, so no readiness is claimed. This page, the gap list and the plan's contract text changed in the same commit.
- **Evidence:** `make check-v2` (7 packages), `REQUIRE_HELM=1 make check-v2` (chart render and Kustomize ran), `make check`, `git diff --check`, `make v2-report`. Coverage: kernel 95.3%, `adapters/files` 85.7%, `filestest` 83.3%, CLI 93.3%.
- **Gaps touched:** the egress row now says egress is denied and the proxy path is due with the first instance that needs it; the delivery row records that the unit is written and its one-line registration is the operator's edit.
- **Independent read-only review, before commit:** should-fix findings, all fixed — the `apps/` refusal read the destination as typed, so `--out .` from inside `apps/` or a symlink to it wrote into the v1 tree (now resolved, with the checkout's own `apps/` identified by `.git` or `clusters/` beside it, which also stops it refusing an unrelated folder called `apps`); a mid-write failure left earlier files written without saying so (now reported, and each file is atomic); the new profile refusals had no tests (now four cases); and this page still said the slice awaited approval. Nits fixed: the fake no longer keeps a partial write, and the shared `Contract` proves a refusal wrote nothing; `--out ""` is a usage error; a `/` destination is accepted; refusals no longer echo the destination; the adapter fixtures are the compiler's exact bytes. Not changed: a `/` destination as the filesystem root still writes into it if the operator asks for it — the `apps/` refusal is the guard, not a sandbox.
- **The live half, 25 October 2026.** `swhurl-try-v2-1` runs in staging, owned only by v2: unit and HelmRelease Ready, the pod `1/1` on the compiled digest, unreachable from a sibling pod and from another namespace, no egress from the pod, and all 37 v1 units unchanged. Three findings, recorded in [current state](current-state.md#first-v2-instance-live-25-october-2026): the first image kept SQLite and crash-looped (the model was right, the image was wrong — a deferred row, not a defect); a timed-out *install* leaves a release Flux cannot roll back (`Stalled`, fixed with `flux reconcile helmrelease --force`, now in the troubleshooting table); and the egress policy is enforced only after a few seconds for a newly created pod, so a fresh pod can egress until then. CI also failed both pushes on the page's cited hash after a rebase, which is why the check now requires a cited commit to be reachable from a ref.
- **Removal, 25 October 2026.** The instance's five files, its unit file and its line in `clusters/home/kustomization.yaml` are deleted, so Flux prunes what it applied; the evidence of what prune actually removed is in [current state](current-state.md#first-v2-instance-live-25-october-2026). `v2-apps/` stays in Git through an empty `.gitkeep`, so the profile's output root exists in every clone, including when it holds no instance.
- **Next gate:** the operator's choice of the next slice.

## Slice record: phase 3 slice 1

- **Outcome and refusal:** an operator can compare two actual HelmRelease documents over a fixed set of HTTP workload fields and read, per field, `equal`, `intentional`, `different` or `not_assessed` with a fixed reason. It refuses invalid or ambiguous JSON, and it cannot report parity for something it did not read: a field outside the fixed shape is `not_assessed`, and a missing fact is never `equal`.
- **Owners and boundaries:** `kernel.RenderHTTP` renders resources from workload facts plus environment facts and a platform profile, and owns no release provenance. `shadow.CompareHTTP` owns a bounded comparison policy and imports no `kernel` types. `Compile` keeps its signature, refusals, golden bytes and compilation IDs, and only translates validated records into rendering inputs.
- **What crosses each seam:** the caller passes HelmRelease bytes and explicit resource choices; nothing is read from disk, Git or a cluster, and no result carries a release ID, instance revision or readiness flag.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** v1 unchanged (no v1 file touched; `coexistence_test.go` passes). Pure — no clock, network, filesystem, environment, randomness or subprocess in `kernel` or `shadow` (the `pure` rule). Golden compilations byte-identical with unchanged compilation IDs, not regenerated. Refusal codes and precedence unchanged (40,495 differential cases against the previous commit). Failure paths tested: invalid and ambiguous JSON, wrong types, missing facts, an overridden or disabled controller, absent quantities. Assessed and unassessed scope stated per field, with no parity claim for anything unread. Offline only: nothing is published, so no readiness is claimed. This page and the gap list change in the same commit.
- **Evidence:** `make check-v2` and `REQUIRE_HELM=1 make check-v2` (chart render and offline Kustomize ran, not skipped), `make check` (987 tests), `git diff --check`, `make v2-report`; kernel coverage 95.3%, shadow 96.7%. Independent read-only review: OK with notes, no blockers; its two should-fix findings were both false parity and are fixed — see [the record](iteration-2-plan.md#first-slice-proposal-pure-http-preview-and-comparison).
- **Gaps touched:** the [deferred list](iteration-2-plan.md#6-deliberately-deferred) rows for scoped preview/comparison and for complete release semantics.
- **Committed:** `c44aef98`, on the tree pushed through `1ea1bbde`.

## Resume

**Read first**, in this order — the rest is the links below, not a substitute for inspection:

1. `AGENTS.md` (and the inherited working preferences), then this page.
2. [Design principles](iteration-2-plan.md#design-principles), then [development layers](iteration-2-plan.md#4-development-layers-and-adapters) and the [phase-3 brief](iteration-2-plan.md#phase-3-shadow-not-synchronise).
3. For the parts being changed: [architecture review](architecture-review.md) (release identity, ownership, transition contracts) and [model review](model-review.html) (where v1's complexity comes from). For the domain itself, [records](../tools/swhurl/apps/model/records.py) and [model tests](../tests/test_app_model.py) are the v1 behaviour worth understanding before translating it.

**How to approach a v2 change:**

1. Read the [design principles](iteration-2-plan.md#design-principles). Start from a domain scenario: the facts and their owners, the request, and the expected result or refusal with its scope and semantic gaps ([fixtures](iteration-2-plan.md#fixtures-and-fakes)).
2. Use v1 behaviour as evidence, not the target contract. Classify it as preserve, deliberately change, remove or not yet modelled, and track gaps in [the gap list](iteration-2-plan.md#6-deliberately-deferred) rather than forcing later domains into this slice.
3. Write the pure functions and their tests first. Identify the orchestration above them and the data crossing each seam; propose the smallest package or check change rather than putting every responsibility in `kernel`.
4. Add an adapter only when the slice needs that dependency, following the [new-adapter recipe](iteration-2-plan.md#extension-recipes). Kubernetes goes through [the API server](iteration-2-plan.md#kubernetes-the-api-server-not-kubectl), never `kubectl`.
5. Run `make check-v2` ([guardrails](iteration-2-plan.md#guardrails)) and the matching [test tiers](iteration-2-plan.md#test-tiers); fix what a rule reports rather than its baseline unless the change is intended and explained. Live checks belong to an approved live phase only.

**Verify where you are** — run with `!`, from the repository root:

```bash
git log --oneline -3 && git status --short
make check-v2
make -s v2-report
python3 -m swhurl check-docs
```

The report shows what the tree adds to `origin/main`: a new package, a changed exported API, a changed rule setting, a budget entry added or raised must each be explained by the slice. A changed golden compilation, a weakened budget or a silent rule change is a stop, not a fix.

**Stop line:** no commit, push, publication, credential access, cluster action or policy-exception expansion without the operator's instruction. Other work may be in progress in the same checkout: re-run `git status`, inspect the diff, and never stage, discard or format unrelated work.

## Suggested opening prompt for a fresh session

> Work on v2 only. Read `docs/v2-status.md` first (status, authority, the resume steps), then `docs/iteration-2-plan.md` for the contracts and the gap list, and `AGENTS.md` for the repository rules. Do not re-derive the state from this prompt: check it.
>
> Then run, from the repository root: `git log --oneline -5`, `git status --short`, `make check-v2`, `make -s v2-report`, `python3 -m swhurl check-docs`. Report what they say before proposing anything. A check that reads the working tree can pass here and fail on a clone; CI is the reader that matters.
>
> Where v2 stands: phases 1 and 2 are done and reviewed; phase 3 slice 1 (release-independent `RenderHTTP` and the pure `shadow.CompareHTTP`) is done; the **delivery slice is done end to end**, including one disposable instance that was applied by Flux, verified (digest, readiness, isolation, no egress) and removed on 25 October 2026. Nothing is in flight and nothing is uncommitted.
>
> The next gate is the operator's choice between two candidate slices, neither approved yet: **phase 3 slice 2** (snapshot acquisition, or legacy translation and default resolution) for the shadow bridge, or **phase 5 observation** (the first real adapter: is the intended digest running, is it Ready, is it unreachable — unavailable must not read as absent). Ask which; do not implement either before the operator approves that slice.
>
> Follow the operator's design principles: maintainable v2 contracts rather than v1 compatibility; a pure core with orchestration above it and integrations at the edges; explicit domain and data ownership; iterative transition with honestly incomplete semantics. Do not require supplemental provenance claims or complete releases merely to preview known workload facts. Answer the eight slice invariants on the status page in the same change as the code, keep the gap list current, and run `make check-v2` and `make check` before asking to commit.
>
> The code graph is a separate workstream and a sidequest: do not build it here. Preserve unrelated work; do not commit, push, publish, access credentials or touch the live cluster without the operator's instruction, and treat any namespace or data deletion as needing explicit confirmation.

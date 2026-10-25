# Offline Go v2: status and handoff

Status: approved
Gate: the egress and credentials decision for one disposable instance
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
| 4. Git delivery | **next**, after the decision below | [contract](iteration-2-plan.md#phase-4-settle-publication-before-automation) |
| 5. Observation | after delivery: the first real adapter | [gate](iteration-2-plan.md#phase-5-explain-current-state-honestly) |
| 6. First live instance | after observation: one disposable instance, then removal | [decision](iteration-2-plan.md#decision-needed-egress-and-credentials-for-the-first-instance) |
| 3. Shadow bridge | slice 1 done; 3.2–3.5 after the first live instance | [slice 1](iteration-2-plan.md#first-slice-proposal-pure-http-preview-and-comparison) |
| 7. Selective replacement | proposed, not approved; needs the bridge first | [phases](iteration-2-plan.md#5-incremental-delivery) |

Of the phase-1 review's findings, the coexistence check's blind spots are deliberately left. Nothing in v2 is published, and no v2 intent or resource reaches an active Flux path.

## Decision needed

The next gate is a decision, not a slice: **egress and credentials for the first disposable instance**, with four egress options and three credential options and a recommendation for each in [the brief](iteration-2-plan.md#decision-needed-egress-and-credentials-for-the-first-instance). Until it is answered, the delivery slice cannot be specified: the compiler's egress policy and who holds the credential are its two inputs.

## Slice record: phase 3 slice 1

- **Outcome and refusal:** an operator can compare two actual HelmRelease documents over a fixed set of HTTP workload fields and read, per field, `equal`, `intentional`, `different` or `not_assessed` with a fixed reason. It refuses invalid or ambiguous JSON, and it cannot report parity for something it did not read: a field outside the fixed shape is `not_assessed`, and a missing fact is never `equal`.
- **Owners and boundaries:** `kernel.RenderHTTP` renders resources from workload facts plus environment facts and a platform profile, and owns no release provenance. `shadow.CompareHTTP` owns a bounded comparison policy and imports no `kernel` types. `Compile` keeps its signature, refusals, golden bytes and compilation IDs, and only translates validated records into rendering inputs.
- **What crosses each seam:** the caller passes HelmRelease bytes and explicit resource choices; nothing is read from disk, Git or a cluster, and no result carries a release ID, instance revision or readiness flag.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** v1 unchanged (no v1 file touched; `coexistence_test.go` passes). Pure — no clock, network, filesystem, environment, randomness or subprocess in `kernel` or `shadow` (the `pure` rule). Golden compilations byte-identical with unchanged compilation IDs, not regenerated. Refusal codes and precedence unchanged (40,495 differential cases against the previous commit). Failure paths tested: invalid and ambiguous JSON, wrong types, missing facts, an overridden or disabled controller, absent quantities. Assessed and unassessed scope stated per field, with no parity claim for anything unread. Offline only: nothing is published, so no readiness is claimed. This page and the gap list change in the same commit.
- **Evidence:** `make check-v2` and `REQUIRE_HELM=1 make check-v2` (chart render and offline Kustomize ran, not skipped), `make check` (987 tests), `git diff --check`, `make v2-report`; kernel coverage 95.3%, shadow 96.7%. Independent read-only review: OK with notes, no blockers; its two should-fix findings were both false parity and are fixed — see [the record](iteration-2-plan.md#first-slice-proposal-pure-http-preview-and-comparison).
- **Gaps touched:** the [deferred list](iteration-2-plan.md#6-deliberately-deferred) rows for scoped preview/comparison and for complete release semantics.
- **Committed:** `354aae1e`, on the tree pushed through `1ea1bbde`.

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

> Work only on v2. Read `docs/v2-status.md` (status, authority, resume steps), `docs/iteration-2-plan.md` (the contracts), `docs/architecture-review.md` and `docs/model-review.html`, plus the repository instructions. Inspect the working tree and the existing Go code and tests. Phases 1 and 2 and phase 3's first slice are implemented, independently reviewed and committed; the next gate is a **proposal** for the next slice (phase 3 slice 2, or the delivery slice — whatever the operator approved), which needs separate approval — do not implement it before that. Start from the operator's design principles: maintainable v2 contracts rather than v1 compatibility; pure core with orchestration above it and integrations at the edges; explicit domain and data ownership; iterative transition with honestly incomplete semantics. Do not require supplemental provenance claims or complete releases merely to preview known workload facts. Answer the slice invariants on the status page in the same change as the code, keep the gap list current, and run `make check-v2` and `make check`. The code graph is a separate workstream: do not build it here. Preserve unrelated work; do not commit, push, publish, access credentials or touch the live cluster.

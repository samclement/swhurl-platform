# Workstreams: what is in flight, and what finishes it

The one table that says where each piece of work stands. Everything else is the work: a **status page** per workstream (its state, current slice, handoff and resume steps), the pages that own contracts, and [current state](current-state.md) for dated evidence.

Read this first when resuming. `make check-docs` compares it with Git, so it cannot quietly go stale ([why it exists](contributing.md#agent-assisted-changes)).

## Workstreams

| Workstream | Kind | State | Current slice | Next gate | Needs | Page | Paths |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Offline Go v2 | main | approved | a migrated instance keeps its records: `v2-records/` is read by the snapshot export, and no port is allocated without a need, 26 October 2026 | the bridge slice that translates those records into compile's inputs, or the read-only backup observation | the operator | [v2 status](v2-status.md) | `v2/`, `v2-apps/`, `v2-records/` |
| Live platform | main | approved | the 24 open items in the plan index | M14, then B4b (records-only removal) | the operator's confirmations for live actions | [plan index](plan.md#0-where-this-paused-and-what-is-left) | — |
| Code graph | sidequest | parked | steps 0 to 3 and `view` committed 24 October 2026 | step 4 (`diff`) | operator approval | [tool page](visual-change-tool.md) | `devtools/codegraph/` |

**Kind** is `main` (a workstream the platform's direction depends on) or `sidequest` (useful now, blocking nothing). **State** is `proposed` (an idea), `approved` (scope agreed, not started), `in progress` (someone is changing its files now), `blocked` (waiting on a decision), `parked` (deliberately set aside) or `done`.

At most one workstream is `in progress`, so a sidequest cannot silently become the main line. Every row names its next gate and what that gate waits for, so "what is next" is a lookup rather than a memory.

## What a status page is

A `docs/*-status.md` page holds, in its first lines, the three facts the registry also states:

```text
Status: approved
Gate: a phase 3 slice-2 proposal: snapshot acquisition, or legacy translation and default resolution
Uncommitted: none
```

`Uncommitted: none` must match a clean `git status` for that workstream's paths; to name work in progress instead, list the paths. Below the block, the page carries:

- **Authority** — what the operator approved, and what is explicitly not approved.
- **Slice record** — one per delivered slice, appended when the slice is committed. It answers the same eight invariants every time, with the evidence for each, so "done" means the gate rather than the code:

  ```text
  ## Slice <n>: <name> — <state>
  Outcome and the refusal it must produce:
  Owners and boundaries:
  What crosses each seam:

  Invariants — say how this slice satisfies each, or that it does not apply and why:
  1. v1 behaviour, files and live state are unchanged.
  2. Pure packages take no effects (no clock, network, filesystem, environment, randomness, subprocess).
  3. Golden bytes and identities are unchanged, or their change is explained; nothing is regenerated to pass.
  4. Refusal codes and their precedence are unchanged, or the change is stated in the contract.
  5. Failure paths are tested, not only success: unavailable is not absent, invalid is not unimplemented.
  6. The result states its assessed and unassessed scope; nothing it did not read is reported as equal.
  7. Offline checks are not live readiness.
  8. This page and the gap list are updated in the same commit.

  Evidence:              <commands> → <result>, dated
  Deferred rows touched: <link>
  Next gate:
  ```

  Only invariant 8 is mechanical (`make check-docs`). The rest are what an independent review applies, which is where false parity and weakened baselines are actually caught.

- **Resume** — the read-first links (three at most), the stop line, and the commands that prove the checkout matches the page.

A status page states a slice once it is committed, not before. Contracts live on the page that owns them and are linked, never copied.

## What `make check-docs` enforces

| Rule | Why |
| --- | --- |
| The table's columns, each row's kind and state, a non-empty next gate and needs | a row without a next gate is work that has quietly stopped |
| At most one workstream `in progress` | sidequests stay sidequests |
| Each page resolves, including the heading when one is named; each path exists | links are how a new session finds the work |
| A status page's `Status` and `Gate` equal the registry's; every `docs/*-status.md` is in the table | one record of progress, not two |
| `Uncommitted` matches `git status` for that workstream | the drift this page exists to catch: a record that says "pending review" after the review |
| Every commit a status page cites exists | evidence cannot be misremembered |
| A change under a workstream's paths also changes its page | "record the slice in the same change", made mechanical |

Remaining staleness is judgement, not mechanism: whether a slice's evidence supports its claim, and whether a deferred feature is still worth deferring. Those are review questions, and [iteration 2](iteration-2-plan.md#6-deliberately-deferred) keeps the gap list they apply to.

# Workstreams: what is in flight, and what finishes it

The one table that says where each piece of work stands. Everything else is the work: a **status page** per workstream (its state, current slice, handoff and resume steps), the pages that own contracts, and [current state](current-state.md) for dated evidence.

Read this first when resuming. `make check-docs` compares it with Git, so it cannot quietly go stale ([why it exists](contributing.md#agent-assisted-changes)).

## Workstreams

| Workstream | Kind | State | Current slice | Next gate | Needs | Page | Paths |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Offline Go v2 | main | in progress | the cutover (approved 31 October 2026): v2 gained `edit`, `remove` and a writer that prunes, offline 31 October 2026, then owns every instance — 1a the nine staging ones, done 31 October 2026, with `migrate` resolving a record's digest to the platform's manifest through the registry so none of them is warned about, 1b the four production ones — two done 31 October 2026 (`demo-orders` and `demo-shop`, which move together because one calls the other; their retained claims adopted, and production's bare host restored) — and v1's writer is deleted; before it, two throwaways removed, the goals table brought up to date, `verify-platform` reading a v2 record, and two v2-owned instances remain (`swhurl-try-10` staging, `demo-notifier` staging) after `swhurl-try-11` and the v1 `swhurl-try-12` were removed on 31 October 2026; what an app offers, the offline half: a release states its description, its API path and the commit its offered documents were read at, an instance renders `SWHURL_DESCRIPTION` and the two annotations, and the documents travel as declared inputs from `v2-records/` to `v2-apps/` where the console reads them — live 30 October 2026, with no adapter; before it, team grants, the first live promotion, image automation, events, Reloader, telemetry, the worker kind, broker minting and the egress proxy | 1b: `swhurl-try-10` and `swhurl-try-30` production, then v1's records, `app-*` commands and readers deleted so v2 is the only writer (the repository read follows) | the operator | [v2 status](v2-status.md) | `v2/`, `v2-apps/`, `v2-records/`, `images/backup/` |
| Live platform | main | approved | the 24 open items in the plan index; the eight v1 app repositories stopped reusing a tested PR image, take the newest template and run Node.js 26 (30–31 October 2026), the two throwaways that could not take the template update were removed, with their namespaces, repositories and packages, and `make verify-platform`'s Image Automation check reads a v2-owned instance's own record (31 October 2026) | M14, then B4b (records-only removal) | the operator's confirmations for live actions | [plan index](plan.md#0-where-this-paused-and-what-is-left) | — |
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

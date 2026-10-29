# Workstreams: what is in flight, and what finishes it

The one table that says where each piece of work stands. Everything else is the work: a **status page** per workstream (its state, current slice, handoff and resume steps), the pages that own contracts, and [current state](current-state.md) for dated evidence.

Read this first when resuming. `make check-docs` compares it with Git, so it cannot quietly go stale ([why it exists](contributing.md#agent-assisted-changes)).

## Workstreams

| Workstream | Kind | State | Current slice | Next gate | Needs | Page | Paths |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Offline Go v2 | main | in progress | 2a is proved live: `restore --confirm` performs the sequence — suspend, stop, a Secret holding the age identity and a presigned read URL for one archive, the Job from the backup image, its log, scale back, resume — and every failure unwinds the app; `swhurl-try-10` staging was restored over its newest archive on 27 October 2026 in 30 seconds, and then moved back to an older archive on purpose in 29 seconds with the file hashes proving the content changed; the backup schedule is suspended for the window (offline-tested, not yet in a live run); a v2-owned instance's record states its own `calls`/`allows`, so the v1-shaped YAML copies are gone; the auth material is mounted as files, the keys are one rendered object per managed namespace (`platform-auth`, live with 17 objects on 28 October), both templates read them (TypeScript `v0.2.19`, Kotlin `v0.2.18`), and a v2-rendered callee gets them only as files — proved live on 29 October: a new caller and a key rotation each took effect in the running pod with no restart; the backup image is Go (48.1 MB against 134 MB) and every rendered job pins it; the always-on instance surface (telemetry, notify, the app-owned team label) is live on both v2-owned instances at `1bca7990`; the worker kind is implemented offline — a `worker` release renders no Service, probes or route and keeps every other instance fact, with `job` still refused; events' part one is offline too — `swhurl-v2 users` renders the NATS user list and `make check-v2` proves it is what the platform runs, with no identity moved; events' part two is offline too — the release's `publishes`, the instance's broker identity, the profile's `nats` block, and the rendered variables, stream, consumers and seed-as-input; the NATS user list is now v2's own file, v1's generator deleted and the Render workflow regenerating it; `demo-notifier` staging is the first migrated event subscriber, receiving `demo-orders.created` from a v1 publisher live; and Reloader's watch list is v2's own platform document (`platform/reloader/values.json`, which the HelmRelease merges as values), v1's writer deleted, with image automation deferred until v2 has a selection path of its own; v2 **mints** a broker identity — `swhurl-v2 mint` derives the pair in the pure model and encrypts the seed to the profile's recipient through the one adapter that runs a command — used for the two v2-owned instances without a pair and rotated live on a third; and a reporting instance's policy now admits the collector its endpoint names (`telemetry.nodeCIDR`, live on three instances) | the operator's choice of the next slice: a delivery path for image automation, or the next domain v2 does not render (secret names, team grants, public routes) | the operator | [v2 status](v2-status.md) | `v2/`, `v2-apps/`, `v2-records/`, `images/backup/` |
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

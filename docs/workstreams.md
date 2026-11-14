# Workstreams: what is in flight, and what finishes it

The one table that says where each piece of work stands. Everything else is the work: a **status page** per workstream (its state, current slice, handoff and resume steps), the pages that own contracts, and [current state](current-state.md) for dated evidence.

Read this first when resuming. `make check-docs` compares it with Git, so it cannot quietly go stale ([why it exists](contributing.md#agent-assisted-changes)).

## Workstreams

| Workstream | Kind | State | Current slice | Next gate | Needs | Page | Paths |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Go replacement (v2) | main | parked | Approved cleanup complete: top-level Python tests and host probe dropped without replacement; installed probe units removed and no timer scheduled; Copier-only console/notify image deployed and checked live, Go/codegraph/backup checks retained with explicit test-only PyYAML ([evidence](current-state.md#python-tests-and-the-host-probe-retired-offline-13-november-2026)) | choose the next slice from remaining work or deferred features | operator scope decision | [v2 status](v2-status.md) | `v2/`, `v2-apps/`, `v2-records/`, `images/backup/`, `tests/` |
| Live platform | main | approved | the 24 open items in the plan index; the eight v1 app repositories stopped reusing a tested PR image, take the newest template and run Node.js 26 (30–31 October 2026), the two throwaways that could not take the template update were removed, with their namespaces, repositories and packages, and `make verify-platform`'s Image Automation check reads a v2-owned instance's own record (31 October 2026) | the next open plan item | the operator's confirmations for live actions | [plan index](plan.md#0-where-this-paused-and-what-is-left) | — |
| Platform telemetry | sidequest | in progress | slices 1 to 3 delivered (14 November 2026): Traefik writes an access line and a span per request at the edge, the reconciler keeps a `Platform: <service>` dashboard for each of thirteen catalogued services, and the console and notify write their own request and job lines ([record](current-state.md#console-and-notify-telemetry-14-november-2026)); slice 3b, the console's own traces and metrics, is implemented offline (D7 answered A), against the measured page latencies ([baseline](current-state.md#console-page-latencies-14-november-2026)); slices 4 and 5 and service metrics remain | slice 3b deployed and a page's trace read in ClickStack, then slice 4's selective scrape | the console deploy and a live check | [status page](platform-telemetry-status.md) | `host/telemetry-inventory.sh`, `infra/traefik/`, `v2/dashboards/` |
| Code graph | sidequest | parked | steps 0 to 3 and `view` retained; README examples analyse the surviving package at HEAD ([cleanup evidence](current-state.md#python-tests-and-the-host-probe-retired-offline-13-november-2026)) | step 4 (`diff`) | operator approval | [tool page](visual-change-tool.md) | `devtools/codegraph/` |

**Kind** is `main` (a workstream the platform's direction depends on) or `sidequest` (useful now, blocking nothing). **State** is `proposed` (an idea), `approved` (scope agreed, not started), `in progress` (someone is changing its files now), `blocked` (waiting on a decision), `parked` (deliberately set aside) or `done`.

At most one workstream is `in progress`, so a sidequest cannot silently become the main line. Every row names its next gate and what that gate waits for, so "what is next" is a lookup rather than a memory.

## What a progress page is

A `docs/*-status.md` page holds, in its first lines, the three facts the registry also states:

```text
Status: in progress
Gate: the post-cutover leftovers (M14, re-scoped), then the ownership milestone
Uncommitted: none
```

`Uncommitted: none` must match a clean `git status` for that workstream's paths; to name work in progress instead, list the paths. Below the block the page is a **map, not a log**:

- **Goal** — one paragraph: what "done" means, in the operator's terms.
- **Where it stands** — one row per capability: `done` or `partial`, and one dated evidence link.
- **Remaining Python / remaining work** — what still stands between it and the goal, ordered.
- **Deferred features** — the one list of what it does not model, and what reopens each.
- **Durable decisions** — rules one line each; the reasoning stays in Git and current-state.
- **How this page is kept** — the rules, stated once.

The page is overwritten, not appended to. A delivered slice changes the row it moved and adds one dated entry to [current state](current-state.md); it does not add a section to the page. Git keeps the narrative: `git log -p -- docs/v2-status.md`. Contracts stay on the page that owns them and are linked, never copied.

## What `make check-docs` enforces

`make check-docs` is `swhurl-v2 workstreams` over this checkout, reading Git without writing to it ([contract](iteration-2-plan.md#the-workstream-check-in-go-approved-5-november-2026)); CI runs it on every push.

| Rule | Why |
| --- | --- |
| The table's columns, each row's kind and state, a non-empty next gate and needs | a row without a next gate is work that has quietly stopped |
| At most one workstream `in progress` | sidequests stay sidequests |
| Each page resolves, including the heading when one is named; each path exists | links are how a new session finds the work |
| A status page's `Status` and `Gate` equal the registry's; every `docs/*-status.md` is in the table | one record of progress, not two |
| `Uncommitted` matches `git status` for that workstream | the drift this page exists to catch: a record that says "pending review" after the review |
| Every commit a status page cites is reachable from a ref (a shallow clone warns instead) | evidence cannot be misremembered |
| A change under a workstream's paths (anything `git status` reports, a new file included) also changes its page | "update the row in the same change", made mechanical |
| A status page has the sections above, states `done` or `partial` per capability, gives each row one evidence link that resolves, keeps a deferred table whose last two cells are non-empty, has no `## Slice record` heading, and stays at or under 250 lines | a map that is overwritten, not a log that grows |
| A status page is the only place its workstream's deferred list lives | one list, so deferring and delivering cannot disagree |

Remaining staleness is judgement, not mechanism: whether a capability's evidence supports its row, and whether a deferred feature is still worth deferring. Those are review questions, and the page's own "How this page is kept" says who applies them.

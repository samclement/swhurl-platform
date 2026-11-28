# Orchestration state review: lifecycles held in flags, phases and nullable state

**Status: design proposal, not approved implementation work.** Saved 28 November 2026 from a read-only review of `a393c5b3`. It records evidence and draft state tables, not deployed behaviour or a decision. [Architecture](architecture.md) and the [iteration-2 plan](iteration-2-plan.md) remain the canonical descriptions of the system and of v2's contracts; [plan](plan.md) section 0 records approved work. Saving this page approves no reshape.

This page holds the evidence pass (sections 1 to 3). The judgement pass, where an explicit state-machine primitive would simplify the code and where it would add ceremony, with its trade-offs and one recommendation, is not written yet.

## Starting example

A restore of `demo-orders/prod` fails at `delete-job`: the restore Job ran, and its deletion was refused. What does the run undo, and what does it tell the operator?

Today the answer is computed from six booleans and two names on the run (`scaled`, `jobMayHaveRun`, `suspended`, `releaseWasSuspended`, `backupSuspended`, `backupWasSuspended`, `jobName`, `secretName`; [`v2/recovery/restore.go:275-288`](../v2/recovery/restore.go#L275-L288)), set and cleared by eleven step functions and read by `unwind` and `failed`. The undo is correct for the cases the table test covers ([`restore_test.go:350`](../v2/recovery/restore_test.go#L350)). But the run's position is not a value anyone can name, list or test as a whole, and one combination already reports a fact its own comment rules out: with a release the operator had suspended before the run, this failure reports `App.Suspended: true` (section 2.2, R1).

The question for the review: **which v2 workflows drive an external system through a lifecycle whose position lives in ad hoc fields like these, and what would each look like as explicit states and transitions?**

## 1. Scope, method and terms

### Terms

| Term | Meaning on this page |
| --- | --- |
| Lifecycle | A sequence of positions one thing goes through across runs, requests or steps, where some positions are reached only by effects on an external system (Kubernetes, GitHub, the registry, ntfy, a stored document). |
| Held state | What the code itself remembers about that position: struct fields, a persisted record, a JSON document. The subject of this review. |
| Observed state | What the code reads back from the external system each time (a Flux unit's conditions, a CronJob's last success). No memory, so no drift between memory and the world. |
| Ad hoc | The position is spread over booleans, free strings, nullable or empty-means-something fields, or untyped map keys, so its valid combinations are implied by code paths rather than declared. |
| Explicit | The position is a named type or a closed set of values, with one function deciding the next one (or a named refusal), as in [design rule 5](../AGENTS.md) of the operator's working agreements. |

### Method

- Read-only over `v2/` at `a393c5b3`: greps for boolean, pointer and time fields, `State`, `Status` and `Phase` strings and string-constant sets, then a read of every candidate's code and tests. Every `path:line` below was read at that commit.
- Two readings were checked with a throwaway test in `v2/recovery`, run once with `go test ./recovery -run ZZProbe -v` and deleted; no code change remains. Everything else marked *reading* is from the code alone.
- No `make check`, no cluster, no credentials. v1, `images/backup/tool/` and `devtools/` are out of scope.
- Each lifecycle is named by its owning capability from the [capability map](iteration-2-plan.md#capability-map).

### Classification

| Class | Meaning | Where |
| --- | --- | --- |
| **A** Ad hoc held state | A candidate: the lifecycle's position is held, and held ad hoc. | Section 2.1 |
| **B** Explicit already | A precedent: held or decided state with a closed set of values and one deciding function. | Section 2.3 |
| **C** Observed only | No held position; a pure classifier of what was read. Not a state-machine candidate, but listed where two classifiers of the same thing disagree. | Section 2.4 |

## 2. Inventory

### 2.1 Candidates (class A)

| # | Lifecycle | Capability | External systems | How its position is held | Persisted |
| --- | --- | --- | --- | --- | --- |
| L1 | Restore run: claim, take down, restore, put back, or unwind | recovery | Kubernetes (HelmRelease, CronJob, Deployment, Secret, Job) | Six booleans and two non-empty names on `restorer` ([`restore.go:275-288`](../v2/recovery/restore.go#L275-L288)); outcome words are free strings ([`:22-38`](../v2/recovery/restore.go#L22-L38)) | No (one process) |
| L2 | Console job: running, succeeded, failed, interrupted | console | Console Secret store, the pod that ran it | `State string` beside `Finished *time.Time`, `Live bool` and `Outcomes bool` ([`v2/pages/rows.go:473-489`](../v2/pages/rows.go#L473-L489)) | Yes, JSON record |
| L3 | Merged change landing: merged, target captured, reached its end | console (presenting delivery's landing) | GitHub (merge commit), Git (main), cluster | Two write-once fields on the job record, `Target *Render` (nil until captured) and `Reached string` (empty until seen), plus `Outcomes bool` as a record-version marker ([`rows.go:484-486`](../v2/pages/rows.go#L484-L486)) | Yes, in L2's record |
| L4 | New app: repository created, first commit pushed, webhook added, first image published, staging pull request opened; or resumed | apps (code in `orchestration`) | GitHub (repository, commit, webhook), registry, console job | `Resume` and `DryRun` booleans in, `Created bool` out ([`v2/orchestration/apprepo.go:113-136`](../v2/orchestration/apprepo.go#L113-L136)); `Finishing bool` on the console request ([`v2/console/newapp.go:66-71`](../v2/console/newapp.go#L66-L71)); resumability derived from L2's `State`, `Link` and `Resume["kind"]` ([`v2/console/activity.go:84-95`](../v2/console/activity.go#L84-L95)) | Partly: L2's record, and GitHub itself |
| L5 | Instance and service incident: healthy, faulting within grace, notified, recovering, recovered | notifications | Kubernetes (snapshot), ntfy, the state ConfigMap | Untyped map keys `since`, `fault`, `notified`, `last_notice`, `healthy_since` ([`v2/notifications/rules/incident/incident.go:94-132`](../v2/notifications/rules/incident/incident.go#L94-L132)); the observation is a `*string` (nil unjudged, `""` healthy, text a fault; [`:133-137`](../v2/notifications/rules/incident/incident.go#L133-L137)) | Yes, version-1 JSON shared with the Python checker ([`document.go:1-3`](../v2/notifications/rules/incident/document.go#L1-L3)) |
| L6 | Notice delivery: pending, retried, delivered or abandoned (then remembered until expiry) | notifications | ntfy, team channels, the outbox document | Two implementations of one shape: untyped `pending`/`seen` maps in incident ([`incident/outbox.go:6-26`](../v2/notifications/rules/incident/outbox.go#L6-L26)) and typed `State{Seen, Pending}` in notify ([`notify/outbox.go:25-46`](../v2/notifications/rules/notify/outbox.go#L25-L46), [`:177-190`](../v2/notifications/rules/notify/outbox.go#L177-L190)) | Yes, both |
| L7 | Console pod: serving, draining | console | Kubernetes (pod shutdown) | `draining bool` ([`v2/console/jobs.go:87`](../v2/console/jobs.go#L87), refused at [`:179-182`](../v2/console/jobs.go#L179-L182)) | No |

### 2.2 What each candidate's ad hoc state does today

Each finding is **observed** (read in code, or run where marked), or a **reading** (inferred from code, not run).

**L1 Restore run** ([`v2/recovery/restore.go`](../v2/recovery/restore.go))

- The flags record *intent before the request* so a timed-out write still counts as applied: `suspended` ([`:465`](../v2/recovery/restore.go#L465)), `backupSuspended` ([`:497`](../v2/recovery/restore.go#L497)), `scaled` ([`:547`](../v2/recovery/restore.go#L547)), `jobName` with `jobMayHaveRun` ([`:590`](../v2/recovery/restore.go#L590)), the last cleared again only on the server's own refusal ([`:592-594`](../v2/recovery/restore.go#L592-L594)). Observed. This is the lifecycle's real contract, and it is only in comments.
- `unwind` ([`:386-419`](../v2/recovery/restore.go#L386-L419)) decides the undo from the flags in a fixed order, with two early stops: a Job that may still hold the claim, and a Job that may have run without evidence the database is in place. `appState` ([`:429-437`](../v2/recovery/restore.go#L429-L437)) derives `running`, `stopped` or `unknown` from the same flags. Observed.
- `suspended` means two things at once: "this run owes a resume" and, via `failed` ([`:377`](../v2/recovery/restore.go#L377)), "the run left the release suspended". With an operator-suspended release (`releaseWasSuspended`), the early stop leaves `suspended` true and the outcome reports `App.Suspended: true`, though `RestoreApp`'s comment says such a release "is not reported" ([`:119-121`](../v2/recovery/restore.go#L119-L121)). **R1, run:** a throwaway test with `releaseSuspended: true, failOn: "delete:Job"` gave `state=failed reason=delete-job app={Replicas:1 Suspended:true State:stopped} backupSuspended=true`. The existing test of this case fails only at `scale-down`, before the Job ([`restore_test.go:596-614`](../v2/recovery/restore_test.go#L596-L614)).
- The test table ([`restore_test.go:350-460`](../v2/recovery/restore_test.go#L350-L460)) is already a transition table in disguise: failure point in, undo steps, app state, reason, replicas and suspensions out. Observed.

**L2 Console job** ([`v2/console/jobs.go`](../v2/console/jobs.go), [`v2/pages/rows.go`](../v2/pages/rows.go))

- Three transitions, each written in place: `Start` sets `running` ([`jobs.go:188-189`](../v2/console/jobs.go#L188-L189)), `finish` sets `succeeded` or `failed` with `Finished` ([`:228-242`](../v2/console/jobs.go#L228-L242)), `settle` makes a record whose pod is gone `interrupted` through `Job.Interrupted` ([`:117-130`](../v2/console/jobs.go#L117-L130), [`rows.go:495-498`](../v2/pages/rows.go#L495-L498)). Observed.
- The invariant "`Finished` is nil exactly while `running`" is kept by those writers but not checked on read: `decodeJob` checks the state is one of four words and parses `Finished` if present, but accepts `running` with a finish time and `succeeded` without one ([`jobs.go:561-589`](../v2/console/jobs.go#L561-L589)). Reading.
- `State` is a free string compared by literal in several places (`rows.go:492`, `activity.go:86`, `activity.go:90`, `jobs.go:578`). Observed.

**L3 Merged change landing** ([`v2/pages/changes.go`](../v2/pages/changes.go), [`v2/console/changes.go`](../v2/console/changes.go))

- The held part is small and write-once: `Capture` sets `Target` once ([`jobs.go:347-355`](../v2/console/jobs.go#L347-L355)), `Reach` sets `Reached` once ([`jobs.go:335-343`](../v2/console/jobs.go#L335-L343)), both called from the page read ([`changes.go:258-260`](../v2/console/changes.go#L258-L260), [`:268-281`](../v2/console/changes.go#L268-L281)). Observed.
- Everything else is derived on every read by `ProgressOf` ([`pages/changes.go:166-179`](../v2/pages/changes.go#L166-L179)) into one of fourteen `Progress.State` words listed only in a comment ([`:133-138`](../v2/pages/changes.go#L133-L138)); `Reached()` is a predicate over four of them ([`:142-144`](../v2/pages/changes.go#L142-L144)). The derivation is pure and honest about failed reads (`unread`, `unchecked`). Observed.
- `Outcomes bool` distinguishes records written before the console kept `Target` and `Reached` ([`rows.go:486`](../v2/pages/rows.go#L486)), and gates the first `ProgressOf` case (`historical`). It is a record version, not a lifecycle position. Observed.

**L4 New app** ([`v2/orchestration/apprepo.go`](../v2/orchestration/apprepo.go))

- A run is create (`Resume` false) or resume (`Resume` true); `repositoryState` refuses a taken name on create and a missing repository on resume ([`:262-277`](../v2/orchestration/apprepo.go#L262-L277)). Observed.
- On resume, an existing repository is one state: its head is taken as the app's commit and the run waits for that commit's image ([`:282-290`](../v2/orchestration/apprepo.go#L282-L290), [`:169`](../v2/orchestration/apprepo.go#L169)). The repository is created with `auto_init` ([`v2/adapters/github/repository.go:177`](../v2/adapters/github/repository.go#L177)), so a run that stopped between `Create` and `FirstCommit` ([`apprepo.go:304-317`](../v2/orchestration/apprepo.go#L304-L317)) leaves a repository whose head is GitHub's initial commit. Resuming it never pushes the rendered tree and waits for an image of that initial commit until the ten-minute refusal, which tells the operator to "fix the app and push" ([`:401-421`](../v2/orchestration/apprepo.go#L401-L421)). Reading; the resume test covers only a repository that already has the app's commit ([`apprepo_test.go:189-204`](../v2/orchestration/apprepo_test.go#L189-L204)).
- Resume also skips the webhook ([`:282-290`](../v2/orchestration/apprepo.go#L282-L290)); that loss is a warning by design, since `make app-hooks` restores it ([`:376-391`](../v2/orchestration/apprepo.go#L376-L391)). Observed.
- Whether a failed job can be resumed is derived from L2 (`failed` or `interrupted`, no `Link`, `Resume["kind"]` is a new app, and no later running or succeeded job for the unit) ([`activity.go:84-95`](../v2/console/activity.go#L84-L95)), and re-checked by the handler ([`v2/cmd/swhurl-v2/console_new.go:85-105`](../v2/cmd/swhurl-v2/console_new.go#L85-L105)). Observed.

**L5 Incident** ([`v2/notifications/rules/incident/incident.go`](../v2/notifications/rules/incident/incident.go))

- `incident` and `fault` ([`:94-132`](../v2/notifications/rules/incident/incident.go#L94-L132)) are the transition function, written as presence tests on map keys: `since` absent is "no incident", `notified` true is "the operator was told", `healthy_since` present is "recovering". Observed.
- Two transitions are made by editing a field rather than naming a state: `Unknown` cancels every recovery by deleting `healthy_since` ([`:264-272`](../v2/notifications/rules/incident/incident.go#L264-L272)), and `CopyHealth` forces an immediate recovery by back-dating `healthy_since` to `now - Recovery` ([`:286`](../v2/notifications/rules/incident/incident.go#L286)). Observed.
- A rollback event opens or extends an incident by writing `since`, `notified` and `last_notice` itself ([`events.go:122-126`](../v2/notifications/rules/incident/events.go#L122-L126)): a second writer of the same record. Observed.
- The document validates that each known field has the right JSON type, not that the combination is one the transitions produce ([`document.go:114-118`](../v2/notifications/rules/incident/document.go#L114-L118)). The version-1 shape is shared with the Python checker, which constrains any reshape of the persisted form. Observed.

**L6 Notice delivery**

- Both outboxes implement "pending, then delivered or abandoned and remembered until `now + History`, or retried at the tail within the retry window" with the same 7200-second bounds ([`incident/document.go:22`](../v2/notifications/rules/incident/document.go#L22), [`notify/outbox.go:14-15`](../v2/notifications/rules/notify/outbox.go#L14-L15)). Incident's `Settle` works on untyped maps and retries only copies with `about` ([`incident/outbox.go:6-26`](../v2/notifications/rules/incident/outbox.go#L6-L26)); notify's is typed ([`notify/outbox.go:177-190`](../v2/notifications/rules/notify/outbox.go#L177-L190)). Observed. An entry's position (pending or remembered) is which collection holds its key, which is explicit enough; the duplication, not the state, is the finding.

**L7 Console pod drain**

- One boolean, one refusal, one-way (serving to draining). Observed. Listed for completeness; nothing further is held.

### 2.3 Precedents (class B)

| Precedent | What makes it explicit | Evidence |
| --- | --- | --- |
| Heartbeat flag | `Decide(observation, flag, now)` returns an `Action` (none, alert, remind, recover) and the `Next` flag; the orchestration saves `Next` only after ntfy accepted the notice. The persisted flag's two times are both null or both numbers. The flag itself is still `Alerting bool` plus zero-valued times. | [`heartbeat/heartbeat.go:34-81`](../v2/notifications/rules/heartbeat/heartbeat.go#L34-L81), [`v2/notifications/heartbeat.go:82-99`](../v2/notifications/heartbeat.go#L82-L99), [`heartbeat/state.go:26-28`](../v2/notifications/rules/heartbeat/state.go#L26-L28) |
| Restore plan | Closed set of plan states and named reasons; the run refuses to start unless the plan is `ready`. | [`v2/recovery/rules/restore.go:8-28`](../v2/recovery/rules/restore.go#L8-L28), [`restore.go:143-145`](../v2/recovery/restore.go#L143-L145) |
| Restore claim | The first change is a Secret only one run can create; a refused create is a `Refusal` ("not started"), any later failure is not, so the type alone says whether anything changed. | [`restore.go:129-134`](../v2/recovery/restore.go#L129-L134), [`:171-178`](../v2/recovery/restore.go#L171-L178) |
| Promotion selection and readiness | Named refusals in a fixed order; readiness returns `Ready` with one named reason. | [`v2/delivery/rules/promote.go:13-35`](../v2/delivery/rules/promote.go#L13-L35), [`v2/delivery/rules/readiness.go:19-45`](../v2/delivery/rules/readiness.go#L19-L45) |
| Image webhook | Typed `ImageHookState` (in place, missing, stale) decided from what GitHub lists; convergent, so nothing is held between runs. `ID` is meaningful only for `stale`. | [`v2/kernel/imagehook.go:73-114`](../v2/kernel/imagehook.go#L73-L114) |
| Read vocabulary | `present`, `absent`, `unavailable`, `not_read` mean the same everywhere, so absence and a failed read are never confused. | [`v2/observe/explain.go:45-67`](../v2/observe/explain.go#L45-L67) |
| Live-test cleanup | Re-reads the cluster and converges (units, labelled namespace, claimed volumes) instead of trusting step flags; only the list of volumes seen is held. | [`v2/orchestration/lifecycletest.go:256-279`](../v2/orchestration/lifecycletest.go#L256-L279) |

### 2.4 Observed-only classifiers that disagree (class C)

- **Flux request handled.** `fluxRun.wait` classifies a unit inline (handled at its generation and Ready, handled and not Ready, otherwise keep waiting) ([`v2/orchestration/flux.go:179-202`](../v2/orchestration/flux.go#L179-L202)). `delivery/rules.FluxUnitAt` classifies the same object into `waiting`, `ready`, `failed`, `suspended` with a "not yet" reason list ([`v2/delivery/rules/unit.go:12-23`](../v2/delivery/rules/unit.go#L12-L23), [`:68-83`](../v2/delivery/rules/unit.go#L68-L83)) for `delivery.FluxWait` ([`v2/delivery/fluxwait.go:175`](../v2/delivery/fluxwait.go#L175)), which also carries a helper marked "Copied from orchestration/flux.go" ([`fluxwait.go:194-195`](../v2/delivery/fluxwait.go#L194-L195)). The two answer different questions (this request handled, versus this revision applied), but `wait` treats any `Ready=False` after handling as failed, where `FluxUnitAt` treats `Progressing` or `DependencyNotReady` as not yet. Reading.
- **Kubernetes Job verdict, both recovery's.** The restore's `jobState` returns the first true `Complete` or `Failed` condition and otherwise `running` ([`restore.go:769-791`](../v2/recovery/restore.go#L769-L791)); the backup report's `outcome` prefers `Complete` over `Failed`, says `running` only with active pods, and otherwise `unknown` ([`v2/observe/backup.go:264-284`](../v2/observe/backup.go#L264-L284)). Same object, two vocabularies (`JobComplete` against `RunComplete`), different answers for a Job with no conditions and no active pod. Reading.
- **Dry run as a mode flag.** `DryRun bool` is threaded through six workflows that decide and then act: [`bootstrap.go:24`](../v2/orchestration/bootstrap.go#L24), [`apphooks.go:22`](../v2/orchestration/apphooks.go#L22), [`apprepo.go:117`](../v2/orchestration/apprepo.go#L117), [`console/publish.go:31`](../v2/console/publish.go#L31), [`notifications/check.go:29`](../v2/notifications/check.go#L29), [`notifications/heartbeat.go:30`](../v2/notifications/heartbeat.go#L30). In ClickStack's bootstrap the flag is checked at each step and `register`'s boolean result doubles as "a dry run stopped here" ([`bootstrap.go:139-158`](../v2/orchestration/bootstrap.go#L139-L158)). Not a lifecycle; noted because a decided-plan-then-apply shape is the usual alternative.

## 3. Draft state tables

Drafts of what the current code implies, for the candidates whose position is non-trivial (L1, L2 with L3, L4, L5). They describe behaviour as read, so they can serve as test tables; they are not a proposed type. Invalid combinations are those the current representation allows and no transition produces.

### 3.1 L1 Restore run

Forward path. Each step's held fact is set before its request unless noted; a failure at any step goes to *Unwinding* with the facts held so far.

| Position after the step | Step | Held fact it adds or clears | Undo owed on failure |
| --- | --- | --- | --- |
| Claimed | `claim` | `secretName` (after the server's answer) | delete Secret |
| Release suspended | `suspend` | `suspended`; `releaseWasSuspended` read first | resume, unless operator-suspended |
| Backup suspended | `backup-suspend` | `backupSuspended`; `backupWasSuspended` read first | resume schedule, unless operator-suspended |
| Backup drained | `backup-drain` | none | as above |
| Scaled down | `scale-down` | `scaled` | scale up |
| App pods gone | `pods-gone` | none | as above |
| Job may have run | `job` | `jobName`, `jobMayHaveRun` (cleared on the server's refusal) | delete Job; then gated, see below |
| Job finished | `job-finished`, `job-result` | `Job.State`, `Job.Log` | gated |
| Job deleted | `delete-job` | clears `jobName` | gated |
| Secret deleted | `delete-secret` | clears `secretName` | gated |
| Backup resumed | `backup-resume` | clears `backupSuspended` | gated |
| Scaled up | `scale-up` | clears `scaled` | gated |
| Released to Flux | `resume` | clears `suspended` | none |
| Restored | `available` | none | none |

Unwinding, in order ([`restore.go:386-419`](../v2/recovery/restore.go#L386-L419)):

| Condition on held facts | Action | Then |
| --- | --- | --- |
| `jobName` set | delete Job | continue |
| `secretName` set | delete Secret | continue |
| `jobName` still set (delete failed) | none | stop: **Held**, a restore pod may hold the claim |
| `jobMayHaveRun` and no evidence the database is in place | record `database: not_in_place` | stop: **Held**, database may be missing |
| `backupSuspended` | resume schedule | continue |
| `scaled` | scale up | continue |
| `suspended` | resume release | end |

Terminal outcomes:

| Outcome | `State` | `App.State` | Left suspended |
| --- | --- | --- | --- |
| Refused (nothing changed) | error only (`Refusal`) | none | nothing |
| Restored | `restored` | `running`, or `stopped` for an app at 0 replicas | nothing the run owns |
| Failed and put back | `failed` | `unknown` (never observed running) | nothing the run owns |
| Failed and held | `failed` | `stopped` | release and schedule, deliberately |

Representable but invalid: `suspended` true for an operator-suspended release reported as left suspended by the run (R1); `jobMayHaveRun` without ever having had a `jobName`; any undo flag still set after `Restored`.

### 3.2 L2 Console job and L3 its landing

| From | Event | To | Writer |
| --- | --- | --- | --- |
| (none) | `Start` accepted (not draining, no running job for the target) | `running` | [`jobs.go:176-200`](../v2/console/jobs.go#L176-L200) |
| (none) | `Start` while draining or a job for the target runs | refused | [`jobs.go:179-187`](../v2/console/jobs.go#L179-L187) |
| `running` | work returned no error | `succeeded` (+ `Finished`, `Link`) | [`jobs.go:228-242`](../v2/console/jobs.go#L228-L242) |
| `running` | work returned an error | `failed` (+ `Finished`, error line) | same |
| `running` (another pod's record) | its pod is known gone | `interrupted` (+ `Finished`) | [`jobs.go:117-130`](../v2/console/jobs.go#L117-L130) |
| `succeeded`, `failed`, `interrupted` | any | unchanged (terminal) | none |

The landing is held only as two write-once facts on a terminal job whose pull request merged:

| Held facts | Meaning | Next |
| --- | --- | --- |
| `Outcomes` false, no `Target`, no `Reached` | record from before outcomes were kept | stays `historical` |
| no `Target` | merge commit not read yet, or its read failed | `Capture` on a later page read |
| `Target`, no `Reached` | following main and the cluster: `ProgressOf` decides | `Reach` once `ProgressOf` is at an end state |
| `Target`, `Reached` | it ran; a later change may replace it, not unsay it | none |

Representable but invalid: `running` with `Finished`, or a terminal state without it (accepted by `decodeJob`); `Reached` without `Target` on a record with `Outcomes` (not produced by `Reach`'s caller, which reads `ProgressOf` after a capture, and not rejected on read).

### 3.3 L4 New app

| Position on GitHub and the registry | How a run tells it today | Create run | Resume run |
| --- | --- | --- | --- |
| No repository | `Exists` false | render, create, push, add webhook, wait for image | refused: "does not exist" |
| Repository with GitHub's initial commit only | `Exists` true (same as below) | refused: name taken | waits for an image of the initial commit; times out (reading) |
| Repository with the app's first commit, no webhook | `Exists` true | refused | waits for the image; webhook stays missing (warned) |
| Repository with commit and webhook, no image yet | `Exists` true | refused | waits for the image |
| Image published | image found by commit tag | returns image and digest | returns image and digest |

The console side ([`activity.go:84-95`](../v2/console/activity.go#L84-L95)): a new-app job is resumable when it is `failed` or `interrupted`, has no `Link` (no pull request opened) and no later `running` or `succeeded` job exists for the unit. Representable but invalid: none beyond L2's; the gap is that three GitHub positions are one `Created` value.

### 3.4 L5 Incident record

| Position | Held keys | Observation | Next |
| --- | --- | --- | --- |
| Quiet | none (or cleared) | healthy (`""`) | Quiet |
| Quiet | none | fault | Faulting: `since`, `fault` set |
| Faulting, within grace | `since`, `fault` | fault | Faulting |
| Faulting, past grace | `since`, `fault` | fault | Notified: notice queued, `notified`, `last_notice` set |
| Faulting, not yet notified | `since`, `fault` | healthy | Quiet (cleared) |
| Notified | `since`, `fault`, `notified`, `last_notice` | fault, reminder due | Notified: reminder queued, `last_notice` moved |
| Notified | as above | healthy | Recovering: `healthy_since` set |
| Recovering | as above, `healthy_since` | healthy for `Recovery` seconds | Quiet: recovery queued, cleared |
| Recovering | as above | fault | Notified: `healthy_since` deleted |
| Any | any | unjudged (`nil`) | same position without `healthy_since` |
| Recovering (notify service) | as above | copies accepted | Quiet at once (`healthy_since` back-dated) |
| Any instance | any | cluster unreadable (`Unknown`) | same position without `healthy_since` |
| Any instance | any | stopped, or suspended while not unhealthy | Quiet (cleared) |
| Any instance | any | rollback event | Notified (written by `events.go`) |

Representable but invalid: `notified` without `since`; `healthy_since` without `notified` (no transition writes it then; `Unknown` and `nil` only delete it); `last_notice` before `since`. The document's validator checks field types only ([`document.go:114-118`](../v2/notifications/rules/incident/document.go#L114-L118)).

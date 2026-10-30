# Offline Go v2: status and handoff

Status: in progress
Gate: a template release writes the declaration label, then the Deploy workflow calls `swhurl-v2 deploy` for v2-owned staging instances, then a live image
Uncommitted: none

Where v2 stands and how to resume it. The [iteration 2 plan](iteration-2-plan.md) owns the **contracts** (records, identity, selection, the compiler, the phase-3 brief and [the gap list](iteration-2-plan.md#6-deliberately-deferred)); [workstreams](workstreams.md) indexes the workstreams; [current state](current-state.md) holds dated live evidence. This page does not restate a contract: link it or change it there.

## Authority

**Approved:** the operator said yes to the concrete phase-1 proposal: root-level `v2/`, Go 1.26.0, JSON-only descriptors, positional canonical encoding, manifest-only `linux/amd64` artifact claims, empty-only bindings and the narrow policy exception. Phase 2's compiler and its review decisions are approved and implemented. The operator's [design principles](iteration-2-plan.md#design-principles) and the revised [phase-3 brief](iteration-2-plan.md#phase-3-shadow-not-synchronise) are approved as direction; only the [first pure HTTP slice](iteration-2-plan.md#first-slice-proposal-pure-http-preview-and-comparison) is additionally approved for implementation: v2 is not bound to v1 contracts, responsibilities and data have explicit owners, and transition may have incomplete semantics. **Order approved 25 October 2026:** retire risk, not phase numbers — one disposable instance through delivery, observation and removal (phases 4–6) comes before the rest of the bridge, which is still required before any existing app is migrated ([why](iteration-2-plan.md#5-incremental-delivery)).

**Not approved:** v1 behaviour changes, a platform console UI, a general framework, bridge integration beyond that slice, adapters, credentials, v2 publication, live migration or feature removal. The [code graph](visual-change-tool.md) is a separate workstream; do not build it in this thread.

**Approved 25 October 2026: phase 5 observation, offline half only** — the pure evidence projection and the first real adapter, with no cluster read, no credential, no command and nothing published. The [slice record](#slice-record-phase-5-observation-offline-half) below states what it does and does not do. Everything else about that phase, including reference derivation, a credential source, the report envelope and any live read, is still not approved.

**Approved 26 October 2026: restore, option 1 then option 2** — v2 first *plans and verifies* a restore while v1 performs it (implemented, below), then v2 performs it (**2a**: v2 orchestrates, a pod decrypts; the operator chose this over a vendored age dependency). 2a's write primitives are in, and its restore Job and sequence are now implemented offline (below).

**Approved 29 October 2026: slice 3's first live migration — `demo-notifier` staging** — the first instance with an event subscription to move, receiving `demo-orders.created` from a v1 publisher: its records, its stream-side manifest and its encrypted seed are v2's, and an event flows through the pair across the two owners. See [the record](#slice-record-the-first-migrated-event-using-worker-demo-notifier-staging-29-october-2026).

**Approved 29 October 2026: slice 3's cutover — v2 owns NATS's user list** — `platform/nats/users.json` is v2's output now, v1's generator is deleted, and the Render workflow regenerates it, so the list the broker serves can no longer be a second writer's opinion. Implemented and checked offline, then applied; see [the record](#slice-record-the-cutover-v2-owns-the-user-list-29-october-2026).

**Approved 29 October 2026: image automation's offline half** — the operator said go ahead on the gate. v2 renders the `ImageRepository`/`ImagePolicy` pair into a staging instance's unit, and gains `swhurl-v2 deploy`: the platform's tag policy picks the newest tag, a read-only registry adapter resolves its `linux/amd64` manifest and reads the declaration the build wrote into the image's config labels, the instance refuses a fact it cannot take, and the use case selects, compiles and writes the pair and the files in one effect. **The pair is live (30 October 2026) and reporting; no workflow calls `deploy` and no image has travelled it**, and the deployed images predate the declaration label. That is the next gate. See [the record](#slice-record-image-automations-offline-half-29-october-2026).

**Approved 29 October 2026: the telemetry export** — the operator said go ahead on the slice found while testing the rotation. A v2-owned instance's policy states `Egress` and admitted only DNS, its broker and its callers, so the collector its env names (`http://$(HOST_IP):4318`, a host-network DaemonSet) was refused. The profile now states the block that address is in (`telemetry.nodeCIDR`, with a telemetry block required to state it), and a reporting image's policy admits that block on the endpoint's own port. Nothing else about the policy changed: an instance that reports nowhere gets no such rule, and everything else is still denied. See [the record](#slice-record-the-telemetry-export-29-october-2026).

**Approved 29 October 2026: minting a broker identity** — the operator said go ahead on the gate. `swhurl-v2 mint` draws a seed, derives the nkey pair in the pure model (pinned against RFC 8032 and v1's independent encoding), encrypts the Secret that holds it to the profile's SOPS recipient, and writes it beside the instance's record before the record that states the public key. It is the first process v2 starts, so the `no-subprocess` rule now names **one** adapter — `adapters/sops`, which runs `sops` with the plaintext on stdin and reads no private key — and still refuses `kubectl`/`flux` and every other adapter. The two v2-owned instances without a pair (`swhurl-try-10`, `swhurl-try-11` staging) were minted and recompiled; a rotation of a live pair is the same command with `--rotate` and was exercised offline only. See [the record](#slice-record-minting-a-broker-identity-29-october-2026).

**Approved 29 October 2026: the Reloader watch list** — the operator said go ahead on the gate. The list v1 wrote into the Reloader HelmRelease is now v2's own platform document: `platform/reloader/values.json`, a ConfigMap the HelmRelease merges as values, rendered by `swhurl-v2 reloader` from the profile's own namespaces and every instance that declares a Secret. v1's writer is deleted and the Render workflow regenerates the file. **The same question for image automation is answered by not doing it in this slice**: it needs a v2 selection path first, because the `ImagePolicy` that starts a deploy is read by v1's Deploy workflow, which writes v1's records — rendering the policy for a v2-owned instance would start a workflow that cannot write its release. It stays in the [gap list](iteration-2-plan.md#6-deliberately-deferred). See [the record](#slice-record-reloaders-watch-list-29-october-2026).

**Approved 29 October 2026: slice 3, events — part two implemented offline** — on the operator's principle that v2 favours a clean contract over anything that preserves v1's: the release states what it publishes, the instance states its broker identity and the events it receives, the profile states the broker, and an instance that signs in renders its variables, its stream and its consumers. The seed travels as the encrypted file it already is — v2 lists it, copies it and never reads it. Nothing was applied; the cutover and the first migrated worker are the next gate. See [the record](#slice-record-events-part-two-the-instance-side-29-october-2026).

**Approved 29 October 2026: slice 3, events — part one implemented offline** — the operator said go, and the slice was split where the credential sits: v2 now renders the server's user list from a pinned snapshot and CI compares it with the file the platform runs (`swhurl-v2 users`), which needs no key material; the per-instance side (streams, consumers, the env and the seed) and the generated-key decision follow. See [the record](#slice-record-events-part-one-the-servers-user-list-29-october-2026).

**Approved 29 October 2026: slice 2, the worker kind** — the release's `runtime.kind` may be `worker`: an instance that serves nothing, so it renders no Service, probes or route and keeps every other fact (quota, policy, unit, telemetry, notify, team, calls, allocation). `job` stays refused: no instance is one, and it would add an instance-level schedule. Implemented offline; nothing was applied and no instance migrated. See [the record](#slice-record-the-worker-kind-29-october-2026).

**Approved 29 October 2026: slice 1, the always-on instance surface** — telemetry, notifications and the team label, with the team owned by a new app-level record (`v2-records/<app>/app.json`). The operator chose the app record over an instance field so staging and production cannot disagree. Implemented offline, then rolled out to both v2-owned staging instances on 29 October 2026.

**Approved 27 October 2026: 2a's offline half, with the design brief's five answers** — the presigned read URL is signed in `adapters/s3` (`Reader.PresignURL`, query-string SigV4, AWS's published vector), the URL travels in a Secret rather than the Job document, the Job is named `<app>-restore-<UTC second>`, the pod reuses the instance's backup NetworkPolicy by label, and `--confirm` is the gate while the plan stays the default. The command stays thin: flags in, exit code out. The **live** half — stopping `swhurl-try-10` staging and restoring its newest archive — is not approved yet and needs a handover note.

**Approved 26 October 2026: the object-store read, route 1 with the operator's own credentials** — `observe --archive` lists the instance's own prefix with a hand-written SigV4 signer (no dependency), the region comes from the bucket itself (no profile field, so no identity moves), and the credential is the operator's environment for the live read only: no new Secret. The live read is taken and recorded.

**Approved 26 October 2026: the backup observation, offline half** — the operator said "go" to the brief: the answer lives in `observe` as a `backup` block (never in the running verdict), the reads are the CronJob, the runs' pods and the newest run's Job, the threshold is `--max-age` (default 26h), and the object store is not read. The live read of one instance is a separate word, and has not been taken.

**Approved 26 October 2026: the `migrate` use case, as its brief proposed** — the translation is a second pure function in `legacy`, so the preview's own scope does not widen, and the full source commit is supplied by the operator and only *checked* against what the records abbreviate: it is never expanded from one. [The slice](#slice-record-the-migrate-use-case).

**Implementing more needs the operator's approval of a concrete slice** — design approval and permission to push are separate decisions.

## Progress towards the goals

The goal is a v2 that can own instances without breaking the running platform. This table is the standing summary; each slice record below carries its evidence, and it is updated in the same commit as the work.

| Phase | What it proves | State |
| --- | --- | --- |
| 1. Release selection | A reviewed target cannot be silently replaced | **Done** (reviewed) |
| 2. Compiler | Byte-identical resources from validated records, with fixed refusals | **Done** (reviewed) |
| 4. Git delivery | Compiled intent reaches the cluster through Git; no egress | **Done end to end** (one disposable instance, applied and removed 25 Oct) |
| 5. Observation | What a cluster actually runs: intended digest, readiness, and unavailable ≠ absent | **Done through a live read** (25 Oct) |
| 3. Bridge | A predecessor's committed output compared with a v2 preview, scoped facts, gaps, an ownership inventory, and a read-only live check of the claims | **Slices 1–4 done** |
| Routes | A signed-in route from platform facts, verified live through the shared sign-in | **Done, live** |
| 6. First live instance | A v2-owned instance published, observed **by v2 itself** and removed | **Done 25 Oct** (two throwaways, both removed) |
| Calls | Consent decided purely, a pair rendered both ways, delivery wired (`resolve` → `compile --calls`), the keys rendered once for every managed namespace, and the auth material reaching a callee only as files its image re-reads | **Done, proved live**: the pair (26 Oct), and on 29 Oct a new caller and a key rotation each taking effect in the running pod with **no restart** |
| Storage | A retained volume, one writer, and a namespace removal cannot prune | **Done, proved live** |
| Backup | A nightly job per database that leaves through the proxy and cannot be lost to removal or quota | **Done and proved live 27 October 2026**: the job, its archive, the restore v2 performs — the plan, the Secret, the Job, the sequence with its undo, the report — and one real restore of `swhurl-try-10` staging in 30 seconds |
| 7. Selective replacement | An existing v1 app migrated to v2 ownership | **Two live** (`swhurl-try-11`, `swhurl-try-10` staging, the second stateful on its own volume), and a migration's records are now **written by v2** (`migrate`) rather than by hand |
| Egress | A stable port per instance, the client variables, the grants annotation, the policy, DNS, and the proxy's own configuration — one port per allocation, identity by port, grants enforced | **Done, live**: v2 renders the proxy and enforces grants (26 Oct) |
| Always-on instance surface | Telemetry (the endpoint and the way out to it), the notify address and token, and the app-owned team label on every instance | **Done and live 29 October 2026**: the env, tokens and team labels rolled to both v2-owned instances, and a reporting image's policy admits the collector the env names (the export was found blocked the same day; [evidence](current-state.md#a-v2-owned-instance-cannot-reach-the-node-collector-29-october-2026)) |
| Reloader watch list | A platform document v2 renders: the platform's own watched namespaces and one per instance that declares a Secret | **Done and live 29 October 2026**: `platform/reloader/values.json` replaces the list in the HelmRelease, which merges it as values |
| App kinds | A worker release compiles to an instance that serves nothing, with every other fact intact | **Done, offline 29 October 2026** (the worker kind); `job` still refused |
| Events | The server's user list, and per instance its key, stream, consumers and seed | **Parts one and two, offline 29 October 2026**: the user list renders identically to the platform's, and a keyed instance renders its variables, stream, consumers and unit; the seed is the encrypted file v2 copies without reading |
| Minting a broker identity | v2 can create a pair, not only adopt one: the seed is encrypted to the platform's recipient and the record states the public key | **Done, live 29 October 2026**: `swhurl-v2 mint`, used for the two v2-owned instances that had no pair; rotation is the same command with `--rotate` |
| Image automation | A migrated staging instance following its published images | **The pair is live 30 October 2026** and reports each repository; the delivery path is offline: `swhurl-v2 deploy` resolves the newest tag, reads the image's declaration label, refuses what the instance cannot take, and writes the records and files. The deployed images predate the label, so a template release and a rebuilt image, then the workflow, are the next gate |

**Retired so far:** the release/instance identity boundary; resource generation that does not need provenance; delivery and pruning; the semantics of "running" (readiness is not the artifact, and a failed read is not absence); and, offline, the bridge's translation and comparison for HTTP workload facts — 135 declared objects inventoried over this repository's own 17 instances, 10 compared with **no field differences** and no cross-instance ownership conflict.

**Not yet true, and what would make it true:** delivery is manual (compile, commit, push), except a v2-owned staging instance's image selection, which is offline until the Deploy workflow calls it; v2 has performed one restore live, but a restore whose content changes by design, another instance's archive and every failure path live are unexercised; v2 renders no job kind, no team grants, no secrets, no alarms and no offers, so a migration reports those as gaps and refuses one whose release declares secrets; a migrated instance's relationships come from the committed resolution, and `resolve` still needs the v1-shaped record copies the handover leaves behind; observation needs a kubeconfig in its JSON form; and the bridge still exports its snapshot with a separate command and cannot yet state ownership for output that no record explains *live*.

**Risks still open:** a first migration of a stateful app (backup/restore, retention, rollback); publication automation and its credentials; whether the pinned chart's conventions hold across a chart upgrade (now pinned by tests, not by memory); the egress cutover, which changes live outbound traffic and needs the grants recorded before it can refuse the rest; and the console image's coupling to `tools/`, which any exporter change re-deploys.

## Where it stands

| Phase | State | Evidence |
| --- | --- | --- |
| 1. Release-selection kernel | done, reviewed, committed 24 October 2026 | [contract](iteration-2-plan.md#2-phase-1-model-and-contract), [evidence](iteration-2-plan.md#phase-1-verification-evidence) |
| 2. Deterministic compiler | done, reviewed, committed 25 October 2026 | [contract](iteration-2-plan.md#phase-2-establish-compiler-ownership) |
| 4. Git delivery | done: offline half and one live instance, 25 October 2026 | [slice](iteration-2-plan.md#first-delivery-slice-implemented-offline-25-october-2026-operator-run) |
| 5. Observation | done through a live read: slices 1, 2a and 2b committed 25 October 2026; `observe` read `demo-orders` read-only and reported the intended artifact not running | [slice 1](iteration-2-plan.md#first-observation-slice-what-is-running-implemented-offline-25-october-2026), [2a](iteration-2-plan.md#second-observation-slice-the-target-from-compiled-output-implemented-25-october-2026) |
| 6. First live instance | done 25 October 2026: applied, verified and removed, see the record above | [evidence](current-state.md#first-v2-instance-live-25-october-2026) |
| 3. Shadow bridge | slices 1–4 done, offline: pure comparison; legacy translation with `preview`; snapshot export and the ownership inventory with `shadow`; the live ownership read of each preview's claims | [slice 1](iteration-2-plan.md#first-slice-proposal-pure-http-preview-and-comparison), [2](iteration-2-plan.md#phase-3-slice-2-legacy-translation-and-a-preview-report-implemented-offline-25-october-2026), [3](iteration-2-plan.md#phase-3-slice-3-snapshot-acquisition-and-the-ownership-inventory-implemented-25-october-2026), [4](iteration-2-plan.md#phase-3-slice-4-live-ownership-implemented-25-october-2026) |
| Calls | done offline: consent decided purely, a pair rendered both ways, and delivery wired (`resolve` → `compile --calls`); `swhurl-try-11` staging calls nothing yet | [slice](iteration-2-plan.md#calls-between-apps-resolver-and-pair-rendering-implemented-offline-25-october-2026) |
| Storage | done for the volume and proved live | [slice](iteration-2-plan.md#sqlite-storage-the-volume-implemented-and-proved-live-25-october-2026) |
| Egress | done and live: the instance side, the proxy's configuration rendered by v2, grants enforced, v1's generator retired | [part one](iteration-2-plan.md#egress-part-one-the-allocation-and-the-instance-side-implemented-offline-25-october-2026), [part two](iteration-2-plan.md#egress-part-two-the-proxys-configuration-implemented-offline-25-october-2026), [cutover](iteration-2-plan.md#the-egress-cutover-implemented-26-october-2026) |
| Backup | **Done and proved live 27 October 2026**: rendered, both facts observed live, a restore planned live, and **v2 performed one**: the age-key and read-URL Secret, the Job from the backup image, the sequence with its undo, the `v2-restore` version-2 report, and `swhurl-try-10` staging restored over its newest archive in 30 seconds | [render](iteration-2-plan.md#sqlite-backup-egress-part-three-decision-brief-and-implementation-25-october-2026), [job](iteration-2-plan.md#the-backup-observation-implemented-offline-26-october-2026), [archive](iteration-2-plan.md#the-object-store-read-implemented-26-october-2026), [restore](iteration-2-plan.md#the-restore-v2-performs-option-2a-offline-half--27-october-2026) |
| Always-on instance surface | done and live 29 October 2026: the env, notifications and team label rolled to both v2-owned instances at `1bca7990`, the app record owns the team, and from the telemetry slice a reporting instance's policy admits the collector its endpoint names | [slice](#slice-record-the-always-on-instance-surface-29-october-2026) |
| Worker kind | done, offline 29 October 2026: a `worker` release renders no Service, no probes and no route, and keeps every other instance fact; `job` still refused | [slice](#slice-record-the-worker-kind-29-october-2026) |
| Broker identity minting | done and live 29 October 2026: `swhurl-v2 mint` derives the pair in the pure model, encrypts the seed through `adapters/sops` (the one command v2 starts) to `sops.recipient`, writes it beside the record and then the record; the two v2-owned instances without a pair were minted, and `make v2-users` and the Reloader list gained them | [slice](#slice-record-minting-a-broker-identity-29-october-2026) |
| Events | parts one and two, offline 29 October 2026, and **the user list is v2's file**: v1's generator is deleted and the Render workflow regenerates `platform/nats/users.json`; the instance side renders variables, stream, consumers, the unit's broker dependency and `decryption`, with the seed as a declared input | [part one](#slice-record-events-part-one-the-servers-user-list-29-october-2026), [part two](#slice-record-events-part-two-the-instance-side-29-october-2026), [cutover](#slice-record-the-cutover-v2-owns-the-user-list-29-october-2026) |
| Image automation | the pair is **live 30 October 2026** (three policies Ready and reporting, no pod restarted); `swhurl-v2 deploy` is offline, and the deployed images predate the declaration label | [slice](#slice-record-image-automations-offline-half-29-october-2026) |
| Reloader watch list | done and live 29 October 2026: v1's writer deleted, `make v2-reloader` (and the Render workflow) renders `platform/reloader/values.json`, and `make check-v2` compares it with Git; the Reloader HelmRelease merges it as values | [slice](#slice-record-reloaders-watch-list-29-october-2026) |
| 7. Selective replacement | three live: `swhurl-try-11` staging, `swhurl-try-10` staging (with its own volume) and, as the first subscriber, `demo-notifier` staging; the `migrate` use case writes their record pairs from v1's records, and the two real migrations are its acceptance test | [phases](iteration-2-plan.md#5-incremental-delivery), [slice](#slice-record-the-migrate-use-case), [evidence](current-state.md#the-first-stateful-migration-swhurl-try-10-staging-keeps-its-volume-26-october-2026) |

Of the phase-1 review's findings, the coexistence check's blind spots are deliberately left. Nothing in v2 is published, and no v2 intent or resource reaches an active Flux path.

## Decisions

**28 October 2026 — the backup host is the platform's grant (approved).** A database instance's nightly upload is granted by the platform, from `PlatformProfile.backup` (its bucket and a new `region`), not declared by the instance; `egress` takes `--profile`. The v2 instance record gains no `egress` field until a v2-owned instance needs an outside host of its own. [Why, and what was rejected](iteration-2-plan.md#the-backup-host-is-the-platforms-grant-implemented-28-october-2026). What reopens it: a second backup destination, or a v2-owned instance that must reach an outside host.

**26 October 2026 — option 2a: v2 orchestrates, a pod decrypts (approved).** v2 performs the restore itself: it suspends the release, scales the workload to zero, creates a restore Job from the platform's backup image (which already carries `age`, the AWS CLI and SQLite), hands that pod a **presigned read URL** for the one archive rather than a long-lived key, mounts the operator's age key from a **Secret that v2 creates and deletes**, waits, reads the Job's log, scales back and resumes. No dependency, so `stdlib-only` stands; the upload key stays upload-only. What reopens it: if the pod must decrypt with something the image lacks, or if a presigned URL is refused by the proxy.

**26 October 2026 — restore, option 1: a plan, not a restore (approved before implementation).** `swhurl-v2 restore` writes a `v2-restore` plan from the bucket and the cluster, read-only: the chosen archive (the newest, or `--from KEY`), the record the backup job published beside it (checksum, table count, `source` as `<namespace>/<application>`), the instance's claim, mount, database path and uid, the observation it was decided from, and the v1 command that performs the restore. It refuses a stateless instance, and its state is `ready`, `stale`, `incomplete`, `no_archive` or `unavailable` — never "restored". v1 remains the executor until option 2, which needs the write path and the age decision this page's gate names. What reopens it: option 2.

**26 October 2026 — the object-store read: route, credential and signer (approved before implementation).** v2 lists the bucket itself (`ListObjectsV2`) rather than scraping the job's log or making v1's check read v2's records; the credential is the **operator's own environment for the live read only** — no new Secret, no rotation — as the phase-6 decision did for delivery and observation; SigV4 is hand-written in `stdlib-only`, proved by AWS's published test vector, rather than adding `aws-sdk-go-v2`; the region comes from the bucket's own `x-amz-bucket-region` header, so no profile field (and no identity) moves; and the read is an opt-in `--archive`, so an observation stays credential-free by default. What reopens it: a console check needing a standing read-only key (its own Secret and rotation), or a second bucket needing a different signer.

**26 October 2026 — the backup observation's shape (approved before implementation, from the brief).** The answer is a `backup` block in `v2-observation` **version 2**, beside the seven running findings and never inside the verdict; it reads the CronJob, the pods the job's runs create (`platform.swhurl.com/backup=true`) and the newest run's Job — reached through the pod's **owner reference**, because a Job created by a CronJob carries no label its own selector could find (v1's `kubectl get jobs -l …` hint selects nothing; its label is on the pod template); and the threshold is `--max-age`, defaulting to v1's 26 hours. What it cannot say: whether an archive exists or is fresh, and whether anything restores — `notCovered` names both. What reopens it: an object-store adapter and its credential, or a CronJob whose Job template states labels of its own (then the pod hop disappears).

**26 October 2026 — `time` in the pure layer, with its clock refused.** `observe` must parse the cluster's RFC3339 timestamps and compare them with the observation time, so `time` joins `PureImports` and the clock's own functions (`Now`, `Since`, `Until`, `After`, `Tick`, `Sleep`, timers) join the calls the `pure` rule refuses. The rule's invariant — "none reads the clock" — is now *checked* rather than implied by an import that could not be made. The archtest's fixture asserts both halves.

**26 October 2026 — `migrate`: where the translation lives, and how the source commit is obtained (approved before implementation).** The translation is [`legacy.Migrate`](../v2/legacy/migrate.go), a pure function beside `Translate` that yields the facts a record pair states, a source per fact and every gap; it may not import `kernel` (the layers rule allows only lower layers), so `orchestration.Migrate` maps those facts onto the kernel's records and encodes them with the kernel's own encoders, then runs the existing compile use case and writes once. The full commit is not in this repository — v1 keeps only a seven-character abbreviation, twice — so `--source-repository` and `--source-commit` are the operator's, corroborated against the image's name, the tag's hex suffix and the commit the offered documents were read at; a contradiction is a refusal, and a record with no abbreviation leaves the commit reported as unverified rather than checked. What reopens it: a Git adapter that resolves the abbreviation (network and credentials, its own approval), or a v2 record that states provenance the pair can verify.

**25 October 2026 — egress and credentials for the first instance (decided, in the operator's words, by accepting both recommendations):** all egress is denied for the first disposable instance (`policyTypes: [Ingress, Egress]`, no rules) rather than routed through the proxy or left open, and delivery and observation use the operator's own credentials from the host rather than a new Secret, runner or control plane. [The record](iteration-2-plan.md#decision-egress-and-credentials-for-the-first-instance-decided-25-october-2026) states what each answer changes, why the alternatives lost, and what reopens them.

**25 October 2026 — observation's first adapter: a thin `net/http` client, not `client-go`.** The operator approved the slice; this is the open decision it carried. It keeps the module free of third-party dependencies and speaks to the four kinds v2 observes through the API server, taking TLS material and a bearer token as arguments. What reopens it: a kind or verb the thin client cannot express, or a credential path (a kubeconfig is YAML; an in-cluster token is another source) that makes `client-go` cheaper than the decision it would replace.

**25 October 2026 — egress part two: what v2 owns, and what it deliberately does not (approved before implementation):** v2 renders the proxy's *generated* objects only — the values ConfigMap and the per-port NetworkPolicy — with **grants enforced**; the policy keeps v1's shape (`policyTypes: [Ingress]`, each port admitted from its own namespace), because the default deny the operator decided is the **instance's** policy, not the proxy's. The values are JSON, which is YAML, so the standard library writes them and the pinned chart renders the same objects from either; the output goes under `--out DIR` with **no new profile root and no kustomization**, because the folder is not yet a whole Flux unit; and the proxy's deployment (chart, image digest, replicas, probes, Namespace, unit) stays the platform's hand-written files. The four facts written twice — the ConfigMap's name and key, the Service's name and namespace, the pod labels, the health port — are tied by an offline test against the live HelmRelease and the pinned chart. [The slice](iteration-2-plan.md#egress-part-two-the-proxys-configuration-implemented-offline-25-october-2026). What reopens it: rendering the proxy's deployment too, which would move the chart pin and image digest into the profile.

**25 October 2026 — the backup job: six decisions, all as proposed ("go").** A database instance renders its own backup — the Secret holding two *references* to the upload key, the nightly CronJob, and a policy for the pod that reaches S3 — from a new `PlatformProfile.backup` block (image and digest, S3 prefix, age recipient, writer Secret, `suspended`); Flux substitution stays the credential mechanism, with a rule that no other `${…}` may appear in a substituted instance's documents; the backup pod is the instance's second workload, so it gets the platform's narrow policy and the quota counts it; the schedule keeps v1's derivation, so a migration keeps its slot; restore and freshness are not modelled. [The slice](iteration-2-plan.md#sqlite-backup-egress-part-three-decision-brief-and-implementation-25-october-2026). What reopens it: v2 owning secret material, or a second workload with different needs.

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
- **Owners and boundaries:** `kernel.RenderInstance` renders resources from workload facts plus environment facts and a platform profile, and owns no release provenance. `shadow.CompareHTTP` owns a bounded comparison policy and imports no `kernel` types. `Compile` keeps its signature, refusals, golden bytes and compilation IDs, and only translates validated records into rendering inputs.
- **What crosses each seam:** the caller passes HelmRelease bytes and explicit resource choices; nothing is read from disk, Git or a cluster, and no result carries a release ID, instance revision or readiness flag.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** v1 unchanged (no v1 file touched; `coexistence_test.go` passes). Pure — no clock, network, filesystem, environment, randomness or subprocess in `kernel` or `shadow` (the `pure` rule). Golden compilations byte-identical with unchanged compilation IDs, not regenerated. Refusal codes and precedence unchanged (40,495 differential cases against the previous commit). Failure paths tested: invalid and ambiguous JSON, wrong types, missing facts, an overridden or disabled controller, absent quantities. Assessed and unassessed scope stated per field, with no parity claim for anything unread. Offline only: nothing is published, so no readiness is claimed. This page and the gap list change in the same commit.
- **Evidence:** `make check-v2` and `REQUIRE_HELM=1 make check-v2` (chart render and offline Kustomize ran, not skipped), `make check` (987 tests), `git diff --check`, `make v2-report`; kernel coverage 95.3%, shadow 96.7%. Independent read-only review: OK with notes, no blockers; its two should-fix findings were both false parity and are fixed — see [the record](iteration-2-plan.md#first-slice-proposal-pure-http-preview-and-comparison).
- **Gaps touched:** the [deferred list](iteration-2-plan.md#6-deliberately-deferred) rows for scoped preview/comparison and for complete release semantics.
- **Committed:** `c44aef98`, on the tree pushed through `1ea1bbde`.

## Slice record: phase 5 observation, offline half

- **Outcome and the refusal it must produce:** given one identified target and the evidence a source returned, an operator can read, per fact, whether the intended release is running — the unit reconciled, its applied revision, the release settled, the workload present, the intended digest running in the pods, the workload available, the intended replica count — and a verdict that is `running` only when all seven are positive, `not_running` when any is negative, and `unknown` when anything was unread or unavailable. It refuses at the boundary: a failed read is never absence, a read the caller never made is neither, an unreadable document returns no report, and an unsupported kind, unnamed object, selector-less list or non-https endpoint is an error rather than evidence.
- **Owners and boundaries:** `observe` (layer 0, pure) owns the projection, findings, verdict, scope and validation, and reads nothing — source and time are supplied strings. `adapters/kube` (layer 1) owns one GET: the stated group/version per kind, response identity and translation, TLS and the token, without following a redirect. The caller owns the target's refs, selector, container, intended digest, replicas and source revision, and the credential material. v1's console is evidence about which questions to ask, not the contract.
- **What crosses each seam:** `observe.Target` and `observe.Evidence` into the pure layer; `observe.Read`/`observe.List` out of the adapter, carrying present bytes or a fixed failure reason, never a decision. No release ID, instance revision, readiness flag or cluster name is invented on the way.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — no v1 file touched; nothing under `apps/`, `clusters/` or `platform/` changed; `coexistence_test.go` and `make check` pass (1012 v1 tests).
  2. **Pure takes no effects** — `observe` imports only allowlisted standard packages (`encoding/json`, `fmt`, `regexp`, `slices`, `strings`, `unicode/utf8`); no clock, network, filesystem, environment, randomness or subprocess, which the `pure`, `places` and `no-subprocess` rules check. The effects are in the layer-1 adapter.
  3. **Bytes and identities unchanged** — `kernel` and `shadow` are untouched: golden compilations, compilation IDs and the 40,495-case refusal differential's subject are unchanged and not regenerated. The API baseline grew by 75 lines (the new packages and the exported surface) and lost or changed none, which the slice explains.
  4. **Refusal vocabulary stated, precedence unmoved** — `kernel`'s codes and their order are untouched. The new vocabularies are stated in the [plan](iteration-2-plan.md#first-observation-slice-what-is-running-implemented-offline-25-october-2026): `refused_request`/`credentials` from the adapter, and field/reason refusals from the pure layer, whose target-validation order (namespace, then each role against the roles before it, then the facts) and evidence validation are tested.
  5. **Failure paths tested** — 401/403 `forbidden`, 429/5xx `status`, a deadline before and during the body read (`timed_out`), a transport failure, an answer over 1 MiB, a `200` that is not the object asked for, a list body without `items`, and a redirect (returned as its 3xx, not followed); unread and unavailable evidence reported as such and never as absence; absence limiting the facts that depend on it; malformed documents (non-JSON, trailing content, wrong types, an API object without a generation); contradictory evidence (absence with content, a failure with an unfixed reason, items with a failure, duplicate and unexpected reads and lists); each image case (another digest, an unpinned image, a pod that has not pulled, no pods, a pod without the container); zero replicas; and the verdict precedence and outcome signs, including a negative deciding over an unread fact.
  6. **Scope stated, nothing unread counted** — seven findings with fixed outcomes and reasons, `Assessed`/`Unassessed` per report, and a fixed `NotCovered` list (`history`, `git_publication`, `source_build`, `pods`, `routes`, `storage`, `secrets`, `calls`, `events`, `team`, `quota`, `foreign_ownership`). No finding can be `running`/`ready`/`equal` from evidence that was not read.
  7. **Offline is not live** — no cluster, credential, kubeconfig or `kubectl` was used; the fixtures are recorded shapes with synthetic status blocks; nothing was applied, so no readiness is claimed.
  8. **Recorded in the same change** — this page, [the plan's slice section](iteration-2-plan.md#first-observation-slice-what-is-running-implemented-offline-25-october-2026) and the [gap list](iteration-2-plan.md#6-deliberately-deferred) change together.
- **Evidence:** `make check-v2` (10 packages) and `REQUIRE_HELM=1 make check-v2` (chart render and offline Kustomize ran, not skipped), `make check` (1012 tests, doc checks after this page changed), `git diff --check`, the shell syntax check and `make -s v2-report` — which lists only the three new packages, their layers, `Pure` and the added API lines, with no dependency, budget or rule-setting change. Coverage: `observe` 97.1%, `adapters/kube` 96.3%, `adapters/kube/kubetest` 79.8%. Nothing was read from a cluster.
- **Independent read-only review, 25 October 2026 (before commit):** **one blocker and six should-fix findings, all fixed.** The blocker was a false negative the frozen scenarios could not see: a Deployment reports the generation it has observed on `status.observedGeneration`, not on its `Available` condition, and the fixture had been given the field it does not have, so a healthy instance would have read `not_ready/status_stale`. The six: redirects were followed (a token could reach plain http, and a 404 behind one could read as absence); a `200` that was not the requested object, and a list body without `items`, were read as a present object or an empty list; the `Contract`'s ownership assertions could not fail because they mutated the bytes they compared against; `unit.revision`'s `different` semantics were undecided; zero replicas always reading `unknown` was unstated; and nothing tied the outcome signs to the verdict. Eight nits fixed too (a pod-collection read silently dropped, items on an unavailable list, `unit.revision`'s reason for an unavailable unit, an uncompared repository field, an invented comma-separated revision list, unvalidated names in the adapter, a timeout during the body read, and two assertions that did nothing). Each fix was mutation-checked in a throwaway copy: the fake returning borrowed bytes, a dropped identity check, a followed redirect, ignored staleness and an accepted pod-collection read each fail at least one test.
- **Committed:** `dc12f5d3`, on `main` (not pushed).
- **Deferred rows touched:** [observation of what is running](iteration-2-plan.md#6-deliberately-deferred), [a credential source](iteration-2-plan.md#6-deliberately-deferred), [an operator-facing report](iteration-2-plan.md#6-deliberately-deferred) and the render-fingerprint row (observation did not need it in the cluster).
- **Next gate:** the operator's choice of the next slice: the next observation slice (reference derivation, a credential source, the report envelope) or phase 3 slice 2 (snapshot acquisition, or legacy translation).

## Slice record: phase 5 observation, target derivation

- **Outcome and the refusal it must produce:** given one instance's compiled HelmRelease, its unit and the source revision Flux should have applied, an operator can obtain the complete observation target — objects, selector, container, digest, replicas — without supplying any of them by hand. It refuses a document shape the compiler does not render: more than one controller, a controller that is not the chart's default Deployment, a missing container, digest or replica count, or missing metadata.
- **Owners and boundaries:** `observe` owns the derivation, as a pure function over document bytes; it reuses its own target validation, so a derived target is one `Explain` accepts. The chart's naming conventions are a fact about the pinned chart, asserted against that chart's render in `cmd/swhurl-v2`'s existing offline Helm test. The caller keeps the two facts the document cannot state: the owning unit (read from the compiled unit document, not re-derived from a naming convention) and the intended source revision.
- **What crosses each seam:** compiled document bytes in, one `Target` out; no filesystem, Git, cluster or clock.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged (no v1 file touched). 2. Pure — `observe` still imports only allowlisted packages; the derivation has no effects. 3. Bytes and identities unchanged: `kernel` and `shadow` are untouched, and no golden compilation was regenerated. 4. Refusals: a new fixed vocabulary, stated above and tested; nothing existing moved. 5. Failure paths tested: both golden compilations and every refusal case. 6. Scope: the derivation produces only the target; what the report can then claim is unchanged. 7. Offline only — the chart render is local and no cluster is read. 8. This page, the plan and the gap list change together.
- **Evidence:** `make check-v2` (10 packages), `REQUIRE_HELM=1 make check-v2` (chart render and offline Kustomize), `make check`, `git diff --check`, `make -s v2-report`; `observe` coverage 96.9%; the chart render test asserts the derived Deployment name and every selector term against the pinned chart's own output, which caught a wrong assumption (`<release>-main` instead of `<release>`) before it reached anything else.
- **Deferred rows touched:** the observation row (target derivation now exists).
- **Next gate:** observation slice 2b — a credential source and an `observe` command that reads one instance, read-only.
- **Committed:** `2a851c61` (with the record flip in the commit that follows it).

## Slice record: phase 5 observation, credentials and command

- **Outcome and the refusal it must produce:** an operator can point v2 at one live instance and read, as a versioned document, whether the intended release is running — `swhurl-v2 observe --instance --release --profile --revision --kubeconfig` — with the target derived from the compiled documents and the credential material taken from a kubeconfig in its **JSON** form. Exit 0 means a report was produced, whatever its verdict; the verdict is in the document. It refuses before any request: a kubeconfig it cannot parse or use (no current context, unknown context/cluster/user, material that is not base64, an `exec`/`auth-provider` plugin, no token and no certificate), a plain-http endpoint, a missing `--revision`, and any invalid compiled input.
- **Owners and boundaries:** `adapters/kube` owns the kubeconfig's format and the connection, and still decides nothing about meaning; the command owns compilation, the derived target and the sequencing of four reads under a 20-second bound; `observe` owns the projection and the document's shape. `observe.IsPositive` exposes the verdict's own sign table so a summary cannot become a second copy of the words.
- **What crosses each seam:** kubeconfig bytes into `Connect`, a `Connection` and a `Reader` out; `observe.Evidence` into the projection; `observe.Report` into `EncodeReport`. No credential material reaches a diagnostic: a refusal carries a fixed reason only, which a test asserts.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged (no v1 file touched; `make check` passes). 2. Pure — `observe` still takes no effects; the clock and the reads live in the command and the adapter. 3. Bytes and identities unchanged: `kernel` and `shadow` untouched, no golden compilation regenerated; the API baseline grows by the new types and functions and loses nothing. 4. Refusals: a new fixed vocabulary, stated in the plan and tested case by case. 5. Failure paths tested: every kubeconfig shape, a `403` (unassessed, `unknown`), a `404` (absent, `not_running`), a bad kubeconfig (exit 2, no report), a missing revision (exit 2). 6. Scope: the document states its assessed, unassessed and not-covered facts; the summary names only non-positive findings. 7. Offline tests are not readiness — and the live read is recorded separately, as what it was: a read-only observation with a `not_running` verdict for the intended artifact, not a parity claim. 8. This page, the plan, the gap list and the current-state evidence change in the same commit.
- **Live evidence, 25 October 2026 (read-only):** two observations of `demo-orders` through the real API server returned `unit reconciled`, `unit.revision applied`, `release settled`, `workload present`, `ready ready`; the verdicts were `not_running` because the artifact v2's fixture descriptor pins is not the running image (`image different/digest`), and staging also reported `replicas different/available_replicas`. Recorded in [current state](current-state.md#first-v2-observation-of-a-live-instance-25-october-2026).
- **Next gate:** the operator's choice after observation: phase 3 slice 2 (snapshot acquisition, or legacy translation), or live verification of a v2-owned instance.
- **Committed:** `671e1de6` (with the record flip in the commit that follows it).

## Slice record: phase 3 translation and preview

- **Outcome and the refusal it must produce:** an operator can preview one predecessor instance from its own records and compare the result with the HelmRelease that predecessor committed — `swhurl-v2 preview --app --settings --release --committed --profile` — and read, as a versioned document, the facts used with a source for each, every legacy fact the preview cannot carry with its owner, and the comparison's findings and scope. It refuses a malformed record, a record version it does not know, a release that is not `web`, a quantity spelling outside whole Mi/Gi, a committed output without its application or main controller, and a digest, replica count or memory limit no document establishes — carrying the document and field, never a value.
- **Owners and boundaries:** `legacy` (layer 0, pure) owns legacy interpretation: which fields are facts, where each came from, and which are gaps for another owner. The renderer and comparator keep their existing ownership and are unchanged. The command owns the composition (translate → render → compare) and the `v2-preview` envelope; the records stay the predecessor's authority, and the translation is not a second one — it neither defaults a value nor writes anything back.
- **What crosses each seam:** four documents as JSON and the profile in; a `Translation` (facts, sources, gaps) out, then the renderer's own inputs, then `shadow.CompareHTTP`'s result. No file, Git, credential, cluster or clock.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged (no v1 file touched; its fixtures were converted once into v2 fixtures). 2. Pure — `legacy` has no effects and imports only allowlisted packages. 3. Bytes and identities unchanged: `kernel`, `observe` and `shadow` untouched, no golden compilation regenerated; the API baseline grows and loses nothing. 4. Refusals: a new fixed vocabulary, stated and tested field by field; nothing existing moved. 5. Failure paths tested: every refusal, an unreadable input, a deliberately changed committed digest, and the empty/never-null lists. 6. Scope: the document states a source per fact, a gap with an owner per unmodelled fact, and the comparison's assessed/unassessed scope; an unknown record field becomes a gap rather than a silent drop. 7. Offline only: records are JSON, so no YAML, Git, credential or cluster was read, and no readiness is claimed. 8. This page, the plan and the gap list change together.
- **Evidence:** the inherited `hello`/staging fixture previews with ten fields equal, `image intentional/tag_beside_digest`, `resources.memory intentional/chosen_single_memory`, three gaps and eleven unassessed areas — the phase-3 example, demonstrated rather than asserted. `make check-v2` (11 packages), `REQUIRE_HELM=1 make check-v2`, `make check` (1012 tests), `git diff --check` and `make v2-report` pass; coverage `legacy` 84.0%, CLI 88.2%, `observe` 96.4%.
- **Next gate:** phase 3 slice 3 — snapshot acquisition and the ownership inventory — or another slice the operator chooses.
- **Committed:** `f355d535` (with the record flip in the commit that follows it).

## Slice record: phase 3 snapshot export and ownership inventory

- **Outcome and the refusal it must produce:** an operator can pin one revision and read, as a versioned document, what the bridge concludes about every instance in it — the preview's facts, the comparison with each instance's own committed HelmRelease, every legacy fact not carried, every ownership collision with all its owners, and the snapshot's coverage. `make v2-snapshot REVISION= OUT=` exports the snapshot; `swhurl-v2 shadow --snapshot FILE --profile FILE` analyses it. It refuses a snapshot whose version it does not know, a revision that is not a commit, and an instance with no identity — and it names an instance it cannot compare rather than dropping it.
- **Owners and boundaries:** the exporter (Python, operator tooling) owns reading one revision and deciding nothing; `legacy` owns interpretation; the renderer and comparator are unchanged; `kernel.Ownership` owns the ownership rule in its plural form, and `CheckOwnership` now uses it so there is one implementation; the command owns the composition and the `v2-shadow` envelope. SOPS files are never read and no manifest body is exported.
- **What crosses each seam:** one JSON snapshot into the command; per instance a translation, a rendered resource set, a comparison and claims; one report out. No cluster, credential or clock.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged: no v1 module or record changed; the new command is additive, the catalogue/table test passes. 2. Pure (`legacy`, `observe`, `kernel`) takes no effects; the exporter and the command hold the I/O. 3. Golden compilations and compilation IDs unchanged; `CheckOwnership`'s behaviour and message are unchanged and its test still asserts them. 4. Refusals: the new snapshot/analysis vocabulary is stated; no existing code moved. 5. Failure paths tested: a cross-instance conflict, an instance with no records, residual units, a snapshot with the wrong version or revision, a non-web kind, a missing record, a mutation the comparison must see. 6. Scope: coverage counts, unavailable instances with reasons, and a fixed not-covered list; a missing record is named, not invented. 7. Offline only: the snapshot is read from Git's objects, so nothing live was touched and no readiness is claimed. 8. This page, the plan and the gap list change together.
- **Evidence:** over this repository's own snapshot at `2d4e06ed`: 135 declared objects, 10 instances compared, 7 unavailable (all non-web kinds, each named), **no field differences** in scope and **no cross-instance ownership conflict**; each compared instance reports its own five expected collisions. `make check-v2` (11 packages), `REQUIRE_HELM=1 make check-v2` (including the chart-default assertion), `make check` (1012 tests) and `make v2-report` pass; the exporter's tests cover the pinned revision, a dirty tree, a missing record, SOPS exclusion and determinism.
- **Next gate:** the operator's choice — phase 6/7 live verification of a v2-owned instance, or the bridge's remaining ownership work (declared objects for output no record explains *live*, and the migration decision itself).
- **Committed:** `06aae224` (with the record flip in the commit that follows it).

## Slice record: live verification of a v2-owned instance

- **Outcome:** a third throwaway instance, `swhurl-try-v2-2` in staging, was compiled by v2 (five files and a unit, no v1 record), applied by Flux at a pushed commit, and then **observed with v2's own `observe` command** rather than by hand: `verdict running` with all seven facts positive — unit reconciled, `unit.revision applied` at the pushed commit, release settled, workload present, the compiled digest running, ready, and the intended replica count. It was then removed the way it was created: deleting its files and its unit registration, pushing, and letting Flux prune.
- **What it proves:** the phase-5 contract holds end to end against a real API server for an instance v2 owns; the phase-4 delivery path and the phase-5 observation path now close a loop that does not need v1 tooling.
- **Not exercised:** an unavailable or absent read (not manufactured), pod-level facts, and isolation re-testing — the compiled NetworkPolicy has the same shape as the one demonstrated earlier, including that egress enforcement lags a new pod by seconds.
- **Evidence:** [current state](current-state.md#third-v2-owned-instance-observed-by-v2-itself-25-october-2026), including the reported verdict and the objects Flux created; the removal's prune evidence is recorded there too.
- **Next gate:** the bridge's remaining ownership work: declared objects against the live cluster, then the first migration decision.

## Slice record: phase 3 live ownership

- **Outcome and the refusal it must produce:** `swhurl-v2 shadow --snapshot … --profile … --kubeconfig …` also reads, from the API server, every object a preview would create, and reports each as `present`, `absent` or `unavailable` with its reason. A failed read is never absence, a failed read never changes a comparison, and without `--kubeconfig` nothing live is read. It refuses what the adapter cannot state: a kind outside its table, a namespaced reference to a cluster-scoped object, and a kubeconfig it cannot use.
- **Owners and boundaries:** the adapter owns the kind table (now including the cluster-scoped Namespace, ResourceQuota and NetworkPolicy), the paths and `RefOf`, so no caller guesses a version; the command owns the read sequence and the document. Scope is stated: only the preview's own claims are read — Secrets, CronJobs, Streams and image policies are not, so this is not full inventory coverage.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged. 2. Pure packages untouched by this slice's effects; the read lives in the command and the adapter. 3. Bytes and identities unchanged; `api.txt` grows by `RefOf`. 4. New refusals stated and tested; nothing existing moved. 5. Failure paths tested: present, absent (404), forbidden (unavailable with reason), and the unconfigured case; a failed read is asserted *not* to be absence. 6. Scope: only claims are read, and the report's `live` list is empty rather than absent when not configured. 7. Offline tests and unit tests are distinguished from the real read, which is recorded as evidence. 8. This page, the plan and the gap list change together.
- **Evidence, read-only, 25 October 2026:** `shadow` over this repository's snapshot at `HEAD` with the operator's kubeconfig in its JSON form: 50 live reads across 10 instances, all `present` — every claim a preview would create already exists, owned by the instance v1 runs; no read failed. Recorded here rather than in current state: nothing on the cluster changed.
- **Next gate:** the operator's choice of the next domain slice — routes (`authenticated-web`), which every web instance needs and which the [candidate analysis](iteration-2-plan.md#first-migration-candidate-the-decision-with-the-evidence-25-october-2026) recommends first — or the first migration decision itself.
- **Committed:** `545f3105` (with the record flip in the commit that follows it).

## Slice record: signed-in routes

- **Outcome and the refusal it must produce:** an `authenticated-web` instance renders its route from platform facts — a host derived as `<env>-<app>.<baseDomain>`, TLS from the profile's issuer, the shared sign-in middleware, the Service backend, and a policy admitting only the ingress controller's pods on the app's port. It refuses `public` and any other exposure (`unsupported: grants.exposure`) and refuses to render a route when the profile states no routing (`unsupported: routing`), rather than inventing a host, issuer or middleware.
- **Owners and boundaries:** the platform owns the domain, class, issuer, middleware and which pods may reach the port; the instance owns only its exposure; the workload owns its port. The route is a v2-rendered object and an ownership claim, not chart values. The adapter now states `networking.k8s.io/v1 Ingress`, so the live ownership check reads it.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged. 2. Pure packages still take no effects. 3. **Identities changed, deliberately and explained:** the profile now covers routing, so its ID and every compilation ID changed; the compiled resource bytes are unchanged apart from the `id`/`inputs.profile` lines, the two goldens were regenerated once, and the canonical profile identity was recomputed independently in Python. 4. Refusals stated and tested (`public`, unconfigured routing, invalid routing fields). 5. Failure paths tested: missing routing, unsupported exposure, invalid domain/labels, and the policy's single rule with egress still denied. 6. Scope: routes are rendered but the comparison still lists them as unassessed, and observation still does not verify them. 7. Offline only: no route has been served, no certificate issued. 8. This page, the plan and the gap list change together.
- **Evidence:** `make check-v2` (11 packages), `REQUIRE_HELM=1 make check-v2` (chart render, Service name pinned), `make check`, `make v2-report`; the bridge previews the inherited `authenticated-web` fixture unchanged in comparison; live ownership over the snapshot read 60 claims, all present, including the ten Ingresses each v1 instance already has.
- **Next gate:** the operator's choice — verify an exposed instance live (compile, apply, wait for its certificate, sign in, observe, remove), or move to the first migration of a database-free web instance.
- **Committed:** `5f2243eb` (with the record flip in the commit that follows it).

## Slice record: a v2 route live, through the shared sign-in

- **Outcome:** a throwaway instance compiled by v2 was exposed `authenticated-web` at its derived host and verified end to end: DNS, a certificate issued by cert-manager **through our Ingress**, signed-out access redirected to the shared Google sign-in (never served), and — the operator's own check — a signed-in request reaching the app, which reported the authenticated caller. It was then removed the way it was created.
- **What it proves:** the route contract is not just rendered: host derivation, the platform's TLS issuer and middleware, the traefik-only policy, and the identity header all work together against the live platform. v2's `observe` reported `running` for the same instance; observation does not check routes, so the route facts are the host's evidence.
- **Deliberate difference from v1:** v2 renders its own Ingress object rather than chart ingress values, which is why the comparison still lists `routes` as unassessed and why the route is an ownership claim.
- **Not exercised:** a production-issued certificate (the test used the staging issuer, so browsers warn), a sibling-pod ingress test, and `public` exposure — still deferred.
- **Evidence:** [current state](current-state.md#first-v2-route-live-signed-in-exposure-25-october-2026), including the certificate issuer, the 302, the signed-in confirmation and what pruning removed.
- **Next gate:** the first migration — implement calls between apps (the only relationship `swhurl-try-11` needs) and then migrate it to v2 ownership.

## Slice record: calls between apps, resolved and rendered

- **Outcome and the refusal it must produce:** a pair connects only when the caller declares the call and the callee consents by app name or team; connected pairs render both halves — the callee's address and a token only it accepts on the caller, `SWHURL_ALLOWED_CALLERS` and the platform's keys on the callee, and each side's policy admitting only the other's pods on the port the call uses. It refuses a malformed declaration, a repeated instance, a callee without the platform's keys, and a key set that is not a JWKS. A waiting pair is not an error.
- **Owners and boundaries:** `calls` (layer 0, pure) owns consent and the three outcomes; `kernel` renders a connected pair it is given and never re-decides consent; the platform profile owns the issuer and keys. The keys are carried into each callee's namespace by a ConfigMap of its own (decision 4A), so rotation changes the compilation — the platform-wide single ConfigMap is the deferred end state.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged. 2. Pure — `calls` has no effects and no clock. 3. Identities changed deliberately: the profile covers auth, so its ID and the two compilation IDs changed; only the `id`/`inputs.profile` lines moved, and the profile identity was recomputed independently. 4. New refusals stated; nothing existing moved. 5. Failure paths tested: no consent, absent callee, worker callee, self-call, malformed names, repeated instance, missing keys, non-JWKS keys. 6. Scope: rendering covers the declared pair; the bridge and delivery do not yet consume a resolution. 7. Offline only: nothing with a call has run. 8. This page, the plan and the gap list change together.
- **Evidence:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make v2-report`; the pair test asserts the address, the token projection, the caller list, the ConfigMap and its ownership claim, the caller's egress rule on the callee's port and the callee's ingress rule per caller.
- **Next gate:** the first migration — a resolution document so `compile` can deliver an instance with calls, the bridge's `calls` gap closing, then `swhurl-try-11` staging.
- **Committed:** in the commit that follows this record.

## Slice record: calls delivered

- **Outcome and the refusal it must produce:** `swhurl-v2 resolve --snapshot FILE` writes the resolved connections as a `v2-calls` version-1 document (pairs with the callee's port, waiting declarations with reasons, refusals, and the revision), and `swhurl-v2 compile --calls FILE` renders one instance's own edges from it. It refuses a document that is not `v2-calls` version 1, a revision that is not a commit, and a malformed or repeated declaration; a pair in another environment is ignored, never cross-applied.
- **Owners and boundaries:** the resolver still decides consent; the document carries its result as evidence, naming the revision it was read at; `compile` selects edges and records their identity in `Inputs.calls` without re-deciding anything. The compilation ID hashes rendered output, so a changed resolution changes it exactly when the output changes.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged. 2. Pure packages untouched by the new effects; the command holds the reads. 3. Identities: the goldens gained `"calls": ""` and nothing else; no compilation ID moved, because the ID hashes output rather than inputs. 4. Refusals stated and tested. 5. Failure paths tested: a non-`v2-calls` document, a bad revision, an instance with no side in the pair, and the two sides of one pair having different connection identities. 6. Scope: the document states only connected pairs, waits and refusals; the bridge's own calls section is still missing. 7. Offline only: nothing with a call has run. 8. This page, the plan and the gap list change together.
- **Evidence:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make v2-report`; over this repository's snapshot the resolver reports the 9 pairs v1's declarations imply, and the compiled caller renders the callee's address while the callee renders its caller list.
- **Next gate:** the first migration — completed as a handover of `swhurl-try-11` staging; the callee-side consent consequence and the choice to complete the pair or roll back are recorded in [current state](current-state.md#first-migration-swhurl-try-11-staging-is-owned-by-v2-25-october-2026).
- **Committed:** in the commit that follows this record.

## Slice record: SQLite storage, the volume

- **Outcome and the refusal it must produce:** a SQLite instance renders a retained volume at `/data`, `DATABASE_PATH`, a single writer, and a namespace Flux will not prune, so removing an instance's files cannot delete its data. It refuses a volume with no database, a database with no size or class, more than one replica, and any database other than `sqlite`.
- **Owners and boundaries:** the release owns the requirement, the instance the size, the platform the class; the renderer owns the shape and the guards. Nothing about data lives in a command.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged. 2. Pure packages still take no effects. 3. **Identities changed deliberately:** release, instance and profile canonical forms gained the storage facts, so their IDs changed; fixtures' identities were recomputed independently in Python, the goldens regenerated once, and the independent escaping vector updated. 4. New refusals, appended after the existing checks, so no existing precedence moved. 5. Failure paths tested: no database with a volume, no size, no class, two writers, an unsupported database. 6. Scope: backup, restore and growth are named as deferred. 7. **Live proof**: the volume bound, the storage-hungry image ran, a deleted pod was replaced on the same volume, and removal left the data behind. 8. This page, the plan and the gap list change together.
- **Evidence:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make v2-report`; [current state](current-state.md#sqlite-storage-proved-live-25-october-2026) for the live record.
- **Next gate:** the egress proxy (which unblocks backup and restore), or migrating `try-10` (storage now renders) to complete the migrated app's calls.

## Slice record: egress, the allocation and the instance side

- **Outcome and the refusal it must produce:** an instance can be given a stable proxy port and the client configuration that sends its traffic there, with its declared grants recorded for the proxy's log and a policy that admits only the proxy's pods and DNS. It refuses a privileged or out-of-range port, an allocation when the platform states no proxy, and any allocation document this command did not write.
- **Owners and boundaries:** the platform owns the proxy's address, its pods and DNS; the instance owns nothing about egress but what it declares; the allocation is a platform-owned document generated from the whole snapshot, seeded from v1's existing ports so a migration keeps its port and no new allocation collides.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged. 2. Pure packages still take no effects. 3. **Identities changed deliberately:** the profile gained `egress` and `dns`; the goldens moved in their profile/id lines and the profile identity was recomputed independently. 4. New refusals stated; the DNS rule is an addition, not a change to an existing one. 5. Failure paths tested: port range, missing proxy, other document kinds, missing allocation, port stability, a migrated instance keeping its port. 6. Scope: the proxy is not yet v2's, and the report of this slice says so; `--egress` must not be used yet. 7. Offline only. 8. This page, the plan and the gap list change together.
- **Evidence:** allocation over this repository's snapshot keeps every v1 port; `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make v2-report`. The [calls defect](iteration-2-plan.md#egress-part-one-the-allocation-and-the-instance-side-implemented-offline-25-october-2026) — no DNS — is fixed here.
- **Next gate:** egress part two — v2 renders the proxy and its configuration from the allocation — then the backup CronJob and restore.

## Slice record: the proxy's configuration (egress part two)

- **Outcome and the refusal it must produce:** `swhurl-v2 proxy --allocation FILE --profile FILE [--out DIR]` renders the platform's egress proxy from one allocation — a `values.yaml` ConfigMap holding Squid's configuration with one port per instance and the hosts each instance's grants allow, and a NetworkPolicy admitting each port from that instance's namespace only — as a `v2-proxy` document or as files under `DIR`. Grants are **enforced**: each instance's requests are allowed to its own granted host:port pairs and everything else is refused. The policy is v1's shape — `policyTypes: [Ingress]`, one rule per allocation — because the egress default deny belongs to an instance's own policy, which part one renders. It refuses an allocation when the profile states no proxy, two instances on one port (the port names the caller), a grant that is not a host name with a port from 1 to 65535, and any allocation document this command did not write or that names no commit.
- **Owners and boundaries:** the platform profile owns the proxy's namespace, address, pods and DNS (no field was added); the allocation owns which port names which instance and what each declares; `kernel.RenderProxy` owns Squid's lines, the object names, the Service ports and the policy's shape; `cmd/swhurl-v2` owns the document, the reads and the write, reusing the files adapter's `apps/` guard. The proxy's **deployment** — Namespace, HelmRelease (chart, image digest, replicas, probes), the Flux unit — stays the platform's hand-written files and is not rendered.
- **What crosses each seam:** `[]kernel.EgressAllocation` and the profile into the renderer; a `ResourceSet` (two files, two `platform` claims, no registrations) out; `[]kernel.File` into the files adapter, the same write contract `compile` uses.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged: nothing under `apps/`, `clusters/` or `platform/` was touched and v1's generator still writes the live file. 2. Pure: `kernel` still takes no effects; the reads and the write are in the command. 3. **No identity changed and nothing was regenerated** — no profile field and no record gained a fact, so the profile ID, both golden compilations and every instance identity are untouched; the only API-baseline change is the two new declarations. 4. New refusals stated above, appended after the existing checks; the one shared helper (`checkedEgress`) keeps the allocation's decode-then-revision precedence exactly as it was. 5. Failure paths tested: every refusal and its precedence, a grant written five wrong ways, a wildcard's coverage, an instance with no grants (refused, but still ported), duplicate grants written differently, and the CLI's write and refusals. **Mutation-checked:** a renamed ConfigMap, another Service in the profile's address, labels the chart does not set, a health port the HelmRelease does not probe, and `http_access allow all` restored each fail a test. 6. Scope: the report states what is rendered and that the proxy's deployment and the cutover are not; the egress policy's rules are derived only from the allocation and the profile. 7. Offline only: the chart and `kubectl` runs are local, no cluster or credential was read, and `make v2-snapshot` reads Git's objects. 8. This page, the plan's slice section and gap list, and the registry change together.
- **Evidence:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2` (the chart tie test rendered the pinned chart), `make check`, `make v2-report`; `kernel` coverage 93.0% (from 91.5%), `kernel/proxy.go` 100%, CLI 85.2% (from 84.9%). Over this repository's snapshot at `900b11bf` the render agrees with the file the platform runs: the same 16 ports, the same 16 `note instance` identities, the same 16 Service ports and the same 16 (namespace, port) policy pairs, and every line of v1's Squid configuration except its three comments and `http_access allow all`; the `logformat` is byte-identical. The only differences are the enforcement line and pods selected by the profile's labels rather than the hand-written `app.kubernetes.io/instance`.
- **Deferred rows touched:** the egress proxy row, the proxy cutover (split out of it with the grant precondition), an instance that must not egress, and SQLite backup/restore.
- **Next gate:** egress part three — the backup CronJob, then restore.

## Slice record: the backup job (egress part three)

- **Outcome and the refusal it must produce:** a database instance renders its protection — `Secret/<app>-backup` (the upload key as two Flux references), `CronJob/<app>-backup` (image by digest, SQLite's own online backup, age encryption with the public key only, upload through the instance's proxy port, on the app's own claim as the app's user, at the namespace's own minute), and `NetworkPolicy/<app>-backup` (no ingress; DNS and the proxy port only) — plus `platform-backup` in its unit's `dependsOn` and `postBuild.substituteFrom`. It refuses a database instance whose profile states no backup facts, one compiled without an allocation (its job could not upload), a document holding a Flux reference that is not the backup's own, and a profile whose backup block is half-stated.
- **Owners and boundaries:** the platform owns the image, bucket, recipient, writer Secret and suspension (the profile); the instance owns only its size; `kernel` renders the objects and the substitution rule; Flux performs the substitution, so v2 never holds a credential value. The job's schedule and pod shape are the platform's contract with the app, not app configuration.
- **What crosses each seam:** the profile's `backup` block and the instance's facts (namespace, app, uid, database, allocation) into the renderer; three files, three claims, a unit dependency and a substitution reference out; no key, no value.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged: nothing under `apps/`, `clusters/` or `platform/` was touched; v1's job is read in a test, not changed. 2. Pure: `kernel` still takes no effects (the schedule uses `crypto/sha256`, no clock); the `kubectl` read lives in a CLI test. 3. **Identity changed deliberately, once:** the profile gained `backup`, so its canonical form and ID moved and `profile-identity.json` was recomputed independently in Python; both goldens moved in their `inputs.profile` line only, because the compilation ID hashes output and a stateless instance renders the same files. 4. New refusals stated above, appended after the existing checks, so no existing precedence moved. 5. Failure paths tested: each refusal and its field, nine malformed profile facts, four documents with a foreign reference, the quota with and without the backup pod (and a stopped database), and the tie itself — which caught two real mistakes (the Secret's data keys are the AWS CLI's names, not the reference names; the scratch volume is `1Gi`). 6. Scope: the render states what it does; restore, the live run and freshness are named as absent. 7. Offline only: no cluster, credential, S3 object or live object was touched, and nothing was applied. 8. This page, the plan's brief and record, the gap list and the registry change together.
- **Evidence:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make v2-report`; `kernel` coverage 93.5%, the backup renderer and `fits` 100%, CLI 85.9%. **The tie:** the rendered Secret is identical to v1's, and the CronJob is v1's field for field except that v2 pins the image by digest alone — compared against `apps/demo-orders/staging/backup.yaml` parsed offline by `kubectl`. Four namespaces keep their v1 schedule slots.
- **Deferred rows touched:** SQLite backup and restore, quota headroom (the backup pod is now counted), and a new row for the freshness of a v2-owned instance's backups.
- **Next gate:** the next slice, chosen by the operator: a read-only backup observation (recommended), then restore.

## Slice record: the egress cutover

- **Outcome:** v2's rendered proxy replaces v1's generated file and **enforces grants**: each instance reaches the hosts and ports its own grants name (`403` and `granted=-` otherwise), and one port per instance is pinned in `platform/egress/allocation.json`. v1 stops generating and comparing the configuration.
- **The precondition, from the live logs:** the retained week held only the six SQLite backup jobs reaching the backup bucket, every line `granted=-`. The cutover therefore grants that host to exactly those six instances first, in its own commit, and logs them `granted=yes` under the still-permissive configuration.
- **Owners and boundaries:** the records own each instance's port and grants; the allocation pins the ports in Git; `kernel.RenderProxy` renders the objects; CI regenerates them and fails on a diff (the allocation compared apart from the commit it records); `make check-v2` compares the committed objects with the render. The proxy's deployment (Namespace, HelmRelease, chart) is untouched.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1's live operation changed **deliberately and with the operator's instruction**: the proxy now refuses ungranted hosts, and v1's generator was retired rather than left to drift. 2. Pure packages take no new effects. 3. No identity changed: the allocation is not identity-bearing, and no profile or record gained a fact. 4. No refusal code moved; `make check-apps` keeps `egress-grants` and loses only the file-staleness rule. 5. Failure paths: the drift check fails on a changed grant or port, and the live half includes an ungranted host refused with 403. 6. Scope: the grants are the only hosts any instance may reach; anything else is refused and logged. 7. **Live:** the rollout, the served configuration and a real backup through the proxy are verified and dated in [current state](current-state.md). 8. This page, the plan's cutover section, `services.md`, `apps.md`, `commands.md`, `AGENTS.md` and the registry change together.
- **Evidence (offline):** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check` (1018 tests), `make check-lint`, `kubectl kustomize platform/egress`, the new values rendered through the pinned chart with the live HelmRelease, and CI's new regenerate-and-diff step.
- **Rollback:** revert the cutover commit, `make flux-reconcile`; the object names are unchanged, so Flux updates the same ConfigMap and NetworkPolicy.
- **Next gate:** the operator's choice: migrate a database instance now that egress is live and a backup renders, or a read-only backup observation first.

## Slice record: the stateful handover primitives

- **Outcome:** `swhurl-v2 egress` keeps an allocation for an instance the snapshot no longer has (port and grants), and a database instance may adopt an existing claim (`storage.claimName`) instead of creating one, so a migrated instance keeps its data and its proxy port.
- **Owners:** the allocation stays the authority for ports, now explicitly outliving the records; the instance record states which volume it adopts; the chart mounts it, and the backup job mounts the same claim.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged. 2. Pure renderer still takes no effects. 3. **Identities moved once, deliberately:** the instance canonical gained `claimName`; `identities.json` recomputed independently, goldens moved in `inputs.instance` only. 4. New refusal for a volume both created and adopted, and for a database with neither; no existing code moved. 5. Failure paths tested: carried-over allocation with grants, adopted claim's values and backup mount, both storage refusals, a claim that is not a label. 6. Scope: adopted claims create nothing, so the platform's class is required only where something is created. 7. Offline only. 8. This page, the plan and the gap list change together.
- **Evidence:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make v2-report`.
- **Next gate:** the migration itself.

## Slice record: the first stateful migration (`swhurl-try-10` staging)

- **Outcome:** `swhurl-try-10` staging moved from v1 to v2 in one handover: v1's records, rendered manifests, events identity, image automation and unit removed, and v2's compiled instance took the same namespace and host — a database instance with an **adopted** volume, an egress grant, a nightly backup and a signed-in route, all rendered by v2.
- **Why the primitives came first:** with its records gone the instance leaves the snapshot, and this is the first migration where that mattered — so `swhurl-v2 egress` was taught to keep a previous allocation for an absent instance (its port and grants), and a database instance gained the adopted claim that keeps its data across the handover.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1's live behaviour changed **deliberately, at the operator's instruction**: `swhurl-try-12` and `swhurl-try-13` lost their v1-rendered relationship with this instance (a call and a subscription), and the instance stopped publishing its `visited` event. 2. Pure packages take no new effects. 3. No identity changed by this commit. 4. No refusal moved. 5. Failure paths: CI's regenerate-and-diff keeps the allocation and the proxy honest after the records are gone; the adopted claim's refusal cases are tested. 6. Scope: the instance's compiled output is now frozen — a v2-owned instance cannot be recompiled from a snapshot, which is a new deferred row. 7. **Live:** the unit, the volume (same PV), the route, `observe` and a backup run are verified and dated in [current state](current-state.md). 8. This page, the plan's records, the gap list and the registry change together.
- **Evidence:** live and dated in [current state](current-state.md#the-first-stateful-migration-swhurl-try-10-staging-keeps-its-volume-26-october-2026): the unit Ready at the pushed revision, the **same PV** bound through the adopted claim, the same digest, the allocation's port 30105 surviving the records' deletion, v2's route with a production certificate, a backup run from the adopted volume through the enforced proxy, and `observe` `verdict running`. `observe` gained `--calls` and `--egress` in the same change: without them it could not compile a database instance at all.
- **Not exercised:** a call into the instance (its v2 caller is itself v2-owned, so no snapshot sees the pair); a restore; and its prod instance, which stays with v1.
- **Next gate:** the operator's choice: keep a v2-owned instance's facts so it can be recompiled (v2-owned records), or the read-only backup observation.

## Slice record: a migrated instance keeps its records

- **Outcome:** a v2-owned instance's records live in `v2-records/<app>/<env>/`, the snapshot export reads them (with v2's compiled output as its declared objects and its `.json` unit as its own), and a port is allocated only to an instance that has something to reach outside or a database whose backup must upload.
- **Owners:** v1 owns its instances' records in `apps/`; v2 owns a migrated instance's, in its own tree, so v1 neither renders nor checks them; the snapshot is the one place both are read.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged (no file under `apps/` changed for v1-owned instances). 2. Pure packages take no new effects. 3. No identity changed. 4. One allocator rule changed, stated above: no port without a need. 5. Failure paths tested: a v2-owned instance read from v2's tree, the app record still found in v1's, a missing record named, and no port for an instance that needs none. 6. Scope: recompiling a v2-owned instance still needs hand-written v2 records — the gap is narrowed, not closed. 7. Offline only. 8. This page, the plan and the gap list change together.
- **Evidence:** `make check` (1018 tests), `make check-v2` (12 packages), `make check-lint`, the exporter's tests.
- **What it made possible:** the snapshot now sees both v2-owned instances, so `resolve` reports the pair **`swhurl-try-11` → `swhurl-try-10`** among nine connected pairs, and `swhurl-try-10` is live with its **callee side** — `SWHURL_ALLOWED_CALLERS`, the platform's keys as its auth ConfigMap, and ingress from each caller's namespace and label. **The pair renders on both sides; it does not connect.** A probe carrying a caller's exact namespace and label is refused by the callee (`curl (7)`, no request in the callee's log), and the same probe for the v1 pair (`demo-shop` → `demo-orders`) is refused too — so no call has ever connected live, in v1 or v2, and the cause is the cluster's policy admission, not this render ([the finding](current-state.md#the-first-call-between-two-v2-owned-instances-26-october-2026)). **Diagnosed:** `ipBlock` peers are honoured and `namespaceSelector`/`podSelector` peers are ignored by this cluster's controller, so a callee admits a caller only by pod CIDR on its port, with the token as the identity — a decision for the operator.
- **Next gate:** the live diagnosis of the policy controller and the call's policy shape, or the bridge slice that translates a v2-owned instance's records into `compile`'s inputs.

## Slice record: calls on the pod network

- **Outcome:** a call's rules state `PlatformProfile.network.podCIDR` — the caller reaches the block on the callee's port, the callee admits it there once for all callers — with the token, its audience and `SWHURL_ALLOWED_CALLERS` as the identity, because this cluster honours `ipBlock` peers and ignores selector peers.
- **The cost:** the callee's port admits every pod in the cluster; the token is what decides who may call. Network-level identity needs a different policy implementation.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged. 2. Pure packages take no new effects. 3. **Identity moved once, deliberately:** the profile gained `network`; `profile-identity.json` recomputed independently; goldens moved in their `profile`/`id` lines only. 4. One new refusal (`unsupported: network`), appended after the shape checks. 5. Failure paths tested: the block on both sides, one callee rule for two callers, and the profile's CIDR refusal. 6. Scope: the rules admit the whole pod block; that is the trade, stated. 7. **Live:** the probe re-run is recorded in [current state](current-state.md). 8. This page, the plan and the gap list change together.
- **Evidence:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`; both v2-owned instances recompiled (`swhurl-try-10`'s callee rule, `swhurl-try-11`'s caller rules and its namespace label corrected to `authenticated-web`). **Live, same day:** the call answers — the callee logs `caller.type: app, caller.id: swhurl-try-11` — with the network by the pod block and the identity by the audience token ([evidence](current-state.md#the-first-call-between-two-v2-owned-instances-26-october-2026)). The probe first failed with `401` because it used the `default` service account: the callee's contract requires the caller's pod to run as the service account named after the app.
- **Next gate:** the bridge slice that translates a v2-owned instance's records into `compile`'s inputs, or a policy implementation that honours selectors.

## Slice record: a v2-owned instance recompiles from Git alone

- **Outcome:** each v2-owned instance's own records (`v2-records/<app>/<env>/{instance,release}.json`) and the resolution it was compiled from (`v2-records/calls.json`) are in Git, and a CI test recompiles every one of them — with the profile, the committed allocation and the resolution — and compares every written file byte for byte with what Git holds.
- **Owners:** v2's records are the instance's facts; the resolution document is the pair's authority; the allocation remains the authority for ports. The v1-shaped records stay only because `resolve` reads them.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged. 2. Pure packages take no new effects. 3. No identity changed. 4. No refusal moved. 5. Failure paths tested: a file Git does not hold, a file that differs, and an instance with no records to check are all failures. 6. Scope: the *translation* into those records is still manual — the gap is narrowed to one step, and named. 7. Offline only. 8. This page, the plan and the gap list change together.
- **Evidence:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`.
- **Next gate:** the bridge command that translates v1's records into the v2 pair, so a migration needs no hand-written JSON; then the v1-shaped records can go.

## Slice record: the orchestration layer

- **Outcome:** a use-case layer exists (`v2/orchestration`, layer 2) with one use case — *compile an instance*, with or without writing — consumed by `compile` and `observe`; the port allocation decision and the two documents a compilation takes moved down to the pure kernel; the command keeps flags, I/O, exit codes, output and the checkout's own `apps/` refusal.
- **Why:** `cmd` was the orchestrator as well as the CLI — five separate inputs for `compile`, the snapshot type shared by three commands, `observe` retrofitted with `--calls`/`--egress`, and a pure decision living at layer 2.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged. 2. Pure packages take no new effects; the allocation decision became one, with its own tests. 3. No golden or identity changed. 4. No refusal code moved; one behaviour changed deliberately (below). 5. Failure paths tested: a malformed document is refused even when the instance needs nothing from it, a refusal writes nothing, and the use case's compilation equals the kernel's for the same values. 6. Scope: `migrate`, `resolve`, `egress`, `proxy`, `preview`, `shadow` stay in the command until a second consumer needs their assembly. 7. Offline only. 8. This page, the plan's layers and guardrails, the gap list and the registry change together.
- **The deliberate behaviour change:** a new instance's port is the lowest free one in the *document's* order (environment, then application), not the snapshot's listing order, so the same facts give the same document. Existing instances keep their recorded ports and the committed allocation is unchanged, which CI confirms.
- **Evidence:** `make check-v2` (13 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make v2-report` (new layer and rule; three command functions removed by the move).
- **Next gate:** `migrate` in `orchestration` — translate v1's records into the v2 pair, compile, write — so a migration needs no hand-written JSON.

## Slice record: the `migrate` use case

- **Outcome and the refusal it must produce:** `swhurl-v2 migrate --snapshot FILE --app A --env E --profile FILE --source-repository OWNER/NAME --source-commit HEX --out DIR [--calls FILE] [--egress FILE]` turns one predecessor instance out of a pinned snapshot into v2's own record pair, compiles that pair with the committed resolution and allocation, and writes both under `--out`: `v2-records/<app>/<env>/{release,instance}.json` and the compiled files. It refuses an instance the snapshot does not hold or names records as missing for, a supplied source the records contradict, a release that declares `secrets` (v2's bindings are empty-only, so dropping them would change what the app can do), a database release whose committed output declares no volume, a malformed input, and any destination inside the checkout's `apps/`. Exit 0 means the document was written; the report is on stdout, not a claim that anything was applied.
- **Owners and boundaries:** `legacy` (layer 0) owns legacy interpretation — one more pure function over the same four documents, with the preview's own scope unchanged; it cannot import `kernel`, because both are layer 0 and the rule allows only lower layers. `kernel` owns what a record may say: validation, canonical encoding and identity, through `EncodeRelease`/`EncodeInstance` and `CompileWith`. `orchestration.Migrate` maps fields, sequences translation → encoding → the existing compile use case → one write through the files adapter, and decides nothing the kernel decides. The command keeps the snapshot read, the flags, the report and the exit codes.
- **What crosses each seam:** the four documents as JSON and the supplied source in; a `legacy.Migration` (facts, a source per fact, gaps, the predecessor's declarations) out; `kernel.ReleaseBundle`/`InstanceSpec` and their encodings; one `[]kernel.File` list (the pair first, then the compilation) into the writer; `[]files.Outcome` back, including those completed before a failure.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. **v1 unchanged** — nothing under `apps/`, `clusters/` or `platform/` was touched; the records it reads are copies already in `v2-records/`, and `make check` passes. 2. **Pure** — `legacy` and `kernel` take no new effects; the translation is a function of the records and the supplied source, and the reads and the write are the command's and the adapter's. 3. **No identity changed and nothing regenerated** — the pair states the same facts the hand-written pairs state, so both goldens, the identity fixtures and the committed allocation are untouched; the API baseline grows by the new declarations and loses none. 4. **Refusals stated, precedence fixed and tested** — workload, configuration, source, secrets, volume, in that order; nothing existing moved. 5. **Failure paths tested** — each refusal and its order, a refusal writing nothing, a partly written destination reported, an absent instance, a missing record, a malformed resolution, and the `apps/` destination. 6. **Scope stated** — the report names a source per recorded fact, every gap (the tag, the team, the offers, events, the v1 volume facts, and provenance the pair states but does not verify), the resolution's edges beside the predecessor's declarations and the ones it does not connect, the allocation's port, and a fixed note that v1's files and the unit's registration are untouched. 7. **Offline only** — nothing was applied, and no cluster, credential or kubeconfig was read. 8. **Recorded in the same change** — this page, [the plan's slice](iteration-2-plan.md#the-migrate-use-case-implemented-26-october-2026) and its gap list, and the [registry](workstreams.md) change together.
- **The source, in one line:** the supplied commit is recorded verbatim in `release.json` → it feeds `rel-sha256` → the instance's `release` → the compilation ID, and the report echoes it with the record fields that corroborated it. Nothing in the pair claims it was *verified*, which stays the [release/build gap](iteration-2-plan.md#6-deliberately-deferred).
- **The claim a database instance adopts:** v1's release declared a data volume and named no claim, so the pinned chart generated one named after the release — rendered offline from `apps/demo-orders/staging/helmrelease.yaml` through the pinned chart and tied by a test. The migration reads `existingClaim` when the committed output names one and that convention when it does not; no live read, no flag.
- **The strongest test:** the two committed v1-shaped record copies are the pre-migration bytes, so `TestMigrateReproducesCommittedInstances` re-runs the migration over them with the committed resolution and allocation and asserts **byte-for-byte the record pair and the files Git holds** — `swhurl-try-10`'s adopted claim and backup included. Mutation-checked in a copy: dropping the claim, skipping the commit corroboration, and never reporting an unmet declaration each fail a test; changing one byte of a committed record fails the golden.
- **The two committed pairs were regenerated by the command**, so no hand-written JSON remains: their bytes are the kernel encoder's, which adds the asserted `releaseId` (and an empty `storage` object on a stateless instance). No identity moved — `release-id` and `instance-revision` over the old and the new files agree, and `TestOwnedInstancesRecompileFromGit` still reproduces every compiled file.
- **One defect fixed on the way:** a use case's refusal with `--out` exited 1 instead of 2, because the shared file-outcome mapping treated every non-filesystem error as I/O. It now maps a refusal to 2 and only `files.Error{IO}` to 1, and a `compile --out` refusal test pins it.
- **Evidence:** `make check-v2` (13 packages), `REQUIRE_HELM=1 make check-v2` (the chart render and offline Kustomize ran, plus the new claim-convention tie), `make check`, `make check-lint`, `make v2-report` and `make -s check-docs`. Coverage: `legacy` 85.8%, `orchestration` 84.0%, CLI 88.0%.
- **What it deliberately does not do:** delete v1's records, manifests or unit; copy the v1-shaped record set the handover leaves for `resolve`; edit the unit's registration in `clusters/home/kustomization.yaml`; regenerate the allocation with `--previous`; apply, reconcile or observe anything. The handover procedure in [current state](current-state.md#the-first-stateful-migration-swhurl-try-10-staging-keeps-its-volume-26-october-2026) stays the operator's.
- **Gaps touched:** the v2-owned-records row (the translation exists; the v1-shaped set remains because `resolve` reads it), and a note on the calls row that a migration reports the declarations the resolution does not connect.
- **Next gate:** the read-only backup observation — a v2-owned database instance's job and its newest archive are invisible to `verify-platform`, which reads v1's records.

## Slice record: the backup observation

- **Outcome and the refusal it must produce:** a database instance's observation also answers whether its protection works: `observe` reads the backup CronJob, the pods its runs create and the newest run's Job, and reports `current`, `stale` (`no_runs`, `no_successful_run`, `last_success_too_old`), `failing` (`last_run_failed`), `suspended`, `absent`, `unavailable` or `not_applicable`, with the schedule, the last scheduled and successful times, the newest run and the threshold. `--max-age` is the operator's threshold (26h by default). It refuses a document or timestamp it cannot decode, a threshold that is not positive, evidence supplied for an instance that renders no backup, and a backup target outside the instance's namespace or on an object another role already reads.
- **Owners and boundaries:** `observe` (layer 0, pure) owns the projection and every decision: the state's precedence, the age against the supplied threshold, and the run's verdict from the Job's own conditions — it reads no clock, only the timestamps it was given. `adapters/kube` states two more kinds (`batch/v1 CronJob`, `batch/v1 Job`) and nothing about meaning. The command owns the read sequence, the duration flag and the summary.
- **What crosses each seam:** backend/request `[]byte` documents and the supplied threshold in; the state, its reason and the run's facts out; the command turns them into the document's `backup` block.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. **v1 unchanged** — nothing under `apps/`, `clusters/` or `platform/` was touched, the backup job's rendering is untouched, and `make check` passes. 2. **Pure** — `observe` still takes no effects; the **rule changed deliberately** (`time` allowed, the clock refused by call) and the archtest's fixture proves both halves. 3. **No identity changed and nothing was regenerated** — no release, instance or profile field moved, no golden or committed record changed; the API baseline grows. 4. **Refusals stated, precedence unmoved** — the new vocabulary is listed above and tested state by state; the seven running findings and the verdict are untouched. 5. **Failure paths tested** — every state and its precedence, a failed read never becoming absence, a run that never finished, a pod that names no Job, the exact threshold boundary (a second either side), an unreadable timestamp in three places, a non-positive threshold, and the `not_applicable`/unexpected-evidence pair. 6. **Scope stated** — `notCovered` gains `backup_archive` and `backup_restore`; the state is about the job, never about the data. 7. **Offline, then one read-only live observation** — the tests use recorded API documents served by the fake; the live half ran on 26 October (`running`, backup `current`, nothing written) and is recorded in [current state](current-state.md#the-first-backup-observation-of-a-v2-owned-instance-swhurl-try-10-staging-26-october-2026). 8. **Recorded in the same change** — this page, [the plan's slice](iteration-2-plan.md#the-backup-observation-implemented-offline-26-october-2026), the gap list and the registry.
- **The finding that shaped it:** a Job created by a CronJob carries `jobTemplate.metadata.labels`, not the pod template's; v2's rendered job (like v1's) labels only the pod. So the runs cannot be selected by a label at all, and the observation reaches the newest Job through its pod's **owner reference** — `observe.NewestRun` names it, the adapter's stated table reads it by name. No naming convention is invented, and no live object changes.
- **Evidence:** `make check-v2` (13 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make -s check-docs`. Coverage: `observe` 93.1% (from 96.4%), `adapters/kube` 98.3%, CLI 87.7% (from 88.0%). **Live, read-only, 26 October 21:07Z:** `swhurl-try-10` staging `verdict running`, backup `current` (last successful run `08:21:21Z`, 26h threshold, newest run complete) — [evidence](current-state.md#the-first-backup-observation-of-a-v2-owned-instance-swhurl-try-10-staging-26-october-2026).
- **What it deliberately does not do:** read the object store (an archive may not exist while the job reports success), restore, judge a *v1-owned* instance's backups differently, or change the rendered job.
- **Next gate:** the object-store read, which needs a credential decision; then re-observe the first scheduled run (27 October, 03:58 UTC).

## Slice record: the object-store read

- **Outcome and the refusal it must produce:** `observe --archive` also answers whether a v2-owned database instance's newest archive exists and how old it is. It lists the instance's own prefix in the bucket the profile names, with the operator's AWS credentials from the environment, and reports `current`, `stale` (`no_archive`, `last_archive_too_old`), `unavailable` (a failed listing, with its reason) or `not_assessed` (no read was made), beside the job's answer and never folded into it. It refuses a prefix that is not the instance's own directory, an object it cannot date, a non-positive threshold, an unreadable observation time, and a listing requested without credentials.
- **Owners and boundaries:** `observe` (layer 0) owns the decision: the suffix that makes a key an archive, newest-by-`LastModified`, the age against the supplied threshold, and the confinement rule. `adapters/s3` (layer 1) owns S3's formats — the URI, SigV4, `ListObjectsV2` and its pages, and the status-to-reason mapping — and returns the evidence vocabulary the pure layer defines. The command owns the credential, the region flag and the read.
- **What crosses each seam:** a query and the operator's credentials in; an `ArchiveList` (objects or a fixed failure) out; the report's `backup.archive` block.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. **v1 unchanged** — nothing under `apps/`, `clusters/` or `platform/` was touched; v1's own check still runs as it did. 2. **Pure** — `observe` takes no new effects; the clock stays the caller's, and `time` was already allowed with its clock refused. 3. **No identity changed and nothing regenerated** — no profile, release or instance field moved, so the profile ID and every golden are untouched; the API baseline grows by the new declarations. 4. **Refusals stated and tested** — the confinement check, an undated object, an unreadable time, a bare bucket name, a request without a bucket; nothing existing moved. 5. **Failure paths tested** — every status, malformed and oversized answers, a timeout, region discovery failing, pagination, objects from another prefix, metadata `.json` files that are not archives, both sides of the threshold boundary, a failed listing versus an empty one, and the two answers disagreeing on purpose. 6. **Scope stated** — the read is confined to the instance's prefix; `notCovered` keeps `backup_restore` and drops `backup_archive`; the document says the archive is a fact of the bucket, not of the job's claim. 7. **Offline, then one read-only live read** — recorded below; nothing was written to the cluster or the bucket. 8. **Recorded in the same change** — this page, [the plan's slice](iteration-2-plan.md#the-object-store-read-implemented-26-october-2026), the gap list, the registry and [current state](current-state.md#the-first-backup-observation-of-a-v2-owned-instance-swhurl-try-10-staging-26-october-2026).
- **The signer, proved independently:** the AWS published vector for a signed GET Object (canonical request, string to sign, signing key, signature `f0e8bdb8…`) is a test, so a wrong canonicalisation fails without a bucket; a session token is signed when present; the canonical query is sorted and encoded once, so a prefix with a space travels unambiguously.
- **Evidence:** `make check-v2` (15 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make -s check-docs`. Coverage: `observe` 92.8%, `adapters/s3` 92.2%, CLI 87.3%. **Live, read-only, 21:12Z:** `swhurl-try-10` staging `archive current` — newest `sqlite-20261026T082103Z.db.age`, `LastModified 08:21:10Z`, 13h old against 26h, written eleven seconds before the run completed; the job and the bucket agree.
- **What it deliberately does not do:** restore, judge v1-owned instances' archives (its check still builds its list from `apps/`), read the bucket's root, or hold a credential of its own.
- **Next gate:** the restore of a v2-owned database from its newest archive — it stops the app and moves data, so it needs its own approval.

## Slice record: a restore plan

- **Outcome and the refusal it must produce:** `swhurl-v2 restore --instance … --release … --profile … --revision … --kubeconfig … [--from KEY] [--max-age D]` writes a `v2-restore` version-1 plan: the archive to restore (newest, or `--from`), the metadata published beside it, the instance's claim/mount/path/uid, the observation, the v1 command and the verification. It never restores. It refuses a stateless instance, and the plan is `ready` only when the archive is within the threshold, its metadata names this instance, and it records a checksum and a table count; otherwise `stale`, `incomplete`, `no_archive` or `unavailable` with a fixed reason.
- **Owners and boundaries:** `observe` decides the plan (`SelectArchive`, `ArchiveMetadataOf`, `RestoreOf`); `adapters/s3` reads one object (`Get`, with `not_found` as a code so an incomplete upload is not an unreachable bucket); `kernel` states the mount and database path once (`DataMount`, `DatabasePath`); the command composes the document.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. **v1 unchanged** — nothing under `apps/`, `clusters/` or `platform/` was touched; v1's restore is untouched and is the executor. 2. **Pure** — `observe` takes no new effects; the clock and the reads stay the command's. 3. **No identity changed** — no release, instance or profile field moved; the API baseline grows. 4. **Refusals stated** — a stateless instance, a key outside the prefix, a checksum of the wrong shape, a plan that cannot be confined. 5. **Failure paths tested** — every plan state, a missing/unreadable/unreachable metadata, another instance's metadata, a chosen archive the bucket lacks, a stale threshold, and both sides of the boundary. 6. **Scope stated** — the document says it restores nothing, and `notCovered` names the restore and the choice of point in time. 7. **Offline, then one read-only live plan** — `ready` for `swhurl-try-10` staging (below); nothing was stopped, written or restored. 8. **Recorded in the same change** — this page, the plan's slice, the gap list, the registry and current state.
- **The live read earned its keep:** the first live plan said `incomplete/metadata_names_another_instance`, because the backup job records `source` as `<namespace>/<application>` and the check compared it with the namespace alone. Fixed, the same archive is `ready`; the metadata is read rather than assumed.
- **Evidence:** `make check-v2` (15 packages), `REQUIRE_HELM=1 make check-v2`, `make check` (1019 tests), `make check-lint`, `make -s check-docs`. Coverage: `observe` 92.8%, `adapters/s3` 88.4%, CLI 87.0%. **Live, read-only, 22:33Z:** `ready` from `sqlite-20261026T082103Z.db.age` (14h old, sha256 `85c53132…`, 2 tables, `source` this instance).
- **What it deliberately does not do:** restore, decrypt, stop the app, or judge which point in time the operator wants.
- **Next gate:** option 2 — v2 performing the restore, which needs the first write path and a decision about age decryption.

## Slice record: the restore's write primitives (option 2a, part one)

- **Outcome:** v2 has its **first write path** to a cluster, and it is deliberately small: `adapters/kube.Writer` — a JSON merge patch, a create, a delete, and a pod's log tail — with a kind table of its own. `Secret` is writable there and still unreadable through `Reader`: creating the restore's age-key Secret does not make Secrets readable, and a test proves the reader still refuses one.
- **Owners and boundaries:** the adapter owns the request shapes, the paths from the stated tables (`RefOf`, and `WritableRefOf` for a create), the token, the bounded bodies, and a fixed `failed: write: <reason>` for a refused change. It decides nothing about what to change. `Connection.Writer()` builds it from the same material as the reader.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. v1 unchanged. 2. Pure packages untouched. 3. **No identity changed**, no golden regenerated; the API baseline grows. 4. New refusals stated and tested (`failed: write: forbidden|status|not_found`, a kind the write table does not state, a name a non-create write needs); a delete of an object already gone is **done**, not an error. 5. Failure paths tested: each status, the absent-object delete, the log tail and its bound, the paths and content types of all three changes, and that a Secret is writable but not readable. 6. Scope: **this is infrastructure, not a capability** — nothing calls it yet, so no instance can be restored by v2. 7. Offline only: every test is a fake server; no cluster was touched. 8. This page, the plan and the registry change together.
- **Evidence:** `make check-v2` (15 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make -s check-docs`. Coverage: `adapters/kube` 96.1%.
- **What remains for 2a:** the **restore Job** and the **sequence** (suspend, scale, wait, secret, job, logs, scale back, resume, delete), the **presigned read** the pod uses instead of a key, the operator's age key as a Secret, and the first live restore — on `try-10`, restoring its newest archive over itself. The decision still open: **where the restore's steps live** — a `restore-job-sqlite` command added to the backup image in this repository (v1 tooling, an image rebuild), or a script rendered inside the Job (no image change, a contract to test).

## Slice record: one source for the keys (delivery, option 1)

- **Outcome:** the platform's issuer and keys are rendered **once**, by `swhurl-v2 auth --snapshot … --profile …
  --out platform`: `platform/auth/` holds one identical `swhurl-auth` ConfigMap for every managed namespace plus
  the folder's kustomization, the `platform-auth` unit applies it, and CI regenerates and diffs it beside the
  egress proxy's. A callee no longer renders `<app>-auth`: it mounts the fixed name and claims none of it, so a
  rotation rewrites one folder instead of every instance.
- **Owners and boundaries:** `kernel.RenderAuth` owns the objects and their `platform` claims; the command owns
  the snapshot's namespaces, the profile and the write; the unit (`clusters/home/platform.yaml`) is the platform's
  hand-written file, as the proxy's is; the instance owns only its allow-list (`<app>-callers`), because that
  content is its own. A ConfigMap cannot cross namespaces, so the fan-out is one object per namespace from one
  source — that is what "centralised" can mean here.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — nothing under `apps/` changed; v1's renderer is untouched.
  2. **Pure** — the renderer is a function of namespaces and the profile; the command holds the read and the write.
  3. **No identity changed**; the instance's compiled output moved where it must: try-10's `auth.json` is gone,
     its HelmRelease references `swhurl-auth`, its kustomization no longer lists the old file; try-11 (caller only)
     and the goldens are untouched.
  4. **Refusals stated** — a profile with no signing keys, a snapshot with no instance, and a namespace that is not
     a DNS label, each before anything is written.
  5. **Failure paths tested** — the render's objects, names, data, claims (`platform`, never the instance), file
     names and kustomization; the command's document and its `--out`; each refusal. The old per-instance object is
     neither rendered nor claimed, which the instance's own test asserts.
  6. **Scope** — the unit cannot be a dependency of an app unit (an app's unit creates its namespace, so the
     dependency would deadlock): a **new** app's pods wait for the platform unit's next reconcile before they can
     mount the keys. Stated in the unit's own comment and the plan.
  7. **Live, 28 October 2026:** the unit applied **17 `swhurl-auth` ConfigMaps** (one per managed namespace),
     try-10's Deployment consumes and mounts `swhurl-auth`, rolled once (a new pod at `08:15:18Z`, no restarts),
     its old `swhurl-try-10-auth` was pruned, and `observe` read `running` at the pushed commit. The "no restart"
     win still needs a template release that reads the files — [evidence](current-state.md#one-rendered-source-for-the-keys-live--and-the-first-nightly-on-the-go-image-28-october-2026).
  8. **Recorded in the same change** — this page, the plan's calls section and gap row, `apps.md`,
     `operations.md#signing-keys`, `architecture.md`'s unit table and diagram (21 units), and CI.
- **Evidence:** `make check-v2`, `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make -s
  check-docs`, `make check-repo` (the unit table), `kubectl kustomize platform/auth` (17 ConfigMaps), and the
  recompilation of both owned instances from Git.
- **Committed:** `9dd0e035`, on `main`, and verified live as above.
- **Next gate:** the template releases that read the files (in the two template repositories, then a
  `STACK_REVISIONS` re-pin), which retire the env forms and the `auth-revision` annotation.

## Slice record: the no-restart proof, live (29 October 2026)

- **Outcome:** on `swhurl-try-10` staging, pod `swhurl-try-10-97f7d4db5-st2nv` (UID `e47835d1…`, created
  `07:24:19Z`), which never restarted, answered four changes in turn. Each probe ran from a `curl` debug container
  in `swhurl-try-11`'s pod, so try-11's egress policy applied, with a token passed on stdin:

  | Change (pushed, then `make flux-reconcile`) | Before | After, same pod |
  | --- | --- | --- |
  | `swhurl-try-13` allowed and calling (record pair, then the resolution re-read) | `07:25:35` 401 not allowed | `07:29:35` 200, `caller.id=swhurl-try-13` |
  | the same, removed | 200 | `07:32:00` 401 not allowed |
  | a test key added to the platform's keys | `07:32:31` 401 unknown key | `07:34:43` 200, `caller.id=swhurl-try-11` |
  | the test key removed | 200 | `07:36:58` 401 unknown key |

  Each change reached the running process 30 to 70 seconds after Flux applied it; the HelmRelease did not change
  for any of them, and no pod in the cluster was created during the key window.
- **How the tokens were made:** try-13's by `kubectl create token` (ten minutes, audience `swhurl-try-10`) with the
  operator's credentials; the forged one by a 2048-bit RSA key generated for the proof, whose public half was in
  the platform's keys for about two minutes (`07:34`–`07:36`, trusted only by the one v2-rendered callee, since v1
  callees read v1's own keys) and whose private half was deleted after the removal was proved. The cluster's real
  signing key was not touched.
- **The eight invariants:** v1 unchanged (try-13's `instance.yaml` went out and back; `make app-calls` reported
  the v2-owned callee as absent, see below); nothing pure changed; the profile identity moved to
  `prof-sha256:69649e8c…` and back, and the revert left `platform/auth` and `v2/testdata` byte-identical; no
  refusal moved; the failure paths are the 401s above; scope is one callee; live as recorded; and this page, the
  registry, the plan and `current-state.md` record it.
- **Found on the way:** v1 renders no caller side for a v2-owned callee (`make app-calls` says it "waits for
  swhurl-try-10/staging to exist"), so a v1 caller cannot reach one even when v2's resolution connects the pair —
  `swhurl-try-12` staging has such a pair and no address. [The gap list](iteration-2-plan.md#6-deliberately-deferred)
  now says so.
- **Next gate:** the operator chose the domains v2 must render before more apps migrate, not the v1-caller gap
  (which stays in the gap list): a decision brief ranking them comes first.

## Slice record: files only — the env forms and the annotation retire

- **Outcome:** a v2-rendered callee gets its verification facts only as mounted files: the
  `SWHURL_ALLOWED_CALLERS` env value, the `envFrom` of `swhurl-auth` and the `platform.swhurl.com/auth-revision`
  pod annotation are gone, and `values()` no longer takes the profile's auth facts. A rotated key, a new issuer or a
  new caller therefore leaves the HelmRelease byte-identical and changes only the object the pod mounts.
- **Owners and boundaries:** unchanged — `RenderAuth` owns `swhurl-auth`, the instance owns `<app>-callers`, the
  image owns reading the files; the pod template no longer depends on either object's content.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — v1 still renders the env forms for its callees; only `interface.py`'s descriptions moved
     (the env forms are now "v1-rendered"), with `apps.md`'s table in step.
  2. **Pure** — rendering lost an input; no effect was added.
  3. **No identity changed** — no record field; the goldens have no callers; `swhurl-try-10` staging's
     HelmRelease lost exactly the three forms, and `swhurl-try-11` (caller only) is byte-identical.
  4. **No refusal moved.**
  5. **Failure paths tested** — `TestARotationOrANewCallerChangesNoPodTemplate`: a rotated key and a new issuer
     leave the HelmRelease identical while `swhurl-auth` changes, and a new caller leaves it identical while
     `<app>-callers` changes; the file test asserts no env form and no `envFrom`. Mutation-checked: a pod
     annotation derived from the callers fails it. The chart tie for the annotation went with it.
  6. **Scope, and the cost chosen** — an image older than the file-reading templates refuses every token on a
     v2-rendered callee; the only one today runs `v0.2.19`.
  7. **Live on push:** one rollout of `swhurl-try-10` staging (its pod template loses the env forms).
  8. **Recorded in the same change** — this page, the registry, the plan's calls section and gap row, `apps.md`,
     `operations.md#signing-keys` and `interface.py`.
- **Next gate:** the live proof.

## Slice record: a new release for a v2-owned instance (`swhurl-try-10` staging, `v0.2.19`)

- **Outcome:** `swhurl-try-10` took the TypeScript template's `v0.2.19` update (Renovate's #18, the Copier
  update, and #19, the workflow pin, both squash-merged after their checks), and its Container workflow built
  `60-<sha>` from the merge ([commits and runs](current-state.md)). v2 then **selected** it: a new release record
  (source: that merge commit; artifact: the image's `linux/amd64` manifest), its ID from `release-id`, `select-release --expect` against the instance's current
  revision, and `compile` into `v2-apps/` with the committed resolution and allocation.
- **Owners and boundaries:** the app's repository owns the source and the build; the release record owns the
  artifact claim; selection owns which release the instance names; the compiler owns the HelmRelease. Writing the
  record is still the operator's hand step: v2 has no command that turns a built image into a release.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — nothing under `apps/`; `swhurl-try-10` prod (v1) keeps its own image.
  2. **Pure** — no code changed; the kernel's selection and compiler did the work.
  3. **Identity moved as selection says:** a new release `rel-sha256:2672110c…`, and the instance's `release`
     field only, so its revision moved; no profile, golden or fixture moved.
  4. **Refusals** — selection ran against the current revision, so a concurrent change would have refused
     (`stale_instance`).
  5. **Checked** — the compiled HelmRelease differs from Git in one field, the image digest; the
     `auth-revision` annotation did not move, so the push rolls the pod for the image alone;
     `TestOwnedInstancesRecompileFromGit` reproduces the instance from the new records. **One test's contract
     moved, deliberately:** `TestMigrateReproducesCommittedInstances` compared the migration's output with the
     records Git holds *now*, so it refused the first release a migrated instance selected. The pair the migration
     committed is now frozen beside the predecessor records (`testdata/legacy/<app>/<env>/migrated/`); the test
     asserts `migrate` writes that pair byte for byte and exactly the files `compile` writes for it, and, while
     an instance still names its migrated release (`swhurl-try-11`), that every file equals what Git holds.
     Mutation-checked: a changed frozen record and a changed committed file each fail it.
  6. **Scope, and a correction** — the migrated release's artifact claim was wrong: it said manifest, and its
     digest is an index. This release names the manifest (`sha256:9ae24d58…`, content type and bytes checked
     against GHCR, `linux/amd64`, UID 65532, port 8080); [the gap row](iteration-2-plan.md#6-deliberately-deferred)
     says what would make the check automatic.
  7. **Live on push:** one rollout of `swhurl-try-10` staging.
  8. **Recorded in the same change** — this page, the registry and the plan's gap row.
- **Next gate:** observe it running, then the live proof — a new caller and a key rotation, each taking effect
  with no new pod.

## Slice record: the backup host is the platform's grant

- **Outcome and the refusal it must produce:** the allocation rendered from the records equals the committed one
  again, so Validate is green. Every instance whose release keeps a database is granted the platform's backup
  host; `egress` refuses to run without `--profile` (exit 2, nothing written), and a profile whose backup block
  has no region, or a malformed one, is refused (`invalid: backup.region`).
- **Owners and boundaries:** the profile owns where backups go (bucket and region); `kernel.Backup.Host` derives
  the endpoint; `kernel.Allocate` decides who is granted it; the command reads the profile and hands the host over;
  `check-rendered.sh` owns the comparison CI and `make check-v2` share.
- **What crosses each seam:** the profile's host string into `Allocate`, beside the facts and the previous
  allocation; the same allocation document out.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — nothing under `apps/` or `tools/` changed; `make check` passes.
  2. **Pure** — `Host` and `Allocate` take supplied facts; the snapshot and the files stay in the command and the script.
  3. **Identity moved once, deliberately:** `region` joined the profile's canonical form, so its ID moved to
     `prof-sha256:ca5c1e1a…`, recomputed independently in Python; the goldens moved in `inputs.profile` only.
     `v2-apps/`, `v2-records/` and `platform/` are byte-identical.
  4. **Refusals stated, precedence unmoved** — `invalid: backup.region` after `backup.s3Uri`; `egress` without
     `--profile` is a usage error like any missing flag.
  5. **Failure paths tested** — a database instance with no declared host is granted the backup host; a host also
     declared is granted once; a stateless instance never gets it; an empty host grants nothing; the region missing
     from the document, absent and malformed are refused; the region changes the profile ID; and the rendered check
     fails, naming the lost grant, when `egress` passes no host (a mutation run by hand).
  6. **Scope** — an instance's own outside hosts are not in the v2 record (the gap list says what would add them);
     the job's region still comes from the tool's default, not the profile.
  7. **Offline only** — nothing live changes: the committed allocation and the proxy's objects are unchanged.
  8. **Recorded in the same change** — this page and its decision, the plan's slice and gap row, `services.md`,
     `commands.md`, `AGENTS.md` and CI.
- **Evidence:** `make check-v2` (15 packages, then the rendered check), `REQUIRE_HELM=1 make check-v2`,
  `make check`, `make check-lint`, `make -s check-docs`, `make -s v2-report` (the API adds `Backup.Region`,
  `Backup.Host` and `Allocate`'s parameter).
- **Next gate:** unchanged — the template-update gate below.

## Slice record: the templates read the auth files (the re-pin)

- **Outcome:** both stack templates prefer the mounted auth files and fall back to the environment, and the
  platform's catalogue pins those releases: **TypeScript `v0.2.19`** and **Kotlin `v0.2.18`** in `STACK_REVISIONS`
  (commits and CI runs in [current state](current-state.md#the-templates-read-the-auth-files-released-and-pinned-28-october-2026)). An app rendered or updated from them reads `SWHURL_AUTH_ISSUER_FILE`,
  `SWHURL_AUTH_KEYS_FILE` and `SWHURL_ALLOWED_CALLERS_FILE` on each verification, so the kubelet's in-place refresh
  of `swhurl-auth` or `<app>-callers` takes effect in the running process. A readable file wins even when empty —
  an empty allow-list admits nobody rather than falling back to the environment's.
- **Owners and boundaries:** each template owns its reader (`platform.ts`'s `material()`, `Platform.kt`'s
  `material()`); this repository owns only the pin. The pin decides what a **new** app renders; an existing app
  takes it through its own reviewed Copier update (Renovate's Copier manager reads the new tags), and a v2-owned
  instance runs the result only when a v2 release record selects its new image.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged in behaviour** — only the pin moved; no rendered manifest, instance or record changed, and
     every app type of both stacks still passes `app-new` and the app policy (`make check-templates`).
  2. **Pure** — no v2 package changed.
  3. **No identity changed** — no release, instance or profile field; no golden regenerated.
  4. **Refusals unmoved** — both templates still refuse answers that are no app type (`check-templates`).
  5. **Failure paths tested, in the templates:** the file wins over the environment, a rewritten callers file
     changes who may call in the same process, an emptied one admits nobody, and (Kotlin) a rotated key verifies
     at once while the old one is refused. Kotlin's case was built and run for all three app types on
     `eclipse-temurin:25-jdk` in podman (26, 18 and 18 tests), and mutation-checked: the env-only `Platform.kt`
     fails it (`reports is not allowed to call orders`). Each template's CI then passed on its tagged commit,
     every type built and smoke-tested.
  6. **Scope** — nothing running reads the files yet: every app's image predates these releases, so the env
     forms and the `auth-revision` annotation stay. The pins also carry the templates' Renovate updates since
     `v0.2.18`/`v0.2.17` (Micronaut 5.2.4, the OpenTelemetry agent 2.33.1, Gradle 9.8.2 among them).
  7. **Live** — nothing on the cluster changed; the two tags are published, and new apps record
     `_commit: v0.2.18` (Kotlin) or `v0.2.19` (TypeScript).
  8. **Recorded in the same change** — this page, the registry, the plan's calls section and gap row, `apps.md`,
     `operations.md#signing-keys` and `current-state.md`.
- **Evidence:** `make check-templates` (9 checks, both stacks), `make check` (1022 tests), `make check-lint`,
  `make check-v2`, `REQUIRE_HELM=1 make check-v2`, `make -s check-docs`.
- **Next gate:** `swhurl-try-10` (TypeScript, at `v0.2.18`) takes the `v0.2.19` Copier update and builds an image;
  a v2 release record selects its digest; then the live proof — rotate the keys and add a caller, and show both
  take effect with no new pod — after which the env forms and the annotation retire for v2-owned callees.

## Slice record: the auth material as files (delivery, option 2)

- **Outcome:** a callee's keys, issuer and allow-list are rendered **twice from the same strings**: the env forms
  as before, and files under `/var/run/swhurl/auth` and `/var/run/swhurl/callers`, each ConfigMap mounted
  read-only with `SWHURL_AUTH_KEYS_FILE`, `SWHURL_AUTH_ISSUER_FILE` and `SWHURL_ALLOWED_CALLERS_FILE` naming them.
  An image that prefers the file picks a rotated key or a new caller up from the kubelet's in-place refresh, so
  **no pod restarts**; the env forms and the `auth-revision` annotation keep the path correct for images that
  predate the files, so nothing breaks during the transition.
- **Owners and boundaries:** `kernel.RenderInstance` owns the mounts, the file variables and the new per-instance
  callers ConfigMap (its content is the record's `allows`, sorted — the fact A made authoritative); the chart
  owns how a ConfigMap becomes a read-only volume; the **templates** (separate repositories) own reading the file
  and caching it, and their release plus a `STACK_REVISIONS` re-pin is what makes the win real for a running app.
  The keys' object stays per instance for now: a ConfigMap cannot cross namespaces, so centralising them is a
  fan-out unit — the next slice.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — v1's renderer and manifests are untouched; only v2-owned instances gain the mounts.
  2. **Pure** — the mounts and the callers document are rendered from facts already in hand; no effects.
  3. **No identity changed and no golden regenerated** (the fixture records have no callers); **one compiled
     instance moved** — try-10's HelmRelease gains the mounts and the file variables, `callers.json` is new and
     the kustomization lists it; try-11, a caller only, is byte-identical.
  4. **No refusal moved.**
  5. **Failure paths tested** — the file variables point into the mounted directories; each ConfigMap is the
     right object, mounted read-only at the right path; the file content and the env form are the same sorted
     string, so they cannot drift; the object is claimed. Four mutations each fail a test: no mounts, no file
     variable, an unsorted file, an unclaimed object. The pinned chart is tied by a render: a configMap
     persistence item becomes a read-only volume on the pods.
  6. **Scope** — the env forms stay, so an image that predates the files keeps working and still rolls via the
     annotation; the templates are not in this repository.
  7. **Live effect on push:** try-10 gains a ConfigMap and a new pod template — **one rollout**, which is the
     adoption. The "no restart" proof needs an app image that reads the files, so it follows the template
     releases.
  8. **Recorded in the same change** — this page, the plan's calls section, `apps.md`'s variables and contract,
     `operations.md#signing-keys` and the registry.
- **Evidence:** `make check-v2`, `REQUIRE_HELM=1 make check-v2` (the chart render ran), `make check` (1022
  tests), `make check-lint`, `make -s check-docs`, `make -s v2-report`.
- **Next gate:** option 1 — the keys' object becomes one rendered platform unit fanning out to a `swhurl-auth`
  ConfigMap per managed namespace, with the instance's unit depending on it; then the template releases retire
  the env forms and the annotation.

## Slice record: the record states its declarations (A)

- **Outcome and the refusal it must produce:** a v2-owned instance's record now states what it calls and who may
  call it (`calls`, `allows`), so `resolve` no longer needs the v1-shaped YAML copies the handover left beside
  the pair — and those four files are deleted. `migrate` writes the declarations from the predecessor's records
  instead of only reporting them, and it **refuses a team grant** (`allows: teams_are_not_modelled`) because the
  record states app names and a team grant would quietly change what the instance accepts.
- **Owners and boundaries:** `kernel` owns the fields, their validation (app names, no repeats), their canonical
  order and their place in the instance's identity; `legacy` owns the translation (its `Callers` renamed
  `Allows`, matching the record and the resolver); `orchestration.Migrate` maps them and refuses a team grant;
  the snapshot exporter owns which record shape it read and states it (`format`); `resolve` reads either shape.
- **What crosses each seam:** the same three documents; a snapshot instance now carries `format`, and a v2-owned
  instance's `records` are its JSON pair plus the app record from `apps/`.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — nothing under `apps/`, `clusters/` or `platform/` changed; the v1 renderer is untouched.
  2. **Pure** — the new validation and canonical order are pure; `legacy` still takes no effects.
  3. **Identity moved once, deliberately:** the instance canonical gained the two lists, so every instance's
     revision moved — `identities.json` recomputed independently in Python, both golden compilations regenerated
     in their `inputs.instance` line only, and the two committed pairs re-encoded by `migrate`. **`v2-apps/` is
     untouched**: the revision lives in the compilation document, not in the rendered resources.
  4. **Refusals stated, precedence unmoved** — `calls`/`allows`: an app name, no repeats (kernel, after the
     existing checks); `unsupported: allows: teams_are_not_modelled` (migration, before the pair is built); the
     exporter refuses two record shapes for one instance and a v1 record left under `v2-records/`.
  5. **Failure paths tested** — a team grant refused with nothing written; both record shapes and a leftover YAML
     refused by the export; a v2 pair resolving to byte-identical pairs to the records it replaced; the record's
     declarations sorted when written and cloned when returned from selection (a record with slices is no longer
     comparable, and `TestRecordsAreValues` now says which fields may be slices and why); the migration still
     reproduces both committed pairs byte for byte from **frozen predecessor fixtures** (`v2/testdata/legacy/`),
     not from mutable copies.
  6. **Scope** — teams remain unmodelled and are refused; the resolution's 9 pairs are unchanged.
  7. **Offline only** — no cluster, credential or bucket; the change to Git is a record pair and two deleted files.
  8. **Recorded in the same change** — this page, the plan's `InstanceSpec` table, the records gap row and the
     exporter notes, `apps.md` and the registry.
- **Evidence:** `make check-v2`, `REQUIRE_HELM=1 make check-v2`, `make check` (1019 tests), `make check-lint`,
  `make -s check-docs`, `make -s v2-report`; the Python exporter suite (10 tests) and the Go command suite.
- **Next gate:** centralised, file-based auth delivery — one platform-wide auth object, mounted as a file the
  apps re-read, so a rotation and a new caller need no restart. The digest added earlier is already a fact about
  the material, so that slice changes which object is rendered and what the container references.

## Slice record: a key rotation rolls the callees

- **Outcome:** a callee's pod template now carries `platform.swhurl.com/auth-revision`, the digest of the three
  facts a callee verifies with — the platform's issuer and keys, and its own callers — so rotating the signing
  keys (or adding a caller) changes the pod template and rolls the Deployment. Without it the keys reached the
  pod only through `envFrom` of the per-instance `<app>-auth` ConfigMap: a ConfigMap update changes no pod
  template, and Reloader ignores ConfigMaps, so a rotation would have left every running callee verifying
  against the old JWKS until something else restarted it. v1 never had the bug because it inlined the keys in
  the HelmRelease env.
- **Owners and boundaries:** `kernel` owns the digest and the annotation (`authDigest`, `authRevision`), computed
  from the canonical form of `(issuer, keys, sorted callers)`. The per-instance ConfigMap copy stays for now;
  the digest is deliberately a fact about the *material*, not about the object, so the end state — one
  platform-wide auth object instead of a copy per namespace — changes `authConfigMap` (which object) and
  `container()` (the reference) and keeps the digest as what rolls the pods that must verify with it.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. **v1 unchanged** — v1's renderer and
  its manifests are untouched. 2. **Pure** — the digest hashes supplied facts; no clock, no read, no environment.
  3. **No identity changed** (the annotation is rendered output, not an instance fact), and exactly one compiled
  file moved: `v2-apps/instances/swhurl-try-10/staging/helmrelease.json` was recompiled with the annotation;
  try-11 (caller only) is byte-identical, the kernel goldens have no callers so they did not move, and the unit
  is unchanged. 4. **No refusal moved.** 5. **Failure paths tested** — the digest changes for a rotated key, a new
  issuer and a new caller, is stable for the same facts, and is absent on a caller-only instance; three
  mutations (no annotation, a digest without the callers, a digest without the keys) each fail a test.
  6. **Scope** — only callees carry it: a caller's calls are literal env, which already rolls it, and the
  pinned chart is tied by a render: `TestCompiledManifests` asserts the chart puts the annotation on the pod
  template (and not on the Deployment's own metadata, where it would roll nothing). 7. **Offline
  only** — nothing was applied; the one live consequence is a single rollout of `swhurl-try-10` when this
  reaches `main`. 8. **Recorded in the same change** — this page, the plan's calls section and its gap row,
  `apps.md` and `operations.md#signing-keys`.
- **Evidence:** `make check-v2`, `REQUIRE_HELM=1 make check-v2`, `make check` (1019 tests), `make check-lint`,
  `make -s check-docs`, `make -s v2-report`; `kernel` coverage 93.7%, `TestOwnedInstancesRecompileFromGit`
  reproduces both owned instances from Git, and `TestCompiledManifests` ties the pinned chart. **Live:
  `d64fc10e`** rolled `swhurl-try-10` on the annotation alone, and `observe` read `running` at that revision.
- **Next gate:** unchanged — `resolve` reading the committed resolution, and after it the platform-wide auth
  object this was shaped for.

## Slice record: the restore v2 performs (2a's offline half)

- **Outcome and the refusal it must produce:** with `--confirm APPLICATION/ENVIRONMENT`, `swhurl-v2 restore` performs the plan it verified: suspend the release and the instance's backup schedule, scale the app to zero, wait for its pods, create a Secret holding the age identity and a presigned read URL for that one archive, create a Job from the backup image, wait, read its log, delete the Job and the Secret, scale back, resume, and wait for the app to be available. Without `--confirm` it is the plan it already was. It refuses a confirmation for another instance, a plan that is not `ready` and an identity that is not an age key — before anything is signed or changed — and an instance that keeps no database, one that renders no backup, a running restore pod and an unreadable Deployment before the first change. A restore the Job reports as failed unwinds the app and still writes the document.
- **Owners and boundaries:** `observe` decides the plan and the archive; `adapters/s3` owns the read URL's format and signing and gives it a bound of its own; `kernel.RenderRestore` renders the two transient objects from the same records the compiler reads, sharing the backup job's pod shape; `adapters/kube.Writer` performs the four changes and reads one pod's log, with a create confined to the kinds its table states; `orchestration.Restore` owns the sequence, the bounds and the undo; the command owns the flags, the age key and the operator's credentials, the two documents' composition, and the exit codes.
- **What crosses each seam:** three records and the allocation in, a `RestorePlan`, the target, two credentials, a `Cluster` (Reader and Writer) and the clock in; a `RestoreOutcome` (steps, the Job's state and log, the app's state) out; the objects' bytes go into `Create`, never into Git.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — nothing under `apps/`, `clusters/` or `platform/` was touched; no rendered manifest moved; `make check` passes (1019 tests).
  2. **Pure** — `kernel` and `observe` take no new effects; the signer's clock and the socket are the adapter's, and the waits take `Sleep`/`Now` from the command, so the use case's tests run on a fake clock. The `orchestration-wires` and `pure` rules are unchanged and pass.
  3. **No identity changed and nothing regenerated** — no release, instance or profile field moved, so the profile ID, both golden compilations, the identity fixtures and the committed records are untouched. The API baseline grows by the new declarations and loses only what changed shape (`s3.Reader`, `observe.ArchiveMetadata`, the fake's own fields); the report says why.
  4. **Refusals stated, precedence unmoved** — the kernel's new vocabulary is `restore.name|archive|ageIdentity|readUrl|archiveSha256|plainSha256|tables` plus `unsupported: database|backup|egress`; the plan gains `metadata_has_no_plain_checksum`; `orchestration.Refusal` is the use case's own and **means nothing was changed**. A plan is judged before a credential is read, and the confirmation before the plan.
  5. **Failure paths tested** — every refusal and its field; a presigned read of another prefix and of an expiry outside SigV4's range; the AWS vector; the sequence in order; an injected failure at each step (pods that do not stop, the Secret, the Job, a Job that fails, a Job that times out, a Job that cannot be deleted, a scale-back that fails, a resume that fails, a rollout that never completes), each asserting the undo that ran, the app's state, the live replica count and that the error is not a refusal — including a pod that outlives its Job, which leaves the app stopped and its release suspended; a refusal writing nothing and signing nothing; an unreadable log leaving the restore `restored`; a Job that fails at the command level exiting 1 with the document written; an age key file with comments; a read URL shorter than the restore's window; a transient read error retried and three in a row failing the step; an app that was already at zero replicas reported `stopped`; an adopted claim with a name of its own; the undo running on a context of its own after the run's context is cancelled; and the two bodies, so the credentials are in the Secret and in neither the Job, the document nor the diagnostic.
  6. **Scope stated** — a plan document drops "the restore itself" and keeps the point-in-time question; a performed one adds the app's own writes during the stop. The two blocks are separate facts, and no test lets one imply the other.
  7. **Offline only** — every cluster is a fake API server or a recording fake, every bucket is scripted, and no kubeconfig, credential or live object was read. No readiness is claimed.
  8. **Recorded in the same change** — this page, [the plan's slice](iteration-2-plan.md#the-restore-v2-performs-option-2a-offline-half--27-october-2026), the gap list, the [registry](workstreams.md) and `operations.md`.
- **Evidence:** `make check-v2` (15 packages), `REQUIRE_HELM=1 make check-v2`, `make check` (1019 tests), `make check-lint`, `make -s check-docs`, `make -s v2-report` (36 API lines added, 3 removed — the widened `s3.Reader`, `observe.ArchiveMetadata` and the fake's own fields). Coverage: `kernel` 93.7%, `orchestration` 84.4%, `adapters/s3` 87.6%, `adapters/kube` 91.2%, CLI 86.6%.
- **What the review changed, 27 October 2026 (independent read-only review; two blockers, four should-fix findings and seven nits, all fixed, and the four behaviours they added are pinned by tests mutation-checked in a throwaway copy):** a Job delete now sends **background propagation**, so its pod goes with it, and the run then **waits until that pod is gone** — if it cannot confirm that, the undo stops *before* scaling the app up, and the report says the app is stopped rather than restarting it over a pod that may still hold the claim; the **age key file is read in `age-keygen`'s own shape** (comment lines, then the key), which the default `./age.agekey` has; a wait **retries up to three consecutive failed reads** before failing the step, so one refused request in a fifteen-minute wait is not the end of a restore; the run **records its intent before each request** (a timeout may still have been applied), so a leftover Secret or Job is deleted by the next run's unwind; the **undo runs on its own bounded context**, and an interrupt unwinds like a failure; the log is read **even when the Job's wait failed**, so a timeout still leaves evidence; a **successful restore of an app that was already at zero replicas reports `stopped`**, not `running`; `--url-ttl` has a floor of the restore's own window; and the **backup observation no longer treats a restore pod as a backup run** (`!platform.swhurl.com/restore` in the derived selector, tied to the kernel's label by a command test).
- **Live, 27 October 2026:** `swhurl-try-10` staging restored from `sqlite-20261027T085907Z.db.age` (12h8m old, 2 tables) in **30 seconds**, all thirteen steps `done`, the Job's log naming `/data/before-restore-20261027T210953Z`, and the app back at one replica with its release resumed. A post-restore observation four minutes later read `running` with all seven findings positive, `unit.revision applied` at the pushed commit, and the backup target's live selector carrying the `!platform.swhurl.com/restore` exclusion this slice added. **A second run restored an older archive on purpose** (26 Oct, `--max-age 72h`) in 29 seconds, and the file hashes prove the change: the live `app.db` equals that archive's recorded plaintext while the file it replaced is a third hash — the app's own writes — kept beside it — [evidence](current-state.md#the-first-restore-v2-performs-swhurl-try-10-staging-newest-archive-over-itself-27-october-2026), [content-changing](current-state.md#the-first-content-changing-restore-swhurl-try-10-staging-moved-back-a-day-27-october-2026).
- **The backup schedule, suspended for the window (offline, 27 October 2026):** the run now reads `<app>-backup`'s own `spec.suspend`, sets it before the app stops, and puts the prior value back once the Job's pod is gone — a backup copying the database while the restore swapped its files would have published a stale archive. A schedule the operator had already suspended stays suspended (the run resumes only what it stopped), the undo waits for the pod before putting it back, an **already-running** backup is left alone because it only reads, and an instance that renders no backup is refused before anything changes. A run that cannot confirm the pod is gone reports `backupSuspended` and says so in its summary line. Four mutations each fail a test: never suspending, always resuming, resuming while a pod may hold the claim, and dropping the refusal.
- **What it deliberately does not do:** restore a content change by design, restore another instance's archive, choose a point in time, restore MongoDB, prune `before-restore-<UTC>/`, or hold a credential of its own. **Residual risks, stated:** a pod killed between the tool's renames can leave `<db>.restore` and a `before-restore-<UTC>/` holding the live database; and a run killed with a signal that does not unwind leaves its Job, its Secret and its suspended backup schedule to the next run's preflight (which refuses until the pod is gone). The live proof above predates the schedule suspension, so the first live run that exercises it is still owed.
- **Committed:** `02010eb3`, on `main`.
- **Next gate:** the schedule suspension is offline-tested and waits for a live re-run; the workstream's next slice is `resolve` reading the committed resolution, so the v1-shaped record copies can go.

## Slice record: the backup image's Go tool, part one

- **Outcome:** all three of the image's jobs are Go — `backup-job-sqlite`, `restore-job-sqlite` and `backup-job-mongodb` — with the platform's contract unchanged: `sqlite-<UTC>.db.age` + `sqlite-<UTC>.json` published second, the same nine metadata keys, the plaintext's `plain_sha256`, and the upload's own checksum with `If-None-Match: *`. The copy is SQLite's `VACUUM INTO` (consistent while the app writes) checked with `PRAGMA integrity_check`.
- **Owners and boundaries:** `images/backup/tool` is a **separate Go module** with its own dependencies (`filippo.io/age`, `modernc.org/sqlite`), so v2's `stdlib-only` rule is untouched; the image, the profile and the rendered jobs are unchanged by this slice.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. **v1 unchanged** — nothing under `apps/`, `clusters/`, `platform/` or `tools/` was touched; the Python job still runs in the image. 2. **Pure** — no v2 package changed at all. 3. **No identity changed** — no profile field, no digest, no golden. 4. **Refusals stated and tested** — a missing setting is named; an archive smaller than the copy is refused; an existing S3 object is refused; a failed upload names the file. 5. **Failure paths tested** — settings, the copy of a missing database, an age round trip and a malformed recipient, the upload's headers and three refusals, and the whole job against a fake bucket with the archive decrypted back to the recorded checksum. 6. **Scope stated** — the image still runs Python; nothing has been deployed or released. 7. **Offline only** — every test is local; no bucket, cluster or credential. 8. **Recorded in the same change** — this page, the plan's slice, the registry and `AGENTS.md`'s exception.
- **Evidence:** `go test ./...` in `images/backup/tool` (10 tests: settings for the three jobs, names, the online copy, the round trip, the upload, the SQLite backup end to end, the restore and its refusals, and the MongoDB job and its refusals), `make check-v2` (15 packages), `REQUIRE_HELM=1 make check-v2`, `make check` (1019 tests), `make check-lint`, `make -s check-docs`.
- **The restore command** fetches the archive from a read URL (a presigned one, from v2), checks the archive's and the plaintext's SHA-256, integrity and the table count, and only then replaces the database, keeping it in `before-restore-<UTC>/`; five refusals are tested to leave the live database untouched and no folder behind.
- **The MongoDB command** pipes `mongodump` straight into age (connection string on stdin, no plaintext copy), reads the version and per-collection counts with `mongo-go-driver`, and publishes the same archive and metadata names the Python job does; `mongodump` itself is the one piece the tool cannot replace, and the reason the image base cannot be `scratch`.
- **The image is the Go one**: `debian:bookworm-slim` + `libgssapi-krb5-2` + `mongodump` + the stripped binary, entrypoint the binary, so the CronJobs' arguments are unchanged. **48.1 MB compressed**, measured by building it locally, against 134 MB today; `mongodump --version` runs in it and the usage lists the three commands. Nothing is deployed: the rendered jobs still name the old digest.
- **What is not done:** the digest move (six rendered jobs and `profile.backup.digest`), the presigned read URL on v2's side, and the live proof.
- **Next gate:** the image's digest move, then a live backup and restore through it, then 2a's restore sequence.

## Slice record: the backup image's digest move

- **Outcome:** every nightly backup job names the Go image — the five v1-rendered database instances, `platform/backup/mongodb.yaml` and v2's `swhurl-try-10` staging CronJob — as `ghcr.io/samclement/swhurl-backup:src-caf51b4f61fadd4a@sha256:49474ad9…`, the image CI built and published from `24db3d31` (48.1 MB compressed). The CronJobs' args, environment, mounts and metadata are unchanged: only the artifact moves.
- **Owners and boundaries:** v1's renderer (`tools/swhurl/apps/contract.py`) owns the reference for its instances; v2's profile owns the digest for a v2-owned one; neither reads the other. The Go image was built and published by CI, not by this change.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):** 1. **v1's live behaviour changes deliberately**: its five rendered backup jobs pin a new image, which is the point of the move; no other v1 behaviour changed. 2. **Pure packages untouched.** 3. **Identities moved once, deliberately:** the profile's `backup.digest` changed, so its canonical form and ID moved to `prof-sha256:e94c3d69…`; `profile-identity.json` was recomputed independently in Python, the two goldens were regenerated once, and `v2-apps/instances/swhurl-try-10/staging/backup-cronjob.json` was recompiled — one line, the image. 4. **No refusal moved.** 5. **Failure paths:** the tie test comparing v2's job with v1's committed job, and v1's pre-M6 render hash, each pin the image; both were updated with the reason recorded here and in the comment beside the hash. 6. **Scope:** the archives, their names, their metadata and the restore procedure are unchanged, so an archive written by either image is readable by both. 7. **Nothing is live yet** — the digests are in Git but not pushed: the rollout is the next act. 8. This page, the plan, the registry and `docs/operations.md` change together.
- **The three pins that moved, and why:** the goldens' `inputs.profile` line (the profile ID); the tie test's literal digest (v2 renders `image@digest`, v1 `tag@digest`); v1's pre-M6 render hash for `('web', True)` only — the one scenario with a database, whose `backup.yaml` carries the image. The fixture renders (`tests/fixtures/apps`) moved in `backup.yaml` and the HelmRelease's `platform.swhurl.com/render` fingerprint, which is what that annotation is for.
- **Evidence:** `make check` (1019 tests), `make check-v2` (15 packages), `REQUIRE_HELM=1 make check-v2`, `make check-lint`, `make -s check-docs`. The image: `ghcr.io/samclement/swhurl-backup:src-caf51b4f61fadd4a@sha256:49474ad990df0f69a31cadd365ce28f20dad48a8e4edebac0c84ab080450250c`.
- **The fixed image, live:** `src-05b9898196028050@sha256:bfe284ed…` is pinned and reconciled, and `make backup-sqlite` published through it from **all six** database instances (2–3 tables each, with metadata), as did `make backup-mongodb` (14 collections). The retry turned the start-up transient into successful uploads. [Evidence](current-state.md#the-backup-images-first-live-night-a-start-up-transient-and-the-retry-that-fixes-it-27-october-2026).
- **The first live restore found a second defect:** it failed at its last step, renaming the decrypted copy from the scratch emptyDir into the claim (`EXDEV`), after the live database had been moved aside; recovered by copying the kept file back and resuming. The copy is now staged beside the database, and the fix's image `src-92042b331d76ba64@sha256:b730422b…` is pinned (identity `prof-sha256:4761e522…`). [Evidence](current-state.md#the-backup-images-first-live-night-a-start-up-transient-and-the-retry-that-fixes-it-27-october-2026).
- **The live night found a defect:** the rollout worked and MongoDB's backup published through the Go image, but all six SQLite jobs failed their upload — a new pod's first requests cannot reach the proxy's ClusterIP, which the AWS CLI's default retries had hidden in the Python image. The tool now retries a transport failure (five attempts, five seconds apart) and names the cause; a new image follows, so the digest moves once more. [Evidence](current-state.md#the-backup-images-first-live-night-a-start-up-transient-and-the-retry-that-fixes-it-27-october-2026).
- **Not done:** a restore through the image, and the nightly schedule's own run on it.
- **Next gate:** a restore of one of these archives through `restore-job-sqlite` in a Job, then 2a's restore Job and sequence. **The first nightly on the Go image succeeded for every job on 28 October** ([evidence](current-state.md#one-rendered-source-for-the-keys-live--and-the-first-nightly-on-the-go-image-28-october-2026)).

## Slice record: the always-on instance surface (29 October 2026)

- **Outcome and the refusals it must produce:** every delivered instance gets telemetry variables when its release reports an SDK, the notify address and a projected `notify` token, and the app's team label on its namespace. The app record is required for delivery (`compile --app APP.json`), so staging and production cannot disagree about the team. It refuses an app record for another application, an invalid or missing team, telemetry when the profile states no collector, and any instance when the profile states no notify service.
- **Owners and boundaries:** the **release** owns whether the image reports telemetry; the **profile** owns the collector's endpoint/protocol and the notify address; the **app record** (`v2-records/<app>/app.json`) owns the team; the **kernel** renders the env, token volume and label; `orchestration` composes the documents and `migrate` writes the app record; the command reads `--app`. No adapter, credential or new platform unit is involved: the notify token is a projected service-account token, not stored material.
- **What crosses each seam:** release, app, instance and profile bytes into `kernel.CompileWith`; a `Compilation` with its app identity and the rendered files out. A preview or live read may omit the app record and renders no team label; a delivered instance always has one.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — no file under `apps/`, `clusters/` or `platform/` changed; `make check` passes (1023 tests).
  2. **Pure takes no effects** — `kernel` remains pure; the app record, profile blocks and renderer take no clock, network, filesystem, environment, randomness or subprocess. No adapter was added.
  3. **Identities moved once, deliberately** — the release gained `telemetry`, the profile gained `telemetry`/`notify`, and the app record is new. Release IDs moved (`r41` `88aa6d1f…` → `55c64a82…`, `r42` `e0ee2918…` → `84a4efe8…`, `r43` `07cb4b75…` → `87ab92d3…`; the two v2-owned releases moved to `d180461e…` and `e357def6…`), the profile ID moved `ca5c1e1a…` → `567cdc63…`, and the two app IDs are new. `prod.json`, `staging.json`, `sqlite.json`, the frozen migrated pairs, both goldens, `identities.json`, `profile-identity.json` and both compiled v2-owned instances were regenerated once; the canonical arrays and IDs were recomputed independently in Python, not copied from Go.
  4. **Refusals stated, precedence unmoved** — new `unsupported: telemetry`, `unsupported: notify`, `invalid: app`/`team`/`appId`, `wrong_application` for an app record of another app, and `invalid: app.team` in a migration without a team. They run after the existing identity, selection and release checks; no existing code or order moved.
  5. **Failure paths tested** — every app-record refusal, an app record for another app, telemetry with no collector, a missing notify service, a release without telemetry, a migration without a team, `--app` missing or repeated, and the two v2-owned instances' recompilation. The token sources keep v1's order: callees first, then `notify`.
  6. **Scope stated** — only telemetry, notifications and the team label. Events, worker/job kinds, `team:` grants, offers, secrets and custom alerts remain gaps. Preview now carries telemetry as a fact and no longer reports the team as a gap; observation and restore still need no app record and claim nothing new.
  7. **Offline checks are not live readiness** — the slice was implemented and checked offline, then `1bca7990` was pushed and reconciled. Both v2-owned instances are Healthy on it; the live env and team labels are recorded in [current state](current-state.md#the-always-on-instance-surface-is-live-29-october-2026).
  8. **Recorded in the same change** — this page, the plan's model and gap list, the workstream registry and the snapshot exporter (which now reads `v2-records/<app>/app.json`) changed together.
- **Evidence:** `make check-v2` (15 packages), `REQUIRE_HELM=1 make check-v2` (chart render and rendered platform files), `make check` (1023 tests), `make check-lint`, `make -s check-docs`, `git diff --check`, `make -s v2-report`; coverage `kernel` 93.5%, `legacy` 84.6%, `orchestration` 83.6%, CLI 86.2% (`go test -cover`). `v2-report` records the intended exported API change (+16/−9: the app record and the new profile/render facts) with no budget baseline added or weakened. **Live:** `1bca7990` applied; both v2-owned instances Healthy with the new env, token and team labels; `make verify-platform` passed. [Evidence](current-state.md#the-always-on-instance-surface-is-live-29-october-2026).
- **Gaps touched:** telemetry and notifications become Done; teams is Done for the label and remains open for `team:` grants; a v2-owned instance's records now include its app record.
- **Next gate:** approval of slice 2 (the worker kind).

## Slice record: the worker kind (29 October 2026)

- **Outcome and the refusals it must produce:** a `worker` release compiles to the platform's files without the HTTP ones — no `service` in the chart's values, so no Service; no probes; no route — while keeping every other fact: the namespace and its team label, the HelmRelease, the quota, the policy, the unit, the telemetry variables, the notify address and token, the calls it makes, and its allocation. It refuses a worker whose record states an HTTP port or a health path, an exposed worker, a worker named as a callee, and a kind it does not model (`job`).
- **Owners and boundaries:** the **release** owns the kind (`runtime.kind`), as v1 does, because the image is what serves or does not. `kernel` owns one definition of what a kind serves (`validateWorkload`), shared by record validation and by rendering, and the renderer alone decides what a kind has: `WorkloadFacts.servesHTTP()` gates the Service and the probes, and `InstanceFacts.validateKind` refuses a route or a caller for a kind without one. `legacy` translates a worker's facts and no longer refuses them; the HTTP **preview** still refuses one, because comparing an HTTP surface it does not have would report parity for nothing. The **resolver** already refused a callee that is not web (`callee_not_web`), so nothing new was needed there. No adapter, credential or platform file is involved.
- **What crosses each seam:** release, app, instance and profile bytes into `CompileWith`; the release's `runtime.kind` into `WorkloadFacts`; a `Compilation` out. A worker's files are the same six a stateless web instance gets, and no file is added for it.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — nothing under `apps/`, `clusters/`, `platform/` or `tools/` was touched; no v1 command, render or check changed; `make check` passes.
  2. **Pure takes no effects** — `kernel` and `legacy` take no clock, network, filesystem, environment, randomness or subprocess; no package was added and no import moved.
  3. **No identity changed and nothing regenerated** — no record field was added, moved or re-encoded (the kind and its two facts were already in the release's canonical array), so every release, instance and profile ID, both goldens, the frozen migrated pairs, the identity fixtures, `platform/egress` and `platform/auth` are byte-identical and no golden was rewritten. The proof is that `TestGoldenCompilations` passes without `-update`, and `check-rendered.sh` still matches.
  4. **Refusals stated, precedence unmoved** — the instance record cannot state a kind (it selects a release), so what a kind cannot have is a rendering refusal, not a record one. `unsupported: runtime.kind` now names a kind the model does not have (`job`) rather than any non-web; new `invalid: runtime.port`/`runtime.healthPath` for a worker that states one; new `unsupported: grants.exposure`/`callers` for what only a web release can have; in the translation, `only_web_and_worker`, `a_worker_has_no_port`, `a_worker_has_no_health_path`, while the preview keeps `only_http_preview`. Each runs where the check it replaces ran; no existing code or order moved.
  5. **Failure paths tested** — every refusal above, both in the record and in the renderer; a worker that calls and reaches the proxy; a worker compiled end to end with the chart (no Service, no probe); and a worker migrated through `orchestration.Migrate`, whose written pair re-decodes as `worker` with port `0` and no health path.
  6. **Scope stated** — this slice adds a kind, not a platform capability. **`job` is still refused** (a CronJob controller and an instance-level schedule; no instance is one), and a worker that states a database is allowed by the model but exercised by no instance, because v1's app types give a worker none. Events, `team:` grants, offers, secrets and custom alerts remain gaps.
  7. **Offline only** — nothing was applied and no record was migrated. Both v2-owned instances and their rendered files are untouched, so there is no live consequence and no readiness is claimed.
  8. **Recorded in the same change** — this page, the plan's field table, refusal table, renderer boundary and gap list, and the workstream registry.
- **The renderer's name moved with its meaning.** `RenderHTTP`, `HTTPWorkload` and `HTTPInstance` became `RenderInstance`, `WorkloadFacts` and `InstanceFacts`: the renderer is no longer HTTP-only, and a name that says HTTP over an entry point that renders a worker would mislead every later kind. `api.txt` was updated for exactly those three declarations and nothing else; `shadow.CompareHTTP` keeps its name, because it still compares an HTTP shape.
- **Evidence:** `make check-v2` (15 packages), `REQUIRE_HELM=1 make check-v2` (the pinned chart rendered a worker with no Service and no probes, and Kustomize listed its files), `make check`, `make check-lint`, `make -s check-docs`, `git diff --check`, `make -s v2-report`; coverage `kernel` 93.6%, `legacy` 84.5%, `orchestration` 83.6%, `calls` 95.6%, `shadow` 96.7%, CLI 86.2% (`go test -cover`). The report records the API change as the rename plus one field on `WorkloadFacts`, with no budget baseline added or weakened.
- **Why no instance migrated:** the seven workers cannot move yet, and this slice does not claim they can. Six use events (`swhurl-try-30` prod and staging, `swhurl-try-31` staging, `swhurl-try-32` staging and `demo-notifier` staging subscribe or publish), which is slice 3; `demo-shopper` staging calls `demo-shop`, which v1 still owns, so moving it alone would leave its call unconnected (`demo-shopper` is otherwise renderable now).
- **Gaps touched:** the app-kinds row becomes done for `worker` and stays open for `job` and a command override; "v2 renders no worker kind" leaves the not-yet-true list.
- **Committed:** `fb8fa17b`, on `main`, and pushed. The push changed no live manifest: every Flux
  Kustomization reconciled `fb8fa17b` Ready, `make verify-platform` passed, and neither v2-owned instance
  nor any other unit's rendered output moved. No [current state](current-state.md) entry is owed, because
  no live object changed.
- **Next gate:** the operator's approval of slice 3 (events), after the credential decision it needs.

## Slice record: events, part one: the server's user list (29 October 2026)

**Approved 29 October 2026 ("go ahead with slice 3").** Events is the largest domain and the only one whose facts
include a private key, so it is split where the credential sits: this part renders what is public and proves it
against what the platform runs; the instance side and the seed follow.

- **Outcome and the refusals it must produce:** `swhurl-v2 users --snapshot SNAPSHOT.json --controller-key FILE --console-key FILE [--out DIR]` renders NATS's user list — the stream controller, the console, and one entry per keyed instance, with each instance's flag permissions, its own subject when its release publishes, and the consumer calls for the subscriptions that connected — as the ConfigMap the NATS HelmRelease reads. For this repository's snapshot it is **the list the server serves today**: 17 users, the same keys in the same order, every permission identical, and `make check-v2` fails when it is not. It refuses a key that is not a user public nkey, two instances claiming one key, an instance whose names a permission could not be spelled with, a subscription that is malformed, to itself, repeated, or whose Consumer object would collide with another's, a subscription to a publisher that states its events and does not publish that one, and an instance that declares events and has no key.
- **Owners and boundaries:** the **release** owns the events its image publishes; the **instance** owns the public key it signs in with and the subscriptions it grants itself; the new pure package [`events`](../v2/events/events.go) owns which subscriptions connect and the names one needs (the publisher's stream, this subscriber's durable consumer, the filter subject, the Consumer object), so a rendered user, a rendered consumer and a stream cannot disagree about one subscription; [`kernel.RenderUsers`](../v2/kernel/users.go) owns the list's grammar — the flag permissions, the publisher's whole prefix, the consumer calls, the platform's two identities and the document's fixed order — because it is platform-wide and names no instance; the **command** reads the snapshot's records and the platform's two *published* public keys, and composes the resolution into the renderer. **No adapter, no credential and no seed**: a public key and a permission are all this document ever holds.
- **What crosses each seam:** the snapshot's JSON into the command; each instance's `allocations.eventsKey`, `release.publishes` and `instance.subscriptions` into an `events.Declaration`; one `Resolved` per declaration out; those into `kernel.NATSUser`; a `ResourceSet` with one ConfigMap out, `nats/users.json`, claiming `ConfigMap messaging/nats-users` for the platform.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — nothing under `apps/`, `clusters/`, `platform/` or `tools/` was touched; v1 still generates `platform/nats/users.yaml` and `make check-apps` still checks it; `make check` passes.
  2. **Pure** — `events` is registered at layer 0 and in `Pure` and imports only the standard library; `kernel` takes no new effect; the command reads only the files it is named.
  3. **No identity moved** — no release, instance or profile field was added, moved or re-encoded, so every ID, both goldens, the fixtures and every rendered file are untouched; `api.txt` gains exactly the `events` package's declarations, `RenderUsers` and its two input types, and a rules fixture gained the new package.
  4. **Refusals stated, precedence unmoved** — the resolver's fixed reasons are `invalid_subscription`, `self_subscription`, `repeated`, `consumer_collision`, `not_published`, `repeated_instance`, `invalid_app`, `invalid_event` and `invalid_environment`, each naming the instance; the renderer's are `users.controllerKey`, `users.consoleKey`, `users.nkey`, `users.instance` and `users.subscription`; the command adds `unsupported: users.key`. They are all new: no existing code or order moved.
  5. **Failure paths tested** — every refusal above; a publisher's, a subscriber's and a flags-only entry's whole permission list; the platform's two identities first; the fixed order (staging before production, each by app name, whatever order the caller gives); two subscriptions to one stream reading its description once; `--out` writing one file and a second run changing nothing.
  6. **Scope stated** — this part renders the *list*. The instance side (the `SWHURL_EVENTS_*` variables, `events.yaml`'s stream and consumers, the unit's `platform-nats` dependency and `decryption`, and the `envFrom` of the `<app>-events` Secret) is not rendered, so nothing about a v2-owned instance's events changed and `swhurl-try-10` staging still cannot publish `visited`. A v2-owned release cannot state publications yet, so a subscription to one is **waiting**, never refused (`events.Declaration.PublicationsKnown`): the same shape as a publisher that is not there, and it goes when the release gains `publishes`.
  7. **Offline only** — nothing was applied. The proof is a comparison rather than a live read: `v2/check-rendered.sh` renders from a snapshot of the working tree and compares with the committed `platform/nats/users.yaml`, decoding the values on both sides (v2 writes JSON where v1 wrote YAML; both are YAML, so Helm reads them alike). **The cutover is not done**: v1 still writes the file, and replacing its writer changes live NATS authorisation, so it needs its own approval and a live check.
  8. **Recorded in the same change** — this page, the plan's gap list and domain order, the workstream registry, and the NATS section of [`services.md`](services.md#nats).
- **Evidence:** `REQUIRE_HELM=1 make check-v2` (16 packages; the chart render and the rendered platform files ran), `make check`, `make check-lint`, `make -s check-docs`, `git diff --check`, `make -s v2-report` (new package `events` at layer 0 and in `Pure`; API +19/−0, no budget baseline added or weakened); coverage `events` 98.6%, `kernel` 93.8%, `legacy` 84.5%, `orchestration` 83.6%, `calls` 95.6%, `shadow` 96.7%, CLI 86.7%.
- **The decision this part defers: where a v2-owned instance's NATS seed comes from.** Every instance that signs in needs a key pair. The public half is now a fact v2 renders; the seed is a Secret in Git, encrypted, and v2 is stdlib-only and decrypts nothing. Two ways forward:
  1. **Adopt the predecessor's ciphertext (recommended for part two).** The migration copies `apps/<app>/<env>/events.sops.yaml` byte for byte into the instance's folder and the unit gains `decryption`; v2 never decrypts, never generates and has nothing to rotate. Every existing events instance can migrate, which is all seven workers. A *new* v2 instance still cannot declare events, and `users` keeps refusing one that has no key.
  2. **v2 generates the pair and encrypts it to the platform's age recipient.** The key pair is stdlib (`crypto/ed25519` plus the nkeys encoding, tested against v1's vectors) and encrypting needs only the **public** recipient, so no private key is ever read; the cost is one `sops` subprocess adapter that sees a fresh seed, a `.sops.yaml` creation rule for the new path, and the recipient as a profile fact. It completes the domain: a new v2 instance could have events.
  Rejected: a seed written unencrypted (a private key in Git), and a command that creates the Secret in-cluster (Flux would not own it, and a rebuilt instance would lose it).
- **Committed:** `5bdbedb4`, on `main`. Nothing live changed: no record, rendered file or manifest under `platform/`
  moved, so no [current state](current-state.md) entry is owed.
- **Next gate:** the operator's decision on the seed, then part two — the instance side, the migration that adopts a pair, the cutover of the file's writer, and one event-using worker migrated live.

## Slice record: events, part two: the instance side (29 October 2026)

**Approved by the operator as "go ahead with your recommendation", with a standing principle: favour a clean v2
implementation over anything that preserves v1 compatibility — a one-way journey is fine.** The seed decision was
theirs to make and they chose adoption: v2 carries the encrypted file it is given and never reads, decrypts or
mints a key. The two instances v2 already owns keep no broker identity until a slice can mint one, because their
seeds were pruned with their v1 units and no v2 tree holds them.

- **Outcome and the refusals it must produce:** a release states the events its image publishes; an instance states
  its broker identity (the public key it signs in with) and the subscriptions it grants itself; the profile states
  the broker (URL, namespace, the Secret Flux decrypts a seed with, and the platform's two service identities). An
  instance that has a key renders `SWHURL_EVENTS_NKEY`/`SWHURL_EVENTS_URL` and, when they apply,
  `SWHURL_EVENTS_PUBLISH`/`SWHURL_EVENTS_SUBSCRIBE`; `envFrom` of `<app>-events` and the Reloader annotation; its
  `Stream` when it publishes and one `Consumer` per connected subscription; a unit that depends on `platform-nats`
  and decrypts its folder; and a kustomization listing `events.sops.yaml`, the instance's seed — the one file v2
  lists, copies and never writes or reads. It refuses a key that is not a user public nkey, a key with no broker in
  the profile, events (published or received) without a key, a subscription that is not `<publisher>.<event>`, and
  a connected subscription without the names its consumer needs. `compile --out` and `migrate` refuse a keyed
  instance without `--events-secret`, and refuse a seed for an instance that has no key.
- **Owners and boundaries:** the **release** owns what its image publishes; the **instance** owns its key and its
  subscriptions; the **profile** owns the broker, the decryption Secret and the two service identities; the pure
  [`events`](../v2/events/events.go) package owns which subscriptions connect and every name one needs; the
  **resolver** writes them into the same `v2-calls` document as the pairs, because relationships are one decision;
  `kernel` renders the instance's side and lists the seed; `orchestration.WithEventsSecret` puts the operator's file
  beside the manifests, so a folder is complete or refused. The seed is never an input to a pure package.
- **What crosses each seam:** `release.json`'s `publishes`, `instance.json`'s `events` block and the resolution
  document's `subscriptions` into the renderer (`WorkloadFacts.Publishes`, `InstanceFacts.Broker`); the encrypted
  file in as bytes on the use case's input, and out as one written file beside the manifests.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — nothing under `apps/`, `clusters/`, `platform/` or `tools/` was touched. v1 still generates
     `platform/nats/users.yaml`; the cutover is the next gate.
  2. **Pure takes no effects** — `events` and `kernel` stay pure and stdlib-only; the seed reaches only the use
     case and the files adapter, as bytes.
  3. **Identities moved once, deliberately** — the release gained `publishes`, the instance gained its `events`
     block and the profile gained `nats`, so every release ID, every instance revision and the profile ID moved:
     `r41` `b357d417…`, `r42` `4e2bf549…`, `r43` `6b5a72e3…`, profile `83933cd5…`, both owned releases `9653a3aa…`
     and `4eaac1d1…`. The fixtures, `identities.json`, `profile-identity.json`, both goldens, the owned records and
     the frozen migrated pairs were regenerated once; the canonical arrays and every ID were recomputed
     independently in Python, not copied from Go.
  4. **Refusals stated, precedence unmoved** — new `invalid: events.key`, `events.publishes`, `events.subscriptions`,
     `nats.url`, `nats.namespace`, `nats.decryptionSecret`, `nats.controllerKey`, `nats.consoleKey`, and
     `unsupported: nats`; the use case's own `events-secret` refusals. Each runs where the check it belongs to
     runs; no existing code or order moved.
  5. **Failure paths tested** — every refusal; a subscriber's and a publisher's whole rendering (variables, Secret,
     annotation, stream fields, consumer fields, unit dependency and decryption, kustomization, inventory);
     `resolve` recording a connected subscription and a waiting one; `compile --out` delivering the seed byte for
     byte, refusing without it, and writing nothing when it refuses; and the profile's two keys tied to the files
     `platform/nats` publishes.
  6. **Scope stated** — v2 **adopts** a pair; it cannot mint one, so the two instances it already owns stay without
     a broker identity (their seeds were pruned with their v1 unit) and `migrate` of their frozen records is
     asserted to differ from the committed pair by exactly those facts. The `users.yaml` cutover, the first live
     migration of an event-using worker, and rotation remain.
  7. **Offline only** — nothing was applied. The user list is still compared with the file the platform runs, and
     the instance side is proved by the chart and by tests; no readiness is claimed.
  8. **Recorded in the same change** — this page, the plan's field tables, refusal table, resolution and gap list,
     the workstream registry, and the NATS section of [`services.md`](services.md#nats).
- **Evidence:** `REQUIRE_HELM=1 make check-v2` (16 packages; the pinned chart rendered the golden and a worker),
  `make check`, `make check-lint`, `make -s check-docs`, `git diff --check`, `make -s v2-report` (API +18/−13: the
  events facts, the profile block, the seed's use case, and the two renderer types; no budget baseline added or
  weakened); coverage `events` 98.6%, `kernel` 93.0%, `legacy` 84.1%, `orchestration` 82.9%, CLI 86.6%.
- **What the operator's principle changed here:** the profile states the broker, the decryption Secret and the
  platform's two public keys, so `users` takes a profile rather than two key files; the resolution document carries
  subscriptions, so a rendered consumer and a user's permission come from one decision; and the seed is a declared
  input of the compiled folder rather than a v1-shaped special case.
- **Committed:** `283d0689`, on `main`, and pushed. Nothing live changed: the two v2-owned instances' compiled
  files are untouched (they have no broker identity yet), all 38 Flux units reconciled `283d0689` Ready, both
  run their previous images, and `make verify-platform` passed. No [current state](current-state.md) entry is
  owed.
- **Next gate:** the live half — cut `platform/nats/users.yaml` over to v2's renderer (v1's writer retired, and CI
  regenerating it), then migrate one event-using worker and prove an event flows, then the minting slice for the
  two instances that have no pair and for rotation.

## Slice record: the cutover, v2 owns the user list (29 October 2026)

**Approved by the operator's "go ahead".** Part one rendered the list; this makes it the list the platform serves.

- **Outcome:** `platform/nats/users.yaml` is replaced by `platform/nats/users.json`, v2's first platform-wide
  *generated* object, listed by `platform/nats/kustomization.yaml` and read by the broker's HelmRelease as before.
  The content is the same list: 17 users, the same keys, order and permissions (the part-one comparison proved it;
  after the cutover `make check-v2` compares the committed file with the render byte for byte).
- **The writer:** v1's `models/events.py:users`, `events.user`/`controller_user`/`console_user`/`users_configmap`
  and the `Platform` fields that fed them are deleted, so `make app-sync` no longer writes the file, and
  `make check-apps` no longer compares it. `make v2-users` renders it: it snapshots the working tree, builds the Go
  tool and writes that one file. The Render workflow runs both (`app-sync`, then `make v2-users`) and gains
  `actions/setup-go`, so a change to any instance's key, publications or subscriptions reaches the file in the
  same rendered commit.
- **The eight invariants:** 1. **v1 changes deliberately** — its generator is gone; nothing else changed, and every
  other v1 check, render and live test is untouched; `make check` passes (1022 tests). 2. **Pure unchanged** —
  no v2 package's effects changed. 3. **No identity moved.** 4. **Refusals unchanged** — `make check-v2` reports a
  stale file rather than a refusal. 5. **Failure paths** — the file's content was compared with v1's before the
  switch (same document, same users), and `check-rendered.sh` now compares bytes with what the records render.
  6. **Scope** — the file only; the broker's HelmRelease, its Deployment and the instances' own manifests are
  untouched. 7. **Live: applied and checked** — the ConfigMap was replaced, the HelmRelease upgraded and NATS
  reloaded with the same users; every app kept signing in (below). 8. **Recorded here**, in
  [services](services.md#nats), [commands](commands.md) and the registry.
- **Evidence:** `REQUIRE_HELM=1 make check-v2`, `make check` (1022 tests), `make check-lint`, `make check-repo`
  (the unit renders with `users.json`), `make -s check-docs`, `git diff --check`. Live: `e4ac053f` pushed and
  reconciled; the NATS ConfigMap is v2's file; `make verify-platform` passed; **no app logged an authorisation
  failure** in the window (checked in every instance namespace) and the number of users NATS serves is unchanged.
- **Next gate:** migrate the first event-using worker (`demo-notifier` staging, which receives `demo-orders.created`
  from a v1 publisher) and prove an event flows; then the minting slice.

## Slice record: the first migrated event-using worker, demo-notifier staging (29 October 2026)

**Approved by the operator's "go ahead".** `demo-notifier` staging is a Kotlin worker that receives
`demo-orders.created` — a pair whose publisher stays v1-owned, so the migration proves an event crossing the two.

- **What moved:** its three records (`v2-records/demo-notifier/{app.json,staging/{release,instance}.json}`), its
  compiled files under `v2-apps/instances/demo-notifier/staging/` — namespace, HelmRelease, quota, policy,
  `consumer-demo-orders-created.json`, the kustomization and **its `events.sops.yaml`, copied byte for byte** — and
  its unit `clusters/home/app-demo-notifier-staging.json`. Its v1 records, manifests, image automation and `.yaml`
  unit are deleted, so Flux prunes them. The image, the team, the key and the subscription are the same facts, so
  the user list v2 serves did not change.
- **Two defects the live migration found, both fixed with tests:** the resolver and the user list read a
  predecessor's `subscriptions`/`allocations.eventsKey` but not a v2 record's own `events` block, so a migrated
  instance's subscription silently dropped out of the resolution and out of the broker's users. Both readers now
  take either shape, and `TestOwnRecordsStateTheBrokerIdentity` pins it.
- **What it proves:** `v2-records/calls.json` (regenerated) connects the pair with its consumer names; the compiled
  folder lists its seed; the pod runs the same image; NATS accepts its key; the consumer
  `demo-notifier_created` exists on `staging-demo-orders` and the app receives events.
- **A second defect the live run found, fixed before the record:** v2's policy denies egress by default, and it
  admitted only DNS, a callee's port and the proxy — so the migrated pod could not reach the broker
  (`UnknownHostException`, then "Unable to connect to NATS servers"). The profile now states the broker's port and
  the pods that serve it, and `kernel` renders a rule for exactly those pods on that port when an instance signs
  in. The regenerated policy was the only file that changed, and the instance then connected.
- **The gap this exposed, recorded rather than hidden:** [`platform/reloader/helmrelease.yaml`](../platform/reloader/helmrelease.yaml)'s
  namespace list is v1's generated output, built from the instances under `apps/`. A migrated instance is therefore
  no longer in it — `demo-notifier-staging` left the list in this change, exactly as `swhurl-try-10-staging` and
  `swhurl-try-11-staging` did before it — so Reloader does not watch that namespace and a changed Secret does not
  restart the pod. It is the next slice: v2 renders the list (another platform document, like the user list).
  Image automation is the same shape of loss: v2 renders no `ImageRepository`/`ImagePolicy`, so a migrated staging
  instance stops following its repository.
- **Evidence:** `1d0b9a17` (and `51ce10ce` for the policy rule) pushed and reconciled; `make verify-platform`; the
  pod on the same image (`sha256:578f4aa7…`); the consumer `demo-orders-created` Ready in NATS; **22
  `event received: demo-orders.created … from demo-orders` lines in 30 minutes**, the publisher v1-owned; and
  `swhurl-v2 observe` reporting `running` with all seven findings positive — the first time v2 observed a worker. Offline: `REQUIRE_HELM=1 make check-v2`, `make check` (1022 tests),
  `make check-lint`, `make check-repo`, `make -s check-docs`.

## Slice record: Reloader's watch list (29 October 2026)

**Approved by the operator's "go ahead" on the gate.** v1 wrote the namespaces Reloader watches into the
Reloader HelmRelease from the instances under `apps/`, so every migrated instance left the list — silently, as
the first three migrations did. The list is now v2's own platform document.

- **Outcome and the refusal it must produce:** `swhurl-v2 reloader --snapshot FILE --profile FILE [--out DIR]`
  renders the namespaces Reloader watches as one ConfigMap, `reloader-values` in `platform-system`, whose
  `values.yaml` key holds `reloader.namespaces`; the Reloader HelmRelease merges it as values and keeps no list
  of its own. The list is the platform's own namespaces from the profile, then staging's instances and
  production's, each by app name, for the instances that declare a Secret a change must restart them for — their
  broker key, or a Secret name their release states. It refuses a platform namespace that is not a namespace
  name or is listed twice, an instance name or environment that could not spell a namespace, and a platform
  namespace that is also an instance's. v1 no longer writes or checks the list.
- **Owners and boundaries:** the **profile** owns the platform's own watched namespaces; the **instance** owns
  its declaration (its broker key and the Secret names its release states); **`kernel`** owns the rule and the
  document; the **command** reads one snapshot and writes one file; `platform/reloader/helmrelease.yaml` stays a
  hand-written platform file that merges what v2 renders, as the egress proxy's HelmRelease reads its own
  generated objects.
- **What crosses each seam:** one snapshot and the profile in; one file and one claim
  (`platform-system/reloader-values`, owner `platform`) out. No cluster, clock, credential or network.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 changes deliberately** — its writer is deleted with what only it used: `render.Shared.reloaded`, the
     Reloader branch of the write plan, and the committed-file check in `generated.problems`, with the tests
     that exercised them. Nothing else changed; `make check-apps`, `make check-repo` and `make test` pass.
  2. **Pure takes no effects** — `kernel` renders from supplied facts and imports no new package; the command
     reads the two files it names and, with `--out`, writes under that directory only.
  3. **Identities moved once, deliberately** — the profile gained `reloader.namespaces`, so its canonical array
     and ID moved (`prof-sha256:1537cad297a915cb4a78868631732498d10e35cbe7b4919896ecb037e1ef9291`, recomputed in
     Python) and the two goldens' recorded profile ID moved with it. No release or instance identity moved, and
     no compiled document changed.
  4. **Refusals stated, precedence unmoved** — `invalid: reloader.namespaces` and `invalid: reloader.instance`
     are new; no existing code or order moved.
  5. **Failure paths tested** — every refusal; an empty watch list rendering `[]` rather than null; both record
     shapes (v1's `allocations.eventsKey`, v2's `events.key`); a release-declared Secret; the order (platform,
     then staging and production by app); the write path twice (`written`, then `unchanged`); and
     `check-rendered.sh` comparing the committed file with the render.
  6. **Scope stated** — the list only. **Image automation is deliberately not restored:** the `ImagePolicy` that
     starts a deploy is read by v1's Deploy workflow, which writes `apps/<app>/<env>/release.yaml`; a v2-owned
     instance's records are not there, so rendering the policy would start a workflow that cannot write its
     release. It needs v2's own selection path first and stays in the [gap list](iteration-2-plan.md#6-deliberately-deferred).
     One semantic difference is stated rather than hidden: a v1 Secret file whose names its release has not
     declared yet is not watched, because v2 reads declarations and the snapshot never reads an encrypted file.
  7. **Offline is not live** — the offline proof is a comparison: `check-rendered.sh` renders from a snapshot of
     the working tree and compares with the committed `platform/reloader/values.json` (18 namespaces: the
     platform's three and 15 instances). **Live, `57d1c8e9`:** the Reloader HelmRelease is Ready and its
     Deployment runs with `--namespaces` equal to the rendered list plus the chart's own `platform-system`; a
     Role and RoleBinding appeared in `demo-notifier-staging` and none in the two v2-owned staging namespaces
     that declare no Secret; the controller logged `Watching scoped namespaces: …`, it logged no error or
     forbidden line in the following ten minutes, and `make verify-platform` passed. A changed Secret restarting
     one of those workloads was not exercised (it needs a live Secret edited); the mechanism is
     `make live-test-reloader`'s. [Current state](current-state.md#v2-renders-reloaders-watch-list-and-a-migrated-instance-is-watched-again-29-october-2026).
  8. **Recorded in the same change** — this page, the plan's gap list, [services](services.md#reloader),
     [commands](commands.md), [apps](apps.md) and the workstream registry.
- **Evidence:** `REQUIRE_HELM=1 make check-v2` (16 packages; the chart render and all four rendered platform
  folders ran), `make check` (1019 tests), `make check-lint`, `make check-repo` (39 entrypoints), `make check-apps`
  (17 instances), `git diff --check` and `make -s v2-report` (API +6/−1: the watch model, its two exports and the
  profile block; no budget baseline added or weakened). Coverage: `kernel` 92.8%, CLI 86.5%.
- **Next gate:** the minting slice — a broker identity for `swhurl-try-10` and `swhurl-try-11` staging, and
  rotation.

## Slice record: minting a broker identity (29 October 2026)

**Approved by the operator's "go ahead" on the gate.** Parts one and two rendered the broker's side of events and
every keyed instance's side; v2 could only *adopt* a pair a predecessor already had. This slice lets it create
one, which the two v2-owned instances without a pair needed — their seeds were pruned with their v1 units — and
rotation needs.

- **Outcome and the refusal it must produce:** `swhurl-v2 mint --instance FILE --profile FILE --out DIR
  [--rotate]` draws a 32-byte seed, derives the user key pair, renders the Secret that holds the seed
  (`<app>-events`, the instance's namespace, `stringData.SWHURL_EVENTS_SEED`), encrypts it with `sops` to the
  profile's SOPS recipient, writes it at `DIR/v2-records/<app>/<env>/events.sops.yaml`, and only then writes
  `instance.json` stating the public key. The report names the key, the key it replaced and the files written,
  and never the seed. It refuses a profile that states no recipient, an instance that already has a key unless
  `--rotate` is given, `--rotate` without a key, a ciphertext that holds the plaintext or no encrypted value, and
  a missing or failing `sops`. `compile` and `migrate` are unchanged: the encrypted file is still a declared
  input they copy and never read.
- **Owners and boundaries:** the **kernel** owns the pair's encoding (`NewUserKey`, Ed25519 and nkeys), the Secret
  document (`EventsSecret`), the record update (`WithBrokerKey`) and what can be said about a ciphertext it cannot
  decrypt (`CheckEncryptedSeed`); the **command** owns randomness and the flags; **`adapters/sops`** owns the one
  process and the format; **`orchestration`** sequences and writes through `adapters/files`; the **profile** owns
  the recipient; the **record** owns the public key. The plaintext seed exists only between the kernel and the
  encrypter: it never reaches the writer, the report or a diagnostic.
- **What crosses each seam:** the record and profile bytes in, a seed as an argument, ciphertext out to the
  writer; the process's stdin and stdout hold the document and its ciphertext, and its stderr is discarded
  rather than echoed.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 changes deliberately, and only where it had to** — nothing under `apps/` was touched, and v1 still makes
     a v1-owned instance's key. The one v1-visible change is the **rule**: `no-subprocess` now reads "v2 code
     starts no process except the encryption adapter `adapters/sops`, and names no kubectl or flux command". Its
     test proves a second adapter importing `os/exec` is still refused and that the allowed one still cannot name
     `kubectl`; the guardrails table says the same. `make check` passes (1019 tests).
  2. **Pure takes no effects** — the kernel derives a pair from a seed it is given; `crypto/ed25519`,
     `encoding/base32` and `encoding/binary` join `PureImports` (derivation, not key generation; no clock,
     randomness, file or process). Randomness is the command's, the process is the adapter's.
  3. **Identities moved once, deliberately** — the platform's age recipient moved out of `backup` to the new
     `sops.recipient`, because it now has two owners (the backup job and every SOPS file v2 encrypts); the profile
     ID moved (`prof-sha256:46e99dd6…`, recomputed in Python) and both goldens changed in their profile line only.
     The two minted instance records gained `events.key`, so their revisions moved and their compiled files
     changed as designed: the env pair, the `envFrom`, the reload annotation, the broker egress rule, the
     `events.sops.yaml` entry, the unit's `platform-nats` dependency and `decryption`.
  4. **Refusals stated, precedence unmoved** — `invalid: events.secret`, `unsupported: events.key` (the kernel),
     `refused: sops: the_profile_states_no_recipient` (the use case) and `refused_encryption` with
     `no_recipient`, `sops_not_available`, `sops_exit_N`, `empty_output` and the size refusals (the adapter). No
     existing code or order moved.
  5. **Failure paths tested** — every refusal; a ciphertext handed back as plaintext; a missing binary and a
     command that exits non-zero; two encryptions of one document differing (SOPS draws a fresh data key); the
     seed written before the record; a refused mint writing nothing; the fake recording the plaintext so the test
     can prove the writer never saw it; and the adapter contract run by both the real command and the fake.
  6. **Scope stated** — minting and rotation. The live use of this slice was two *new* keys; **the operator then
     asked for a rotation**, and `demo-notifier` staging's pair was replaced with `--rotate`: one authorisation
     violation while the server refreshed its config, then events again, 0 restarts, `verify-platform` green
     ([evidence](current-state.md#demo-notifier-stagings-broker-identity-was-rotated-live-29-october-2026)).
     The same check found a defect outside this slice: a v2-owned instance's policy denies the node collector,
     so its telemetry does not export ([evidence](current-state.md#a-v2-owned-instance-cannot-reach-the-node-collector-29-october-2026)). A public key cannot be checked against its seed without decrypting, so
     v2's own check is "not the plaintext, and encrypted"; whether the platform can read it is proved by the
     workload signing in. `mint` writes the record folder; it does not compile, so the operator recompiles.
  7. **Offline is not live** — the offline proof is the tests, the adapter contract (run against the real `sops`
     when it is installed, and required in CI by `REQUIRE_SOPS=1`, where the tooling job installs the pinned
     release and checks its published checksum), and the recompile test. The live half — the two
     instances minted, recompiled and applied, their ciphertext decrypting and the broker serving their keys, while
     neither client logged a sign-in (not recorded as one) — is in
     [current state](current-state.md#v2-minted-a-broker-identity-for-both-keyless-instances-29-october-2026).
  8. **Recorded in the same change** — this page, the plan's gap list, backup field table and refusal list, the NATS
     section of [services](services.md#nats), the [workstream registry](workstreams.md) and CI's `REQUIRE_SOPS`.
- **Evidence:** `REQUIRE_HELM=1 REQUIRE_SOPS=1 make check-v2` (18 packages; the real encryption contract, the chart
  render and all four rendered platform folders ran), `make check` (1019 tests), `make check-lint`,
  `make check-repo`, `make check-apps`, `git diff --check` and `make -s v2-report` (API +25/−2: the pair, the Secret,
  the record update, the adapter and its fake; two new layer-1 packages; three pure imports added; no budget
  baseline added or weakened). Coverage: `kernel` 92.8%, `adapters/sops` 75.0%, `orchestration` 83.1%, CLI 86.0%.
- **Next gate:** the operator's choice of the next slice, with a v2-owned instance's telemetry export — found
  blocked by its own policy — recommended first; then a delivery path for image automation, or the next domain
  v2 does not render (secret names, team grants, public routes).

## Slice record: the telemetry export (29 October 2026)

**Approved by the operator's "go ahead" after the rotation found it.** The always-on surface gave every instance an
OTLP endpoint; a v2 instance's own policy then refused to let anything reach it.

- **Outcome and the refusal it must produce:** a release that states its image reports (`telemetry: true`) renders a
  policy rule admitting the profile's `telemetry.nodeCIDR` on the port the endpoint states — so the endpoint the
  container is given and the way out of the pod come from one place. An instance that reports nowhere gets no such
  rule, and every other destination stays denied. The profile must state `nodeCIDR` inside a telemetry block, and
  the endpoint must state the port; it refuses `invalid: telemetry.nodeCIDR` for a value that is not an IPv4 block,
  for a block with an endpoint that states no port, and for a telemetry block without it.
- **Owners and boundaries:** the **profile** owns the block (a platform fact: the nodes that serve the collector);
  the **release** owns whether its image reports; the **endpoint** owns the port; **`kernel`** renders the rule and
  decides nothing else about egress. The instance's policy stays deny-by-default, and this is its only new way out.
- **What crosses each seam:** `WorkloadFacts.Telemetry` and `PlatformProfile.Telemetry` into the policy renderer; one
  egress rule out. No cluster, clock or credential.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1 unchanged** — nothing under `apps/`, `clusters/`, `platform/` or `tools/` was touched; v1's policies state
     `Ingress` only, so its apps were never affected.
  2. **Pure** — the rule is rendered from supplied facts; `CollectorPort` parses the endpoint the profile states, and
     `kernel` takes no new effect.
  3. **Identities moved once, deliberately** — the profile gained `telemetry.nodeCIDR`, so its canonical form and ID
     moved (`prof-sha256:20ea752d…`, recomputed in Python) and the two goldens changed in their profile ID and in one
     document, the network policy, which gained exactly the collector rule. The three owned instances were
     recompiled: their policy is the only file that changed. No release or instance identity moved.
  4. **Refusals stated, precedence unmoved** — the new refusals are `invalid: telemetry.nodeCIDR` in three cases; no
     existing code or order moved.
  5. **Failure paths tested** — a reporting instance's rule (block and port), an instance that reports nowhere (no
     rule), a bad block, a missing block, and an endpoint without a port.
  6. **Scope stated** — the collector's block on one port, nothing more. The block is `/32` here (one node); a
     cluster with more nodes states a block covering them. OTLP is not routed through the egress proxy, and the
     proxy's own configuration is unchanged.
  7. **Offline is not live** — the offline proof is the rendered policy and the goldens. **Live, `09571ebb`:** the
     three NetworkPolicies were updated at `23:19:18Z` with no pod restart; `demo-notifier`'s exporter stopped
     failing at `23:19:15Z`, its traces (absent since its v2-owned pod started) resumed at `23:19Z`, and
     `swhurl-try-10`/`-11` went from no gauge points to about 154 and 153 a minute; a debug container in the app's
     own network namespace got HTTP 200 from the collector; `make verify-platform` passed
     ([evidence](current-state.md#the-three-v2-owned-instances-export-telemetry-29-october-2026)).
  8. **Recorded in the same change** — this page, the plan's gap list and profile field sentence, [apps](apps.md#telemetry),
     [services](services.md#clickstack-and-otel) and the workstream registry.
- **Evidence:** `REQUIRE_HELM=1 REQUIRE_SOPS=1 make check-v2` (18 packages; the chart render and all four rendered
  platform folders ran), `make check` (1021 tests), `make check-lint`, `make check-repo`, `make check-apps`,
  `git diff --check` and `make -s v2-report` (API +2/−1: the profile field and `CollectorPort`). Coverage:
  `kernel` 92.8%, CLI 86.0%.
- **Next gate:** the operator's choice of the next slice: a delivery path for image automation, or the next domain v2
  does not render (secret names, team grants, public routes).


## Slice record: image automation's offline half (29 October 2026)

**Approved by the operator's "go ahead with the offline first slice", after choosing image automation over the next unrendered domain.**

- **Outcome and the refusals it must produce:** a staging instance whose image is published from its own source
  repository renders `ImageRepository/<app>` and `ImagePolicy/<app>-staging` — v1's object, field for field, with
  the platform's tag contract — and its unit waits for `platform-image-automation`. `swhurl-v2 deploy` reads the
  newest tag that policy would pick, resolves its `linux/amd64` manifest, reads the declaration the build wrote
  into the image's config labels, and writes the release record, the selected instance and the compiled files in
  one effect. It refuses a declaration v2 cannot carry (`secrets`, a `memoryMiB` above the instance's, another
  `kind`, a different `database`, another `uid` on a volume), an image with no declaration, a tag whose commit is
  abbreviated, a stale `--expect`, a candidate for another app, and everything compile refuses; a tag matching
  no platform tag is a refusal, and a registry it cannot read is exit 1 and writes nothing. The image already
  selected is a no-op that writes nothing.
- **Owners and boundaries:** the **image** owns its declaration (the build writes it from `swhurl.yaml`); the
  **release** owns the facts that declaration states, with the digest and media type the registry answered; the
  **instance** owns selection and configuration and changes only its release; the **platform** owns the tag
  contract, the object names and the pair's placement (kernel constants, tied to v1's committed objects); the
  **registry adapter** owns the protocol (token, tags, index → platform manifest, config labels) and speaks no
  policy; **`kernel`** decides the newest tag, decodes the declaration, refuses what the instance cannot take and
  renders the pair; **`orchestration`** sequences the read and the one write; the **command** is flags, exit codes
  and the `v2-deploy` document. The workflow that calls it is not in this slice.
- **What crosses each seam:** one repository's tags and one tag's digest, media type and labels, read anonymously
  from the registry; the declaration label into the kernel; the selected pair into the compiler; the record and
  compiled files into the files adapter. No cluster, credential, clock or subprocess.
- **The [eight slice invariants](workstreams.md#what-a-status-page-is):**
  1. **v1's files and instances are unchanged** — nothing under `apps/`, `tools/` or `platform/` was touched. The
     three v2-owned staging units gained the pair and one dependency, so Flux now reports their repositories: the
     v1 Alert dispatches its workflow for those reports and the workflow's existing path finds no `apps/` record
     and deploys nothing. That extra, idempotent run is the one v1 behaviour this change has, and the next slice
     replaces it for v2-owned instances.
  2. **Pure** — the tag policy, the declaration decode, the acceptance rules and the pair's rendering are pure;
     the only new effect is the read-only registry adapter (standard-library HTTP, no credential, no subprocess).
  3. **Identities changed only where stated** — no release, instance, app or profile identity moved. The staging
     golden's compilation ID moved with two new documents and the unit's dependency; the three owned instances
     were recompiled (their records unchanged) and their committed output is what the compiler now renders. The
     goldens were regenerated once, for that change.
  4. **Refusals stated, precedence unmoved** — the new refusals are the kernel's existing codes with the
     declaration's fields (`declaration.secrets`, `declaration.memoryMiB`, `declaration.database`, `runtime.kind`,
     `runtime.uid`), the use case's `image/no_tag_matches` and `declaration/absent`, and the adapter's
     `unavailable` (exit 1) and `no_platform_manifest`. Deploy's order is decode, tags, newest, describe, no-op,
     declaration, acceptance, selection, compile, write.
  5. **Failure paths tested** — the registry's recorded answers, an index with no `linux/amd64`, a forbidden,
     missing, malformed or oversized answer, a bad image, each declaration refusal, every acceptance refusal, a
     stale revision, an abbreviated commit, a no-op, a registry failure and a write failure.
  6. **Scope stated** — the offline half. No workflow calls `deploy`, so a migrated instance still does not follow
     a new image; a third-party image renders no pair and is not automated; production renders none; the
     declaration carries no offers (`provides`, `flags`), because what an app offers is still unmodelled; and an
     image that drops an event another instance subscribes to is not yet refused across instances.
  7. **Offline is not live** — the pair itself is **live, 30 October 2026**: the three policies are Ready and
     report their newest tags (the repositories scan; no pod restarted), the v1 Deploy workflow ran for the three
     reports and wrote nothing, and `verify-platform` passed. The delivery path was exercised read-only against
     the real registry: `swhurl-try-10` resolved to its record's platform manifest (no change); the other two
     refused `declaration: absent` because their images predate the label. No image travelled the path and no
     record was written ([evidence](current-state.md#the-three-v2-owned-staging-instances-report-their-images-30-october-2026)).
  8. **Recorded in the same change** — this page, the plan's declaration contract and gap list, [apps](apps.md#deploy-a-new-image),
     the workstream registry and the adapter list.
- **Evidence:** `REQUIRE_HELM=1 make check-v2` (20 packages; the chart render and the four rendered platform
  folders ran), `make check` (1021 tests), `make check-lint`, `git diff --check`, `make -s v2-report` (two new
  layer-1 packages; API +36/−1; new budget entries, none raised) and `make -s check-docs`. Coverage: `kernel`
  93.2%, `adapters/registry` 82.2%, `orchestration` 82.9%, CLI 85.8%. The registry adapter's contract runs against
  recorded answers and its fake; the pair is compared with v1's committed `image-automation.yaml` field for field.
- **Next gate:** a template release writes the declaration label (both stacks), an image is rebuilt with it, then
  the Deploy workflow calls `swhurl-v2 deploy` for v2-owned staging instances (leaving v1's path for v1's apps)
  and a real image travels dispatch → record → compile → Flux, with `make app-status` and `make verify-platform`
  as evidence.

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
make -s check-docs
```

The report shows what the tree adds to `origin/main`: a new package, a changed exported API, a changed rule setting, a budget entry added or raised must each be explained by the slice. A changed golden compilation, a weakened budget or a silent rule change is a stop, not a fix.

**End every turn with progress against the goals.** State where the workstream stands against the table above — what moved this turn, which phase it advances, and the next gate — in one short block, so progress is read rather than reconstructed.

**Stop line:** no commit, push, publication, credential access, cluster action or policy-exception expansion without the operator's instruction. Other work may be in progress in the same checkout: re-run `git status`, inspect the diff, and never stage, discard or format unrelated work.

## Suggested opening prompt for a fresh session

> Work on v2 only. Read `docs/v2-status.md` first (status, the gate, authority, the resume steps), then
> `docs/iteration-2-plan.md` for the contracts, the layers and [the gap list](iteration-2-plan.md#6-deliberately-deferred),
> and `AGENTS.md` for the repository rules. Do not re-derive the state from this prompt: check it.
>
> Then run, from the repository root: `git log --oneline -5`, `git status --short`, `make check-v2`,
> `REQUIRE_HELM=1 make check-v2`, `make -s v2-report`, `make -s check-docs`, and look at CI's latest Validate run
> (`gh run list -w Validate -L 3`). Report what they say before proposing anything. A check that reads the working
> tree can pass here and fail on a clone; CI is the reader that matters.
>
> Where v2 stands (29 October 2026). Release selection, the compiler, Git delivery, observation, routes, calls,
> storage, egress, backup and events are done and proved live. Three apps are v2-owned — `swhurl-try-11`
> staging, `swhurl-try-10` staging (on its own adopted volume) and `demo-notifier` staging — and each recompiles
> from Git alone. A v2-rendered callee
> gets its keys and callers only as files, and a new caller and a key rotation were shown to take effect in the
> running pod with no restart. `make check-v2` also renders `platform/egress`, `platform/auth`, `platform/nats` and `platform/reloader`
> from the records and fails on any difference.
>
> **The gate is a decision brief, not code.** v2 renders only what those two instances needed, so the other
> apps cannot migrate. Rank the domains v2 does not render by what each unblocks: telemetry (the OTLP endpoint and
> its variables), notifications (`SWHURL_NOTIFY_URL` and the projected `notify` token), the worker and job kinds,
> teams (the namespace label, isolation, `team:` grants), events (NATS users, streams, consumers), secrets
> (`envFrom`, Reloader), alerts and dashboards, and what an app offers. A census taken on 29 October, to be
> checked rather than trusted: every v1 instance gets telemetry and notify variables (so the two v2-owned ones run
> without them today), 7 of 15 are workers or jobs, every app has a team, 4 instances grant calls by team, and 2
> subscribe to events. For each domain say who owns the fact today (v1's records, the console, a platform unit),
> what v2 would own, which apps it unblocks, its identity cost, and whether a v2-owned instance is missing it
> live. Recommend a first slice and its order, using the `decision-brief` skill, and stop for approval.
>
> **Out of scope:** the v1-caller gap (v1 renders no caller side for a v2-owned callee), which the operator
> chose not to address; it stays in the gap list.
>
> The layers are checked: 0 pure (`kernel`, `calls`, `legacy`, `observe`, `shadow`), 1 adapters (`files`, `kube`,
> `s3`), 2 use cases (`orchestration`: compile, migrate, restore), 3 the command. Follow the operator's design
> principles: maintainable v2 contracts rather than v1 compatibility; a pure core with orchestration above it and
> integrations at the edges; explicit domain and data ownership; iterative transition with honestly incomplete
> semantics. Answer the [eight slice invariants](workstreams.md#what-a-status-page-is) on the status page in the
> same change as the code, keep the gap list current, and run `make check-v2`, `REQUIRE_HELM=1 make check-v2`,
> `make check`, `make check-lint` and `make -s check-docs` before asking to commit. Identity changes are
> deliberate: a fact added to a release, instance or profile changes its ID, so regenerate the goldens once,
> recompute the fixtures' identities independently in Python, and say why. A selected release for a new image is
> still a hand step (record, `release-id`, `select-release --expect`, `compile`); check its artifact claim against
> the registry, since a tag's digest is usually an index.
>
> The code graph is a separate workstream: do not build it here. Preserve unrelated work; do not commit, push,
> publish, access credentials or touch the live cluster without the operator's instruction, and treat any
> namespace or data deletion as needing explicit confirmation.

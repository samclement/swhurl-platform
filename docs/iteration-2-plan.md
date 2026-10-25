# Iteration 2: incremental replacement alongside the current platform

**Status: phase 1 implemented and offline-verified 23 October 2026, committed 24 October 2026.** The operator approved the root-level Go module, JSON-only descriptors, positional canonical identity encoding, manifest-only `linux/amd64` artifacts and empty-only bindings. Phases 2–7 remain proposals. v2 and the [visual change-tool investigation](visual-change-tool.md) are independent workstreams; neither is a prerequisite for the other. No publication, live migration, v1 behaviour change or feature removal is approved. Do not commit or push without instruction.

This plan develops the recommendations in [the architecture review](architecture-review.md). [Architecture](architecture.md) remains the canonical description of deployed behaviour; [plan](plan.md) section 0 remains the index of approved work and links here for the phase-1 contract, evidence and handoff.

## Goal and recommendation

Build iteration 2 alongside the current implementation, initially as an **offline model and compiler—not a second live control plane**.

The foundational distinction is release construction and identity versus environment selection. The existing `Release` and `Settings` records already separate much of the information, but releases are still stored as environment snapshots rather than independently identified objects.

The first milestone must prove this example without GitHub, Kubernetes or the console:

```text
Before:
  staging → R42 + staging configuration + staging bindings
  prod    → R41 + prod configuration    + prod bindings

Select R42 for prod.

After:
  staging → R42 + staging configuration + staging bindings
  prod    → R42 + prod configuration    + prod bindings
```

**Foundational contract: selecting a release changes what runs, not the environment's authority, configuration or resource identities.**

## 1. Approach decision

### Assumptions

- Git remains the authority for deployment intent; Kubernetes and Flux remain the runtime substrate. These are current boundaries documented in [architecture](architecture.md); retaining them for iteration 2 is proposed.
- Lower maintenance cost matters more than preserving every convenience. This is an assumption to confirm, not an approved feature-removal policy.
- The current platform remains operational during development.
- The phase-1 supported workload is a private, stateless HTTP application. This restriction does not change v1.

### Options

1. **Refactor v1 in place.** Least duplication, but existing workflow contracts constrain each step.
2. **Parallel offline v2 kernel, then selective cutover. Recommended.** Bounded duplication, independent verification and reversible early phases.
3. **Separate repository and parallel live platform.** Strong isolation, but adds bootstrap, infrastructure drift and migration work before proving the model.

The recommendation prioritises blast-radius control, clear ownership, offline testability and a short path to proving the core contract. A requirement for a different runtime or independent platform operation would change the choice.

### Language direction: Go kernel, eventual HTMX console

**User preference:** a single fast binary with a small runtime; interest in HTMX for a future UI. **Approved for phase 1:** implement the bounded offline kernel in Go, then assess maintainability before expanding it. No platform console UI or broader rewrite is approved. The [visual change tool](visual-change-tool.md) is a separate investigation using v1 code and Git history, not part of the Go kernel or a prerequisite for v2.

Options:

1. **Keep Python:** least migration cost, compact operator code, existing test/tooling reuse. A fast HTMX UI remains possible.
2. **Go for the bounded v2 kernel. Recommended:** portable application binary, fast CLI startup, shared CLI/server domain code and a strong standard library. Costs a new toolchain and deliberate reimplementation of existing safety contracts.
3. **Rewrite everything in Go immediately:** avoid. Too much behavioural and migration risk before the kernel proves its value.

Go is attractive primarily for distribution and operational simplicity, not as a cure for remote-read latency. Potential memory/CPU improvements are hypotheses to measure, not established results. Go includes a runtime and garbage collector; “single binary” does not mean zero runtime overhead.

#### What single binary means

Start with **one application binary**, not a promise of no external executable dependencies for every future operation.

- The pure model, parsing, validation and offline CLI should not need Python or external executables at runtime.
- An eventual server can embed templates, CSS and HTMX using Go's asset embedding and share the domain package with the CLI.
- Add direct GitHub/Kubernetes clients only when those phases require them.
- Git, SOPS or other external tools may remain explicit adapter dependencies later. Do not embed large Helm/Flux/SOPS libraries solely to reduce executable count.
- Leave Copier/scaffolding outside the initial v2 boundary.
- A CLI and a console may use the same binary but must receive separate credentials and permissions appropriate to their deployment.
- A future container should start simple: non-root, CA certificates, an explicit temporary directory. A completely empty base image is not a goal in itself.

The current console image includes Python, git, kubectl, Helm and SOPS; see [its Dockerfile](../images/console/Dockerfile). Removing Python alone would not eliminate the other dependencies. Rebuild the binary regularly for Go/runtime/dependency security updates; host package updates do not update linked components.

#### Go-specific contracts

- Structs are not automatically immutable. Own slices/maps carefully so transitions cannot mutate their inputs through aliases.
- Strict decoding must distinguish missing values from explicit zero/false where relevant, reject unknown fields and versions, and validate combinations.
- Specify canonical identity encoding independently of Go's default serializers. Include golden vectors so another implementation can reproduce release/spec identities; account for YAML normalization and JSON escaping.
- Preserve error, secret-redaction, dry-run and failed-read contracts intentionally. Go's types do not replace these tests.
- Prefer ordinary packages/functions and narrow interfaces at actual effect boundaries. Do not introduce a provider/plugin framework.

#### Future UI: Go plus HTMX

The current console already uses HTMX: [base template](../tools/swhurl/console/templates/base.html) and page templates contain HTMX navigation, form and refresh attributes. Go + HTMX would continue the existing server-rendered approach, not replace a client-side SPA.

The useful performance direction is:

```text
Browser → useful page shell → independently loaded status sections
        → bounded observation reads → HTML fragments
```

Return useful content before optional integrations finish; refresh only relevant sections; avoid duplicate expensive reads across fragments; show loading/unavailable/stale evidence honestly. Preserve ordinary links/forms, backend authentication/authorisation and CSRF protections. Polling is sufficient initially; use SSE only where streaming adds value.

HTMX does not make server work fast, and fragmenting one expensive request into many can increase work. The existing [read-performance documentation](console.md#read-performance) and architecture review identify remote historical-evidence work as a major cost. Simplifying that promise and sharing immutable reads matter more than changing template engines. No frontend work belongs in phase 1.

## 2. Phase-1 model and contract

Two value records and one pure operation live in [`v2/kernel`](../v2/kernel/records.go). All domain members are scalars or nested value structs: copying a record cannot alias a map, slice or pointer. Construction alone does not validate a Go value; identity, encoding and selection validate it. No network, filesystem, environment, clock, randomness or subprocess access belongs in this package.

### ReleaseBundle: what can run

[`r42.json`](../v2/testdata/r42.json) is a complete synthetic descriptor, not a published image. Its exact schema is:

| Field | Accepted value |
| --- | --- |
| `schemaVersion` | Integer `1` |
| `application` | v1 app-name grammar: lowercase DNS label starting with a letter, at most 40 characters |
| `source.repository` | ASCII `owner/repository`; owner starts alphanumeric and then alphanumerics/hyphens; repository starts alphanumeric and then alphanumerics/dot/underscore/hyphen |
| `source.commit` | Exactly 40 lowercase hex characters; SHA-1 source commits only in this slice |
| `artifact.image` | Lowercase dotted DNS registry plus slash-separated repository components; each component is alphanumeric groups separated by a single dot, underscore or hyphen; no scheme, credentials, registry port, tag or digest suffix |
| `artifact.digest` | `sha256:` followed by exactly 64 lowercase hex characters |
| `artifact.mediaType` | Exactly `application/vnd.oci.image.manifest.v1+json` |
| `artifact.platform` | Exactly `linux/amd64` |
| `runtime.kind` | Exactly `web`: stateless HTTP, no command override or additional needs |
| `runtime.port` | Integer `1–65535` |
| `runtime.healthPath` | Absolute ASCII path; no authority (`//`), query or fragment. Allowed characters after the initial slash: alphanumerics and `._~!$&'()*+,;=:@%/-` |
| `runtime.uid` | Integer `1–2147483647`; non-root |
| `releaseId` | Optional identity assertion; if supplied, must match the computed release ID |

Values are compared exactly, never normalised: `Owner/Repo` and `owner/repo` are different sources and give different release IDs, though GitHub treats them as one repository.

All fields except `releaseId` are required. Absence of the assertion computes it; encoding always includes it. Empty/null assertions are invalid, not absence. No grants, database, secret requirements, offers, command override or integrations are accepted.

The artifact digest names an **OCI image manifest**, not an index/list or a configuration blob. Indexes, Docker media types and other platforms are unsupported. This intentionally does not accept every v1 image pin. These are descriptor claims: the offline kernel cannot prove existence, object type, runtime platform or trusted build provenance. It never contacts a registry or fetches source.

### InstanceSpec: where and under what conditions it runs

[`prod.json`](../v2/testdata/prod.json) and [`staging.json`](../v2/testdata/staging.json) have deliberately different configuration. Every field is required:

| Field | Accepted value | Changes on selection? |
| --- | --- | --- |
| `schemaVersion` | Integer `1` | No |
| `application` | Same app-name grammar as the release | No |
| `environment` | `staging` or `prod` | No |
| `release` | `rel-sha256:` plus 64 lowercase hex characters | **Yes** |
| `configuration.replicas` | Integer `0–2147483647`; zero explicitly means stopped | No |
| `configuration.memoryMiB` | Integer `1–2147483647`; no Kubernetes quantity parser | No |
| `grants.exposure` | Exactly `private` | No |
| `bindings` | Explicit empty object `{}` | No |

Non-empty bindings are refused, not ignored. This phase proves preservation of **empty** bindings, not supported resource attachments. There is no create-instance operation or copying of staging defaults. Secrets and live observations do not belong in either record.

### Strict document boundary

Input is exactly one UTF-8 **JSON** document. YAML is not supported in this slice, avoiding a decoder dependency and YAML normalisation rules. Reject duplicate keys (including escaped spellings), case variants, unknown fields at every level, missing required fields, nulls, incorrect scalar types, unsupported versions and trailing documents. Integers use JSON integer syntax: no fractions or exponent notation. There are no implicit defaults. Invalid documents return no usable record; diagnostics identify the field/category without dumping rejected values.

### Release identity and instance revision

Canonical encoding is independent of Go struct and map serialisation. Build these positional arrays in exactly this order:

```text
release:
["swhurl.release.v1", schemaVersion, application,
 source.repository, source.commit,
 artifact.image, artifact.digest, artifact.mediaType, artifact.platform,
 runtime.kind, runtime.port, runtime.healthPath, runtime.uid]

instance:
["swhurl.instance.v1", schemaVersion, application, environment, release,
 configuration.replicas, configuration.memoryMiB, grants.exposure, {}]
```

Encode as compact UTF-8 JSON, with no whitespace or trailing newline. Integers are decimal without leading zeroes. Strings use JSON double quotes: escape double quote/backslash with a backslash; control characters use the standard short escapes where defined, otherwise lowercase `\\u00xx`; do not escape slash, HTML characters or printable ASCII. Accepted domain strings are ASCII; alternate input JSON escape spellings decode to those same values. There is no Unicode normalisation/default filling. The only object in this encoding is literal `{}`.

Hash those bytes with SHA-256. The release is `rel-sha256:<64 lowercase hex>`; the instance revision is `inst-sha256:<64 lowercase hex>`. `releaseId` is excluded to avoid self-reference. Every accepted payload field participates, including source provenance. Formatting, input key order, filenames, environment defaults, Git HEAD and clocks do not participate. A changed descriptor cannot reuse an asserted ID unless its content hash agrees.

[`identities.json`](../v2/testdata/identities.json) records literal canonical bytes and expected hashes, independently calculated with Python's standard-library JSON and SHA-256. Tests compare both bytes and identities. Versions/profile fields participate even though alternate values are refused.

Instance revisions identify **content, not edit history**. Changing a spec then restoring identical content restores its revision (ABA). There is no store, sequence counter or publication CAS in phase 1; a later writer must protect its own read/write boundary.

### SelectRelease: the first transition

```go
func SelectRelease(current InstanceSpec, candidate ReleaseBundle,
    expected InstanceRevision) (InstanceSpec, error)
```

Order is contractual:

1. Validate current, candidate (including its identity assertion) and expected revision syntax.
2. Refuse candidates from a different application.
3. Refuse an expected revision that differs from the complete current spec's revision.
4. Return a copy with **only the release reference replaced**. An already-selected release is a successful no-op only after the preceding checks.

Configuration, grants, bindings and both input values remain unchanged. A stale same-release request is still stale; repeat selection against the resulting revision makes no further change. Refusals return a zero result and a typed error with stable codes: `invalid`, `unsupported`, `identity_mismatch`, `wrong_application`, `stale_instance`. Exact prose is not the machine contract. Unsupported capabilities never provision or acquire authority.

This is selection, not promotion: there are no staging-readiness, actor-authorisation or approval checks, and no publication. Successful model checks do not authorise production delivery.

### Refusal codes

The code is the machine contract; the field says where; the reason is prose for people.

| Code | When |
| --- | --- |
| `invalid` | Not one valid UTF-8 JSON document, a duplicate key, not an object, a missing required field, a `null`, a wrong type or out-of-range integer, or a value outside its field's format or range; also a malformed `releaseId` assertion or expected revision |
| `unsupported` | A field the schema does not have (including a case variant next to the real key, a grant in a release or a non-empty `bindings`), `schemaVersion` other than 1, and a well-formed value outside this phase's profile: a `sha512:` digest, another media type or platform, a `runtime.kind` other than `web`, an environment other than `staging` or `prod`, an exposure other than `private` |
| `identity_mismatch` | A `releaseId` assertion that differs from the computed release ID |
| `wrong_application` | A candidate release for another application |
| `stale_instance` | An expected revision that is not the current instance's |

When an input breaks several rules, the first in this order decides, so the same input always gets the same refusal: the document itself; then object by object, the top level first: missing required fields (in schema order), unsupported fields, nulls (each in key order); then types and integer ranges; then each record's fields in the order of its field table; then the identity assertion; then selection's contractual order above. An unsupported field is reported at its parent object (`unsupported: runtime: unsupported field`), because the key is input and is not echoed; a type or range error names the record (`release` or `instance`).

### Acceptance tests

[`kernel_test.go`](../v2/kernel/kernel_test.go) covers record round-trips, strict parsing, golden identities/normalisation, per-field identity changes, ID/content mismatch, environment separation, stale/no-op ordering, refusal input preservation and value-only records. Refusal precedence is tested on inputs that break several rules at once, each decoded 50 times, and selection's order on requests that break two of its checks. [`main_test.go`](../v2/cmd/swhurl-v2/main_test.go) covers CLI boundaries and a real built binary with empty PATH and no credential/config environment. It checks success, no-op, stale/wrong-app/unsupported refusals by their code, I/O errors, safe diagnostics, empty stdout on refusal and unchanged input files. [`coexistence_test.go`](../v2/coexistence_test.go) guards the current v1 discovery and console-image boundaries.

## 3. Coexistence boundaries

The approved Go module is [`v2/`](../v2/go.mod), module path `github.com/samclement/swhurl-platform/v2`. Fixtures stay in `v2/testdata/`; compiled output goes to temporary directories. The narrow exception in `AGENTS.md` and [contributing](contributing.md#operator-tooling) covers this offline module, Go tests and direct CLI invocation only; external-effect adapters need separate approval.

Root-level placement matters: the console Dockerfile copies `tools/`, and its content hash includes the entire `tools` tree. Putting Go under `tools/` would include it in v1 console image inputs. `v2/` is outside those inputs, the Docker build allowlist, active Flux paths and v1's `apps/*/*` record discovery. v1's Python package is unchanged; the Makefile, command catalogue and Validate workflow gain only `check-v2`. CI runs it in Validate's tooling job; `make check` does **not**, so run both locally.

- v1 remains the sole live authority until an explicit cutover.
- Do not reference v2 fixtures or scratch output from Flux.
- Avoid importing v1 orchestration into the v2 domain.
- Reuse existing infrastructure where language boundaries permit, but do not call Python merely for reuse. A Go implementation keeps the guarantees of v1's `Runner`/`FakeRunner` (an unexpected fake call fails, dry runs change nothing, Secret values are never printed, a failed read is reported as a failure) on typed adapters, not on a command runner ([development layers](#4-development-layers-and-adapters)). Phase 1 needs no adapter.
- Use an explicit read-only adapter to translate v1 records.
- Do not build bidirectional synchronisation or maintain two authoritative specifications for an instance.
- Never let v1 and v2 reconcile the same Kubernetes resource.

Current working-tree changes must be inspected again before implementation; this plan does not authorise overwriting or incorporating unrelated work.

## 4. Development layers and adapters

**Approved 24 October 2026 as the working method for v2.** Phases 2–7 still need their own approval; this section says how to build them, not that they are approved.

v2 has two layers, so that most work iterates offline in seconds:

```text
pure model    inputs → decision, plan, resources or refusal      (no effects)
adapters      read or change Git, GitHub, Kubernetes, ...        (effects, behind narrow interfaces)
```

### The pure model

A pure function's output depends only on its arguments: no network, filesystem, environment variables, current clock, randomness or subprocesses. Time, identifiers and snapshots are passed in. The pure layer holds more than the records:

| Responsibility | Example |
| --- | --- |
| Validation | Is this release compatible with this instance? |
| Change planning | Select R42 for prod, keeping prod's configuration and grants |
| Authorisation decisions | Does this actor and policy permit these effects? |
| Relationship resolution | Which calls and subscriptions are connected, pending or refused? |
| Allocation decisions | Choose an unused port from the supplied allocation snapshot |
| Resource compilation | Generate an instance's resources and its shared contributions |
| Change comparison | Which resources are created, updated or removed? |
| Evidence projection | Does this observation show the intended release running? |
| Process transitions | Given this operation and this result, what happens next? |
| Notification decisions | Given observations, previous incident state and an explicit time, send a notice? |

Generating Kubernetes resources is pure; applying them is not. Deciding which credentials are needed is pure; generating and encrypting them is not. v1's [`allocate`](../tools/swhurl/apps/model/write.py) mixes allocation decisions, filesystem checks, key generation and encryption in one step; in v2 split it into (1) a pure calculation of the allocations and material required, (2) adapters that return keys and encrypted material, (3) a pure validation of what came back and compilation of the completed plan. Preparation failures are then testable without generating credentials or writing files. This is a v2 design rule, not a v1 change.

### Adapters

An adapter owns one external dependency and speaks domain terms ("read workload evidence for this target"), not command lines. Add an adapter only when a slice needs it: the table is an inventory, not a build list.

| Dependency | Owns | Offline replacement | Still needs real verification |
| --- | --- | --- | --- |
| Git and files | Read snapshots, write, commit, push, resolve revisions | Frozen snapshots; temporary local repositories | Concurrent pushes, stale bases, publication recovery |
| GitHub | Pull requests, approvals, checks, repository creation, archives | Recorded reads; behavioural fake for changes | Authentication, permissions, ambiguous failures |
| Builds and registry | Build evidence, image metadata, digests | Sample descriptors, build results, image metadata | Real publication, provenance, runnable images |
| Kubernetes and Flux | Observe resources; request reconcile, suspend, other changes | Recorded API documents; scripted observation sequences | Controller behaviour, RBAC, eventual convergence |
| Credentials and encryption | Generate keys, read secrets, encrypt and decrypt | Synthetic keys, deterministic outputs | Real SOPS round trips and key compatibility |
| NATS and live flags | Read and write live values, connect, authenticate | In-memory store with revisions and injected failures | Access controls, concurrent updates |
| Operation and notification state | Store progress and incident state | In-memory store with conflicts, restarts, failures | Durability, concurrent writers |
| ClickStack | Dashboards, alerts, webhooks, queries | Response fixtures, fake inventory | Service-specific update and authentication |
| Notification delivery | Send to ntfy or elsewhere | Captured messages; rejection and timeout | Real transport |
| Backups and storage | Object storage, snapshots, volumes, restore | Backup manifests, object listings, fault scenarios | Restorable data, incomplete uploads |
| Identity | Authenticate a request, establish who is acting | Explicit test principals | Proxy header trust, sign-in integration |
| Clock, randomness, environment | Time, sleep, identifiers, configuration | Explicit timestamps, deterministic generators, supplied config | Selected adapter checks |

### Kubernetes: the API server, not `kubectl`

**Decided:** v2 reads and changes Kubernetes and Flux objects through the API server. It does not start `kubectl` or `flux` processes. v1 measured the reason: one API request from the console takes 4 to 16 ms where starting `kubectl` takes 50 to 120 ms, and each `kubectl` process uses about 56 MB, which is why v1 runs at most three reads at once ([v1 tooling](contributing.md#operator-tooling)). v1 only translates the console's reads; v2 has no `kubectl` path at all.

- Credentials: the pod's service account inside the cluster, a kubeconfig outside it (`$HOME/.kube/config`, as for v1's scripts). The CLI and a server get separate credentials (section 1).
- Flux actions are object changes, as the [v1 console already does them](console.md): reconcile sets `reconcile.fluxcd.io/requestedAt` on the source and the unit; suspend and resume set `spec.suspend`.
- Each kind's API group and version is stated, not discovered, and checked against the manifests in Git (v1's `kube.RESOURCES` does this).
- A failed request (forbidden, unavailable, timeout) is unavailable evidence; only a successful `404` means absent.
- Raw fixtures are API JSON. That is the shape `kubectl get -o json` prints, so recorded v1 outputs can be reused.

**Recommended, not yet decided (decide in phase 5):** a thin `net/http` client for the few kinds v2 uses, keeping the module free of third-party dependencies. It must handle the k3s kubeconfig (client certificate and CA) and the in-cluster token. The alternative, `client-go`, is complete but adds a large dependency tree and binary size.

### Fixtures and fakes

Test at two boundaries and keep both:

| Fixture | Tests | Example |
| --- | --- | --- |
| Raw response | An adapter interprets the dependency correctly | API JSON for a Deployment → workload observation |
| Domain scenario | Decisions, independent of external formats | Snapshot + request + observations → plan or refusal |

Testing only domain scenarios misses adapter mistakes; testing everything through raw responses makes every domain change expensive.

A domain scenario states everything the decision depends on. For a promotion: the pinned starting snapshot, the requested release and target instance, actor and policy, observations (each with source, revision and observation time), an explicit evaluation time, and the expected change and effects, or the expected refusal. When sanitising real data, keep revisions, digests and observations consistent with each other, or the fixture describes a situation that could not happen.

Recorded responses suit reads. Changes need small **behavioural fakes** that implement only what the adapter contract depends on, never a simulated Kubernetes or GitHub. Write these as explicit scenarios whether or not v1 ever hit them:

- A create succeeds remotely, the response is lost, the process restarts, and the retry finds the existing object, checks it is ours and carries on.
- Git advances between planning and publication.
- An approval refers to an older version of the proposed change.
- Flux reports an older target after a newer one is published.
- A cluster read fails; it must not count as absence.
- Storing progress fails after an external effect succeeded.
- Events arrive twice or out of order.

### Reusing v1

v1 is evidence of required behaviour, not an expected-output generator. Classify each behaviour as **preserve**, **deliberately change** or **remove**, using the [architecture review](architecture-review.md) and [model review](model-review.html). Starting points:

- [`tests/fixtures/instance`](../tests/fixtures/instance): a frozen staging/production example with records and rendered resources.
- [`tests/fakecluster.py`](../tests/fakecluster.py): healthy-cluster responses across Flux, apps, observability, backups and notifications.
- [`tests/test_console_stages.py`](../tests/test_console_stages.py): earlier targets, current intent and live observations combined.
- [`tests/test_build_image.py`](../tests/test_build_image.py): build evidence and sample image archives.
- [`tests/test_app_model.py`](../tests/test_app_model.py): deterministic rendering and relationship rules.

Copy scenarios and data, not the `FakeRunner` command matching: v2 tests name operations, not command strings. Some v1 fixtures read the current checkout or clock; freeze those values when exporting.

### Test tiers

| When | What runs | External processes |
| --- | --- | --- |
| Every edit | Pure model tests | None |
| Each completed change | Domain scenarios through fake adapters, including failures and restarts | None |
| Adapter changes | Contract tests on raw fixtures; cheap real components such as a temporary Git repository | Local only |
| Before release | A small live suite: publication, reconciliation, permissions, recovery | Live, with the usual confirmations |

The first delivery scenario beyond phase 1 is "promote an existing release to an existing production instance": it exercises the central model without repository creation or first provisioning. First production, removal and recovery follow. This is not implemented by phase 1's release selection; a visual preview must label the additional steps as proposed.

### Code-derived contract views

The [visual design/change tool](visual-change-tool.md) is an **independent workstream**, using v1 code and Git history as its proving ground, with component/interface graphs, commit comparisons and user-journey overlays. It is not a v2 supporting milestone, compiler gate or part of the Go kernel. Its brief and handoff live only in that document.

v2 continues through the phase-1 review and separately approved delivery phases below without waiting for the visual tool. Future use of the tool on v2 is optional, not a dependency. The prior Go-only release-selection viewer recommendation is superseded by the separate v1-based investigation; no viewer implementation or placement under `v2/` is approved.

## 5. Incremental delivery

Each phase ends in something usable and independently checkable. Only phase 1's bounded offline slice is implemented; the remaining phases require their own decisions and approval. The independent [visual-tool workstream](visual-change-tool.md) neither blocks nor authorises these phases.

| Phase | Build | Completion gate |
| --- | --- | --- |
| 1. Release-selection kernel | The two records, release identity and pure selection operation | Offline contract tests pass; no external access |
| 2. Deterministic compiler | Compile one private stateless HTTP instance using one pinned platform profile | Identical inputs produce identical output; duplicate resource ownership fails |
| 3. Read-only v1 bridge | Translate a pinned v1 snapshot into v2 inputs; compile into scratch output | Differences explained; unsupported features reported; no writes to v1 |
| 4. Git delivery | Local plan/diff, validation and narrow proposal/publication through existing command infrastructure | Failed preparation publishes nothing; concurrent changes cannot overwrite each other |
| 5. Current-state observation | Read Git, Flux and workload evidence for an identified target | Unavailable differs from absent; publication differs from running |
| 6. First isolated live instance | Deploy a new disposable stateless instance owned only by v2 | Digest, readiness and private access boundary verified; removal demonstrated |
| 7. Selective replacement | Add capabilities needed by the next chosen app; migrate one ownership boundary at a time | Each migration has parity/safety evidence and a defined recovery path |

### Phase 1: prove the semantic boundary

Approved implementation: the [phase-1 contract](#2-phase-1-model-and-contract), tests and [offline CLI demonstration](#offline-cli-and-build). No compiler, bridge, publication, console, GitHub workflow or cluster deployment. The module has no third-party dependencies and builds with C disabled. Go 1.26.0 is the installed, pinned experimental toolchain; its status as the latest security patch has not been verified. Revisit the patch pin deliberately before distributing or deploying anything.

Judge model clarity and ease of change as well as binary characteristics before expanding Go. Measurements have no invented performance target and establish no v1 speedup.

### Phase 2: establish compiler ownership

Use a small contract:

```text
validated instance + resolved release + platform profile
    → resources + ownership inventory + compilation identity
```

No network, clock, randomness, credential generation or live reads inside compilation.

Use the existing pinned deployment primitive where practical. Rewriting the application chart is a separate decision, not a prerequisite.

The initial compiler must preserve the promised private access/isolation boundary, not merely emit a Deployment that starts. Define the generated resource set and its owners explicitly, and validate it with focused tests and appropriate manifest checks.

### Phase 3: shadow, not synchronise

The bridge is one-way and read-only:

```text
Pinned v1 records → explicit translation → v2 model → scratch output
```

Read all inputs from one identified snapshot. Do not substitute moving `main` after a failed read.

Compare semantic output for the supported profile. v1 output is evidence, not automatically the specification: some differences will be intentional simplifications. Unsupported features must be visible rather than dropped during translation.

### Phase 4: settle publication before automation

Recommendation: initially publish one accepted commit containing validated intent and its compiled output, rather than immediately introducing an asynchronous Render bot.

This trades larger diffs for fewer intermediate states. It is a deliberate departure from v1's records-only PR contract and needs approval. Design validation, ownership and coexistence with the existing Render workflow before connecting a v2 writer.

Concurrency has two distinct checks:

- **Instance revision:** protects the operator's reviewed target from silent replacement.
- **Git head:** protects atomic publication from other writers.

An unrelated Git change can trigger revalidation. A changed same-instance target must not be silently overwritten. Changed release selection requires renewed review. Publication uses non-force/CAS semantics; preparation or validation failure publishes no intent.

Keep the actor/approval policy explicit. A trusted-operator profile is valid; a general multi-team authorisation system is not required for this milestone. Promotion evidence policy must be agreed before exposing a production promotion command.

### Phase 5: explain current state honestly

Observe an identified desired target and report source and observation time.

- A successful Git publication is not a running workload.
- Flux reconciliation and workload readiness are distinct facts.
- Failed reads are unavailable, not absent or successful.
- A workload must match the intended artifact, not merely report Ready.
- Current state does not establish that an earlier target ever ran.

Do not reconstruct historical Activity as part of this phase.

### Phase 6: first isolated live instance

Use a new approved disposable instance, not an existing stateful app. Retain the repository's naming and live-test safety conventions. Do not create a public app repository/package merely to test the model; if one is needed, obtain the required confirmation.

Prove publication, reconciliation, intended artifact, readiness and access/isolation from a separate workload. Check that v1 instances and their owners remain unchanged. Exercise failure reporting and approved removal without deleting unrelated resources or data.

Apply the new-component checklist before adding anything that runs, stores data or holds a credential. Any root Flux configuration changes require separate confirmation.

### Phase 7: selective replacement

Let the next migration supply the capability requirement:

1. Add storage and secrets before migrating an app that needs them.
2. Add relationship resolution and shared compilation before migrating connected apps.
3. Add selected console commands only after the domain and delivery contracts work through Git/CLI.
4. Retire a v1 path only once its replacement owns the relevant inputs, output and operations.

Existing-instance migration follows the [documented staged Flux ownership handover](architecture.md#flux-units). Do not change the old unit's path in the cutover commit or permit it to prune handed-over resources.

Stateful migration additionally requires demonstrated backup/restore, explicit resource-retention rules and operator approval. It must not be the first migration exercise. Define recovery per migration; do not imply that rolling back an image reverses data migrations.

## 6. Deliberately deferred

Do not make feature parity the first goal. Initially defer:

- Console repository creation and seed images.
- Automatic release discovery and auto-merge.
- Historical Activity reconstruction.
- Live feature flags.
- Events, calls and egress integrations.
- Multiple stacks and app types.
- Generic plugins, workflow engines and provider abstractions.

This does not approve removing those features from v1. It keeps them out of the replacement's critical path while the core contract is proved.

## 7. Verification and execution gates

- Tie each model/compiler contract to focused offline tests.
- Existing Python adapters use `Runner`/`FakeRunner`. Any approved Go adapter must have equivalent focused tests for failed calls, dry-run behaviour and secret handling; do not bypass those guarantees. Run the [test tiers](#test-tiers) that match the change.
- Run `make check` before every implementation commit, plus the additional checks required by [contributing](contributing.md) and `AGENTS.md` for the files changed.
- Confirm integration with existing validation before claiming v2 is safely ignored or checked by it.
- Do not claim live readiness from offline tests. Live phases need observed evidence recorded in [current state](current-state.md).
- Confirm hard-to-reverse actions separately: data deletion, real credential rotation, backup destination changes and changes under `flux-system`.
- Once execution is approved, keep phase status and remaining decisions here and link from `docs/plan.md` section 0. Do not create another parallel status artifact.

### Guardrails

v2's design principles are checked, not only described. The rules are code in [`v2/archtest`](../v2/archtest): test files using the standard library only, which parse v2's source and never build or run it, so every `go test ./...` checks them. This table lists the same rules; a test fails when the two differ.

| Rule | What it enforces |
| --- | --- |
| `stdlib-only` | v2 imports only the standard library and its own packages; `go.mod` requires nothing ([why](#kubernetes-the-api-server-not-kubectl)). A dependency needs the operator's approval first. |
| `layers` | Every v2 package has a declared layer (0 pure model, 1 adapters, 2 commands) and imports only v2 packages in lower layers ([why](#4-development-layers-and-adapters)). A new package is given its layer in `Layers`. |
| `pure` | Pure packages (`kernel`) import only the allowlisted standard packages, print nothing and assign no package variable ([why](#the-pure-model)). Time, identifiers and snapshots are arguments. |
| `no-subprocess` | v2 code (not its tests) starts no process and names no `kubectl` or `flux` command ([why](#kubernetes-the-api-server-not-kubectl)). |
| `public-api` | The exported API of every library package matches `archtest/api.txt`, so an interface change is a deliberate, reviewed diff. |
| `budgets` | No function exceeds complexity 10 or 60 lines unless `archtest/budgets.json` lists it with a reason; a listed function may shrink but not grow, and its entry goes once it fits. |
| `places` | A package's place matches its layer: layer 0 is listed in `Pure`, layer 1 is under `adapters/`, layer 2 is a `main` package under `cmd/` ([why](#extension-recipes)). |
| `adapter-complete` | Each adapter `adapters/<name>` follows the [new-adapter recipe](#extension-recipes): it exports an interface, has raw fixtures in `testdata/`, and its fake in `adapters/<name>/<name>test` satisfies each interface and shares one `Contract` suite with the real adapter; only tests import a fake ([why](#fixtures-and-fakes)). |
| `rules-documented` | This table lists exactly the rules in code, each rule's principle link resolves, and `AGENTS.md` names `make check-v2`. |

**Run** `make check-v2` from the repository root: `gofmt`, `go vet` and `go test` (with the rules) on the pinned toolchain with network access off, in about two seconds. CI runs it in Validate's tooling job, with Go set up from the version in `v2/go.mod`; it is not part of `make check`. A failure prints one line per violation (rule, file and line, what is wrong), then each broken rule's fix and the section explaining why.

**In the agent loop:** the project's pi extension [`.pi/extensions/v2-guardrails.ts`](../.pi/extensions/v2-guardrails.ts) runs `make check-v2` after every edit or write under `v2/` and attaches any failure to that tool result, so the agent sees it straight away. Before a run ends, if anything under `v2/` changed during it and the check fails, the extension hands the failures back and asks for another turn, at most twice per run, then tells the operator the check still fails. When the check passes and `v2/` changed, it shows the operator the [change report](#change-report)'s summary as a message, without another turn. Pi loads project extensions once the project is trusted (`/trust`).

**Changing a baseline:** a rule is satisfied by fixing the code; a baseline changes only when the change is intended, and the diff shows it.

- Exported API: `go test ./archtest -run TestPublicAPI -update` rewrites `api.txt`. Say why the interface changed.
- Budgets: `go test ./archtest -run TestBudgets -update` only lowers or removes entries. Adding one is a hand edit with a reason. None is listed: the two phase-1 entries (the CLI's `run` and `ReleaseBundle.validate`) went when those functions were split on 25 October 2026.
- Layers and pure imports: `Layers`, `Pure` and `PureImports` in `archtest/rules_test.go`.
- There is no comment that silences a rule; every exception is an entry in these files.

**Verification (24 October 2026, working tree before commit):** each rule has a test on a small module written to a temporary directory, showing what it catches and what it leaves alone (a parameter that shadows a package variable, a test that starts a process); breaking a check on purpose makes those tests fail. On v2 the code rules pass; two phase-1 functions are over budget and listed. End to end, three runs of pi (Haiku 5.5, a throwaway copy of the repository, the extension loaded with `-e`): an agent asked to add a counter to the kernel got the `pure` failure on its edit, reverted it, re-ran the check and offered compliant alternatives without touching a baseline; a change made through the shell, which the edit hook does not see, was caught when the agent tried to finish, handed back once, and fixed; an agent told never to fix it got two attempts and then a message for the operator, after which the run ended. Not exercised: interactive mode after `/trust`.

**CI (24 October 2026):** Validate's tooling job sets up Go with `actions/setup-go@v7` from `v2/go.mod` (no cache: there is no `go.sum`) and runs `make check-v2` after the code graph tests; a failure there holds the console deploy like any other check. First run on the push of `be041cf4` ([current state](current-state.md#v2-and-code-graph-push-24-october-2026)): `go1.26.0 linux/amd64`, all four packages passed; Go setup took 7 s and the check 11 s. `actionlint` was not run (not installed here).

#### Change report

`make v2-report` says what a change does to v2, for the person reviewing it and for agents. It is a report, not a gate: `make check-v2` decides pass or fail. For example, a change that adds an adapter and lets the kernel read the clock prints:

```text
v2 change: origin/main@be041cf4 → working tree
review:
  ! new package adapters/git (layer 1)
  ! Layers + adapters/git 1
  ! PureImports + time
  ! exported API: +1 -0 (api.txt changed)
files: 2 added, 3 changed
packages: + adapters/git (layer 1)
imports: adapters/git + io/fs, adapters/git + os, kernel + time
api: 1 added
budgets (complexity/lines): + adapters/git.Read 4/31
```

- **What it compares:** `v2/` in the working tree (as on disk, untracked files included), or at `AFTER=<rev>`, against `v2/` at the merge base of `BASE` (default `origin/main`) and that revision. So it covers unpushed commits and uncommitted edits, and later commits on `main` do not show as reversals. A revision without `v2/` is absent (`present: false`) and everything is added.
- **Facts,** each read the same way from each tree's own source and baselines, never by building or running it: files added, changed or removed (by SHA-256, with their kind: module, baseline, rules, test, testdata, source or other); packages added or removed, with their declared layer and whether they are pure or tests only; imports per package (standard, local or external, test-only ones marked); exported API lines, computed from the code, and whether `api.txt` changed; function sizes and `budgets.json` entries and limits; and the rule settings (`Layers`, `Pure`, `PureImports`, rule IDs), read as literals from `archtest`.
- **Review** lists what the change must explain: v2 or the design rules being new, a changed `go.mod`, a new package and its layer, an external import, any changed rule setting, changed budget limits, a `budgets.json` entry added or raised, and a changed exported API. Lowering an entry is the ratchet and is not listed.
- **Output:** the text above, at most five items per line; `FORMAT=json` prints the document (`"kind": "v2-change-report", "version": 1`), with sorted lists that are never `null`, so identical trees give identical bytes. A change to its shape raises `version`.
- **Errors:** it reports whenever it can read both trees, whatever it finds. A missing merge base, Go that does not parse, rule settings that are not plain literals, an invalid `budgets.json` or files without `go.mod` stop it (the script exits 1), as does a wrong `FORMAT` or `AFTER` (2); through `make` either fails the target.
- **Boundaries:** [`v2/archtest/report.sh`](../v2/archtest/report.sh) resolves the revisions and extracts them with `git archive` into a temporary directory (never through the index or working tree), then runs `archtest`'s `TestChangeReport`, which compares the two directories offline. Without `-base` that test skips, so `go test ./...` is unchanged. It is a test so that it reuses `archtest`'s loader and measures without moving them into a package the rules govern.

**Verification (24 October 2026, before commit):** nine tests on small trees in temporary directories cover identical trees (an empty report with no `null`), a new package with local, standard and external imports and API, an updated baseline with removed code, undeclared and tests-only packages, budget entries added, raised and lowered with changed limits, changed rule settings, a base without v2 (one review line for the new rules; every setting still in the document), the five-item limit with byte-identical documents, and six inputs it refuses. Eight deliberate breaks to the comparison (reversed import changes, external imports not reviewed, rules never new, the test-only flag inverted, raised entries not reviewed, absent trees accepted without `go.mod`, changed files dropped, no item limit) each failed at least one test. Through `make`: the working tree against `origin/main`; `BASE=HEAD AFTER=HEAD` (no changes); phase 1 to guardrails (`2641c018` → `c63fe75e`: the rules new and both budget entries for review); before v2 to phase 1 (`5c85a793` → `2641c018`: v2 new, two packages without layers); `FORMAT=json`; and a wrong `FORMAT`, `AFTER` and `BASE` each failing with a message. One pi run (`claude-bridge/claude-haiku-5-5`, a throwaway worktree, the extension loaded with `-e`), asked to add a comment line in the kernel: the edit check passed and, at the end, the report appeared once as a message, with no further agent turn.

#### Extension recipes

What each kind of change touches, so a change can be checked against it rather than remembered. The rules check what holds in every tree; the [change report](#change-report) shows what one change added, for the parts no rule can see.

| Change | Touches | Checked by |
| --- | --- | --- |
| New pure-model package | Declared at layer 0 in `Layers` and listed in `Pure` | `places`, `pure` |
| New adapter | `adapters/<name>/` at layer 1, as below | `places`, `adapter-complete`; the wiring line by review |
| New command | `cmd/<name>/` as `package main` at layer 2, declared in `Layers`; wiring only | `places`, `layers` |
| New exported function or type | `archtest/api.txt`, with the reason in the change | `public-api` |
| New dependency | The operator's approval first | `stdlib-only` |

A new adapter, for example one that reads Git:

```text
adapters/git/git.go                 package git: the interface it offers (Reader) and the real adapter
adapters/git/testdata/              raw responses the real adapter's contract test reads
adapters/git/git_test.go            package git_test: gittest.Contract(t, <the real adapter on raw fixtures>)
adapters/git/gittest/fake.go        package gittest: Fake; var _ git.Reader = (*Fake)(nil); func Contract(t *testing.T, r git.Reader)
adapters/git/gittest/fake_test.go   Contract(t, &Fake{...})
archtest/rules_test.go              Layers: "adapters/git": 1, "adapters/git/gittest": 1
cmd/swhurl-v2/main.go               one wiring line
```

- **The interface** speaks domain terms ([adapters](#adapters)). Its users, commands and their tests, depend on it, not on the real adapter.
- **`var _ git.Reader = (*Fake)(nil)`** makes the compiler prove the fake satisfies the interface; `adapter-complete` only checks the line is there, since the rules read source and never build it.
- **`Contract`** is one behaviour suite, failures included ([fixtures and fakes](#fixtures-and-fakes)), run against the real adapter on raw fixtures (or a cheap real component such as a temporary Git repository) and against the fake, so the fake cannot drift from what the real adapter does. The real adapter's test is an external test package (`git_test`) because the fake imports the adapter.
- **Fakes are for tests:** only test files (and other fakes) import a `<name>test` package, so no fake reaches the binary.

**Verification (25 October 2026, before commit):** on v2 both rules pass (there is no adapter yet, so `adapter-complete` checks nothing until the first). On fixtures, `places` reports eight misplaced packages (layer 0 not pure, pure at layer 1, layer 1 outside `adapters/`, `adapters/` at layer 2, layer 2 not `main` or outside `cmd/`, `cmd/` at layer 0, an unknown layer) and leaves correctly placed, undeclared and test-only packages alone. A complete adapter passes `adapter-complete`, including an aliased import of the fake in a test; each of ten broken versions is reported (no interface, no raw fixtures, no fake, no assertion, an assertion against another package's interface, a `Contract` without `*testing.T`, the real adapter not run through `Contract` or run through another package's, the fake not run through it, a command importing the fake). The complete fixture also builds and passes `go vet` and `go test` as a real module, and removing the fake's method fails compilation at the assertion. Eight deliberate breaks to the two checks each failed a test.

The first real adapter needs phase-2 approval; it is checked by `adapter-complete` from the commit that adds it.

### Offline CLI and build

Prerequisite: Go **1.26.0** already installed. `make check-v2` runs the format, vet and test steps below, [guardrails](#guardrails) included. The following disables automatic dependency access and C dependencies. There is no `go.sum`: the module has no third-party dependencies. CI runs them through `make check-v2`; `make check` does **not** include them.

**Run with `!`**, from the repository root:

```bash
cd v2
export GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0
test -z "$(gofmt -l .)"
go test -count=1 ./...
go vet ./...
go build -trimpath -buildvcs=false -o /tmp/swhurl-v2 ./cmd/swhurl-v2
```

Build twice with the same toolchain, source and flags and compare hashes for a local reproducibility check. `-trimpath` removes checkout paths; `-buildvcs=false` excludes the unrelated dirty checkout/Git revision. The default binary retains debugging information. No claim of reproducibility across different Go patch versions is made.

**Test it yourself — run with `!`**, from the repository root after building:

```bash
/tmp/swhurl-v2 release-id v2/testdata/r42.json
revision=$(/tmp/swhurl-v2 instance-revision v2/testdata/prod.json)
selected=$(mktemp /tmp/swhurl-v2-selected.XXXXXX.json)
/tmp/swhurl-v2 select-release \
  --instance v2/testdata/prod.json --release v2/testdata/r42.json \
  --expect "$revision" > "$selected"
diff -u v2/testdata/prod.json "$selected"
```

The diff changes only `release`; `diff` exits `1` to report that expected difference. Staging remains R42 with two replicas/128 MiB; production selects R42 with one replica/256 MiB. Both retain private grants and empty bindings. The built-binary test proves that no input file changed. Do not redirect stdout over an input filename: the shell would truncate it before the CLI starts.

The three commands are `release-id FILE`, `instance-revision FILE`, and `select-release --instance FILE --release FILE --expect REVISION`. Only explicitly named files are read; there is no root discovery, registry, external command, credential/config lookup or file-write command. Valid results go to stdout, diagnostics to stderr. Exit `0` means success/no-op, `2` means usage/validation/refusal (including an input file larger than 1 MiB, which is not read past that), and `1` means file/output I/O failure. A flag given twice is a usage error, not the last one winning; flags may also be written `-instance=FILE`, and `--` ends them; `--help` or `-h` prints the usage to stdout and exits `0`. Refusal stdout is empty. If stdout is a pipe whose reader has gone, the process ends by `SIGPIPE`, as Unix tools do, rather than exiting `1` (catching it would need `syscall`, which the `no-subprocess` rule keeps out of v2). Output failures cannot retract bytes already accepted by the destination; they must return nonzero rather than claim success.

To repeat selection, calculate `instance-revision "$selected"` and select R42 against that revision: output is identical. Selecting it against the **old** revision exits `2` with `stale_instance` and no stdout. Wrong-app and unsupported-candidate cases are exercised by `go test` using temporary synthetic inputs, with all files compared afterwards.

Also run `make check` from the repository root and `for f in host/*.sh tests/fixtures/*.sh; do bash -n "$f"; done`. No template pin, app type or `app-new` change belongs to this slice, so `make check-templates` is not required. Do not run credential or live checks for this offline phase.

### Phase-1 verification evidence

Observed 23 October 2026, on the working tree before commit (nothing published):

- Initial and final v1 baselines: 13 focused record/promotion tests passed using `PYTHONPATH=tools UV_OFFLINE=1 uv run --frozen --offline python -m unittest` with `RecordTests`, `PromotionTests` and the five selected promotion/refusal edit tests. The pre-edit tests establish the v1 contracts being retained; Go tests capture the new independent identities/selection boundary.
- `GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go test -count=1 -cover ./...` passed all three packages (13 top-level tests, with table-driven subtests). Statement coverage: kernel 93.4%, CLI 95.8%. `go vet ./...` and `test -z "$(gofmt -l .)"` passed.
- `make check` passed: **983 v1 tests**, lint, 20 app-policy instances, 38 active schema/render entrypoints, OTel fixtures and relative doc links. Existing OTel deprecated component-name warnings (`k8sobjects`, `hostmetrics`, `kubeletstats`) and Starlette/httpx test-client deprecation warnings remain outside this slice. Refusal/error messages printed by negative fixture tests are expected test output, not live operations.
- `for f in host/*.sh tests/fixtures/*.sh; do bash -n "$f"; done` passed. `make check-templates` was not run: this slice changes none of its triggering inputs. No live or credential checks ran; `make check` validates encrypted-file structure, not decrypted credentials.
- The documented real-binary demonstration produced a one-line `release` diff. Selecting again with the resulting revision was byte-identical; selecting with the original revision exited `2` with empty stdout. The binary-level tests also exercised wrong-app and unsupported requests and checked unchanged files.
- Two builds using the documented flags were byte-identical (`cmp` and SHA-256): `520b747f6230c4224dca16054df47aca93c3df33f7e2faf4f7c478e83a0092c8`. Size **3,854,556 bytes** (about 3.68 MiB), with default debug information retained. `file` identifies a static x86-64 ELF; `go version -m` confirms Go 1.26.0, `CGO_ENABLED=0`, `linux/amd64`, `GOAMD64=v1`. `go list -deps` lists only standard-library and the two local packages.
- Measurement platform: Linux `7.0.14-arch1-1`, x86-64, glibc 2.43; Go 1.26.0. Command: built binary `release-id` over the synthetic R42 fixture, PATH empty, stdout/stderr to `/dev/null`. After 10 warm-up processes, 100 fresh processes had wall-time min/median/max **3.270 / 5.243 / 135.467 ms**, and per-child Linux `wait4` peak-RSS min/median/max **3,240 / 3,428 / 6,748 KiB**. Timing spans fork/exec through wait completion: it includes startup, parsing, hashing and output, excludes compilation, and uses warm filesystem caches. A temporary small native launcher used `CLOCK_MONOTONIC` and `wait4` under the machine lease; it is a measurement aid, **not** a Go build/runtime dependency. The first Python-launcher RSS reading was discarded after `/usr/bin/true` showed the same inherited Python RSS floor. Host scheduling/load still affects results; no v1 comparison or speedup claim is established.
- Unrelated pre-existing dirty files were compared to pre-edit SHA-256 hashes and remain byte-identical. The pre-existing `AGENTS.md` introduction is preserved; only the approved exception was added there. v1 code, tests, fixture contents, delivery wiring and live state were not changed by this phase.
- **Independent review completed 24 October 2026** (reviewer agent, read-only, on `2641c018`): **OK with notes, no blockers.** Two should-fix findings: `fields` checks keys in Go's random map order, so an object with both an unknown key and a `null` gets either refusal from run to run (the reviewer measured 178 to 22 over 200 decodes; confirmed by reading `kernel/json.go`), against the plan's stable codes; and the contractual order of checks is untested (swapping the wrong-application and stale checks still passes), with strict-decoding and built-binary refusal tests that do not assert the code. Nits: some diagnostics name only the parent field; a repeated flag silently wins; `instance-revision ""` names the release file; a broken pipe ends the process by signal 13, not exit 1; inputs are read without a size limit; mixed-case repositories give distinct release IDs; the coexistence check would miss some spellings of a reference to `v2/`; and the code mapping, `-0`, `--help` and other accepted inputs are not stated in section 2. The reviewer confirmed the field table, strict reading, identities (recomputed independently), selection order and CLI exit codes, and could not verify system-call-level file access, reproducible builds or very large inputs. **No fix is made until the operator decides.** Two earlier review attempts had failed before producing analysis (`lost ×5`).
- **Review fixes (25 October 2026, operator-approved):** both should-fix findings and four nits. `fields` checks unsupported fields before nulls, each in key order, and section 2 now states the [refusal codes and their precedence](#refusal-codes); strict-decoding rows and the built binary's refusals assert their code; new tests cover refusal precedence (each input decoded 50 times) and selection's check order. The CLI refuses a repeated flag, reads at most 1 MiB of a named file (refused with exit `2`), switches on the command so `instance-revision ""` names the instance file, and documents the `SIGPIPE` exit. `run` and `ReleaseBundle.validate` were split along the same lines (per command; source, artifact, runtime), so their budget entries are gone; `ReleaseBundle.validate` now checks `runtime.healthPath` before `runtime.uid`, the field table's order, and the exported API is unchanged. Mixed-case repositories are stated, not changed. Left as they are: unsupported fields reported at their parent (stated in section 2) and the coexistence check's blind spots (a Flux `path:` without `./` or quoted, a kustomize `resources:` entry reaching `v2/`, `!/v2` in `.dockerignore`). Reintroducing each problem in a temporary copy (stale checked before application, map order, nulls before unsupported fields, a repeated flag accepted, no size limit, the uid check removed) failed at least one test. `make check-v2` and `make check` passed.

Component checklist: no deployed process, persistent store, default credential, Secret or runtime config consumer is introduced. Retention/backups, ClickStack collection, `verify-platform` health and restart wiring are not applicable. CLI exit/output failures are tested; the toolchain is pinned; operator steps are above. This offline result establishes no live readiness or supported non-empty bindings.

## 8. Fresh-context handoff

### State and authority

**Approved:** the operator said yes to the concrete phase-1 proposal: root-level `v2/`, Go 1.26.0, JSON-only descriptors, positional canonical encoding, manifest-only `linux/amd64` artifact claims, empty-only bindings and the narrow policy exception. Keep v1 unchanged; no platform console UI, general framework, compiler, bridge, credential access, integrations, publication, live changes or feature removals. The operator separated the [visual change-tool workstream](visual-change-tool.md) from v2; it uses v1 code/history and has its own scope and handoff. Do not implement visual tooling in this v2 thread. **Do not commit or push without instruction.**

**Done:** the two value records, strict codec, identities and pure selection operation, thin offline CLI, synthetic fixtures and focused tests are implemented. Phase-1 Go tests/vet/format, the v1 baseline, full `make check`, shell syntax, repeat-build and demonstration checks passed; binary measurements and their limits are in section 7. v2's design rules are checked as code by `make check-v2` and, in pi, after each edit and before a run ends ([guardrails](#guardrails)). The independent visual-tool brief is linked above; it is not a v2 deliverable. No live action has been taken.

**Left:** of the [independent review's findings](#phase-1-verification-evidence), the coexistence check's blind spots, deliberately not fixed. Nothing is published. Phases 2–7 still need their own decisions and approval.

**Next:** agree the phase-2 compiler contract and obtain approval before implementing it. Do not wait for or build the visual tool here. No platform console, live-adapter or publication work is authorised by this handoff.

### Read first

1. Root `AGENTS.md` and inherited working preferences; inspect `git status` and relevant diffs. Other work was in progress when this was saved.
2. This document, particularly [development layers](#4-development-layers-and-adapters), then [architecture review](architecture-review.md) (release identity, ownership, transition contracts and acceptance criteria) and [model review](model-review.html) (where v1's complexity comes from, so v2 does not repeat it).
3. [Architecture](architecture.md), [glossary](../GLOSSARY.md), [contributing](contributing.md) and [plan section 0](plan.md#0-where-this-paused-and-what-is-left) for current behaviour, terminology, validation and other work.
4. [Current records](../tools/swhurl/apps/model/records.py) and [model tests](../tests/test_app_model.py) for behaviour worth retaining. Read relevant tests before translating a contract.
5. [Runner](../tools/swhurl/run.py) for later effect-boundary contracts. The phase-1 kernel itself must have no external effects.

Read the actual [`kernel`](../v2/kernel/records.go), [`identity`](../v2/kernel/identity.go), [`selection`](../v2/kernel/select.go), [`codec`](../v2/kernel/json.go), [`CLI`](../v2/cmd/swhurl-v2/main.go) and tests before changing them. These references are a map, not a substitute for current inspection. Existing Go/CI boundary decisions are in sections 2, 3, 4 and 7; do not reopen them without a concrete need.

### How to approach a v2 change

1. Start from a domain scenario: the inputs, the request and the expected plan or refusal ([fixtures](#fixtures-and-fakes)).
2. Find the v1 behaviour it covers and classify it as preserve, deliberately change or remove. Check the relevant review findings so the change does not rebuild a known problem.
3. Write the pure function and its tests first. It takes time, identifiers and snapshots as arguments.
4. Add an adapter only when the slice needs that dependency, following the [new-adapter recipe](#extension-recipes): a domain-level interface, raw fixtures, and a behavioural fake that shares one contract suite with the real adapter, failure cases included. Kubernetes goes through the API server ([why](#kubernetes-the-api-server-not-kubectl)).
5. Run `make check-v2` ([guardrails](#guardrails), format, vet and tests) and the matching [test tiers](#test-tiers); live checks only in an approved live phase. Fix what a rule reports rather than its baseline, unless the change is intended and explained.

### Working-tree preservation

Other work may be in progress in the same checkout. Re-run `git status` and inspect diffs before each task; do not stage, discard, fix or incorporate other work. No implementation, policy-exception expansion, commit, push or live action is authorised without the operator's instruction.

### Completed phase-1 deliverable

Immutable `ReleaseBundle`, environment-owned `InstanceSpec`, pure `SelectRelease`, strict parsing/identity tests and an offline demonstration. Unsupported capabilities fail explicitly. A successful selection changes only the release reference; stale, wrong-application and incompatible requests change nothing.

Build/test commands and outputs must be reported. Run the applicable repository checks before a commit; do not stage unrelated changes, commit, push or reconcile as a side effect of merely preparing the slice.

The phase-1 demonstration uses different environment configuration, **not non-empty resource references**: bindings are unsupported except for `{}`. Select R42 for production, show only its release reference changes, and prove stale/incompatible requests change nothing. Do not claim storage, secret-binding or live-readiness support.

### Suggested opening prompt for a fresh session

> Work only on v2. Read `docs/iteration-2-plan.md`, its phase-1 contract, evidence and handoff, the repository instructions, `docs/architecture-review.md` and `docs/model-review.html`. Inspect the working tree and existing Go code/tests. Phase 1 is implemented, independently reviewed and its review findings fixed; the next step is agreement and approval of the phase-2 compiler contract. The visual change tool is a separate v1/history-based workstream: do not build it or wait for it here. Preserve unrelated work. Do not commit, push, publish, access credentials or touch the live cluster.

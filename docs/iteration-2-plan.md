# Iteration 2: incremental replacement alongside the current platform

**This page is the contract: records, identity, selection, the compiler, the phase-3 brief, the gap list and the design rules. [Status, authority and the handoff](v2-status.md) live on their own page, and [workstreams](workstreams.md) is the index. Phases 4–7 and the rest of phase 3 are proposals, not approved work.**

This plan develops the recommendations in [the architecture review](architecture-review.md). [Architecture](architecture.md) remains the canonical description of deployed behaviour; [plan](plan.md) section 0 remains the index of approved work and links here for the v2 contracts.

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

Two value records and one pure operation live in [`v2/kernel`](../v2/kernel/records.go). All domain members are scalars or nested value structs: copying a record cannot alias a map, slice or pointer. Phase 2's `PlatformProfile` is the one exception, its `delivery.dependsOn` list, which the compiler copies wherever it is used. Construction alone does not validate a Go value; identity, encoding and selection validate it. No network, filesystem, environment, clock, randomness or subprocess access belongs in this package.

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
| `wrong_release` | Phase 2: compiling a release the instance does not select |
| `over_quota` | Phase 2: an instance whose pods can never fit the profile's quota |
| `ownership_conflict` | Phase 2: an object claimed by two compilations, or by one and someone else |

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

**Approved 24 October 2026 as the working method for v2.** Phases 3–7 still need their own approval; this section says how to build them, not that they are approved.

### Design principles

**Operator direction, approved after the first phase-3 draft.** These principles apply to subsequent v2 phases and fresh sessions, not just the bridge. They guide contract design; the existing schemas, package layout and architecture checks are the current implementation, not the destination.

1. **Design maintainable v2 contracts, not v1 compatibility.** v1 supplies examples, operational evidence and migration constraints; it does not dictate v2's models, defaults, APIs or orchestration. Preserve v1's live operation while experimenting offline, not its internal contracts by default. Capture current behaviour in tests to understand it, then distinguish behaviour deliberately retained from behaviour deliberately changed. Do not turn every legacy convenience into a replacement requirement.
2. **Separate pure core, orchestration and integrations.** Core functions own domain rules and transformations, with explicit inputs/results and no effects. Orchestration sits above them: it sequences reads and operations, passes facts between domains and handles workflow failures, without reimplementing domain decisions. Integration code owns external dependencies such as Git, files, registries or Kubernetes, translating their formats and failures at narrow boundaries. A command is an entry point, not the owner of all three responsibilities. Start with a few concrete functions; do not build a general workflow/provider framework.
3. **Make domain and data ownership explicit.** Name the authority for each fact, who may change it, and whether a returned value is owned or shared. Pass the smallest coherent domain inputs an operation needs, not one universal record merely because it already exists. An integration can report missing build evidence; it must not invent release provenance. A bridge can translate legacy settings; it must not become their second source of truth. Resource generation need not depend on source history merely because both currently appear in `ReleaseBundle`.
4. **Transition iteratively, with honest intermediate semantics.** Early slices may have incomplete release semantics or missing domain models. Deliver useful, explicitly scoped results while those models develop; do not fabricate identities, supply made-up defaults, require a manual parallel catalogue or weaken a complete-release API to make an intermediate step fit. Distinguish not-yet-modelled semantics, unsupported operations, invalid data, absence and failed reads. A preview of known workload facts is not a reconstructed release, migration plan or authorisation to deploy.
5. **Keep the structure and checks serving those principles.** Prefer the smallest reshape that makes the boundary clearer, with tests for retained behaviour and deliberate changes. Current `kernel` placement and numeric layer rules must not force unrelated domains/orchestration into one package. If a sound boundary needs a rule/API change, propose and explain it, then update code, checks, fixtures and docs together in the approved implementation; do not silently weaken a baseline to get a pass. No rule change is made by this documentation update.

**Ownership guide** (responsibilities, not a requirement to create one package per row):

| Domain or boundary | Owns | Does not own |
| --- | --- | --- |
| Release | Artifact/runtime identity; source/build relationships and compatibility as those models develop | Environment configuration, grants or allocations |
| Instance | Release selection, environment configuration, grants and bindings | Discovering or reconstructing build provenance |
| Platform | Deployment primitives, defaults, quotas and delivery conventions | Application requirements or build facts |
| Resource generation | Deterministic resources from the workload, instance and platform facts it actually needs | Resolving Git, verifying a build or deciding publication authority |
| Integration | Reading/changing its external system and reporting identified facts or failures | Domain policy or filling missing facts with guesses |
| Orchestration | Sequencing a use case and carrying results across boundaries | Becoming a second implementation of domain rules |
| Transition bridge | Legacy interpretation, scoped comparison and explicit gaps | Permanent release authority, ongoing synchronisation or approval to apply a preview |

For each change, state the facts and their owners, the pure functions and orchestration, what crosses each integration boundary, and the result's scope. Track incomplete features in [section 6](#6-deliberately-deferred), naming the domain or next boundary that must resolve them; do not create a second backlog for temporary semantics.

```text
entry point → orchestration → pure core functions
                    ↕
               integrations → Git, files, registry, Kubernetes, ...
```

This is a responsibility model. The current checked layout is `kernel` (layer 0), adapters (layer 1), and command wiring (layer 2); it does not yet provide a separate orchestration package. A later slice should introduce or revise package boundaries only where its use case needs them, rather than treating the numbers as a permanent domain architecture.

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

**Decided 25 October 2026: a thin `net/http` client, not `client-go`.** It is the reader [`adapters/kube`](../v2/adapters/kube/kube.go), keeps the module free of third-party dependencies (`stdlib-only`), and takes the k3s material it needs as arguments: a CA bundle, a client certificate or key, and a bearer token. `client-go` would be complete but adds a large dependency tree and binary size for four kinds and two verbs. Resolving a kubeconfig or an in-cluster service-account token is **not** in that adapter: a kubeconfig is YAML, so reading one needs the dependency or parser decision this slice did not take, and it is the [next observation slice](#first-observation-slice-what-is-running-implemented-offline-25-october-2026).

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

v1 is evidence to evaluate, not the specification of v2 or an expected-output generator. Under the [design principles](#design-principles), classify relevant behaviour as **preserve**, **deliberately change**, **remove** or **not yet modelled**, using the [architecture review](architecture-review.md) and [model review](model-review.html). This is not a requirement to model every v1 feature now. Starting points:

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

Each phase ends in something usable and independently checkable. Phases 1 and 2 are implemented, offline; the remaining phases require their own decisions and approval. The independent [visual-tool workstream](visual-change-tool.md) neither blocks nor authorises these phases.

| Phase | Build | Completion gate |
| --- | --- | --- |
| 1. Release-selection kernel | The two records, release identity and pure selection operation | Offline contract tests pass; no external access |
| 2. Deterministic compiler | Compile one private stateless HTTP instance using one pinned platform profile | Identical inputs produce identical output; duplicate resource ownership fails |
| 3. Read-only v1 bridge | Translate known facts from a pinned v1 snapshot into scoped resource previews and comparison | Differences, coverage, incomplete semantics and ownership conflicts explained; no writes to v1 or claim of a deployable replacement |
| 4. Git delivery | Local plan/diff, validation and narrow proposal/publication through existing command infrastructure | Failed preparation publishes nothing; concurrent changes cannot overwrite each other |
| 5. Current-state observation | Read Git, Flux and workload evidence for an identified target | Unavailable differs from absent; publication differs from running |
| 6. First isolated live instance | Deploy a new disposable stateless instance owned only by v2 | Digest, readiness and private access boundary verified; removal demonstrated |
| 7. Selective replacement | Add capabilities needed by the next chosen app; migrate one ownership boundary at a time | Each migration has parity/safety evidence and a defined recovery path |

**Order approved 25 October 2026: retire risk, not numbers.** Phases 4–6 for one disposable instance come before the rest of phase 3. Delivery, ownership, observation and removal are the untested paths, and the bridge protects nothing until something runs; building it afterwards means it is built against a delivery contract known to work. The bridge is still required before any existing app is migrated (phase 7), and v1 stays untouched either way. The order of this table is therefore not the order of work: 1, 2, 3-slice-1, then 4 → 5 → 6 on one disposable instance, then 3.2–3.5, then 7.

### Phase 1: prove the semantic boundary

Approved implementation: the [phase-1 contract](#2-phase-1-model-and-contract), tests and [offline CLI demonstration](#offline-cli-and-build). No compiler, bridge, publication, console, GitHub workflow or cluster deployment. The module has no third-party dependencies and builds with C disabled. Go 1.26.0 is the installed, pinned experimental toolchain; its status as the latest security patch has not been verified. Revisit the patch pin deliberately before distributing or deploying anything.

Judge model clarity and ease of change as well as binary characteristics before expanding Go. Measurements have no invented performance target and establish no v1 speedup.

### Phase 2: establish compiler ownership

**Contract approved 25 October 2026** (operator: digest-only images, one memory value, an output-based compilation ID, integrations removed for this phase, ownership checked within a compiled set and against a supplied inventory, output on stdout; after the review, the egress proxy deferred, a quota-fit refusal and instance files under `instances/`). Implementation status and evidence are at the end of this section.

```text
compile(instance + the release it selects + platform profile)
  → files:          Namespace, HelmRelease, ResourceQuota, NetworkPolicy, kustomization, Flux unit
  → inventory:      each cluster object those files create, and its owner (app/env)
  → registrations:  the Flux unit file, for the shared list of units (a contribution, not an edit)
  → identity:       comp-sha256 over the canonical output, beside the release ID, instance revision and profile ID
```

Pure, in `kernel`: no network, clock, randomness, credential generation, file or live reads. `Compile(spec, release, profile)` builds one instance; `CheckOwnership(compiled, foreign)` refuses two compilations that claim the same object, or one that claims an object in `foreign` (objects someone else owns: empty in phase 2, filled from v1 in phase 3). The `layers` rule lets no layer-0 package import another, so the compiler shares `kernel` with the records until a split is worth a rule change.

**PlatformProfile** is a third strict JSON record ([`profile.json`](../v2/testdata/profile.json)) with its own identity, `prof-sha256:` over `["swhurl.profile.v1", schemaVersion, name, chart.name, chart.version, chart.repository, chart.repositoryNamespace, delivery.source, delivery.outputRoot, delivery.unitsRoot, delivery.dependsOn, workload.cpuRequestMilli, workload.startupSeconds, workload.startupPeriodSeconds, quota.pods, quota.requestsCpuMilli, quota.requestsMemoryMiB, quota.limitsMemoryMiB]`, encoded as in [release identity](#release-identity-and-instance-revision). It holds only what the platform owns:

| Field | Accepted value |
| --- | --- |
| `schemaVersion` | Integer `1` |
| `name` | App-name grammar |
| `chart.name`, `chart.repository`, `chart.repositoryNamespace` | DNS labels: the pinned deployment primitive and its Flux `HelmRepository` |
| `chart.version` | `MAJOR.MINOR.PATCH` |
| `delivery.source` | DNS label: the Flux `GitRepository` the unit reads |
| `delivery.outputRoot` | Relative path of lowercase DNS-label segments: where the instance's files go |
| `delivery.unitsRoot` | Relative path of lowercase DNS-label segments: where the instance's Flux unit goes, so that kustomize can list it from its own directory. Neither root may be `apps/` or below it, where v1 records its instances |
| `delivery.dependsOn` | DNS labels, distinct, in order: the units every instance waits for |
| `workload.cpuRequestMilli` | Integer `1–2147483647` |
| `workload.startupPeriodSeconds`, `workload.startupSeconds` | Integers; the allowance is a positive multiple of the period |
| `quota.pods`, `quota.requestsCpuMilli`, `quota.requestsMemoryMiB`, `quota.limitsMemoryMiB` | Positive integers |

Timings that belong to the compiler (Helm and unit intervals, the 3-minute timeout, one remediation retry) are constants; a change to them changes the compilation ID.

**What it generates**, for `demo-orders` in `prod` with `outputRoot` `v2-apps`, classified against v1's renderer (`apps/model/workload.py`, `exposure.py`, `calling.py`):

| v1 behaviour | Phase 2 |
| --- | --- |
| Namespace `<app>-<env>` with managed, app, environment and exposure labels | **Preserve** |
| Namespace `team` label | **Remove for now:** v2 records have no team |
| HelmRelease of the pinned chart: timeout 3m, one install and upgrade retry | **Preserve**, chart from the profile |
| Pod security: non-root uid, group and fsGroup from the release, `RuntimeDefault` seccomp, no service account token, read-only root, no privilege escalation, all capabilities dropped | **Preserve** |
| Image as repository, tag and digest | **Change:** repository and digest only (`image@sha256:…`), as the release identity pins it |
| Readiness, liveness and startup probes on the health path and port; startup allowance 120 s every 5 s | **Preserve**, allowance from the profile |
| `/tmp` as an `emptyDir`; a Service on the release port | **Preserve** |
| CPU and memory from image minimums and instance choices | **Change:** CPU request from the profile; memory request and limit both `configuration.memoryMiB` |
| `replicas` optional | **Change:** always set; `0` means stopped |
| ResourceQuota; a NetworkPolicy selecting the app's pods with no ingress, and no egress either ([decided 25 October 2026](#decision-egress-and-credentials-for-the-first-instance-decided-25-october-2026)) | **Preserve** the isolation boundary, and **tighten** it: a phase-2 instance reaches nothing |
| Refusing an instance that can never fit its quota (`over_quota`: its pods and one for a rolling update, plus headroom for v1's other pods) | **Preserve** as `over_quota`, without the headroom, which covers pods (backups, certificate solvers) a phase-2 instance does not have |
| `SWHURL_APP`, `SWHURL_ENV` | **Preserve** |
| Notify URL, caller-token issuer and keys, the projected `notify` token | **Remove for phase 2:** integrations are [deferred](#6-deliberately-deferred) |
| Egress proxy variables (`HOST_IP`, `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `NODE_USE_ENV_PROXY`, `JAVA_TOOL_OPTIONS`) and the egress port allocation every instance gets | **Remove for now** ([deferred](#6-deliberately-deferred)): the policy denies all egress, so nothing reaches outside unlogged. The proxy path is due with the first instance that must reach outside |
| Image automation; the render fingerprint annotation | **Remove:** automatic deploys are deferred; the compilation ID is in the compilation document, not in the resources |
| Flux unit `app-<app>-<env>`: `dependsOn`, prune, wait, timeout | **Preserve**, at `<unitsRoot>/`; its registration is returned, not written |

The NetworkPolicy selects `app.kubernetes.io/instance: <app>`, which the chart sets to the Helm release name; Flux names the release after the HelmRelease when it has no `targetNamespace`, so the compiler sets none. Files are JSON (YAML's subset), so the standard library writes them: `<outputRoot>/instances/<app>/<env>/{namespace,helmrelease,resourcequota,networkpolicy}.json`, a `kustomization.yaml` listing them, and `<unitsRoot>/app-<app>-<env>.json`. The unit sits where kustomize can list it, because kustomize refuses a `resources:` entry outside the kustomization's own directory — which is why a unit returned for someone else to register could never become active ([the slice](#first-delivery-slice-implemented-offline-25-october-2026-operator-run)). File paths are not ownership-checked: `CheckOwnership` compares cluster objects, while the two roots are validated against `apps/`. Objects the chart renders (Deployment, Service, ServiceAccount) are owned through the HelmRelease; the inventory lists the objects compiled directly, by API group, kind, namespace and name.

**Compilation ID:** `comp-sha256:` over `["swhurl.compilation.v1", [[path, document], ...], [[group, kind, namespace, name, owner], ...], [registration, ...]]`, encoded as the record identities are, with each document as its own canonical JSON (keys sorted). The inputs' identities are recorded beside it and do not participate.

**Refusals,** besides each record's own: `wrong_application` when the release is another application's, then `wrong_release` when it is not the one the instance selects, then `over_quota` (field: the first quota the pods exceed, in the profile's order) when the instance's replicas and one more for a rolling update, none when stopped, each with the profile's CPU request and the instance's memory, cannot fit the profile's quota; `ownership_conflict` from `CheckOwnership`, naming the object (an object identity is platform-chosen, not input values).

**Output:** `swhurl-v2 compile --instance FILE --release FILE --profile FILE` prints one JSON document (`"kind": "v2-compilation", "version": 1`, with the inputs' identities, the files, the inventory, the registrations and the ID) to stdout and writes no file. Identical inputs give identical bytes: Kubernetes documents with sorted keys, the compilation document's own fields in a fixed order, no HTML escaping and a fixed file order. Exit codes are phase 1's. `--out DIR` adds one bounded write effect, whose contract is in [the delivery slice](#first-delivery-slice-implemented-offline-25-october-2026-operator-run).

**Verification required:** golden documents for both fixture instances; byte-identical repeat compilation; a change to configuration, release or profile changes only what it should; every refusal; two instances claiming one object, and a `foreign` claim; the golden HelmRelease rendered with the pinned chart (`helm template`, offline from the chart cache) showing the digest image, the replicas, the probes and pods the NetworkPolicy selects; and the files accepted by `kubectl kustomize`. Those two tool checks skip without `helm`, `kubectl` or the cached chart, and fail instead under `REQUIRE_HELM=1`, which CI sets.

**Implemented 25 October 2026, before commit:** [`profile.go`](../v2/kernel/profile.go) and [`compile.go`](../v2/kernel/compile.go) in `kernel`, the [`profile.json`](../v2/testdata/profile.json) fixture, golden compilations in [`testdata/compiled/`](../v2/testdata/compiled/prod.json), and `swhurl-v2 compile`. The profile section the contract called `flux` is `delivery`: the `no-subprocess` rule reads a bare `"flux"` string as a command name, and the code was renamed rather than the rule weakened (identities are positional, so nothing else changed). Evidence:

- The profile's canonical bytes and ID match an independent Python calculation ([`profile-identity.json`](../v2/testdata/profile-identity.json)); each field that can take another valid value changes the ID (`schemaVersion` cannot); 13 malformed profiles get the code and field section 2's precedence gives them, without echoing input.
- Both fixture instances compile to their golden documents, byte-identically on a repeat, from the CLI and the built binary as from the kernel. Selecting R42 for prod, stopping it, or another CPU request changes only `helmrelease.json`; another quota only `resourcequota.json`; another output root moves the instance files and the unit's `spec.path`, while another units root moves only the unit's file; renaming the profile keeps the compilation ID and changes only the recorded profile ID; changing the profile afterwards does not change a compilation.
- The output has none of what phase 2 removes (tag, notify, caller tokens, team label, route, render annotation). Refusals: `wrong_release`, `wrong_application`, an invalid instance before either, an invalid profile; `CheckOwnership` passes staging with prod, refuses a repeated instance and a `foreign` HelmRelease by name, and does not claim objects the chart renders.
- With the cached app-template 5.2.1, `helm template` of the golden HelmRelease renders a Deployment and Service with `image: ghcr.io/samclement/demo-orders@sha256:…`, one replica, the `/healthz` probes, 256Mi, uid 65532 and no service account token, and pods labelled as the NetworkPolicy selects; `kubectl kustomize` accepts the JSON files. Without `helm` the check skips, and fails under `REQUIRE_HELM=1`, as without the chart; a broken image fails it. CI fetches the chart before `make check-v2`, which it runs with `REQUIRE_HELM=1`.
- Guardrails held: `public-api` caught the 19 new declarations (all additions, `api.txt` updated); `budgets` caught a CLI function at complexity 11, which was split.
- **Independent review, 25 October 2026** (reviewer agent, read-only, on `1262960f`): OK with notes, no blockers. Fixed in the same change: (1) the isolation boundary rested on three names written separately, the HelmRelease name, the NetworkPolicy's selector and the inventory, and no test tied them (renaming the HelmRelease passed every test while its policy would select no pods); a kernel test now compares the inventory with what each file declares and the selector with the release name, and the chart test renders under the compiled name and checks every label the compiled policy selects; both of the reviewer's probes now fail; (2) the check for removed v1 items searched for `"team` and `"render` instead of the full `platform.swhurl.com/` keys; (6) the compilation ID's encoding is stated above, and the reviewer's independent Python recomputation matched both golden IDs; (7) the records-are-values statements name `delivery.dependsOn`; (8) an invalid profile is tested beside another application's release, a zero startup period names its own field, output-root segments are at most 63 characters, and two overstated sentences are corrected; (9) the CI step honours `XDG_CACHE_HOME`, as v1 and the test do. **Decided by the operator the same day:** (3) the egress proxy is removed for phase 2 and decided in phase 6 (rows above); (4) the compiler refuses `over_quota`, as v1 does, without v1's headroom (with one memory value, `requests.memory` always binds before `limits.memory`); (5) instance files moved under `instances/`, and file paths are stated to be outside the ownership check. Evidence: `TestQuotaFit` (each quota field at and over its limit, a stopped instance needing nothing, no overflow at the largest values, the release checked first) and `TestNoAppReachesTheUnitsFolder`; `public-api` reported the one new code, `over_quota`.

### Phase 3: shadow, not synchronise

**Revised direction:** the operator approved documenting the [design principles](#design-principles) and revising this brief around them. This is not approval to implement the bridge. The goal is a useful comparison of known workload facts, **not complete reconstruction of v1 as v2 releases**. No migration, publication, live reads or changes to existing v1 behaviour.

**Example:** v1 supplies a digest, port, health path, uid and environment settings, but not a full source commit or verified artifact type/platform. We can preview resources from the known facts and compare them with committed v1 output. The report must say that release semantics are incomplete; it must not invent a release ID, assume an amd64 manifest or require a manually maintained provenance catalogue merely to satisfy today's `ReleaseBundle` schema. An instance with SQLite can still yield a comparison of its HTTP workload, but not a claim that its storage or the whole instance has been replaced.

```text
Git/file integration → identified legacy facts and committed evidence
                           ↓
                     orchestration
                           ↓
        pure translation → scoped workload facts + semantic gaps
        pure rendering   → resource preview for the known subset
        pure comparison  → differences, coverage and ownership findings
                           ↓
                     shadow report
```

#### Responsibilities and contract choices

1. **Compare available facts rather than force complete releases.** The bridge owns legacy interpretation and records each fact's source. The release domain will own complete artifact/source/build semantics as those models develop. Missing provenance limits the claim about a release; it need not prevent a workload preview. Do not add mandatory supplemental claims or a second release catalogue in phase 3.
2. **Separate resource generation from complete-release validation.** The renderer needs selected workload/runtime facts, instance configuration and platform choices, not unrelated source history. A small extraction may let the existing `Compile(spec, release, profile)` validate its full inputs and delegate resource generation, while the shadow path uses the same pure renderer with explicitly scoped inputs. Retain current full-release validation, identity checks and output behaviour unless a separate contract change is proposed. A resource preview must not masquerade as a complete `Compilation` with fabricated `Inputs` identities.
3. **Keep translation out of permanent release/instance authority.** Integrations read Git/files and decode legacy formats. Pure bridge functions translate and explain gaps. Orchestration sequences the reads, translation, rendering and comparison; it does not hide decisions in I/O code or reproduce domain rules. Do not put all of this in `kernel` solely because today's numeric layer rule makes that convenient. Establish concrete function/data boundaries first, then propose the smallest package/rule changes they require.
4. **Let gaps limit coverage, not erase useful evidence.** Distinguish the cases below. A preview always states its scope; it never represents a deployable replacement or publication approval. Partial comparison is not partial success disguised as full parity.

| Situation | Meaning for the shadow result |
| --- | --- |
| Source/build relationship or artifact verification not yet modelled | Record a semantic gap owned by the release/build boundary. Compare workload facts that do not require it; do not manufacture a complete release. |
| SQLite, calls, routes or another capability absent from v2 | Mark that capability and its dependent comparisons outside the preview's coverage. Other independent comparisons may proceed. Do not count excluded fields/resources as equal or silently change the instance to private/stateless. |
| Required rendering fact missing (for example, no digest for a digest-pinned image) | That preview cannot be produced; explain the missing fact without guessing or resolving a mutable tag remotely. Other comparisons can still be useful. |
| Invalid or contradictory input | Report an error at the responsible boundary, not a future-feature gap. Never pass a malformed value to the renderer. |
| Successful read showing something absent | Record absence for that identified source. |
| Failed or incomplete read | Evidence is unavailable, not absent. Do not claim comparison/inventory completeness or fall back to moving `main` or the current checkout. |

#### Initial comparison policy

The first slice below implements only its fixed comparison policy. Legacy translation/default resolution and the rest of this bridge policy remain proposals, not new v2 domain contracts or broader implementation approval:

- Preserve known application/environment identity and runtime facts. A tag beside a digest is unnecessary for the already-approved digest-only output. Neither the tag nor an `offers` commit proves the missing build relationship, media type or platform.
- Keep legacy default resolution separate from v2 policy. Materialise an absent replica count only when its effective default has identified evidence (expected to be one for the pinned v1 chart; verify it). Explicit zero stays zero. Unknown defaults remain gaps, not copied assumptions.
- For the current one-memory-value preview, use v1's effective **limit** and report the resulting request change (for example, 32Mi/128Mi → 128Mi/128Mi). This is a preview policy choice, not a demand that v2 preserve v1 resource contracts. Resolve quantities exactly within the supported range; do not round or hide contradictory request/limit values.
- Use the supplied platform profile for CPU, chart, quota and startup choices, and explain differences from v1. Do not alter a profile merely to make the comparison pass. Preserve applicable resource/quota checks at the rendering boundary; being incomplete about release semantics does not excuse invalid workload inputs.
- Explain phase-2 omissions such as team labels, notification wiring, default proxy configuration, image automation and output paths. They are neither an implicit permanent-removal decision nor permission to run without the relevant safety controls. Keep the [single deferred-feature tracker](#6-deliberately-deferred) current as domain models develop.

#### Snapshot, comparison and ownership boundaries

- Read one identified local Git snapshot. Resolve the revision once; all legacy records and committed manifest evidence come from that tree. Dirty/untracked files have no effect. Do not execute code from the snapshot, fetch, access credentials, decrypt/read secret values or contact a cluster/registry/GitHub. Public checked-in resources are desired-output evidence, not live observations; do not regenerate them and label the regeneration observed evidence.
- Identify the consumed paths, comparison coverage and read failures. Malformed records and missing partner files are not absent apps. Unsupported layouts make their evidence/coverage unavailable rather than silently disappearing. The bridge need not become a general Kustomize interpreter.
- Compare semantics, not YAML formatting or filenames. Explain deliberate changes precisely; do not blanket-ignore env arrays, annotations or security fields. A changed security context or policy inside the compared scope must remain visible. Unmodelled fields/resources are explicitly outside coverage, never counted as matching.
- Feed declared v1 object identities into ownership checks, including relevant output of unmodelled instances and residual output without records. Inventory coverage is independent of whether a workload preview can be rendered. It is snapshot-declared ownership, not proof of live or whole-cluster ownership. Missing inventory evidence prevents a clear-ownership claim.
- Do not exempt the v1 app being compared from conflicts. Its Namespace/HelmRelease should collide; the report retains those findings alongside useful comparisons. Candidate-to-candidate conflicts also remain visible. No ownership result authorises applying a preview.
- Output is deterministic report/scratch data only, with explicit identity of the snapshot/profile, scope, semantic gaps, differences, unavailable evidence and ownership findings. Separate analysis completeness from replacement readiness; completing the scoped analysis does not mean all release semantics/capabilities exist. Exact report/exit schemas remain to be specified with the concrete implementation slice, rather than inheriting the withdrawn all-or-nothing draft.

#### Integration options, after the boundaries

The integration choice is secondary to the domain design. Checked facts: v1 uses YAML ([records](../tools/swhurl/apps/model/records.py), [loader](../tools/swhurl/yamlio.py)); the current Go module is JSON-only, standard-library-only and starts no subprocesses. Its current package/rule layout is described in [section 4](#4-development-layers-and-adapters).

1. **Separate Python export to JSON, then Go analysis:** likely the smallest initial adapter, reusing existing YAML parsing and Git via `Runner`. Costs two development commands, a small interchange format, and console image-input changes if implemented under `tools/swhurl/` and later pushed. No Go runtime invocation of Python is needed.
2. **Native Go reading:** one operator command, but adds YAML/Git dependencies or requires a deliberate subprocess-rule change. Consider only if its maintenance benefit justifies those costs; do not handwrite YAML/Git parsers to preserve a rule mechanically.
3. **Frozen JSON fixtures first:** enough to prove pure boundaries and tests cheaply, but not the final pinned-snapshot bridge. Use as the first implementation step, not a substitute for integration verification.

**Recommendation:** prove the pure preview/comparison boundary with fixtures first; then select the smallest read-only integration. No exporter command, extra provenance input or new adapter/package layout is fixed by this brief.

#### First slice proposal: pure HTTP preview and comparison

**Approved by the operator for this first slice only; implemented, offline-verified, independently reviewed and committed 25 October 2026.** Two composable Go functions and frozen fixture scenarios, not a new operator command or the complete bridge. Example result: “HTTP image, probes and security settings agree; memory request changes from 32Mi to 128Mi under the selected preview choice; release provenance, storage, routing and ownership have not been assessed.” This is useful without a full source commit or a claim that the instance can be migrated.

**Structural problem observed:** [`Compile`](../v2/kernel/compile.go) already validates the full release/instance/profile before constructing resources, but its rendering helpers receive those whole records. Construction uses image/digest, runtime facts, application/environment and configuration—not source repository/commit, media type, platform or the selected release ID. That coupling forces callers to manufacture a complete release merely to ask for resources.

**Options:** (1) keep all preview/legacy comparison code in `kernel`—least file movement, but gives the release/compiler package ownership of transition policy; (2) **extract a release-independent renderer entry point in the existing compiler and add a separate pure `shadow` comparator—recommended**; (3) move renderer, profile and shared types into new domain packages now—potentially useful later, but requires more API/dependency restructuring than this slice proves necessary. Option 2 changes the actual dependency without a compatibility layer, duplicate renderer or general comparison framework. Keeping resource construction where it already lives is deliberate, not a claim that `kernel` is the permanent home of every pure domain.

**Implemented Go boundary** (these are ordinary Go values, not new versioned JSON descriptors):

```go
// kernel: facts needed to render one private HTTP workload, not a release.
type HTTPWorkload struct {
    Image, Digest string
    Port int32
    HealthPath string
    UID int32
}
type HTTPInstance struct {
    Application, Environment string
    Configuration Configuration
}
type ResourceSet struct {
    Files []File
    Inventory []Claim
    Registrations []string
}
func RenderHTTP(HTTPInstance, HTTPWorkload, PlatformProfile) (ResourceSet, error)

// shadow: the resource choices being compared, not publication authority.
type ResourceChoices struct {
    CPURequestMilli, MemoryMiB int32
}
func CompareHTTP(before, after []byte, choices ResourceChoices) (HTTPComparison, error)
```

- **Renderer ownership:** `HTTPWorkload` contains supplied workload facts; the resource domain validates and consumes them, but does not discover or verify build provenance. `HTTPInstance` contains the environment-owned facts needed for this private template, not a release selection or a claim that a legacy app is private. `PlatformProfile` stays platform-owned. Rendering validates names/environment, digest image syntax, port/path/non-root uid, configuration, profile and quota fit, in that order. It neither resolves defaults nor accepts absent required facts. Invalid values return a zero resource result and the existing typed refusal categories.
- **Result ownership:** `ResourceSet` holds the same six files, five direct object claims and registration the compiler currently produces, with no release ID, instance revision, compilation ID or readiness flag. Each call owns fresh slices/JSON bytes; it neither retains nor mutates inputs. In a shadow flow it is only resource-preview data, not a complete instance replacement. A valid digest reference makes no assertion about its manifest type or runtime platform.
- **Full compilation:** `Compile` retains its existing validation/refusal precedence, translates its already-validated records into these rendering inputs, calls the same `RenderHTTP`, and adds the real input identities and unchanged output identity encoding. Keep its public signature, CLI behaviour, golden bytes and IDs unchanged. Share field predicates rather than creating placeholder `ReleaseBundle`/`InstanceSpec` values or implementing a second set of validation rules. Additional renderer validation must not alter which refusal a full compilation returns first.
- **Comparator ownership:** `shadow` owns a bounded transition comparison policy. It accepts the two actual HelmRelease JSON documents, not release records or the renderer's desired input repeated as evidence. It owns decoding/projection of the selected resource fields and comparison, all pure. The future Git/YAML integration will supply identified evidence; neither function reads files or Git. The `before`/`after` direction is explicit, and returned results do not alias the input bytes.
- **Composition, not package coupling:** `shadow` imports no `kernel` types. Later orchestration takes the HelmRelease document from `ResourceSet`, passes it with the baseline and explicit resource choices to `CompareHTTP`, and retains the surrounding source identity/coverage. This slice uses an external test scenario to exercise that composition; it does not add a production workflow package before there are effects to sequence. It proves comparison of rendered output, not just agreement between two copies of desired inputs.

**Fixed first comparison scope:** app-template 5.2.1's one-controller/one-container HTTP HelmRelease shape, checked by raw JSON fixtures. Other chart/controller shapes are outside coverage, not auto-detected as equivalent. Require valid, unambiguous JSON (including no duplicate keys); structural/type errors return no comparison and safe field/category errors, never raw input values.

| Compared group | Initial interpretation |
| --- | --- |
| HelmRelease name/namespace and chart/source reference | Compare exactly; an unplanned difference is visible, not an automatically approved platform change. |
| Image repository/digest, HTTP Service port, health probes/startup settings, explicit replicas, app/env identity variables | Compare actual selected fields. A tag beside the same digest is a named digest-only change; a missing digest or unresolved replica default leaves that item unassessed, not equal. Zero replicas is an explicit value. |
| Pod security context, service-account automount and container security context | Compare complete selected subtrees, not a list that silently ignores extra security fields. Missing/changed settings are visible differences; do not assume a chart default makes them equal. |
| CPU request and memory request/limit | Compare exact quantities in the initial supported grammar: whole cores or integer `m` CPU, integer `Mi`/`Gi` memory, normalised without floats. Other quantity spellings remain unassessed. Explain a CPU change only if it exactly matches the supplied CPU choice. Explain the one-memory-value change only when the prior limit equals the chosen memory, the prior request does not exceed it, and the new request/limit both equal that choice. A changed limit or another unexplained value is a difference, not a generic memory exception. |

`HTTPComparison` contains `Findings []Finding`, `AssessedScope []string` and `UnassessedScope []string`; each `Finding` has a fixed `Field`, `Outcome` (`equal`, `intentional`, `different` or `not_assessed`) and `Reason`. Findings/scopes have deterministic schema order, with empty arrays rather than null. These are per-field/group results with fixed reason identifiers and explicit included/excluded scope. Report fixed safe field paths/reasons rather than dumping before/after documents or arbitrary env values. No single `supported`, `ready` or migration-success boolean. An error is not a “not assessed” success. The actual report envelope/CLI exit contract belongs to a later orchestration slice, not this Go-only result.

Everything outside that fixed scope is explicitly unassessed: full release/source/type/platform semantics, routes, storage, secrets, calls, additional env/config/mounts/containers, Namespace/team semantics, NetworkPolicy, quota-object equivalence, Flux units/registration and foreign ownership. Existing renderer tests still protect generated policy/quota/unit behaviour; this comparator does **not** establish parity for those objects. Known source capabilities may be carried as fixture context, but this slice does not yet promise their automatic discovery from legacy records. No whole-instance parity claim, even if every assessed finding agrees.

**Files and guardrails:** add `kernel/render.go` and focused tests for the extracted boundary; `shadow/http.go`, its tests and raw JSON fixtures; register `shadow` at layer 0 and in `Pure`, and deliberately update `api.txt` for the new APIs. The packages exchange only document bytes through a caller; no same-layer import is needed. No numeric layer-rule change, new dependency, budget exception, subprocess, adapter, CLI, Python exporter or console-image input change. Avoid promising a permanent interchange schema before a real reader exists.

**Acceptance and sequence:**

1. Capture current compiler output/refusal behaviour, then extract rendering. Both existing golden compilations remain byte-identical with the same IDs; retain existing mismatch/validation precedence tests. No golden regeneration merely to make the refactor pass.
2. Test rendering without any source/commit/media/platform/release-ID fields, explicit zero, invalid workload/profile/configuration, quota boundaries, repeated output and independent ownership of returned slices/bytes.
3. Compare actual rendered HelmRelease documents against frozen legacy-shaped JSON. Cover 32Mi/128Mi → 128Mi/128Mi, exact quantity equivalence, unexplained CPU/limit changes, missing defaults/digest, malformed/duplicate-key input, unsupported shapes and excluded features. A fixture may include storage/routing/extra config while the result remains explicitly scoped; it must never count those omissions as parity.
4. Deliberately mutate the rendered image, probe, security context and explicit replicas and prove each affected finding changes. An extra security-context field must not be silently dropped. Changing source-only provenance must leave resource output unchanged while full compilation still records the true input identities.
5. Run `make check-v2`, `make check`, `git diff --check`, `make v2-report`, and the existing required offline chart/Kustomize tests. Review the new API and result/coverage semantics independently. Update section 6 and this handoff; stop before the next integration slice. No commit/push/live work is implied.

Approval covers **this first slice only**. Full legacy translation/default resolution, snapshot acquisition, foreign ownership integration, production orchestration and an operator-facing report remain separate follow-up work. The bridge is not complete when this slice passes.

**Implementation and evidence (25 October 2026, working tree based on `1ea1bbde`; nothing published):**

- [`kernel/render.go`](../v2/kernel/render.go) owns the extracted renderer. Shared validation predicates keep the original full-record field order; `Compile` validates complete records and selection/quota constraints before calling `RenderHTTP` and attaching the real identities. No placeholder records or second renderer. The two existing golden compilations were not edited or regenerated; their bytes and IDs still pass unchanged.
- [`shadow/http.go`](../v2/shadow/http.go) owns strict JSON decoding, fixed projection and comparison. It checks selected structure/types, rejects duplicate keys even in excluded subtrees, invalid UTF-8, multiple documents and lossy unpaired Unicode escapes. Safe `shadow.Error` fields/reasons accompany a zero comparison on invalid input. This is not a Kubernetes schema validator: unknown security members are retained literally in the complete selected subtree rather than silently dropped or interpreted as valid Kubernetes settings.
- The fixed finding order is `metadata`, `chart`, `image`, `service.http`, `probes`, `replicas`, `env.identity`, `pod.securityContext`, `pod.automountServiceAccountToken`, `container.securityContext`, `resources.cpuRequest`, `resources.memory`. Chart/source references are exact; metadata compares name/namespace. Other chart/controller shapes, a disabled controller, a `valuesFrom`-style HelmRelease with no `values`, a per-controller pod override or pod-options strategy, and a named probe port leave their findings `not_assessed` (`outside_http_shape`), while metadata/chart remain assessed so their differences are visible. Missing digest, absent/string-valued unresolved replicas, absent quantities (`missing_quantity`) and unsupported quantity spellings are explicitly unassessed. Null selected scalars/objects and wrong types are errors, not absent defaults. Explicit zero replicas is assessed. Resource choices must be positive integer milliCPU/MiB.
- The frozen [`shadow/testdata`](../v2/shadow/testdata) JSON documents retain the shape of `tests/fixtures/instance/apps/hello/staging/helmrelease.yaml`; arbitrary auth configuration is synthetic. The incomplete variant removes digest/replicas and adds a storage mount. The external `shadow_test` composition test calls the actual renderer with known workload facts and no source/commit/media/platform/release-ID fields, then passes its HelmRelease bytes to the comparator. It explains digest-only output and 32Mi/128Mi → 128Mi/128Mi without claiming parity for routes, storage, config, policy, quota, Flux units or ownership.
- Focused tests cover preview validation order, explicit zero, all quota dimensions/boundaries, overflow, repeatability, input/result ownership, full compilation refusal precedence and source-only changes that preserve resource output/compilation ID but change real release and instance input identities. Rendered-output mutations to repository/digest, readiness/startup, container/pod security, extra/nested/unknown security members, automount, replicas, Service port, env identity, metadata and chart/source reference all change the affected finding to `different`. Exact whole-core/milliCPU and Mi/Gi equivalence, unexplained limits/CPU, memory direction and unknown defaults have focused assertions.
- `shadow` is registered at layer 0 and in `Pure`; `api.txt` intentionally adds only the renderer inputs/result/API and scoped comparison types/API/error. The purity-rule fixture now includes the declared pure package. Numeric layer rules, pure imports, dependencies and budgets are unchanged. No adapter, workflow package, CLI, exporter or console input change.
- Baseline before extraction: `GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0 make check-v2` passed all four existing packages. After implementation, `make check-v2` and `REQUIRE_HELM=1 make check-v2` pass all five packages, with unchanged golden tests and the required cached-chart Helm rendering/offline Kustomize test. Focused `go test -count=1 -cover ./kernel ./shadow` passed (95.3% and 96.0% statements respectively). `make check` passed: lint, 20 app-policy instances, 38 active schema/render entrypoints, OTel fixtures, relative doc links, 98 codegraph tests and 987 v1 tests. Existing OTel component-name and Starlette/httpx deprecation warnings remain; BAD/WARN lines from negative fixture scenarios are expected test output, not live observations.
- Final gates passed: `for f in host/*.sh tests/fixtures/*.sh v2/archtest/report.sh; do bash -n "$f"; done`, `git diff --check` and `make -s v2-report`. The report identifies only the approved new pure package/layer entry and ten added API declarations for review; no budget or dependency changes. No timing/benchmark, credential, network, cluster or live checks were performed. `make check-templates` is not required: no template pin, app type or `app-new` change. **Independent read-only review, 25 October 2026** (reviewer agent, on this working tree): **OK with notes, no blockers.** It reproduced the main claims by differential testing: 34 single-field mutations across instance, release and profile, in every single, pair and triple combination (40,495 cases), gave byte-identical error strings, compilation IDs and file counts against `1ea1bbde`, so the renderer extraction changed no refusal and no identity; duplicate keys, invalid UTF-8, unpaired escapes and multiple documents are refused, including inside excluded subtrees; quantities, explicit zero, unknown security members, determinism, owned results and diagnostic safety behave as stated; `shadow` imports `kernel` only in its external test. It could not verify syscall-level access, real v1 HelmReleases using per-controller pod overrides, or `make check` and `git diff --check` (not in its task; both were run here).
- **Review should-fix findings, both false parity (fixed 25 October 2026):** (1) app-template merges `controllers.<name>.pod` over `defaultPodOptions` (strategy `overwrite`), so a per-controller override changed the effective security context and automount while the comparator still reported the `defaultPodOptions` subtree as `equal`; a non-empty `controllers.main.pod`, or any `defaultPodOptionsStrategy`, now makes `pod.securityContext` and `pod.automountServiceAccountToken` `not_assessed` (`outside_http_shape`), while an empty override changes nothing and stays comparable; (2) a chart outside the pinned one, and a `valuesFrom`-style HelmRelease with no `values`, were refused as invalid because `values` and `controllers` were required before the chart check; both now stop at the shape check as `not_assessed`, and a null or wrong type is still an error, as the contract requires. **Nits fixed:** a disabled controller (which renders no workload) is outside the shape instead of comparing as `equal`; an absent quantity is `not_assessed`/`missing_quantity`, so absence is not mislabelled as a spelling problem; `digest_only_image` became `tag_beside_digest`, because the change is named in either direction; a named probe port, which Kubernetes accepts, leaves `probes` unassessed instead of refusing the document; the per-call regexes are commented, since the `pure` rule forbids package variables and the digest grammar is repeated deliberately rather than importing `kernel`. **Documented, not changed:** `RenderHTTP`'s refusals reuse the compilation's field names (`artifact.image`, `runtime.port`), which name release paths a preview caller never supplied. **Not adopted:** nothing. After the fixes, `REQUIRE_HELM=1 make check-v2` (chart render and offline Kustomize ran, not skipped), `make check` (987 tests) and `git diff --check` passed; `go test -cover` reported kernel 95.3% and shadow 96.7%. Implementation approval still does not approve a push or the next integration slice.

**Test it yourself — run with `!`**, from the repository root (offline, files unchanged):

```bash
export GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0
(cd v2 && go test -count=1 ./kernel ./shadow)
REQUIRE_HELM=1 make check-v2
```


#### Remaining phase-3 acceptance gate

The [first-slice proposal](#first-slice-proposal-pure-http-preview-and-comparison) supplies concrete inputs/results and owners for pure rendering/comparison. Later slices must similarly specify orchestration and integration seams, and any package/rule changes, before implementing those effects.

The eventual bounded implementation should proceed through checkable slices:

1. **Pure preview first:** fixtures with known workload facts and missing release semantics; minimal renderer extraction if needed. Prove existing compiler bytes/identities/refusals remain intact, explicit zero is preserved, resource choices are explained and preview results cannot be mistaken for complete releases.
2. **Scoped comparison and inventory:** prove unmodelled capabilities remain visible, independent comparisons still work, failed evidence is not absence, preserved security/policy mutations are detected, and ownership conflicts include unmodelled/residual apps. No blanket equivalence or false complete-coverage result.
3. **Read-only integration and orchestration:** pin a temporary local repository, change its branch/checkout after resolution and prove the selected input stays fixed. Test missing reads, malformed data, unexpected paths and secret-safe errors. Use raw fixtures and the real/fake shared contract for each Go adapter; Python external calls use `Runner`/`FakeRunner`. Test the actual end-to-end entry point and unchanged inputs.
4. **Review and handoff:** run `make check-v2`, `make check`, applicable shell syntax, `git diff --check` and `make v2-report`; independently review the result before requesting a push. Update this section, the feature tracker and handoff. Phase 4 and live effects remain separate decisions.

**Authority:** the principles, revised brief and the first pure HTTP preview/comparison slice above are approved. No further phase-3 implementation, dependency/rule relaxation, commit, push, console deployment, credentials or live operation is authorised by that approval.

### Phase 4: settle publication before automation

Recommendation: initially publish one accepted commit containing validated intent and its compiled output, rather than immediately introducing an asynchronous Render bot.

This trades larger diffs for fewer intermediate states. It is a deliberate departure from v1's records-only PR contract and needs approval. Design validation, ownership and coexistence with the existing Render workflow before connecting a v2 writer.

Concurrency has two distinct checks:

- **Instance revision:** protects the operator's reviewed target from silent replacement.
- **Git head:** protects atomic publication from other writers.

An unrelated Git change can trigger revalidation. A changed same-instance target must not be silently overwritten. Changed release selection requires renewed review. Publication uses non-force/CAS semantics; preparation or validation failure publishes no intent.

Keep the actor/approval policy explicit. A trusted-operator profile is valid; a general multi-team authorisation system is not required for this milestone. Promotion evidence policy must be agreed before exposing a production promotion command.

#### First delivery slice: implemented offline 25 October 2026, operator-run

**Approved by the operator on 25 October 2026 and implemented the same day. The live half has not run:** Flux applying one disposable instance, and its removal, need the operator at the cluster. It is the first live slice under the [approved order](#5-incremental-delivery) and uses the [decided egress and credential answers](#decision-egress-and-credentials-for-the-first-instance-decided-25-october-2026).

**Structural problem: phase 2's output cannot reach the cluster.** Three gaps, in the order they block:

1. `swhurl-v2 compile` prints one document and writes no file (deliberate in phase 2). Nothing puts the files at their paths.
2. The generated Flux unit is *returned as a contribution, not written* — and it cannot simply be referenced. kustomize refuses a `resources:` entry outside its own directory, so a unit file at `v2-apps/units/` cannot be added to `clusters/home/kustomization.yaml`, which is how a unit becomes active.
3. The compiled NetworkPolicy permits all egress, which the decision above closes.

**Scope: three parts, one change.**

1. **Deny all egress.** `policyTypes: ["Ingress", "Egress"]` and no `egress` rules. Goldens change deliberately and are explained, not regenerated by habit; the profile is unchanged, and the compilation IDs change because the files do.
2. **`compile --out DIR`.** One write mode with its own contract (below). Everything else about `compile` stays: stdout mode is unchanged, byte-identical to today.
3. **Unit placement.** The profile gains `delivery.unitsRoot` so an instance's unit file can be written where kustomize can see it. A strict-schema change: profile fixture, identity vectors and both golden compilations move with it.

**Options for the placement:**

| Option | What it buys | Cost |
| --- | --- | --- |
| **1. `delivery.unitsRoot: clusters/home`** — the unit file is written beside v1's units and listed once, by the operator, in `clusters/home/kustomization.yaml` | v1's exact shape (root → unit → instance files), one-tier ownership, and phase 7's handover is "change one unit's `path`" | One schema field and its goldens; `docs/architecture.md`'s units table is compared with Git, so it gains the row in the same change |
| 2. A v2-owned root Kustomization applying everything under `v2-apps/`, unit files included | No change to the profile | A third tier, a second root with its own prune semantics, and two places that decide what v2 owns; removal reasoning gets harder |
| 3. The operator copies the generated unit into `clusters/home/` by hand | No code change at all | Registration leaves the compiler's output, so the operator becomes the source of that file and a recompile cannot update it safely |

**Recommendation: 1.**

**`--out DIR` contract.** This is the slice's only new effect, and it is bounded:

- Writes only the paths the compilation names, under DIR, at their compiled relative paths. Refuses an absolute path, a `..` segment, a symlink at or below DIR, or a path that would resolve outside it.
- All or nothing as far as preparation can tell: every path is validated and compared before the first write, so a refused destination or path writes nothing. A filesystem failure during the writes cannot be undone — the outcomes already completed are printed before the error, and each file is written to a temporary name beside it and renamed, so an interrupted run cannot leave a truncated file.
- Never deletes or overwrites a file it does not name. A file whose bytes already match is reported `unchanged` and not rewritten; a named file with different content is replaced and the change is reported per path.
- Never writes into v1's app tree: a destination that resolves into the `apps/` of a checkout — one with `.git` or `clusters/` beside it — is refused however it is spelled, including through a symlink; and a profile root may not be `apps/` or below it.
- One line per path with its outcome on stdout, including the outcomes completed before a failure; diagnostics on stderr; phase 1's exit codes, plus `refused_destination` (exit 2) for a rejected destination and `io` (exit 1) for a filesystem failure.

**Delivery is the operator's Git.** After a successful compile, `git status` shows exactly the compiled paths plus the one line in `clusters/home/kustomization.yaml`; the operator reviews, commits and pushes. Git supplies the compare-and-swap, so "failed preparation publishes nothing" and "a concurrent change cannot overwrite" are properties of that composition — the acceptance tests say so rather than claiming a publish command that does not exist.

**The first adapter.** Writing files is not wiring, so it belongs at layer 1 (`adapters/files/`), not in `cmd/swhurl-v2`: an interface, raw fixtures, a behavioural fake and one shared contract suite, per the [adapter recipe](#extension-recipes). This is the first real adapter, so it also exercises `adapter-complete`, which currently checks nothing. `no-subprocess` and `pure` stay intact — no git binary is invoked.

**Image and name:** a public pinned image or the digest the platform already deploys (the fixture digests are synthetic), with a throwaway instance name per the repository's convention; no new repository or package is created. A collision with a v1 instance is *not* detected in this slice (`foreign` ownership is phase 3), so the operator confirms the name is unused.

**Out of scope, deliberately:** any Git, GitHub or Kubernetes adapter; a publish command; automation; the bridge; teams, calls, events, storage, secrets; the egress proxy; any v1 change.

**Acceptance, per invariant:**

- *Isolation:* the compiled policy declares `["Ingress","Egress"]` with no rules; a pod in the instance's namespace reaches neither another namespace's service nor the internet.
- *Writes:* an escape attempt (`..`, absolute, symlink, `apps/`) is refused and writes nothing; a repeat compile reports every path `unchanged` and changes no byte; a partial failure leaves earlier files untouched.
- *Determinism:* `--out` output equals stdout mode byte for byte, and two compiles to the same DIR are identical (invariant 3).
- *Live:* Flux applies the unit; the instance runs the compiled digest; it is Ready; it is unreachable from another namespace; deleting its files and committing prunes only its objects; every v1 instance is unchanged.
- *Checks:* `make check` still renders every active Flux path — including the new instance, since it is now under `clusters/home/kustomization.yaml` — and the units table matches Git.

**Costs and risks taken:** the profile schema and all goldens moved together; one write effect entered a module that had none; removal is "the operator deletes the files", so prune will be observed rather than assumed; and a name collision with v1 is undetected until phase 3 gives the compiler `foreign` objects — including at the unit name, where a v2 unit lands beside v1's `app-<app>-<env>.yaml`, so a careless `--out .` on the fixtures shows exactly the collision the plan accepts rather than hides it.

**Implementation and evidence (offline half, 25 October 2026):**

- **Deny-all egress.** The compiled NetworkPolicy now declares `policyTypes: ["Ingress","Egress"]` with empty `ingress` and `egress` lists: an empty rule list denies every connection. `TestCompiledResources` asserts all three fragments, so the policy cannot drift back without a failing test.
- **`delivery.unitsRoot`.** The profile's 16th identity element, right after `outputRoot`; both roots are validated as relative DNS-label paths and refused if they are `apps/` or below it, because that is where v1 records its instances. The canonical bytes and `prof-sha256` were recomputed in Python, not by the compiler ([`profile-identity.json`](../v2/testdata/profile-identity.json)), and the compiler's own answer matches: `prof-sha256:e2e1b313…`.
- **The golden diff is exactly the change and nothing else:** compilation ID, profile ID, the two network-policy fields, the unit's path and the registration. The five instance documents are byte-identical, and no golden was regenerated for another reason.
- **`compile --out DIR`.** One write effect, with the contract above. The destination is refused when any path segment is `apps`; the adapter refuses an absolute path, a `..` segment, a path that cleans differently, and a symlink at or below the destination. Every file is prepared (validated and compared) before the first write, so a detected problem writes nothing; identical bytes are reported `unchanged` and not rewritten; stdout prints one `written PATH` or `unchanged PATH` line per file and never the compilation document; refusals exit 2 and filesystem failures exit 1.
- **The first adapter.** [`adapters/files`](../v2/adapters/files/files.go) (layer 1) exposes `Writer`, `Target`, `Outcome` and an error type with two codes, `refused_destination` and `io`. Its fake is [`filestest`](../v2/adapters/files/filestest/fake.go), which keeps bytes in memory and applies the same destination rules; one `Contract` suite runs against the real adapter on the raw fixtures in [`testdata/`](../v2/adapters/files/testdata) and against the fake, so the two cannot drift. `adapter-complete` now checks something instead of nothing.
- **One rule change, stated rather than worked around:** `layers` rejected a fake importing the adapter it fakes, although the [adapter recipe](#extension-recipes) requires exactly that (`var _ files.Writer = (*Fake)(nil)`). Both at layer 1 is the documented shape, so the rule now allows that one same-layer import and the guardrail table says so.
- **Checks:** `make check-v2` (7 packages), `REQUIRE_HELM=1 make check-v2` (the chart render and offline Kustomize ran, not skipped), `make check` (v1 unchanged), `git diff --check` and `make v2-report` (two new packages, the layer and API changes, and every reviewed item named). Coverage: kernel 95.3%, `adapters/files` 85.7%, `filestest` 83.3%, CLI 93.3%. Tests cover the refusal set, the symlink escape, a refused path writing nothing, an unnamed file left alone, both filesystem-failure branches, and a repeat compile reporting `unchanged`.
- **Not done, and not claimed:** the live half. No instance has been compiled into the repository, no unit is listed in `clusters/home/kustomization.yaml`, and nothing has run on the cluster.

### Phase 5: explain current state honestly

Observe an identified desired target and report source and observation time.

- A successful Git publication is not a running workload.
- Flux reconciliation and workload readiness are distinct facts.
- Failed reads are unavailable, not absent or successful.
- A workload must match the intended artifact, not merely report Ready.
- Current state does not establish that an earlier target ever ran.

Do not reconstruct historical Activity as part of this phase.

#### First observation slice: what is running (implemented offline 25 October 2026)

**Approved by the operator on 25 October 2026:** the first real adapter, its **offline half only** — no cluster read, no credential, no command. The [design principles](#design-principles) govern it, so v1's console is evidence about the questions, not the contract.

**Structural problem:** rendering decides what should exist ([`RenderHTTP`](../v2/kernel/render.go)) and [`shadow.CompareHTTP`](../v2/shadow/http.go) compares two desired documents. Neither can say what a cluster runs, and v1 answers that question by mixing `kubectl` reads into rendering. This phase's five sentences are decisions, so they live in a pure package with the reads behind one adapter.

**Owners and boundaries.**

| Piece | Owns | Does not own |
| --- | --- | --- |
| [`observe`](../v2/observe/explain.go) (layer 0, pure) | Projecting evidence onto one target: what each fact is, and the verdict | Reading anything, knowing a server, resolving a target |
| [`adapters/kube`](../v2/adapters/kube/kube.go) (layer 1) | One API-server GET: the path per kind, response translation, TLS and the token | Deciding what an answer means, retrying, caching, discovering kinds or credentials |
| The caller (later: orchestration) | Supplying the target's refs, selector, container, intended artifact, replicas and commit; providing credential material; stamping source and time | Inventing a fact, filling a gap, or reading a clock in the pure layer |

**The target and the evidence** are ordinary Go values, not new versioned descriptors. `Target` carries the instance namespace, four refs (the unit, the HelmRelease, the Deployment, and the Pod collection, which names a collection and so has no object name), the pod label selector, the container name, the intended digest, the intended replica count, and the intended source revision (optional: empty means not supplied). The digest is the artifact's identity: the same content under another repository name is not a difference, so no repository is compared, and none is asked for. `Evidence` carries the three object reads, at most one pod list, and the source identity and observation time as **supplied strings** — the pure layer never reads a clock. A single object read about the pod collection is refused rather than dropped, because the target asked for a list.

A read is `present` (with the object's bytes), `absent` (a successful 404, reason `not_found`) or `unavailable` (a reason from a fixed set: `forbidden`, `timed_out`, `transport`, `status`, `malformed`, `response_too_large`). Absence may not carry content and a failure may not carry an unknown reason: contradictory evidence returns no report. A read the caller did not make is `not_read`, which is neither absence nor a failure, and the adapter never returns it.

**Seven findings, in this order.** Each answers one question, so a fact that cannot be established leaves the others standing.

| Finding | Positive | Other outcomes (with their reasons) |
| --- | --- | --- |
| `unit` | `reconciled` | `not_reconciled` (`suspended`, `condition_absent`, `condition_not_ready`, `status_stale`), `absent`, `unavailable`, `not_assessed` (`not_read`) |
| `unit.revision` | `applied` | `different`, `not_applied` (`revision_not_applied`), `not_assessed` (`revision_not_supplied`, `unrecognised_revision`, `unit_absent`, `not_read`) |
| `release` | `settled` | as `unit`: Helm applying the chart is a different fact from Flux applying the commit |
| `workload` | `present` | `absent`, `unavailable`, `not_assessed` (`not_read`) |
| `image` | `running` | `different` (`digest`, `unpinned_pod_image`, `container_absent`), `not_running` (`no_pods`, `not_pulled`), `not_assessed` (`no_replicas_intended`, `workload_absent`, `workload_unavailable`, `pods_unavailable`, `not_read`) |
| `ready` | `ready` | `not_ready` (`condition_absent`, `condition_not_available`, `status_stale`), `not_assessed` (the `workload` gates) |
| `replicas` | `as_intended` | `different` (`available_replicas`), `not_assessed` (the `workload` gates) |

A positive outcome carries no reason. `status_stale` refuses evidence about an older spec, and the two kinds state that differently: a Flux condition carries the generation it observed (v1's `release_ready` reads it the same way), while a Deployment states it once on `status.observedGeneration`, which must equal `metadata.generation` before its `Available` condition is read. The `image` fact is read from the **pods**, not the Deployment's spec, because a rollout in progress keeps the old pods available while the spec already names the new image; it compares the pinned digest, as v1 does, so the same content under another repository name is not a difference. A pod that has not pulled its image is told apart from one running another digest, and pod phase, restarts, events and terminating or evicted pods are not filtered: those are the [`pods` gap](#6-deliberately-deferred). Zero intended replicas is a value: no pod is then expected, the image is not assessed, and the count agrees — so a deliberately scaled-to-zero instance reports `unknown`, never `running`.

**The verdict is three-valued, with a stated precedence:** any negative finding makes it `not_running`; otherwise any `unavailable` or `not_assessed` fact makes it `unknown`; only seven positive findings make it `running`. There is no `supported`, `ready` or `healthy` boolean, so an unread fact cannot be mistaken for success or for absence. `unit.revision` is `different` when the unit has not applied the supplied revision — Flux applies a source revision as a whole, so the revision supplied is the one Flux is expected to be *at* (the head it tracks), and "not at it" is negative in either direction; supplying the commit that last touched the instance would make every later unrelated commit read as `not_running`, which is why the caller owns that choice.

**Scope.** Each report lists its assessed findings, its unassessed ones, and a fixed `NotCovered` list: `history` (whether an earlier target ever ran), `git_publication`, `source_build`, `pods` (restarts, events, scheduling), `routes`, `storage`, `secrets`, `calls`, `events`, `team`, `quota`, `foreign_ownership`. "Running" therefore says what it read and no more.

**The adapter** states each kind's group and version rather than discovering it — `v1 Pod`, `apps/v1 Deployment`, `helm.toolkit.fluxcd.io/v2 HelmRelease`, `kustomize.toolkit.fluxcd.io/v1 Kustomization` — and refuses anything else, so no other kind is read by accident. Read through the API a successful `200` must be the object the reference names (`apiVersion`, `kind`, namespace and name), and a list must carry an `items` array: a proxy page, a `Status` document, `null` or another kind is a failed read, not an empty object and not an empty list. A collection read never reports absence: an empty successful list is `present` with no items. A redirect is not followed — the 3xx is the answer — so a token cannot reach another host or plain http, and a 404 behind a redirect cannot become absence. A request it cannot make is a Go error, because no evidence was attempted: an unsupported kind, a name or namespace the API could not have (so a reference cannot reshape the path), a read without a name, a list without a selector, an endpoint that is not https, and TLS material that does not parse. Everything the server answers is a result. One response body is bounded at 1 MiB.

**Fixtures and fakes.** [`testdata`](../v2/adapters/kube/testdata) holds one hand-written API document per supported kind, with the field names `apps/v1` and the Flux CRDs use, shaped after the v1 instance ([`tests/fixtures/instance`](../tests/fixtures/instance)) and the phase-6 live instance. They are not captured from a cluster and their status blocks are synthetic; a claim about what a live server returns is not made here. The [adapter recipe](#extension-recipes) is followed: `kubetest.Fake` asserts it satisfies `kube.Reader`, and one `Contract` suite runs against the real adapter (over a fixture transport) and the fake, so presence, absence, a failed read, an unusable answer — including a `200` that is not the object asked for — and the refusals cannot drift. Nothing binds a listener, opens a socket or needs a credential.

**Files:** `observe/explain.go` (target, evidence, findings, verdict, validation), `observe/project.go` (decoding API documents into facts), `observe/explain_test.go`, `observe/verdict_internal_test.go` (the outcome signs and the verdict precedence); `adapters/kube/kube.go` (+ `kube_test.go`, `testdata/`), `adapters/kube/kubetest/`; `archtest/rules_test.go` (`observe` at layer 0 and in `Pure`; `adapters/kube` and its fake at layer 1), `archtest/checks_test.go` (the purity fixture names the new pure package), `archtest/api.txt` (+75 lines, nothing removed or changed). No dependency, budget entry, rule setting, command, golden compilation, kernel or shadow change.

**Acceptance, per the [eight invariants](workstreams.md#what-a-status-page-is):** v1 unchanged (no v1 file touched; the coexistence test passes; `make check` passes); pure — `observe` imports only allowlisted standard packages and has no clock, network, filesystem, environment, randomness or subprocess, which the `pure` and `places` rules check; kernel and shadow bytes, IDs and refusal precedence untouched; the new refusal vocabularies are stated above and tested; failure paths tested (each read status and reason, the refusals, contradictory evidence, malformed documents, a `200` that is another object, a list without items, a redirect, a body that fails mid-read, the verdict precedence and the outcome signs); scope reported per finding and fixed for the rest; offline only, so no readiness is claimed. Frozen scenarios cover: the target running; each reconciliation, readiness and image failure; unread and unavailable evidence (never absence); absence limiting what depends on it; the other-digest, unpinned, not-pulled, no-pod and missing-container image cases; zero replicas; the applied-revision cases; a negative deciding over an unread fact; determinism and owned results; invalid targets and contradictory evidence.

**Evidence** (25 October 2026, working tree; nothing published): `make check-v2` passed 10 packages; `REQUIRE_HELM=1 make check-v2` passed with the chart render and offline Kustomize actually run; `make check` passed 1012 v1 tests and the doc checks; coverage `observe` 97.1%, `adapters/kube` 96.3%, `adapters/kube/kubetest` 79.8%; `make -s v2-report` listed only the three new packages, their layers, `Pure`, and the added API lines; `git diff --check` and the shell syntax check passed. **Nothing was read from a cluster:** no credential, no `$HOME/.kube/config`, no live fixture.

**Independent read-only review, 25 October 2026 (reviewer agent, on this working tree, mutation-tested in a throwaway copy):** **blockers — one, fixed.** The review's central finding was a false negative the tests could not see: a Deployment states the generation it has observed once, on `status.observedGeneration`, and its `Available` condition carries no such field, so comparing a condition field made every healthy instance read `not_ready`/`status_stale` — the fixture had been given a field the API never serves. `readyFinding` now requires `status.observedGeneration` to equal `metadata.generation` and then reads the condition's status; the fixtures and the scenario builder carry the real shape, and the stale case is asserted through it. Six should-fix findings, all fixed: (1) a redirect was followed, which could carry the token to plain http and turn a 404 behind it into absence — a copy of the client now returns the 3xx as the answer; (2) a `200` that was not the object asked for (a `Status` document, `null`, another kind) was `present`, and a list body without `items` was an empty list, so unusable bytes decided `no_pods` — both now require the response to identify the requested object; (3) the `Contract`'s ownership checks could not fail, because they mutated the bytes they then compared against — the expected bytes are copied first, and removing the fake's copy now fails; (4) `unit.revision`'s `different` semantics were undecided — now stated as "not at the supplied revision, in either direction", with the reason the caller owns the choice; (5) zero replicas always reads `unknown` — now stated; (6) nothing kept `outcomeSign` in step with the outcome constants, and the test that looked like it did evaluated nothing — the unclear outcomes are named explicitly and an internal test walks all seventeen, plus the verdict precedence. Nits fixed: a single-object read about the pod collection was accepted and silently dropped (`owns` now excludes it); an unavailable list could carry items; an unavailable unit reported `unit.revision`'s reason as `not_read` (now `unit_unavailable`); the target required a repository nothing compared (dropped); the comma-separated revision list was invented (removed); the adapter could send a name like `..` into the path (names and namespaces are now checked); a body read that timed out reported `transport` (now `timed_out`); and two test lines that asserted nothing. Not changed, with reasons: an unstarted pod running another digest is skipped, which matches v1 and is covered by the release's own `wait`; an evicted pod left with an old digest reads `different`, which is the pod-phase gap the `pods` row names. The reviewer could not run `make check` or the live-server behaviour of redirects, and said so.

**What this slice does not do, and what therefore reopens the work:** deriving the target's refs, selector and commit from compiled output (chart-generated names are a chart-convention decision, not an observation one); a credential source (kubeconfig is YAML; an in-cluster token is a different path); an operator-facing report envelope and exit codes; pod-level facts (phase, restarts, events, terminating and evicted pods); ownership against foreign objects; and any live read, which needs phase 6's approval.

### Phase 6: first isolated live instance

Use a new approved disposable instance, not an existing stateful app. Retain the repository's naming and live-test safety conventions. Do not create a public app repository/package merely to test the model; if one is needed, obtain the required confirmation.

Prove publication, reconciliation, intended artifact, readiness and access/isolation from a separate workload. Check that v1 instances and their owners remain unchanged. Exercise failure reporting and approved removal without deleting unrelated resources or data.

Decide egress before the first instance runs: phase 2 generates no proxy variables or port allocation, so a v2 instance would reach outside the cluster directly and unlogged ([what is not implemented](#6-deliberately-deferred)). Apply the new-component checklist before adding anything that runs, stores data or holds a credential. Any root Flux configuration changes require separate confirmation.

#### Decision: egress and credentials for the first instance (decided 25 October 2026)

**The operator accepted both recommendations.** What is true today, and why the decision was needed: the compiled [NetworkPolicy](../v2/kernel/render.go) declares `policyTypes: [Ingress]` and no `egress` rules, so Kubernetes allows every outbound connection from a v2 pod, and the compiler emits no proxy variables and no port allocation. v1 sends every instance through the egress proxy, which logs each request with the instance and applies [grants](apps.md#reaching-outside-the-cluster). A v2 instance as compiled today is therefore weaker than a v1 instance: it egresses, and nothing records that it did.

| Decided | Consequence |
| --- | --- |
| **Deny all egress** for the first disposable instance: `policyTypes: [Ingress, Egress]` and no rules | A compiler change of its own, with its golden files changed and the change explained rather than regenerated (invariant 3). The instance answers probes and reaches nothing: strictly safer than v1, and no allocation model is needed yet |
| **The operator's own credentials** for delivery and observation: push with the operator's Git auth, read the cluster with `$HOME/.kube/config` | No new Secret, runner, deployment or second control plane. Every delivery criterion is provable from the host. Delivery is not unattended, which is deliberate while the contract is unproven |

**Rejected, and why:** direct egress, unlogged (silently weaker than v1; it makes the grant model unenforceable for anything v2 later owns); a proxy with a fixed shared identity and no allocation (logs stop attributing requests to an instance, and the allocation is the decision that needs designing anyway); an in-cluster writer with a deploy key and RBAC, and a GitHub Actions job, for credentials (both automate a contract that has not run once; the first adds rotation and a second control plane, the second is the asynchronous bot phase 4 deliberately avoids).

**Still open, and what reopens this:** the egress proxy path — proxy variables, per-instance port allocation and its snapshot — is due with the first v2 instance that must reach outside, whether to another app or the internet, and is expected to be a slice of its own because the allocation is where v1 mixes a decision, filesystem checks and key generation. Delivery automation (in-cluster writer or a workflow job) reopens the credential choice when unattended delivery is wanted; nothing about this decision makes it harder.

### Phase 7: selective replacement

Let the next migration supply the capability requirement:

1. Add storage and secrets before migrating an app that needs them.
2. Add relationship resolution and shared compilation before migrating connected apps.
3. Add selected console commands only after the domain and delivery contracts work through Git/CLI.
4. Retire a v1 path only once its replacement owns the relevant inputs, output and operations.

Existing-instance migration follows the [documented staged Flux ownership handover](architecture.md#flux-units). Do not change the old unit's path in the cutover commit or permit it to prune handed-over resources.

Stateful migration additionally requires demonstrated backup/restore, explicit resource-retention rules and operator approval. It must not be the first migration exercise. Define recovery per migration; do not imply that rolling back an image reverses data migrations.

## 6. Deliberately deferred

Do not make feature parity the first goal. This is the one list of unimplemented features and incomplete transition semantics: how v2 treats each today, and which domain or next boundary must resolve it. v1 supplies examples, not the required replacement contract. Update the list in the change that defers, implements or removes something; a phase's own classification table links here. The list does not approve removing anything from v1.

| Feature or semantic gap | v2 today | Decide by |
| --- | --- | --- |
| Egress proxy: proxy variables, port allocation, egress grants | **All egress denied** for the first instance ([decided 25 October 2026](#decision-egress-and-credentials-for-the-first-instance-decided-25-october-2026)): no proxy path, and no instance can reach outside | With the first instance that must reach outside, as a slice of its own |
| Routes: `authenticated-web` and `public` exposure, Ingress, TLS, the sign-in middleware | Refused (`unsupported`): only `private` | The first instance that needs a route |
| App kinds `worker` and `job` (a CronJob schedule); a command override | Refused (`unsupported`): only `web`, and no command | Phase 7, per app |
| Teams: the namespace `team` label, team isolation | Not in the records; no label | Before an instance needs team isolation |
| Calls between apps: caller tokens and signing keys, `SWHURL_<APP>_URL`, callers in the NetworkPolicy | Not generated | Phase 7, with the first app that calls another |
| Notifications: `SWHURL_NOTIFY_URL`, the projected `notify` token | Not generated | Phase 7 |
| Events: NATS users, streams and consumers | Not generated | Phase 7 |
| Secrets (`envFrom`, Reloader) and bindings to resources | Non-empty `bindings` refused; no secrets | Phase 7, per app |
| Telemetry: the OTLP endpoint and its variables | Not generated | Phase 7 |
| SQLite storage, backups and restore; alerts | Not generated | Phase 7, per app |
| Resources from the image's minimums; a per-instance CPU choice | CPU from the profile; one memory value for request and limit | When an app needs more |
| Quota headroom for an instance's other pods | Not counted: a phase-2 instance has no other pods | With the first feature that adds pods |
| Image automation, automatic release discovery, auto-merge | Not generated | After phase 4's delivery |
| The render fingerprint in the resources | The compilation ID is in the compilation document only. Observation did not need it in the cluster: the unit's applied revision is read instead, and compared only when the caller supplies the intended commit | Phase 5, if a later observation needs an artifact identity in the cluster |
| The unit's registration in `clusters/home/kustomization.yaml` | The unit file is written at `delivery.unitsRoot` by `compile --out`; its one-line registration stays a reviewed edit by the operator ([decided with the delivery slice](#first-delivery-slice-implemented-offline-25-october-2026-operator-run)) | Decided; revisit only if per-instance registrations become noise |
| v1's objects as `foreign` ownership | Empty; the [phase-3 brief](#phase-3-shadow-not-synchronise) proposes declared app-object identities from a pinned snapshot, independent of preview coverage, including unmodelled/residual apps | Phase 3 |
| Complete release/source/build relationships and artifact verification | The current descriptor requires source/type/platform claims but does not verify them. Phase 3 would preview known workload facts without inventing complete releases or requiring a manual claims catalogue | Release/build domain modelling as it develops; before claiming complete or independently verified release semantics |
| Scoped workload preview/comparison independent of complete releases | The approved [first phase-3 slice](#first-slice-proposal-pure-http-preview-and-comparison) implements shared `RenderHTTP` and pure `shadow.CompareHTTP` over actual JSON with fixed, explicit coverage. Full legacy translation/default resolution, snapshot acquisition, ownership integration and report orchestration remain unimplemented | Resource-generation and transition domains in the next separately approved phase-3 slice; before claiming the bridge complete |
| Observation of what is running (the first adapter) | The [first observation slice](#first-observation-slice-what-is-running-implemented-offline-25-october-2026) implements the pure projection and one read-only adapter for four kinds. The target's refs, selector, container, intended artifact, replicas and commit are **supplied**; nothing derives them from compiled output, and there is no command. Pod-level facts (restarts, events, scheduling), ownership and every kind but those four are not read | The next observation slice (reference derivation, a credential source, the report envelope), then live verification with phase 6 |
| A credential source for reading a cluster | None: [`adapters/kube`](../v2/adapters/kube/kube.go) takes TLS material and a bearer token from its caller. A kubeconfig is YAML and an in-cluster token is another path, so both need a decision | With the first thing that reads a cluster: the next observation slice |
| An operator-facing observation report (envelope, exit codes, document schema) | None: `observe.Report` is a Go value. No `swhurl-v2` command reads a cluster, and the report's shape is not a published schema | With the first command that reads a cluster |
| What an app offers (offers documents) | Not generated | Phase 7 |
| Live feature flags | None | Later |
| Console: repository creation, seed images, historical Activity | None | Later |
| Multiple stacks and app types (the templates) | None | Later |

This table records missing work, not a promise to copy all v1 features. A scoped shadow preview may compare known facts while a row remains unmodelled; it must identify that gap and the domain/next boundary responsible, not call the entire instance supported. Resolve the gap in this table when its domain model arrives. Complete release semantics need not be forced into the first transition step ([design principles](#design-principles)).

Not planned: generic plugins, workflow engines and provider abstractions.

## 7. Verification and execution gates

- Tie each model/compiler contract to focused offline tests.
- Existing Python adapters use `Runner`/`FakeRunner`. Any approved Go adapter must have equivalent focused tests for failed calls, dry-run behaviour and secret handling; do not bypass those guarantees. Run the [test tiers](#test-tiers) that match the change.
- Run `make check` before every implementation commit, plus the additional checks required by [contributing](contributing.md) and `AGENTS.md` for the files changed.
- Confirm integration with existing validation before claiming v2 is safely ignored or checked by it.
- Do not claim live readiness from offline tests. Live phases need observed evidence recorded in [current state](current-state.md).
- Confirm hard-to-reverse actions separately: data deletion, real credential rotation, backup destination changes and changes under `flux-system`.
- Once execution is approved, keep phase status and remaining decisions here and link from `docs/plan.md` section 0. Do not create another parallel status artifact.

### Guardrails

Several structural v2 design rules are checked as code in [`v2/archtest`](../v2/archtest): test files using the standard library only, which parse v2's source and never build or run it, so every `go test ./...` checks them. This table lists the same rules; a test fails when the two differ. Passing these checks does not prove domain/data ownership, useful contracts or honest preview semantics; those [design principles](#design-principles) also need scenario tests and review. The current numeric layout is a checked implementation choice, not a substitute for those principles.

| Rule | What it enforces |
| --- | --- |
| `stdlib-only` | v2 imports only the standard library and its own packages; `go.mod` requires nothing ([why](#kubernetes-the-api-server-not-kubectl)). A dependency needs the operator's approval first. |
| `layers` | Every v2 package has a declared layer (0 pure model, 1 adapters, 2 commands) and imports only v2 packages in lower layers, except that a fake may import the adapter it fakes ([why](#4-development-layers-and-adapters)). A new package is given its layer in `Layers`. |
| `pure` | Pure packages (`kernel`, `shadow`) import only the allowlisted standard packages, print nothing and assign no package variable ([why](#the-pure-model)). Time, identifiers and snapshots are arguments. |
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

**CI (24 October 2026):** Validate's tooling job sets up Go with `actions/setup-go@v7` from `v2/go.mod` (no cache: there is no `go.sum`) and runs `make check-v2` after the code graph tests, with `REQUIRE_HELM=1` and the chart the v2 profile pins fetched into the chart cache first, so the [compiler's](#phase-2-establish-compiler-ownership) manifest check runs; a failure there holds the console deploy like any other check. First run on the push of `be041cf4` ([current state](current-state.md#v2-and-code-graph-push-24-october-2026)): `go1.26.0 linux/amd64`, all four packages passed; Go setup took 7 s and the check 11 s. `actionlint` was not run (not installed here).

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

The first real adapter needs separate phase approval; phase 3 will select its read-only integration after establishing pure preview/comparison boundaries. It is checked by `adapter-complete` from the commit that adds it.

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

The four commands are `release-id FILE`, `instance-revision FILE`, `select-release --instance FILE --release FILE --expect REVISION` and, from phase 2, `compile --instance FILE --release FILE --profile FILE` ([compiler](#phase-2-establish-compiler-ownership)). Only explicitly named files are read; there is no root discovery, registry, external command, credential/config lookup or file-write command. Valid results go to stdout, diagnostics to stderr. Exit `0` means success/no-op, `2` means usage/validation/refusal (including an input file larger than 1 MiB, which is not read past that), and `1` means file/output I/O failure. A flag given twice is a usage error, not the last one winning; flags may also be written `-instance=FILE`, and `--` ends them; `--help` or `-h` prints the usage to stdout and exits `0`. Refusal stdout is empty. If stdout is a pipe whose reader has gone, the process ends by `SIGPIPE`, as Unix tools do, rather than exiting `1` (catching it would need `syscall`, which the `no-subprocess` rule keeps out of v2). Output failures cannot retract bytes already accepted by the destination; they must return nonzero rather than claim success.

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

Status, decisions, evidence and the fresh-context handoff live in [v2 status](v2-status.md). Contracts stay here: [the gap list](#6-deliberately-deferred), [guardrails](#guardrails), the [change report](#change-report) and the [extension recipes](#extension-recipes). Nothing in this page authorises implementation beyond the approved slice.

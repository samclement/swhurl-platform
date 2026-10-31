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

Three value records and one pure operation live in [`v2/kernel`](../v2/kernel/records.go): the release, the instance, and the app-level record the always-on slice added. All domain members are scalars or nested value structs: copying a record cannot alias a map, slice or pointer. Phase 2's `PlatformProfile` is the one exception, its `delivery.dependsOn` list, which the compiler copies wherever it is used. Construction alone does not validate a Go value; identity, encoding and selection validate it. No network, filesystem, environment, clock, randomness or subprocess access belongs in this package.

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
| `runtime.kind` | `web` (a stateless HTTP service) or `worker` (reacts, or works in a loop, and serves nothing); no command override or additional needs. Fixed at creation |
| `runtime.port` | Integer `1–65535` for `web`; `0` (absent) for `worker`, which has no Service |
| `runtime.healthPath` | Absolute ASCII path for `web`; no authority (`//`), query or fragment; absent for `worker`. Allowed characters after the initial slash: alphanumerics and `._~!$&'()*+,;=:@%/-` |
| `runtime.uid` | Integer `1–2147483647`; non-root |
| `database` | Optional `sqlite`; empty for an image that keeps no state |
| `telemetry` | Optional boolean; true when the image has an SDK that reports to the platform collector |
| `publishes` | Optional list of event names, each an app-name-style segment, each at most once: what the image's AsyncAPI document says it publishes |
| `description` | Optional one line of 1–200 printable characters: what the image says about itself, served to it as `SWHURL_DESCRIPTION` |
| `apiPath` | Optional absolute path (`/openapi.json`-shaped) where a **web** image serves its API description; a `worker` states none. It is the path the app serves, not the file its repository keeps |
| `offers` | Optional 40-hex commit the app's offered documents were read at. It is **not** `source.commit`: the image moves with every deploy and the documents are refreshed separately. The annotation renders its first seven characters, which is what the documents' own `source.yaml` states |
| `releaseId` | Optional identity assertion; if supplied, must match the computed release ID |

Values are compared exactly, never normalised: `Owner/Repo` and `owner/repo` are different sources and give different release IDs, though GitHub treats them as one repository.

All fields except `releaseId` are required. Absence of the assertion computes it; encoding always includes it. Empty/null assertions are invalid, not absence. No grants, secret requirements, command override or integrations are accepted. The three offered facts are what the
image declares about itself; the documents they describe are files beside the instance
([what an app offers](apps.md#what-an-app-offers)), never a resource.

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
| `calls` | Optional list of app names: what this instance calls | No |
| `allows` | Optional list of app names: whose calls it accepts | No |
| `events.key` | Optional user public nkey (`U` plus 55 base32 characters): the identity it signs in to the event service with. Required for anything its release publishes or it receives | No |
| `events.subscriptions` | Optional list of `<publisher>.<event>` grants, each at most once: the events it receives | No |

`calls` and `allows` are the instance's own declarations, read by `resolve` to decide consent; the compiler never renders them, only the connected pairs a resolution gives it. `calls` states app names, because a call needs one instance's address; an `allows` entry is an app name or a team grant (`team:<team>`) that admits any caller whose app record names that team. A grant names the team and never its members, so membership is one fact in one place — the caller's own `app.json` — and moving an app between teams needs no other instance's record rewritten. They are sorted and deduplicated when a record is written, and they participate in the instance's identity like every other fact. Non-empty bindings are refused, not ignored. This phase proves preservation of **empty** bindings, not supported resource attachments. There is no create-instance operation or copying of staging defaults. Secrets and live observations do not belong in either record.

### AppSpec: the app-level facts one record owns

[`app.json`](../v2/testdata/app.json) is one record per app, not per environment:

| Field | Accepted value |
| --- | --- |
| `schemaVersion` | Integer `1` |
| `application` | Same app-name grammar as the release |
| `team` | A team name; the same in every environment, so staging and production cannot disagree |
| `appId` | Optional identity assertion; `app-sha256:` over `["swhurl.app.v1", schemaVersion, application, team]` |

The record is required for delivery: `compile --app` renders the namespace's `platform.swhurl.com/team` label from it, and the compilation names its app ID. A preview or live read that does not deliver an instance may omit it and renders no team label. The record is the future home of app-level settings (custom alerts), not a second instance record.

### Image automation: what an image states

A staging instance whose artifact image is published from its own `source.repository` renders Flux's
`ImageRepository` (`<app>`, in `flux-system`, with the platform's `managed` and `app` labels) and its
`ImagePolicy` (`<app>-staging`, the tag contract `^(?P<run>[0-9]+)-[0-9a-f]{7,40}$`, extract `$run`, numerical
ascending, `digestReflectionPolicy: Always`, hourly) into its own compiled folder, and its unit waits for
`platform-image-automation`. Production renders neither: it follows a promotion. A pinned third-party image renders
neither: it is not the app's own publication. The names are the workflow's contract — `<app>-staging` is how the
dispatch names the app — so they are kernel constants, tied by a test to v1's committed objects and to
`contract.py`.

The trigger is Flux's; the facts are the image's. An image states its declaration in its config labels under
`platform.swhurl.com/declaration`: one JSON document the build writes from `swhurl.yaml` and the events document
it names (the internal `swhurl image-declaration` command), read by `adapters/registry` with the manifest.

The same build states the commit it was built from, in the standard `org.opencontainers.image.revision` label. The
tag carries an abbreviation (`<run>-<7 hex>`), and a release states a full 40-character commit, so the image states
one and the tag has to agree: a tag whose suffix the revision does not start with is another build and is refused,
and a revision that is itself an abbreviation is not a commit. Neither label is optional.

| Field | Accepted value |
| --- | --- |
| `schemaVersion` | Integer `1` |
| `runtime` | The release's own runtime rule: `kind` `web` or `worker` and a non-root `uid`; a web image states `port` and `healthPath`, a worker states neither |
| `telemetry` | Optional boolean: the image has an SDK that reports to the platform collector |
| `database` | Optional `sqlite`; empty for an image that keeps no state |
| `publishes` | Optional event names, each an app-name-style segment, each at most once |
| `secrets` | Optional secret names. Read, never applied: a non-empty list is refused |
| `memoryMiB` | Optional integer: the image's minimum memory. Read, never applied: above the instance's memory it is refused |

The document is strict: unknown fields, nulls, wrong types and a version other than 1 are refused, and the
runtime is validated by the release's own rule. It is the delivery contract, not the whole app declaration:
`description`, `stack`, `provides`, `flags` and the offer documents are not facts this path applies, and the
catalogue's offers remain [deliberately deferred](#6-deliberately-deferred). `secrets` and `memoryMiB` are present
only so an image that adds one is held rather than run without it.

`adapters/registry` is the one new integration: one public repository's tags, then one tag's index resolved to its
`linux/amd64` manifest, then that manifest's config labels. It reads anonymously, takes no credential, and names no
policy: the tag ranking is the kernel's. `swhurl-v2 deploy` composes them — decode the records, read the tags, take
the newest by the platform's policy, describe it, read the declaration and the revision, refuse what the instance
cannot take, select the release against `--expect`, compile, and write the record pair and the compiled files in one
effect. A
tag that matches no policy is a refusal; an image already selected is a no-op that writes nothing; a registry it
cannot read is exit 1; and a refused image leaves the running one selected. With `--records` the same delivery runs
for every `v2-records/<app>/staging`: one instance's refusal or failure does not stop the others, the exit status is
the worst outcome, and an instance takes the allocation document only when it records a grant for it ([the
workflow](v2-status.md#slice-record-image-automations-workflow-30-october-2026)). Nothing dispatches it yet: the
image Alert starts it, and the hourly run begins, once a rebuilt image carries the label.

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

### Promotion: selection for production (implemented offline and first promoted live 30 October 2026)

`kernel.SelectPromotion` is promotion's rule on top of selection, and `swhurl-v2 promote --records DIR --app APP
--env prod --profile PROFILE.json --expect REVISION --out DIR` performs it from the tree both instances keep their
records in. Order is contractual:

1. the candidate is the release staging selects — a release whose ID staging's record does not state is refused
   (`wrong_release`), so a promotion cannot carry a hand-picked or stale release;
2. the source is staging and the target is production (`invalid: environment`);
3. one application throughout (`wrong_application`);
4. the user cannot change on an instance that keeps a volume (`unsupported: runtime.uid`);
5. then selection's own checks — a stale `--expect`, then the release already selected as a no-op.

Selection replaces only the release reference: production's configuration, grants, `calls`, `allows`, storage and
broker identity are its own and stay, and production's files are compiled from them. What a promotion cannot carry
is refused by the compiler rather than by a promotion rule — a release that keeps a database on an instance with no
volume, or the reverse, fails `storage`; a quota the profile cannot hold fails `over_quota` — so the refusal names
the fact rather than the operation.

**What v2 does not decide.** A promotion's *direction*: a release states no build order (the tag carries the run
number and a release does not keep the tag), so the document states both releases and a person reads a rollback
from them; v2 does not guess one. Production's *readiness* is the caller's check (`observe`, `make app-status`),
not a promotion rule. A *first* promotion — creating production — is not modelled, because v2 has no
create-instance operation: `promote` refuses a production with no record. There is no auto-merge: the command is
the person. The first live promotion (`swhurl-try-11` production, 30 October 2026) is
[recorded in current state](current-state.md#the-first-live-promotion-swhurl-try-11-production-moves-to-v2-30-october-2026).

### Refusal codes

The code is the machine contract; the field says where; the reason is prose for people.

| Code | When |
| --- | --- |
| `invalid` | Not one valid UTF-8 JSON document, a duplicate key, not an object, a missing required field, a `null`, a wrong type or out-of-range integer, or a value outside its field's format or range; also a malformed `releaseId` assertion or expected revision |
| `unsupported` | A field the schema does not have (including a case variant next to the real key, a grant in a release or a non-empty `bindings`), `schemaVersion` other than 1, and a well-formed value outside this phase's profile: a `sha512:` digest, another media type or platform, a `runtime.kind` that is neither `web` nor `worker`, an environment other than `staging` or `prod`, an exposure other than `private`, an instance with a broker identity and a profile that states no broker |
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

This is a responsibility model, and since 26 October 2026 it is also the checked layout: **layer 0** the pure model (`kernel`, `calls`, `legacy`, `observe`, `shadow`), **layer 1** one adapter per dependency (`adapters/files`, `adapters/kube`), **layer 2** the use cases (`orchestration`), **layer 3** the command (`cmd/swhurl-v2`). `orchestration` assembles the inputs a use case needs, calls the kernel's decisions in order, performs the use case's one effect through an adapter, and reports either the result or where it stopped; it parses no arguments, prints nothing, chooses no exit status and holds no mutable state (the `orchestration-wires` rule). A command is then flags, I/O, exit codes and output. Boundaries are still revised only where a use case needs it, not for the numbers' own sake.

### The pure model

A pure function's output depends only on its arguments: no network, filesystem, environment variables, current clock, randomness or subprocesses. Time, identifiers and snapshots are passed in. Parsing or comparing a timestamp the caller supplied is a pure computation; asking for the current one is not, and the `pure` rule refuses the clock's own functions. The pure layer holds more than the records:

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
| Builds and registry | `adapters/registry` reads one public repository's tags and resolves a tag to its `linux/amd64` manifest and config labels, anonymously; build evidence and provenance are not read | Recorded registry answers, a scripted registry | Real GHCR answers, a private package's refusal, the image's own declaration |
| Kubernetes and Flux | Observe resources; request reconcile, suspend, other changes | Recorded API documents; scripted observation sequences | Controller behaviour, RBAC, eventual convergence |
| Credentials and encryption | Generate keys, read secrets, encrypt and decrypt | Synthetic keys, deterministic outputs | Real SOPS round trips and key compatibility |
| NATS and live flags | Read and write live values, connect, authenticate | In-memory store with revisions and injected failures | Access controls, concurrent updates |
| Operation and notification state | Store progress and incident state | In-memory store with conflicts, restarts, failures | Durability, concurrent writers |
| ClickStack | Dashboards, alerts, webhooks, queries | Response fixtures, fake inventory | Service-specific update and authentication |
| Notification delivery | Send to ntfy or elsewhere | Captured messages; rejection and timeout | Real transport |
| Backups and storage | Object storage, snapshots, volumes, restore | Backup manifests, object listings, fault scenarios | Restorable data, incomplete uploads |
| S3 (object store) | Listing one bucket prefix: its URI, SigV4, `ListObjectsV2` and its pages | Recorded `ListBucketResult`, AWS's published signing vector | Real credentials, real region discovery, many pages |
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

**PlatformProfile** is a third strict JSON record ([`profile.json`](../v2/testdata/profile.json)) with its own identity, `prof-sha256:` over a positional canonical array, encoded as in [release identity](#release-identity-and-instance-revision). The array begins `["swhurl.profile.v1", schemaVersion, name, chart.name, chart.version, chart.repository, chart.repositoryNamespace, delivery.source, delivery.outputRoot, delivery.unitsRoot, delivery.dependsOn, workload.cpuRequestMilli, workload.startupSeconds, workload.startupPeriodSeconds, quota.pods, quota.requestsCpuMilli, quota.requestsMemoryMiB, quota.limitsMemoryMiB]`; each later approved platform block appends its facts in the order its slice record states, including `telemetry.endpoint`, `telemetry.protocol` and `notify.address` from the always-on slice, and `telemetry.nodeCIDR` from the telemetry-export slice. It holds only what the platform owns:

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

**Output:** `swhurl-v2 compile --instance FILE --release FILE --app FILE --profile FILE` prints one JSON document (`"kind": "v2-compilation", "version": 1`, with the inputs' identities, the files, the inventory, the registrations and the ID) to stdout and writes no file. Identical inputs give identical bytes: Kubernetes documents with sorted keys, the compilation document's own fields in a fixed order, no HTML escaping and a fixed file order. Exit codes are phase 1's. `--out DIR` adds one bounded write effect, whose contract is in [the delivery slice](#first-delivery-slice-implemented-offline-25-october-2026-operator-run).

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
// kernel: facts needed to render one instance's resources, not a release.
type WorkloadFacts struct {
    Kind string
    Image, Digest string
    Port int32
    HealthPath string
    UID int32
}
type InstanceFacts struct {
    Application, Environment string
    Configuration Configuration
}
type ResourceSet struct {
    Files []File
    Inventory []Claim
    Registrations []string
}
func RenderInstance(InstanceFacts, WorkloadFacts, PlatformProfile) (ResourceSet, error)

// shadow: the resource choices being compared, not publication authority.
type ResourceChoices struct {
    CPURequestMilli, MemoryMiB int32
}
func CompareHTTP(before, after []byte, choices ResourceChoices) (HTTPComparison, error)
```

- **Renderer ownership:** `WorkloadFacts` contains supplied workload facts; the resource domain validates and consumes them, but does not discover or verify build provenance. `InstanceFacts` contains the environment-owned facts, not a release selection or a claim that a legacy app is private. The name is the [worker kind's](v2-status.md#slice-record-the-worker-kind-29-october-2026): the renderer's kind, not its HTTP surface, decides the shape. `PlatformProfile` stays platform-owned. Rendering validates names/environment, digest image syntax, the kind's runtime facts (a `web` workload's port/path/non-root uid, a `worker`'s absence of the first two), what the kind cannot have (a route, a caller), configuration, profile and quota fit, in that order. It neither resolves defaults nor accepts absent required facts. Invalid values return a zero resource result and the existing typed refusal categories.
- **Result ownership:** `ResourceSet` holds the same six files, five direct object claims and registration the compiler currently produces, with no release ID, instance revision, compilation ID or readiness flag. Each call owns fresh slices/JSON bytes; it neither retains nor mutates inputs. In a shadow flow it is only resource-preview data, not a complete instance replacement. A valid digest reference makes no assertion about its manifest type or runtime platform.
- **Full compilation:** `Compile` retains its existing validation/refusal precedence, translates its already-validated records into these rendering inputs, calls the same `RenderInstance`, and adds the real input identities and unchanged output identity encoding. Keep its public signature, CLI behaviour, golden bytes and IDs unchanged. Share field predicates rather than creating placeholder `ReleaseBundle`/`InstanceSpec` values or implementing a second set of validation rules. Additional renderer validation must not alter which refusal a full compilation returns first.
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

- [`kernel/render.go`](../v2/kernel/render.go) owns the extracted renderer. Shared validation predicates keep the original full-record field order; `Compile` validates complete records and selection/quota constraints before calling `RenderInstance` and attaching the real identities. No placeholder records or second renderer. The two existing golden compilations were not edited or regenerated; their bytes and IDs still pass unchanged.
- [`shadow/http.go`](../v2/shadow/http.go) owns strict JSON decoding, fixed projection and comparison. It checks selected structure/types, rejects duplicate keys even in excluded subtrees, invalid UTF-8, multiple documents and lossy unpaired Unicode escapes. Safe `shadow.Error` fields/reasons accompany a zero comparison on invalid input. This is not a Kubernetes schema validator: unknown security members are retained literally in the complete selected subtree rather than silently dropped or interpreted as valid Kubernetes settings.
- The fixed finding order is `metadata`, `chart`, `image`, `service.http`, `probes`, `replicas`, `env.identity`, `pod.securityContext`, `pod.automountServiceAccountToken`, `container.securityContext`, `resources.cpuRequest`, `resources.memory`. Chart/source references are exact; metadata compares name/namespace. Other chart/controller shapes, a disabled controller, a `valuesFrom`-style HelmRelease with no `values`, a per-controller pod override or pod-options strategy, and a named probe port leave their findings `not_assessed` (`outside_http_shape`), while metadata/chart remain assessed so their differences are visible. Missing digest, absent/string-valued unresolved replicas, absent quantities (`missing_quantity`) and unsupported quantity spellings are explicitly unassessed. Null selected scalars/objects and wrong types are errors, not absent defaults. Explicit zero replicas is assessed. Resource choices must be positive integer milliCPU/MiB.
- The frozen [`shadow/testdata`](../v2/shadow/testdata) JSON documents retain the shape of `tests/fixtures/instance/apps/hello/staging/helmrelease.yaml`; arbitrary auth configuration is synthetic. The incomplete variant removes digest/replicas and adds a storage mount. The external `shadow_test` composition test calls the actual renderer with known workload facts and no source/commit/media/platform/release-ID fields, then passes its HelmRelease bytes to the comparator. It explains digest-only output and 32Mi/128Mi → 128Mi/128Mi without claiming parity for routes, storage, config, policy, quota, Flux units or ownership.
- Focused tests cover preview validation order, explicit zero, all quota dimensions/boundaries, overflow, repeatability, input/result ownership, full compilation refusal precedence and source-only changes that preserve resource output/compilation ID but change real release and instance input identities. Rendered-output mutations to repository/digest, readiness/startup, container/pod security, extra/nested/unknown security members, automount, replicas, Service port, env identity, metadata and chart/source reference all change the affected finding to `different`. Exact whole-core/milliCPU and Mi/Gi equivalence, unexplained limits/CPU, memory direction and unknown defaults have focused assertions.
- `shadow` is registered at layer 0 and in `Pure`; `api.txt` intentionally adds only the renderer inputs/result/API and scoped comparison types/API/error. The purity-rule fixture now includes the declared pure package. Numeric layer rules, pure imports, dependencies and budgets are unchanged. No adapter, workflow package, CLI, exporter or console input change.
- Baseline before extraction: `GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0 make check-v2` passed all four existing packages. After implementation, `make check-v2` and `REQUIRE_HELM=1 make check-v2` pass all five packages, with unchanged golden tests and the required cached-chart Helm rendering/offline Kustomize test. Focused `go test -count=1 -cover ./kernel ./shadow` passed (95.3% and 96.0% statements respectively). `make check` passed: lint, 20 app-policy instances, 38 active schema/render entrypoints, OTel fixtures, relative doc links, 98 codegraph tests and 987 v1 tests. Existing OTel component-name and Starlette/httpx deprecation warnings remain; BAD/WARN lines from negative fixture scenarios are expected test output, not live observations.
- Final gates passed: `for f in host/*.sh tests/fixtures/*.sh v2/archtest/report.sh; do bash -n "$f"; done`, `git diff --check` and `make -s v2-report`. The report identifies only the approved new pure package/layer entry and ten added API declarations for review; no budget or dependency changes. No timing/benchmark, credential, network, cluster or live checks were performed. `make check-templates` is not required: no template pin, app type or `app-new` change. **Independent read-only review, 25 October 2026** (reviewer agent, on this working tree): **OK with notes, no blockers.** It reproduced the main claims by differential testing: 34 single-field mutations across instance, release and profile, in every single, pair and triple combination (40,495 cases), gave byte-identical error strings, compilation IDs and file counts against `1ea1bbde`, so the renderer extraction changed no refusal and no identity; duplicate keys, invalid UTF-8, unpaired escapes and multiple documents are refused, including inside excluded subtrees; quantities, explicit zero, unknown security members, determinism, owned results and diagnostic safety behave as stated; `shadow` imports `kernel` only in its external test. It could not verify syscall-level access, real v1 HelmReleases using per-controller pod overrides, or `make check` and `git diff --check` (not in its task; both were run here).
- **Review should-fix findings, both false parity (fixed 25 October 2026):** (1) app-template merges `controllers.<name>.pod` over `defaultPodOptions` (strategy `overwrite`), so a per-controller override changed the effective security context and automount while the comparator still reported the `defaultPodOptions` subtree as `equal`; a non-empty `controllers.main.pod`, or any `defaultPodOptionsStrategy`, now makes `pod.securityContext` and `pod.automountServiceAccountToken` `not_assessed` (`outside_http_shape`), while an empty override changes nothing and stays comparable; (2) a chart outside the pinned one, and a `valuesFrom`-style HelmRelease with no `values`, were refused as invalid because `values` and `controllers` were required before the chart check; both now stop at the shape check as `not_assessed`, and a null or wrong type is still an error, as the contract requires. **Nits fixed:** a disabled controller (which renders no workload) is outside the shape instead of comparing as `equal`; an absent quantity is `not_assessed`/`missing_quantity`, so absence is not mislabelled as a spelling problem; `digest_only_image` became `tag_beside_digest`, because the change is named in either direction; a named probe port, which Kubernetes accepts, leaves `probes` unassessed instead of refusing the document; the per-call regexes are commented, since the `pure` rule forbids package variables and the digest grammar is repeated deliberately rather than importing `kernel`. **Documented, not changed:** the renderer's refusals reuse the compilation's field names (`artifact.image`, `runtime.port`), which name release paths a preview caller never supplied. **Not adopted:** nothing. After the fixes, `REQUIRE_HELM=1 make check-v2` (chart render and offline Kustomize ran, not skipped), `make check` (987 tests) and `git diff --check` passed; `go test -cover` reported kernel 95.3% and shadow 96.7%. Implementation approval still does not approve a push or the next integration slice.

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

**Authority:** the principles, the revised brief, the first pure HTTP preview/comparison slice, the legacy translation and preview report, the snapshot export and ownership inventory, the read-only live ownership check, and (25 October 2026) signed-in routes below are approved. No further phase-3 implementation — snapshot acquisition, ownership inventory, dependency/rule relaxation — is authorised by that approval, and neither is any commit, push, console deployment, credential or live operation beyond those slices.

#### Phase-3 slice 2: legacy translation and a preview report (implemented offline 25 October 2026)

**Approved 25 October 2026 as the next phase-3 slice, translation before acquisition.** The bridge compared two HelmRelease documents, but nothing turned a predecessor's records into the facts the renderer takes, and nothing named what those records state that a preview cannot carry.

**Structural problem:** v1's records are the authority for an instance's intent, and v2's preview needs a handful of typed facts. Reading them ad hoc in a command would put legacy interpretation in the wiring, and quietly ignoring the fields it did not read would turn a capability into a silent omission.

**Contract.** [`legacy.Translate`](../v2/legacy/legacy.go) takes one instance's records as JSON — the app record, its settings, its release, and the HelmRelease the predecessor **committed** — and produces:

- the HTTP workload facts (image, tag, digest, kind, port, health path, uid) and the environment facts (exposure, replicas, memory, team) the preview renders from;
- a **source for every fact**: which document stated it, so a reader can tell a record's value from one only the committed output established;
- a **gap for every legacy fact it cannot carry**, with the owner who must resolve it: a field the records state that the preview does not use, and a field this translator does not know. An unknown field is a gap owned by the bridge itself, never a silent drop.

Two facts are taken from the committed output because only rendering establishes them: a replica count the settings omit (refused when neither document states it — an absent value is never replaced by a guess) and the effective memory **limit**, which the preview uses as its one memory value and the comparison then explains as the request change (32Mi/128Mi → 128Mi/128Mi). The image's provenance is always a gap: a legacy record carries no source/build relationship or verified artifact type and platform.

**Refusals, fixed:** `invalid` (a document that is not one JSON object, a field of the wrong type, an unusable quantity, a committed output without its application or main controller), `unsupported` (a record version other than 1, a release that is not `web` — the preview is HTTP-only — and a quantity spelling outside whole Mi/Gi), and `missing` (a digest, a replica count or a memory limit that no document establishes). A refusal carries the document and field, never a value.

**The command.** `swhurl-v2 preview --app FILE --settings FILE --release FILE --committed FILE --profile FILE` composes the bridge's pure parts and nothing else: translate, `kernel.RenderInstance` from the translated facts, then `shadow.CompareHTTP` between the committed HelmRelease and the rendered one, with the profile's CPU choice and the translated memory. It writes a `v2-preview` version-1 document to stdout — identity, the preview's facts and their sources, the gaps, the comparison's findings and its assessed/unassessed scope, and a fixed note that it is a preview and not a migration plan — plus a one-line summary on stderr naming every field that is not equal. Exit 0 means a report was produced; 2 is a refusal or invalid input; 1 is I/O.

The composition lives in the command, not a package: a package above layer 0 that could own the envelope does not exist yet, and layer-0 packages may not import each other. Introducing one is a later, separate decision.

**What the inherited fixture previews:** `hello`/staging from [its records](../tests/fixtures/instance/apps/hello/staging), converted to JSON once and frozen in [`legacy/testdata`](../v2/legacy/testdata). Ten compared fields agree; `image` is `intentional/tag_beside_digest` (v2 renders the digest alone); `resources.memory` is `intentional/chosen_single_memory`; three gaps are named (`app.team`, `instance.exposure` because it is `authenticated-web`, `image.provenance`); eleven areas are explicitly unassessed. The report therefore demonstrates the phase-3 example rather than asserting it.

**Files and evidence:** `legacy/legacy.go`, `legacy/record.go` (field-by-field decoding that makes every field either a fact or a gap), their tests over the frozen fixture and every refusal, `cmd/swhurl-v2/preview.go` and its tests (the inherited fixture, a deliberately changed committed digest, an unsupported release, an unreadable input), `legacy` registered at layer 0 and in `Pure`, and `archtest/api.txt` (+11). `make check-v2` (11 packages) and `REQUIRE_HELM=1 make check-v2` pass; `make check`, `git diff --check` and `make v2-report` pass. Offline only: the records are JSON, so no legacy YAML, Git, credential or cluster was read.

#### Phase-3 slice 3: snapshot acquisition and the ownership inventory (implemented 25 October 2026)

**Approved 25 October 2026 with the phase-3 continuation.** The preview read five named files and said nothing about who else claims the objects it would create. This slice pins the analysis to one revision and adds the inventory.

**Decided: the snapshot is exported by a `swhurl` command, not read by v2.** `make v2-snapshot REVISION= OUT=` is a Python command in the operator tooling: it resolves the revision once, reads every document through `git show` (so a dirty tree, a later commit or another checkout changes nothing), parses the legacy YAML with the loader the rest of the tooling uses, and writes one JSON document. v2 then reads only JSON, which keeps the Go module free of a YAML dependency and of subprocesses. The cost is deliberate and named: the exporter lives under `tools/`, so a change to it is a change to the console image's inputs, and the Validate deploy job redeploys the console — which is why it is a command rather than a script.

**What the exporter emits** (`v2snapshot.py`): `version`, the resolved `revision`, and per instance its four records and **the identities of every object its committed manifests declare** — including the HelmRelease it commits, its own Flux unit, and every document of a multi-document manifest; plus `residual` units whose output no instance record explains. SOPS-encrypted files are never read, no manifest body is written, and the export is deterministic. A missing partner record is listed, not invented.

**The analysis** (`swhurl-v2 shadow --snapshot FILE --profile FILE`) previews every instance through the renderer and compares it with its own committed HelmRelease, then checks the whole snapshot's ownership: `kernel.Ownership` is the new plural form of `CheckOwnership` (which now uses it), so the report names **every** collision with all its owners rather than the first. The instance being compared is not exempt: its own committed Namespace and HelmRelease collide with its own preview, which is reported as one owner with two claims — the expected collision — while two owners on one object is a real conflict. A translation or rendering refusal makes that instance unavailable **with its reason** and leaves the others' comparisons intact.

**The pinned chart's default is verified, not assumed:** five instances commit no replica count, so `legacy` materialises the pinned chart's default and records it as the fact's source; the existing offline Helm render now renders the same values without a replica count and asserts the chart defaults to one, so a chart change that alters it fails a test rather than silently changing a preview.

**Result over this repository's own snapshot** (revision `2d4e06ed`, offline): 135 declared objects, 10 instances compared, 7 unavailable — every one a worker or job kind, each named — **no field differences** in the compared scope (the only changes are the two named intentional ones), and **no cross-instance ownership conflict**. That is the bridge's claim as evidence: for the HTTP workload facts in scope, v2's compiler and v1's committed output agree, while what is out of scope is named.

#### Phase-3 slice 4: live ownership (implemented 25 October 2026)

**Approved with the same instruction as slice 3.** The inventory said what the snapshot *declares*; it could not say whether the cluster holds it, which is the question a migration turns on.

**Contract.** `swhurl-v2 shadow --snapshot … --profile … --kubeconfig …` additionally reads, from the API server the kubeconfig names, every object a preview would create — the five compiled claims per instance — and reports each as `present`, `absent` (a successful 404) or `unavailable` with its reason. A failed read is never absence and never changes a comparison; an instance that cannot be previewed is not read at all. Without `--kubeconfig` nothing live is read and the list is empty. The adapter's stated kinds grew to cover the claims — `v1 Namespace` (cluster-scoped, so the path carries no namespace segment and a namespaced reference to it is refused), `v1 ResourceQuota` and `networking.k8s.io/v1 NetworkPolicy` — and `kube.RefOf` turns a compiled object's group, kind, namespace and name into a reference from that same table, so a caller never guesses a version.

**Scope, stated:** only the preview's own claims are read live. The other declared objects an instance's manifests contain — a Secret, a CronJob, a Stream, an image policy — are not read: some are sensitive, the adapter states no version for them, and existence alone would not change the ownership question. That is the honest limit of this check, and the report does not claim it as inventory coverage.

**Evidence, read-only, 25 October 2026, this repository's snapshot at `HEAD`:** 50 live reads across the 10 compared instances, **all present** — every claim a preview would create already exists, owned by the instance v1 runs today. That is the expected collision, now verified rather than assumed, and no read failed, so the ownership statement for those instances is complete for the claims.

**Files:** `adapters/kube`'s kind table, cluster scope and `RefOf` (with tests for the table, the cluster-scoped path and its refusal); `cmd/swhurl-v2/shadow.go`'s `--kubeconfig` mode, `liveRead` and the document's `live` list; tests over a TLS fixture server asserting present, absent, unavailable-with-reason and that a failed read is not reported as absence.

**Files and evidence:** `tools/swhurl/v2snapshot.py` and its FakeRunner tests (pinned revision, dirty tree irrelevant, missing record named, SOPS skipped, deterministic), the command catalogue, `Makefile` and `docs/commands.md` entries; `kernel`'s `Ownership`/`Conflict` and the refactored `CheckOwnership`; `cmd/swhurl-v2/shadow.go` and its tests (the fixture snapshot, a cross-instance conflict with both owners, an instance without records, residual units, refusals, reproducibility); `legacy`'s chart-default materialisation and the chart-render assertion. `make check-v2` (11 packages), `REQUIRE_HELM=1 make check-v2`, `make check` (1012 tests, including the command-catalogue checks) and `make v2-report` pass.

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

**Structural problem:** rendering decides what should exist ([`RenderInstance`](../v2/kernel/render.go)) and [`shadow.CompareHTTP`](../v2/shadow/http.go) compares two desired documents. Neither can say what a cluster runs, and v1 answers that question by mixing `kubectl` reads into rendering. This phase's five sentences are decisions, so they live in a pure package with the reads behind one adapter.

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

**What this slice does not do, and what therefore reopens the work:** deriving the target's refs, selector and commit from compiled output (the [next slice](#second-observation-slice-the-target-from-compiled-output-implemented-25-october-2026)); a credential source (kubeconfig is YAML; an in-cluster token is a different path); an operator-facing report envelope and exit codes; pod-level facts (phase, restarts, events, terminating and evicted pods); ownership against foreign objects; and any live read, which needs phase 6's approval.

#### Second observation slice: the target from compiled output (implemented 25 October 2026)

**Approved 25 October 2026 with the following slice.** The first slice proved the semantics, but every ref, selector, digest and replica count had to be handed in. This one sources them from what v2 already compiles.

**Structural problem:** the chart's conventions are facts about the chart, not about the instance. A single controller's workload is named after the release, and its pods carry `app.kubernetes.io/instance=<release>` and `app.kubernetes.io/controller=<controller>`. Encoding those by memory would read another workload after a chart change; encoding them with no check would do the same silently.

**Contract.** [`observe.TargetOf`](../v2/observe/target.go) takes the compiled HelmRelease bytes, the unit that owns the instance, and the intended source revision, and derives the rest: the namespace and release name from `metadata`; the HelmRelease ref; the workload as `apps/v1 Deployment` **named after the release**; the pod collection as `v1 Pod` in the namespace; the selector as the chart's two pod labels; the container `main`; the pinned digest; and the replica count stated by the controller. It then validates the result with the same target rules as before, so a derived target cannot be one the projection would refuse.

`observe.UnitRef` reads the unit's identity from the compiled unit document, so the unit naming convention is not repeated in a command. Two facts stay the caller's: which unit owns the instance and which source revision Flux should have applied — neither is in the HelmRelease, and inventing the revision would defeat the fact it feeds.

**Refusals, fixed:** `single_utf8_json_required`, `metadata_required`, `single_controller_required` (not exactly one controller named `main`), `controller_type_unsupported` (anything but the chart's default Deployment), `container_required`, `digest_required` (missing, or not a sha256 digest), `replicas_required` (absent or negative). A shape the compiler does not render is refused rather than guessed: a described workload the instance does not have is worse than no description.

**The chart pin.** `TestCompiledManifests` renders the golden production HelmRelease with the pinned chart, then asserts that the derived workload name appears in the render and that every selector term is on the rendered pods. It caught its own first assumption: the chart names the Deployment `<release>`, not `<release>-main`. A chart change that moves a name or label now fails that test instead of a live read.

**Files and evidence:** `observe/target.go` and `observe/target_test.go` (both golden compilations, every refusal, the unit identity read from its document) and the extended `cmd/swhurl-v2/chart_test.go`; `archtest/api.txt` gains two functions. `make check-v2` (10 packages) and `REQUIRE_HELM=1 make check-v2` pass with the chart render and offline Kustomize run; `observe` coverage is 96.9%. Offline only: nothing was read from a cluster.

#### Third observation slice: a credential source and an `observe` command (implemented 25 October 2026)

**Approved 25 October 2026 with the target derivation.** The projection and the derivation were both inert without a way to point them at a server.

**Decided: the credential source is a kubeconfig in its JSON form**, the document `kubectl config view --raw -o json` prints. `adapters/kube.Connect` reads it and returns one endpoint, its authority and one user's material; `Connection.Reader` builds the reader. v2 reads no YAML: a YAML parser is a dependency decision the `stdlib-only` rule reserves for the operator, an in-cluster service-account token is a different path again, and the operator's existing tool already understands their file. Nothing is copied or kept, and no part of a kubeconfig reaches a diagnostic: the error carries a fixed reason only. **What reopens it:** an approved YAML dependency, or a v2 that runs inside the cluster.

**Refusals, fixed:** `single_json_object_required`, `current_context_required` (no current context, or one the file does not define), `cluster_required`, `user_required`, `server_required`, `invalid_ca`, `invalid_client_certificate`, `invalid_client_key` (material that is not base64), `unsupported_credential` (an `exec` or `auth-provider` plugin, which would run something else), and `credential_required` (no token and no client certificate). The endpoint rule is the reader's as well: plain http is refused, so a token is never sent in the clear.

**The command.** `swhurl-v2 observe --instance FILE --release FILE --profile FILE --revision COMMIT --kubeconfig FILE [--source NAME]` compiles the instance in memory, reads the unit's identity from the compiled unit document, derives the target, reads the three objects and the pod list with a 20-second bound, and writes the report to stdout with a one-line summary on stderr. **Exit 0 means a report was produced, whatever its verdict**; the verdict is in the document, so a script cannot mistake "not running" for a failed command. Exit 2 is usage, validation or a refusal (including a kubeconfig it cannot use) and exit 1 is I/O.

**The report document** (`observe.EncodeReport`): `kind: v2-observation`, `version: 1`, the source and observation time, the verdict, the seven findings in their fixed order with their reasons, the assessed and unassessed findings, the fixed not-covered list, and the target. Identical reports encode identically, every list is present even when empty, and no field is HTML-escaped.

**Ownership.** The adapter owns the kubeconfig's format and the connection; the command owns compilation, the target and the sequencing of reads; `observe` owns the projection and the document's shape. `observe.IsPositive` exposes the verdict's own sign table, so the command's summary cannot drift into a second copy of the words.

**Tests:** the kubeconfig reader's every shape and refusal (with synthetic material, no real keys, and a check that an error never echoes the file), the report document's stability and its non-null lists, and the command end to end against a TLS test server: the four stated paths, the bearer token on every request, a `403` leaving the fact unassessed and the verdict `unknown`, a `404` reading as absence and deciding `not_running`, a named source, a bad kubeconfig exiting 2 with no report, and a missing `--revision` exiting 2.

**Live evidence, 25 October 2026 (read-only; recorded in [current state](current-state.md#first-v2-observation-of-a-live-instance-25-october-2026)):** `observe` ran against the live cluster with the operator's kubeconfig in its JSON form. For `demo-orders`/prod it reported `unit reconciled`, `unit.revision applied` (the pushed commit), `release settled`, `workload present`, `ready ready`, `replicas as_intended`, and `image different/digest` — the artifact v2's descriptor pins is a fixture digest, not the running image, so the verdict was `not_running` for exactly that reason. For staging it added `replicas different/available_replicas` (two intended, one available). Both reports are what the contract says they should be: readiness and a settled release are reported as what they are, and the artifact and replica gap are named rather than assumed away. This is not a parity claim about v1; it is v2 reading a live instance honestly.

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

#### Calls between apps: resolver and pair rendering (implemented offline 25 October 2026)

**Decided with the operator on 25 October 2026:** both-sided consent, direct in-cluster egress to a declared callee, the projected audience token, and a **per-instance auth ConfigMap** with one platform ConfigMap plus replication as the deferred end state (see the [gap list](#6-deliberately-deferred)).

**What v1 does, and what v2 now does:**

| Fact | v1 | v2 |
| --- | --- | --- |
| Consent | Caller `calls`, callee `allows` (app or `team:<team>`); connected, pending, or refused | The same, as a pure resolver over supplied declarations |
| Address | `SWHURL_SERVICE_<CALLEE>_URL`, callee in the caller's own environment | The same variable; the value is rendered in full rather than through the kubelet's `$(SWHURL_ENV)` expansion |
| Credential | Projected service-account token, `audience: <callee>`, 600 s, kubelet-rotated | The same projection at `/var/run/swhurl/tokens/<callee>/token` |
| Verification | Callee checks the token with `SWHURL_AUTH_ISSUER`/`SWHURL_AUTH_KEYS` | The same two variables, **from a ConfigMap in the instance's own namespace** and consumed with `envFrom`; the keys stay in the platform profile, so they remain identity-bearing |
| Network | Callee admits each caller's pods; the caller's own egress is unrestricted | Callee admits each caller's pods on its port; the caller reaches each declared callee's pods on the callee's port, and nothing else — **stricter than v1** |

**Ownership.** [`calls`](../v2/calls/calls.go) (layer 0, pure) owns the consent rule and the three outcomes; it decides nothing else and reads nothing. `kernel` owns rendering of a *connected* pair it is given — consent is never re-decided there — and refuses a callee without the platform's keys, because it could admit no caller it could check. The caller supplies the declarations (in a later slice, from the snapshot).

**Refusals, fixed:** `invalid: allows: invalid_team`, `invalid: allows: invalid_app`, `invalid: calls: invalid_app`, `invalid: calls: repeated_instance`; and from rendering, `unsupported: auth` (a callee without keys) and `invalid: auth.keys` (a key set that is not a JWKS). A waiting declaration is **not** an error: `callee_absent` and `no_consent` render nothing for that pair.

**The identity change, explained again:** the profile now covers `auth.issuer` and `auth.keys`, so its canonical form and ID changed once more; the two golden compilations changed only in their `id` and `inputs.profile` lines, and `profile-identity.json` was recomputed independently in Python. The keys are rendered into the ConfigMap, so they must be identity-bearing: a key rotation is a new compilation, which is correct.

**Evidence, offline:** 12 packages pass, including the resolver's connected/team/pending/refused/malformed/deterministic cases and the renderer's pair test (address, token projection, `SWHURL_ALLOWED_CALLERS`, the ConfigMap and its claim, the caller's egress rule on the callee's port, the callee's ingress rule per caller, and the refusal without keys). `make check-v2`, `REQUIRE_HELM=1 make check-v2` and `make check` pass.

**The resolution document and delivery (implemented offline 25 October 2026).** `swhurl-v2 resolve --snapshot FILE` reads the snapshot's declarations and writes a `v2-calls` version-1 document: the resolved `pairs` (each with the callee's port, so a caller needs nothing else), the `waiting` declarations with their reasons, the `refused` ones, and the revision they were read at. `swhurl-v2 compile --calls FILE` selects the compiled instance's own edges from it — what it calls and who may call it, in its own environment — and renders only those; `Inputs.calls` records the resolution's identity, empty for an instance in no pair. The compilation ID still hashes rendered output, so the two goldens changed only by gaining `"calls": ""`, and a resolution that changes a file changes the ID with it.

Over this repository's own snapshot the resolver reports **9 connected pairs** — `demo-shop → demo-orders` in both environments, `demo-shopper → demo-shop`, and the three `swhurl-try-1x` pairs in both — with nothing waiting and nothing refused, which matches the declarations v1 holds.

**What remains:** the bridge's `calls` section (it still names them as gaps; the resolver's output is the input it needs) and the first migration itself, which is a **handover**: the predecessor owns the instance's records and unit today, so v2 taking it over means removing v1's files and unit, letting Flux prune, and then applying v2's output — with a `git revert` as the rollback. That is a live, ordered, operator-confirmed step, not a rendering one.

Existing-instance migration follows the [documented staged Flux ownership handover](architecture.md#flux-units). Do not change the old unit's path in the cutover commit or permit it to prune handed-over resources.

Stateful migration additionally requires demonstrated backup/restore, explicit resource-retention rules and operator approval. It must not be the first migration exercise. Define recovery per migration; do not imply that rolling back an image reverses data migrations.

#### Phase-2 slice: signed-in routes (`authenticated-web`) — implemented offline 25 October 2026

**Approved 25 October 2026 with the four decisions:** the host is derived, the TLS issuer and sign-in middleware are the platform's, and the app's policy admits only its ingress controller.

**Contract.** An instance's `grants.exposure` is now `private` or `authenticated-web` (an empty exposure means private where the renderer is used directly; `public` stays refused as deferred). An exposed instance renders one more file and one more ownership claim than a private one, and its policy changes:

| Fact | Owner | Rule |
| --- | --- | --- |
| Host name | Platform (profile) | Derived as `<environment>-<application>.<baseDomain>`; no instance stores a host, so two instances cannot disagree about one name's shape |
| TLS | Platform (profile) | `cert-manager.io/cluster-issuer: <issuer>` on the Ingress, secret `<application>-tls` |
| Sign-in | Platform (profile) | `traefik.ingress.kubernetes.io/router.middlewares: <middleware>` (the shared ForwardAuth) |
| Backend | Instance + workload | Service `<application>` on the workload's port, path `/` (`Prefix`) |
| Ingress controller's reach | Platform (profile) | The policy admits pods matching the profile's labels in its namespace, on the app's port only; egress stays denied |

**Refusals, fixed:** `unsupported: grants.exposure` for any exposure other than private or authenticated-web (including `public`, still deferred), and `unsupported: routing` when a profile states no routing block and an exposed instance is compiled — a preview fails rather than inventing a host, an issuer or a middleware. A configured routing block is validated field by field (domain, labels, issuer, middleware reference, ingress labels).

**The identity change, explained.** A profile's identity is a canonical encoding of the facts it owns, so adding routing changes the profile's ID and therefore every compilation ID. That is intended: the compiled resource bytes for the two golden private instances are unchanged (the only diff is the `id` and `inputs.profile` lines), and `profile-identity.json` was recomputed independently in Python rather than copied from Go's output. Nothing has been published or migrated, so no released identity is disturbed; a future profile change that alters rendered output must change the ID in the same way.

**Evidence, offline:** `kernel` tests render the route (host, class, both annotations, TLS secret, path, backend), assert the policy's single ingress rule and denied egress, and refuse an exposed instance without platform routing and `public` exposure; the chart render still pins the Service name our route targets. The bridge renders routes for the inherited fixture (still `authenticated-web`), and its comparison is unchanged: routes remain outside the compared scope and the report still lists them as unassessed. **Live ownership over this repository's snapshot:** 60 live reads across the ten compared instances, **all present** — the six claims per instance, including the Ingress each v1 instance already has.

**Not claimed:** observation does not verify a route (Ingress readiness, certificate state), and no exposed instance has been compiled into the cluster. `public` exposure, per-instance host overrides and route-level telemetry remain deferred.

#### Proposed slice: SQLite storage (decision brief, 25 October 2026 — not approved)

The first migrated app (`swhurl-try-11` staging) runs on v2, and its two calls stay blocked because its callees need domains v2 lacks: `swhurl-try-10` keeps SQLite, `swhurl-try-12` publishes an event. Storage is the larger unblock — four of the ten web instances keep SQLite.

**What v1 does, from its own records and rendered objects:**

| Fact | v1's mechanism |
| --- | --- |
| Requirement | The *release* states `database: sqlite`; a worker or job may not |
| Volume | The chart's `persistence.data`: a PVC with the instance's `storageClass` (`local-path-retain`), `ReadWriteOnce`, its `size` (`1Gi`), `retain: true`, mounted at `/data` |
| Path | `DATABASE_PATH=/data/app.db` |
| One writer | `replicas: 1` and `strategy: Recreate` — never two pods on one database file |
| Removal safety | The namespace carries `kustomize.toolkit.fluxcd.io/prune: disabled`, so deleting the instance's files does not delete its data; `make destroy-data` deletes it deliberately |
| Quota | Storage is **not** in the ResourceQuota (pods, CPU, memory only) |
| Backup | One Secret per instance (AWS keys from platform SOPS) and a CronJob at `13 4 * * *` running the platform's backup image with `backup-job-sqlite`, the data volume mounted, an S3 prefix per instance, an age recipient for encryption, and **egress through the proxy** |
| Restore | `make restore-sqlite APP= ENV= CONFIRM=<app>/<env>`: stops the app, replaces the live database from the newest backup, needs the age key and S3 |

**The five decisions:**

1. **Where the facts live.** *Recommend*: the release states `database: sqlite` (or nothing); the instance states the volume **size** — a data volume's size is a decision, not a default; the profile states the **storage class** (a platform choice, `local-path-retain`). All three are identity-bearing, so the release, instance and profile canonical forms grow and the goldens regenerate once, explained.
2. **Removal safety.** *Recommend both v1 guards*: the retained class **and** the namespace's `prune: disabled`, because removing a v2 instance means deleting its files, and files are not data. The report must then say plainly that removing a database instance leaves its data behind, and that deleting it is a separate, explicit action.
3. **Backups.** This is the first thing that **must leave the cluster**, which is exactly the case the 25 October egress decision deferred to the proxy. *Recommend splitting*: this slice renders the volume, the path, the single writer and the removal guards; `backup` comes with the **egress-proxy slice** (proxy variables, per-instance port allocation, and the backup CronJob using it), and `restore` after that. An unlogged S3 rule would be weaker than v1, and a per-instance database with no backup is a named risk, acceptable only for a throwaway.
4. **Restore.** *Recommend*: an operator command in the same shape as v1's — it stops the app, replaces the live database from the newest backup, and requires an explicit `CONFIRM=<app>/<env>` because it destroys data. It is a live action every time, never part of a reconcile.
5. **Which instance proves it first.** *Recommend* a **throwaway** (`swhurl-try-v2-4` with SQLite), not `try-10`: it proves the volume, the single writer, restart survival and that removal leaves data behind, without touching the chain the migrated app depends on. `try-10` then becomes the migration that completes `try-11`'s calls — which still leaves `try-12`'s event for the events slice.

**What this slice would not do:** events (so `try-12` still waits), backup or restore, a storage quota, or a size change on a live instance (v1's `app-scale` handles storage growth; v2 would refuse rather than resize).

#### SQLite storage: the volume (implemented and proved live 25 October 2026)

**The four decisions as approved:** the release states the database, the instance states the size, the profile states the class; both removal guards; the volume now with backup and restore deferred to the egress-proxy slice; restore stays a `CONFIRM`-gated command; a throwaway proves it before `try-10`.

**Facts and rendering.** `ReleaseBundle.database` is `"sqlite"` or empty (the image's requirement); `InstanceSpec.storage.sizeMiB` is the instance's size, required for a database and refused without one; `PlatformProfile.storage.class` is the platform's class. A SQLite instance renders v1's shape: a `persistentVolumeClaim` at `/data` with the class, `ReadWriteOnce`, the size and `retain: true`; `DATABASE_PATH=/data/app.db`; `strategy: Recreate`; and a namespace annotated `kustomize.toolkit.fluxcd.io/prune: disabled`. **Refusals:** a volume with no database, a database with no size or no class, more than one replica ("a database admits one writer"), and any database other than `sqlite`. The new checks run after the existing ones, so no existing refusal's precedence moved.

**Identity:** the release, instance and profile canonical forms gained these facts, so their IDs changed and the fixtures' identities were recomputed independently in Python; the goldens were regenerated once and the independent escaping vector updated. Nothing released exists, so no consumer of an old ID is disturbed.

**Evidence, live (`swhurl-try-v2-4`, applied and removed the same day):** the PVC bound at `1Gi` on `local-path-retain`; the `demo-orders` image — which crash-looped without storage on 25 October — ran `1/1`; a deleted pod was replaced on the same volume; `observe` reported `running`; and after the instance's files and unit were removed, Flux left the namespace and the claim bound, so the data survived the removal by design. **Not exercised:** reading data back through the app, backup, restore, resize.

**Deferred, with the reason:** backup and restore wait on the **egress proxy** — a backup is the first thing that must leave the cluster, and the 25 October egress decision made the proxy the path for that; a per-instance database with no backup is a named risk that is acceptable only while it is a throwaway. Volume growth is refused rather than resized.

#### Proposed slice: the egress proxy (decision brief, 25 October 2026 — not approved)

Backup and restore, and any app that reaches the internet, need the proxy: it is the path the 25 October egress decision named for traffic leaving the cluster. v2 denies all egress today, which is why a database has no backup.

**What v1 does, from its own records and generated platform file:**

| Fact | v1's mechanism |
| --- | --- |
| Addressing | One **port per instance** on a shared Squid (`egress.egress.svc.cluster.local:<port>`, 30101 and up), kept in `instance.yaml` as `allocations.egressPort`, so regenerating never moves it |
| Client variables | `HTTP_PROXY`/`HTTPS_PROXY` at that port, `NODE_USE_ENV_PROXY=1`, `JAVA_TOOL_OPTIONS` with the JVM proxy properties, `NO_PROXY` = `localhost,127.0.0.1,.svc,.cluster.local,$(HOST_IP)`, and `HOST_IP` from the downward API |
| Identity | The proxy identifies the caller **by the port it arrived on** (`note instance <app>-<env>`), not by anything the app sends, and logs `instance=… granted=… method=… host=… status=…` |
| Grants | `egress: [host:port]` in `instance.yaml`; rendered as a HelmRelease annotation. **The proxy still allows every host** (`http_access allow all`) and logs whether a grant covers it, so enforcement can be judged from logs first — v1's deferred stage E3 |
| Configuration | `platform/egress/proxy.yaml` is **generated from all instances** by `make app-sync`: the Squid config, the Service's ports, and a policy admitting each port only from its instance's namespace |
| Backup | The backup CronJob uses the instance's own proxy port (`HTTP_PROXY`) |

**A defect this brief found in what v2 already renders.** v2 denies all egress and, for calls, admits only the callee's pods. **DNS is denied** with it: a v2 instance cannot resolve `…svc.cluster.local`, so its calls would fail even where the callee admits it. Any egress slice must add a DNS rule (UDP and TCP 53 to the cluster's DNS) for every instance allowed to reach anything — and the calls slice stands corrected.

**The five decisions:**

1. **Who owns the port.** *Recommend a generated platform allocation*, not an instance field: the port is proxy configuration ("who is asking"), it must be stable, and the proxy's own config is generated from every instance anyway. The allocation is one document, produced from the snapshot like `resolve`, and `compile --egress FILE` renders the instance's variables from it. A new instance's port is the lowest free one in a deterministic order; an existing instance keeps its port because the document is in Git.
2. **Enforcement.** *Recommend enforcing grants*, unlike v1's log-only stage: v2's model already states them, and "no unlogged, ungranted egress" is the reason the proxy exists. The cost is that a grant must be right before an app depends on it; the alternative — v1's `http_access allow all` with `granted=yes|-` logging — is the lower-risk first step if enforcement feels too strict for the first slice.
3. **What v2 renders, and where.** The instance side (variables, annotation, policy egress to the proxy and DNS) belongs in the compiler. The proxy side is a **platform** document v2 renders from the allocation and the grants — the first platform object v2 would own, and the reason the slice needs its own approval.
4. **The boundary with calls.** Calls stay direct pod-to-pod (already rendered and stricter than v1); the proxy is for leaving the cluster. Both rules coexist, and both need the DNS rule.
5. **Backups and restore.** With the proxy in place, the backup CronJob does what v1 does (its own proxy port, the data volume, an S3 prefix, an age recipient); restore stays a `CONFIRM`-gated command. *Recommend* that as the slice immediately after, so a database is not left without a backup.

**Not in this slice:** a per-instance proxy *deployment* (v1 shares one Squid), proxy-side caching or TLS interception (neither exists), and egress for workers and jobs until those kinds are rendered.

#### Egress, part one: the allocation and the instance side (implemented offline 25 October 2026)

**The five decisions as approved:** a platform-owned generated allocation, grants enforced (part two), v2 renders the proxy as a platform object (part two), calls stay direct, backup after. This part delivers everything that belongs to an instance; the proxy itself is next.

**Facts and rendering.** `PlatformProfile.egress` states the proxy's DNS name and the pods that serve it; `PlatformProfile.dns` states the cluster's name service; both are identity-bearing. `swhurl-v2 egress --snapshot FILE [--previous FILE]` allocates one proxy port per instance — **seeded from v1's existing `allocations.egressPort`**, so a migrated instance keeps the port its grants and logs already name, and a new allocation can never land on a port the proxy serves for someone else; a new instance takes the lowest free port above 30101. `compile --egress FILE` renders that instance's side: `HTTP_PROXY`/`HTTPS_PROXY` at its port, `NODE_USE_ENV_PROXY`, the JVM's proxy properties, `NO_PROXY`, `HOST_IP` from the downward API, the declared grants as a `platform.swhurl.com/egress` annotation for the proxy's log, and a policy that admits the proxy's pods on that port and **DNS**.

**The calls defect is fixed.** Any instance that may reach anything — a declared callee or the proxy — now gets a DNS egress rule (UDP and TCP 53 to the cluster's DNS). Without it a call could not resolve its callee's name, which is what the previous slice would have done.

**Refusals:** `invalid: egress.port` (privileged or out of range), `unsupported: egress` (an allocation when the profile states no proxy), and the allocation document's own refusals (wrong kind or version, a missing revision, an instance with no allocation).

**Identity:** the profile gained `egress` and `dns`, so its canonical form and ID changed once more; the goldens moved in their `profile`/`id` lines, and `profile-identity.json` was recomputed independently. The allocation is not identity-bearing: it selects an instance's port, which is rendered into its files, so a changed allocation changes the compiled output and therefore its compilation ID.

**Boundary, stated plainly:** this part renders what an instance must do, but **the proxy is still v1's**. A v2 instance compiled with `--egress` today would point at a port whose proxy configuration v1 generates from *its* instances. So `--egress` must not be used for a v2 instance until part two replaces the proxy's configuration — the docs say so, and the allocation already keeps the ports stable for that moment.

**Evidence:** the allocation over this repository's snapshot keeps every instance's v1 port (`demo-orders`/prod 30111, `shop`/prod 30112, and so on); tests cover the proxy variables, `HOST_IP`, the JVM properties, the grants annotation, the DNS and proxy rules, the refusals, port stability across runs, a migrated instance keeping its port, and a new instance taking the next free one. `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check` and `make v2-report` pass. Offline only.

**Remaining:** part three is the backup CronJob, which uses the instance's port, and restore after that.

#### Egress, part two: the proxy's configuration (implemented offline 25 October 2026)

**The decisions as approved:** v2 renders the proxy's **generated** objects only — the values ConfigMap and the per-port NetworkPolicy — from the allocation; their values are JSON, which is YAML, so the standard library writes them exactly; the render is a pure `kernel.RenderProxy`, with the allocation staying a CLI document and the new `swhurl-v2 proxy` command writing through the same files adapter and `apps/` guard as `compile`; `--out DIR` writes `DIR/<egress namespace>/{configmap.json,networkpolicy.json}` with **no profile change and no kustomization**, because that folder is not yet a whole Flux unit; grants are enforced; and the facts v2 and the platform's own HelmRelease each state are tied by an offline test rather than by memory.

**Facts and their owners.** The platform profile already states the proxy's address, namespace and pods, and the cluster's name service; this part added no field. The allocation decides which instance sits on which port and carries the hosts it declares. `kernel` renders Squid's lines, the two object names, the Service ports and the policy's shape; the proxy's deployment — chart version, image digest, replicas, probes, the Namespace and the Flux unit — stays the platform's hand-written files, which this part does not touch.

| What crosses the seam | Instance side (part one) | Proxy side (this part) |
| --- | --- | --- |
| the port: the caller's identity | `HTTP(S)_PROXY` at that port; the policy's proxy rule | `http_port <port> name=<app>-<env>`, `acl <app>-<env> myportname`, `note instance` |
| the grants | the `platform.swhurl.com/egress` annotation | `dstdomain` hosts per port, and one `http_access allow` per instance and grant port, before `http_access deny all` |
| the proxy's address and pods | the profile's address and labels in env and in the policy | the profile's namespace and labels in the objects and the policy |
| the cluster's DNS | the instance's egress rule on 53 | the proxy's own egress rule on 53 |

**What it renders.** `swhurl-v2 proxy --allocation FILE --profile FILE [--out DIR]` writes a `v2-proxy` version-1 document: the revision the allocation was taken at, the profile's identity, the two files and their inventory — both objects claimed by `platform`, so an instance or a second platform writer colliding on them is reported by the existing ownership rule. The ConfigMap is the one the hand-written HelmRelease reads through `valuesFrom`; the probe rules, the `logformat` and the note keys are v1's, byte for byte, because ClickStack's queries read them.

**The proxy's policy is v1's, and the default deny is the instance's.** The rendered NetworkPolicy states `policyTypes: [Ingress]`: one rule per allocation, admitting that instance's namespace on that instance's port and nothing else. The default deny for egress that the operator decided (25 October 2026) is the *instance's* policy, which part one renders as `[Ingress, Egress]` with no rules until a call or a proxy port gives it one; the proxy's own namespace keeps the state the platform has today, which a later platform-wide decision would change, not this slice.

**Refusals:** `unsupported: egress` (the profile states no proxy), `invalid: egress.port` (out of range, or two instances on one port — the port is the identity, so a shared port would let one instance answer for another), `invalid: egress.grant` (not a host name with a port from 1 to 65535; IP addresses, paths and schemes included), the record naming rules (`invalid: application`, `unsupported: environment`, reused so one rule names an instance everywhere), and the allocation document's own (`unsupported: egress`, `invalid: egress: revision_required`). All appended after the existing checks; nothing existing moved.

**Identity: none changed.** No profile field and no record gained a fact, so the profile's canonical form, its ID, both golden compilations and every instance identity are untouched and nothing was regenerated — the first slice in this workstream that adds behaviour without moving an identity. The allocation stays non-identity-bearing: a changed port changes the rendered proxy file and the instance's own compilation output, which is the evidence.

**Two ties rather than two guesses.** `profile.egress.address` encodes the chart's Service name and namespace; the ConfigMap's name and key are what the HelmRelease's `valuesFrom` reads; and the pod labels decide whether either policy selects anything. `TestProxyManifests` parses `platform/egress/helmrelease.yaml` offline (`kubectl create --dry-run=client -o json`), renders the pinned chart from v2's values and that file's own values, and asserts all four plus one Service port per allocation. Each was mutation-checked and fails: a renamed ConfigMap, another Service in the address, labels the chart does not set, a health port the HelmRelease does not probe, and `http_access allow all` restored in place of the denial.

**Evidence, offline, over this repository's snapshot at `900b11bf`** (`make v2-snapshot` → `egress` → `proxy`): all 16 instance ports, all 16 `note instance` identities, all 16 Service ports and all 16 (namespace, port) policy pairs are what the platform runs today; every line of v1's Squid configuration survives except its three comments and `http_access allow all`, which the enforcement line replaces; the `logformat` is byte-identical. The rendered policy differs only in selecting the profile's pod labels rather than v1's hand-written `app.kubernetes.io/instance` — both on the chart's pods, which the tie test asserts. **A finding for the cutover:** no instance in this repository declares a grant, so an enforced proxy refuses every outbound host for every instance. Replacing v1's generator therefore needs the grants recorded first (v1's `granted=-` history, [stage E3b](plan.md#stage-5-egress-grants)); that cutover is its own live decision, not part of this slice.

**Not in this slice:** the proxy's deployment (chart version, image digest, replicas, probes, Namespace), the unit and its registration, any live change, and any change to the instance side. Nothing was applied: `platform/egress/` is untouched and v1's `make app-sync` still generates the live file.

**Checks:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check` and `make v2-report`; `kernel` coverage 93.0% (was 91.5%) with `kernel/proxy.go` at 100%, CLI 85.2% (was 84.9%). Offline only: no cluster, credential or kubectl that talks to a server was used.

#### SQLite backup (egress part three): decision brief and implementation, 25 October 2026

The [storage slice](#sqlite-storage-the-volume-implemented-and-proved-live-25-october-2026) named the risk: a per-instance database with no backup. The proxy now renders, so the job that must leave the cluster is the next thing, and it is the first thing v2 renders that carries a **credential** and a **second pod** in the instance's namespace.

**What v1 does, from its own generated file and records:** for a release that declares a SQLite database, `apps/model/backup.py` writes `backup.yaml` beside the instance — a Secret `<app>-backup` whose two values are Flux substitution references `${BACKUP_AWS_ACCESS_KEY_ID}`/`${BACKUP_AWS_SECRET_ACCESS_KEY}`, and a CronJob `<app>-backup`:

| Fact | v1's mechanism |
| --- | --- |
| Image | `ghcr.io/samclement/swhurl-backup:src-069ed3…@sha256:…`, args `backup-job-sqlite` |
| Schedule | `sha256(namespace)` → a minute between 03:35 and 04:24 UTC (`demo-orders`/staging is `13 4 * * *`), so a migration keeps its slot; MongoDB's job is at 03:30 |
| Suspend | Every SQLite job follows `spec.suspend` of `platform/backup/mongodb.yaml`, read at render time: one switch (`make app-sync`) |
| Credential | Flux substitutes the upload-only key from the `backup-writer` Secret in `flux-system` into the per-instance Secret; v2 would never see a value |
| Destination | `s3://…/app-sqlite/<namespace>/`, with `BACKUP_SOURCE=<namespace>/<app>`, the age **public** key as `AGE_RECIPIENT`, and the job encrypts before uploading |
| Network | HTTP(S)_PROXY at the instance's own egress port, `HOST_IP`, `NO_PROXY`; the pod carries `platform.swhurl.com/backup: "true"`, **not** the app's instance label, so today no NetworkPolicy selects it |
| Pod | the app's claim mounted at `/data`, scratch 1 GiB and `/tmp`, the app's uid, no service-account token, 10m/64Mi requests, 256Mi limit, `backoffLimit 2`, 900 s deadline |
| Dependency | the instance's unit depends on `platform-backup`; that unit holds the key and the MongoDB job |
| Quota | v2 refuses `over_quota` for `replicas + 1` (rolling update) and **counts no backup pod** — a backup refused by quota is the risk this slice exists to remove |
| Run now / freshness | `make backup-sqlite` lists CronJobs by the platform's backup label and runs each with `kubectl create job --from`; `verify-platform` judges freshness from S3, but from the **app records**, so a v2-owned instance is invisible to it |

**What this slice would render,** for a database instance, in its own namespace: `Secret/<app>-backup` (two substitution references), `CronJob/<app>-backup` (the table above, one object per file as v2 does), a second NetworkPolicy for the backup pod, the unit's `dependsOn: platform-backup` and its `postBuild.substituteFrom`, and two more inventory claims. It refuses a database instance when the profile states no backup facts, when a document would contain a `${…}` the substitution does not cover, and when the pods the instance needs (app, rolling update, backup) do not fit the quota.

**The decisions:**

1. **Where the platform's backup facts live.** *Recommend a `PlatformProfile.backup` block* — image and digest, the S3 prefix, the age recipient, and the name and namespace of the Secret it substitutes from — because they are what the platform owns and every database instance shares, exactly as `egress` and `dns` are. Cost: an identity change once more, the goldens moving in their `profile`/`id` lines with the profile identity recomputed independently. *Alternative:* a `--backup FILE` document like `--calls`, no identity change, but a second place for platform facts.
2. **The upload credential.** *Recommend keeping Flux substitution*, the mechanism the platform already runs: the rendered Secret holds `${…}` references, the instance's unit gains `postBuild.substituteFrom` naming `backup-writer`, and the compiler refuses any other `${` in a substituted instance's documents (the escaping lesson, made a rule). v2 stays credential-free. *Alternatives:* the platform copying the key into each namespace (new machinery and a second writer to app namespaces); per-instance SOPS (v2 has no SOPS support, so a credential adapter — separate approval). What reopens this: v2 owning secret material at all.
3. **The suspend switch.** *Recommend `backup.suspended` in the profile*: one reviewed Git fact instead of v1's render-time read of another unit's file. Pausing then changes only database instances' CronJobs (the compilation ID hashes output), while the profile's identity moves for every compilation that records it — stated, and the reason a pause is one edit plus a recompile.
4. **The backup pod as a second workload.** *Recommend both consequences*: a second NetworkPolicy selecting `platform.swhurl.com/backup: "true"` that admits DNS and the instance's own proxy port (so the pod that reaches S3 is the one pod whose egress is least restricted today), and `over_quota` counting `replicas + 1 + 1` for a database instance. *Alternative for a smaller step:* v1 parity — no policy, quota unchanged — which leaves the backup pod unrestricted and able to be refused by quota.
5. **The schedule.** *Recommend keeping v1's derivation* (the namespace's hash into 03:35–04:24 UTC) so a migrated database keeps its slot, with a tie test against v1's committed CronJob rather than a new per-instance field.
6. **Restore, and freshness.** Not in this slice: restore scales the app to zero, streams `age -d` into a pod on the claim and moves files — an effect that needs its own approval, adapters and a live proof on a throwaway. *Recommend the slice after it be a read-only backup observation* (the CronJob exists, when it last succeeded, how old the newest archive is), because v1's freshness check reads app records and cannot see a v2-owned instance at all.

**Not in this slice:** restore, any live run, the MongoDB job, changes to `platform/backup/`, S3 credentials or the bucket, and any v1 change.

**Checks it would owe:** the rendered Secret and CronJob compared field by field with v1's committed `apps/demo-orders/staging/backup.yaml` (the evidence that a migration is a drop-in), the schedule slot tie, the `${…}` refusal, the quota cases, the second policy's rules, the unit's dependency and substitution, and the profile identity recomputed independently.

**Implemented offline 25 October 2026, all six decisions as proposed.** A database instance now renders its backup: `backup-secret.json`, `backup-cronjob.json` and `backup-networkpolicy.json` beside its other files, `platform-backup` in its unit's `dependsOn`, and `postBuild.substituteFrom` naming the profile's writer Secret.

| Fact | Owner | What is rendered |
| --- | --- | --- |
| Job image, S3 prefix, writer Secret, suspension | `PlatformProfile.backup`, validated as a block that states all of its facts or none. The age recipient is `PlatformProfile.sops.recipient`, which the job shares with every SOPS file v2 encrypts | the job's image by digest, `BACKUP_S3_URI`, `AGE_RECIPIENT`, the unit's `substituteFrom`, `spec.suspend` |
| The upload key | the platform's `backup-writer` Secret, reached by Flux substitution | two references in the job's Secret — under the names the AWS CLI reads (`AWS_ACCESS_KEY_ID`), pointing at the platform's (`${BACKUP_AWS_ACCESS_KEY_ID}`); v2 never holds a value |
| Proxy address and port | the profile and the allocation | `HTTP(S)_PROXY`, `NO_PROXY`, `HOST_IP`, and the backup pod's own policy |
| Database path, claim and uid | the release and the instance | `/data/app.db`, the app's claim, the app's user |
| The schedule | the namespace's hash, as v1 derives it | a minute between 03:35 and 04:24 UTC, so a migration keeps its slot |

**The tie.** `TestBackupMatchesTheCommittedJob` compiles the new SQLite fixture and compares what it renders with v1's committed `apps/demo-orders/staging/backup.yaml`, parsed offline with `kubectl create --dry-run=client -o json`: the Secret is identical, and the CronJob is identical field for field — every env value, resource, volume, security context, policy field and label — except the image, which v2 pins by digest alone (`repo@sha256:…`) where v1 names the tag beside it, the same artifact. Four namespaces keep v1's schedule slots (`demo-orders`/staging `13 4 * * *`, `prod` `42 3 * * *`, `try-10`/staging `58 3 * * *`, `prod` `7 4 * * *`).

**Two consequences of a second pod, both taken.** The backup pod carries `platform.swhurl.com/backup: "true"` rather than the app's instance label, so v1 leaves it with unrestricted egress; v2 renders it its own policy — no ingress, DNS and the instance's proxy port only. And `over_quota` now counts the pod: `replicas + 1` for a rolling update plus one for the backup, which a stopped database still needs (its job runs), with the job's own requests and limit rather than the app's.

**Refusals:** `unsupported: backup` (a database instance whose profile states no backup facts), `unsupported: backup.egress` (a database instance compiled without an allocation — its job could not upload through a policy that admits only the proxy), `invalid: backup` (a document holding a Flux reference that is not the backup key reference, since substitution is one switch for every document in the folder), and the profile's own field refusals (`backup.image`, `backup.digest`, `backup.s3Uri`, `backup.writerNamespace`, `backup.writerName`, and `sops.recipient`, where the age key now lives). All appended after the existing checks; nothing existing moved.

**Identity changed deliberately, once.** The profile gained `backup`, so its canonical form and ID changed and the profile identity was recomputed independently in Python (`prof-sha256:ad680792…`). Both golden compilations moved in their `inputs.profile` line **and nowhere else**: the compilation ID hashes output, and a stateless instance renders the same files, so no golden ID and no document changed. The fixture profile carries the platform's real facts (image digest, bucket prefix, age recipient, `flux-system`/`backup-writer`).

**Checks:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check` and `make v2-report`; `kernel` coverage 93.5% (from 93.0%), the backup renderer and `fits` at 100%, CLI 85.9% (from 85.2%). Offline only: no cluster, credential, S3 object or bucket was read or written, and nothing was applied.

**Not in this slice:** restore (it scales the app to zero and streams into a pod — effects that need their own approval, adapters and a live proof), any live run of a backup, the MongoDB job, changes to `platform/backup/`, and knowing whether a v2-owned instance's backups are *fresh*: `verify-platform` reads app records, so a v2-owned instance is invisible to it. That observation is the recommended slice next.

#### The egress cutover (implemented 26 October 2026)

**What was decided.** The operator asked for the proxy cutover after part two: v2's rendered objects replace v1's generated `platform/egress/proxy.yaml`, and enforcement goes live. The design had already been approved with part two (grants enforced); the cutover adds only the ownership change and the grants enforcement needs.

**The precondition, found in the proxy's own logs.** Before the switch, the proxy's lines for the retained week held exactly one kind of request and no other: the six SQLite backup jobs, two runs each, every one to `swhurl-platform-backups-110927251694.s3.eu-west-2.amazonaws.com:443`, and every one `granted=-`. Enforcing without those grants would have stopped every backup — the risk the storage slice named. So the cutover is two commits: the grants first (`make app-egress` on the six instances, which v1 logs as `granted=yes` and allows as before), then the switch.

**What changed.**

| Before | After |
| --- | --- |
| `make app-sync` wrote `platform/egress/proxy.yaml`; `make check-apps` failed when it was stale (rule `egress-ports`); `verify-platform` read it | `swhurl-v2 proxy` renders `configmap.json` and `networkpolicy.json` from `platform/egress/allocation.json`; the three are regenerated by direct CLI invocation and CI diffs them (the allocation compared apart from the commit it records), so `make check-v2` and the workflow fail when the committed objects are not what the allocation renders |
| Squid allowed every host (`http_access allow all`) | each instance's own grants are allowed, then `http_access deny all`: `403` and `granted=-` for anything else |
| nothing recorded which ports exist except v1's records | `allocation.json` pins one port per instance in Git, seeded from the records, so a migration keeps its port and a removed instance's port is not reused |
| the instance's side only reached a port | unchanged: the instance's variables, annotation and policy are the same bytes |

**What v1 stopped doing.** It no longer renders or writes the proxy's configuration (`apps/egress.py` keeps the instance's variables, the grants' format and the port rules; `squid_conf`, `proxy_documents`, `proxy_text` and the staleness comparison are gone), and `verify-platform`'s egress check reports the proxy's replicas and each instance's port from the records. The proxy's *deployment* is untouched: the same Namespace, HelmRelease and chart.

**Rollback** is the cutover commit's revert plus `make flux-reconcile`: the object names are unchanged, so v1's file returns and Flux updates the same ConfigMap and NetworkPolicy in place. The grants may stay; under v1 they only change what is logged.

**Verification (offline, before the push):** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check` (1018 tests), `make check-lint`, `kubectl kustomize platform/egress` (the four resources), and the new objects rendered through the pinned chart with the live HelmRelease's own values (the enforced `squid.conf` in the chart's ConfigMap, one Service port per allocation plus the probe port). The live half is verified after the reconcile and recorded in [current state](current-state.md).

#### A stateful handover: an allocation outlives the records, and a volume can be adopted (implemented 26 October 2026)

Migrating a database instance found two gaps that no previous slice could see, both from the same cause: v1 deletes an instance's records when v2 takes it over ([the first migration](#first-migration-candidate-the-decision-with-the-evidence-25-october-2026)), and a handover recreates the objects v1's files described.

- **The allocation keeps an instance the snapshot no longer has.** `swhurl-v2 egress` seeds from the records and from the previous allocation; a previous allocation whose instance has left the snapshot is now carried over unchanged, port and grants, because the port is that instance's identity to the proxy and v2 owns it. Without this, migrating a database instance would drop its port from the proxy's configuration at the next allocation, and its backup job — which uploads through that port — would stop.
- **A volume can be adopted instead of created.** `storage.claimName` names a claim that already exists; the chart mounts it, the backup job mounts the same one, and nothing is created, so a migrated instance's data is the same bytes before and after the handover. It refuses a volume that is both created and adopted, a database with neither, and a claim that is not a DNS label; the platform's storage class is required only where something is created.

**Why the second exists.** The chart names a database's claim after its release (`swhurl-try-10`), so removing v1's HelmRelease and letting v2 install its own would leave the retained claim in place and fail the install on the existing name — the volume survives, the instance does not start. Adopting the claim is what makes the handover work without deleting data, and it is the primitive every stateful migration needs.

**Identity changed deliberately, once:** the instance record gained `claimName`, so its canonical form and revision moved; `identities.json` was recomputed independently in Python and both goldens moved in their `inputs.instance` line only, because the compilation ID hashes output and no fixture asks for a claim.

**Checks:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check` and `make v2-report`; tests cover an allocation carried over for an absent instance (with its grants), an adopted claim's values and backup mount, both refusals and the record's own label check. Offline only.

#### The backup host is the platform's grant (implemented 28 October 2026)

**The defect.** [Slice A](v2-status.md#slice-record-the-record-states-its-declarations-a) deleted `swhurl-try-10` staging's v1-shaped `instance.yaml`, the only place that said `egress: [<bucket>.s3.eu-west-2.amazonaws.com:443]`; a v2 record has no egress field, and `Allocate` used a database only to give a port. From `4bfc82ac` on, the allocation rendered from the records granted that instance nothing, so CI's freshness check failed on every push — and its advice, "re-render it", would have made the proxy refuse the instance's nightly upload with 403. The live proxy kept the grant throughout, because nothing re-rendered it.

**The decision (approved 28 October 2026).** Where backups go is the platform's fact, so the platform grants it: `PlatformProfile.backup` gains `region` (required with the block; an AWS region), `Backup.Host()` is the bucket's regional endpoint on 443, and `Allocate(facts, previous, backupHost)` adds it to every instance whose release keeps a database, once even when a v1 record also lists it. `swhurl-v2 egress` therefore requires `--profile`. Rejected: an `egress` field on the v2 instance record (every instance's identity moves, and every database instance would repeat the platform's bucket — the same omission waiting to recur), and restoring the deleted copy (it reverses slice A).

**Identity.** The region joins the profile's canonical form after `s3Uri`: `prof-sha256:4761e522…` → `prof-sha256:ca5c1e1a…`, recomputed independently in Python (which first reproduced the old canonical bytes and ID), and the two goldens moved in their `inputs.profile` line only. No instance, release or rendered resource moved: `v2-apps/` and `platform/egress` are byte-identical, and the allocation rendered from the records equals the committed one.

**The check that would have caught it locally.** `make check-v2` now ends with [`check-rendered.sh`](../v2/check-rendered.sh): it snapshots the working tree's tracked files (`git stash create`), renders the allocation, the proxy's two objects and `platform/auth` into a temporary directory, and fails on any difference; CI's two hand-written steps are replaced by it. Mutation-checked: an `egress` that passes no backup host fails it, naming the lost grant.

**Residual, stated:** the backup tool builds the same host from `AWS_DEFAULT_REGION`, which no rendered job sets (it defaults to `eu-west-2`), so the profile's region and the job's can diverge if either moves; rendering the region into the job is the step that makes them one fact, and it changes the job v1's tie test compares.

#### A migrated instance keeps its records (implemented 26 October 2026)

The stateful migration left a hole it could not close by itself: v1 deletes an instance's records at the handover, so the v2-owned instance vanished from the snapshot — `resolve` could not see its declarations, `egress` could only keep its port by carrying the previous allocation, and there was nothing to recompile from.

**What changed.** A v2-owned instance now keeps its records in **`v2-records/<app>/<env>/{instance,release}.yaml`** — the same v1-shaped records, in v2's own tree, out of `apps/` so v1 neither renders nor checks them — and the snapshot export reads three roots at the pinned revision: v1's `apps/`, those records, and v2's compiled output (`v2-apps/instances/<app>/<env>/`) as the evidence of what the instance declares. An app record is still read from `apps/<app>/app.yaml` wherever the app has a v1 instance, and a `.json` unit counts as the instance's own rather than as residual. **A port is now allocated only to an instance that needs one** — a grant it declares, or a database whose backup must upload — so an instance with nothing to reach outside gets no port instead of one the proxy serves for nobody.

**What it fixes.** `resolve` sees v2-owned instances again, so a pair between two of them resolves and can be rendered on both sides; a migrated instance's facts are in Git, so a later slice can translate them into the records `compile` takes; and the allocation derives from the records again, with the carry-over left as the safety net for the window between a handover and its records.

**The cost, stated:** two record sets during the transition (v1's for v1-owned instances, v2's for v2-owned ones), and an app's record still in v1's tree while any v1 instance remains. A translation from these records to `compile`'s inputs is the next step, not part of this one.

**Checks:** `make check` (1018 tests), `make check-v2` (12 packages), `make check-lint`, and the exporter's own tests: a v2-owned instance read from v2's tree with its compiled output and `.json` unit, the app record found where v1 keeps it, a missing record named, and the allocator giving no port to an instance that needs none. Offline only.

#### Calls on the pod network, with the token as the caller's identity (implemented 26 October 2026)

The [diagnosis](current-state.md#the-first-call-between-two-v2-owned-instances-26-october-2026) left one decision: this cluster honours `ipBlock` peers and ignores `namespaceSelector`/`podSelector` peers, so a callee cannot admit a caller's pods by selector. **The operator chose the address block**, the shape that works today, with the token as the identity.

**What changed.** `PlatformProfile.network.podCIDR` states the cluster's pod network — an identity-bearing platform fact, as the proxy's address and the DNS service are. A call's rules now name that block: a caller reaches the network on the callee's port, and a callee admits the network on its own port, once, however many callers it has. The caller's token, its audience and the callee's `SWHURL_ALLOWED_CALLERS` and key set are unchanged: the token is now the whole of the caller's identity rather than a second check behind the network's.

**What it costs, stated plainly:** the call's network rules admit every pod in the cluster on the callee's port. The port is the boundary and the token is the identity; a pod that is not the caller can reach the port but cannot pass the callee's check. The alternative — network-level identity — needs a policy implementation that honours selectors, which is a different platform decision.

**Refusals:** `unsupported: network` when an instance takes part in a call and the profile states no pod network, and the profile's own field refusal for a block that is not IPv4 CIDR. Appended after the call shape's checks, so no existing precedence moved.

**Identity changed deliberately:** the profile gained `network`, so its canonical form and ID moved and `profile-identity.json` was recomputed independently in Python; both goldens moved in their `profile`/`id` lines only. The two v2-owned instances were recompiled: `swhurl-try-10`'s callee rule and `swhurl-try-11`'s caller rules, and `swhurl-try-11`'s namespace label corrected from `private` to the `authenticated-web` its record states and its route serves.

**Checks:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make v2-report`; the pair test now asserts the address block on both sides and one callee rule for two callers. The live half is the probe from the diagnosis, re-run after the reconcile and recorded in [current state](current-state.md).

#### A rotation rolls the callees, and the auth material is also a file (implemented 27 October 2026)

**Rotation first.** The platform's keys reach a callee as environment variables (`envFrom` the instance's own
`<app>-auth` ConfigMap), and a running pod never re-reads them; Reloader ignores ConfigMaps. So the pod template
carries `platform.swhurl.com/auth-revision`, the digest of `(issuer, keys, sorted callers)`: rotating the keys, or
changing who may call, changes the template and rolls the Deployment. v1 never had the gap because it inlined the
keys in its env.

**Then the files.** A callee now also mounts the same strings as files: the keys and issuer at
`/var/run/swhurl/auth` (the `auth` ConfigMap) and the allow-list at `/var/run/swhurl/callers` (a per-instance
`<app>-callers` ConfigMap whose content is the record's `allows`, sorted), with `SWHURL_AUTH_KEYS_FILE`,
`SWHURL_AUTH_ISSUER_FILE` and `SWHURL_ALLOWED_CALLERS_FILE` naming them.

**One source for the keys (option 1, 27 October 2026).** A ConfigMap cannot be read across namespaces, so
"centralised" means one rendered source fanning out: `swhurl-v2 auth --snapshot … --profile … --out platform`
writes `platform/auth/` — one identical `swhurl-auth` ConfigMap per managed namespace, plus the folder's
kustomization — and the `platform-auth` unit applies it. The instance no longer renders `<app>-auth`, mounts the
fixed name, and claims none of it; a rotation changes that folder alone, and CI regenerates and diffs it beside the
egress proxy's. Two consequences are stated rather than hidden: no app unit can `dependsOn` the keys (an app's own
unit creates its namespace, so a dependency would deadlock), which means a **new** app's pods wait for the
platform unit's next reconcile before they can mount the keys; and the allow-list stays per instance, because its
content is.

**Files only (29 October 2026, approved).** An image that reads the files takes a rotated key or a new caller from
the kubelet's in-place refresh — **no restart**. Both templates release such a reader (TypeScript `v0.2.19`, Kotlin
`v0.2.18`), and the one v2-owned callee runs one, so the env forms (`SWHURL_ALLOWED_CALLERS`, `envFrom` of
`swhurl-auth`) and the `auth-revision` annotation are **retired**: a v2-rendered callee's HelmRelease no longer
depends on the keys or its callers, so a change to either rewrites only the mounted objects. The cost is chosen
and stated: an image that predates the files finds no keys and refuses every token — loud and closed, rather
than verifying silently against stale environment values. Rejected: a release fact that keeps the env forms for
old images (release identity and a compiler branch for a transitional case), and dropping only the annotation
(an old image would verify against stale keys after a rotation).

**The limit, restated:** a *new call* still restarts the **caller**, because each call is a projected
service-account token source in a volume and volumes are fixed when a pod is created. Only weakening the
per-callee audience would avoid that, and it would weaken replay protection, so it is not taken.

#### A v2-owned instance recompiles from Git alone (implemented 26 October 2026)

The stateful migration left the instance's records in v2's tree but its compiled output frozen: recompiling it needed a pair of hand-written JSON records in `/tmp`. That is closed.

**What is committed for each v2-owned instance:** its own records in v2's schema — `v2-records/<app>/<env>/instance.json` and `release.json` — beside the v1-shaped records the snapshot still reads for `resolve`, and **the resolution** the instances were compiled from as `v2-records/calls.json`, so a pair's authority is a document in Git rather than a command's output.

**The guarantee, as a test:** `TestOwnedInstancesRecompileFromGit` takes every `v2-records/*/*/instance.json`, compiles it with its release, the profile, the committed resolution and — for a database instance — the committed allocation, writes the files as an operator does, and asserts each one is **byte-for-byte what Git holds**. A changed profile, allocation or pair now reaches a migrated instance with no hand-written JSON, and a stale compiled file fails `make check-v2` instead of waiting to be noticed.

**What is still manual, and next:** the *translation* from v1's records to these two JSON documents (provenance included: v1's tag abbreviates the source commit, so the full one was resolved once from the app's repository). The command that does it belongs in the bridge, over `legacy.Translate`'s facts; until it exists, a migration writes the pair by hand and this test keeps it honest. The two v1-shaped record sets stay for now because `resolve` reads them; once calls come from the committed resolution alone, they go.

**Checks:** `make check-v2` (12 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`. Offline only.

#### The orchestration layer (implemented 26 October 2026)

The `cmd` package had become the orchestrator as well as the CLI: 1.8k lines, 26 functions in `main.go`, five separate inputs for `compile`, a snapshot type three commands shared, and a **pure decision — the port allocation — living at layer 2**, contrary to the model above.

**What changed.** `orchestration` is the use-case layer, with one use case so far: *compile an instance* (`Compile` without an effect, `CompileInto` writing through the file adapter and reporting each outcome). `compile` and `observe` are its consumers — the second had already needed `--calls`/`--egress` retrofitted because it assembled the same inputs its own way. The allocation decision moved to `kernel` (`Allocate` over `EgressFacts`), and the two documents a compilation takes moved with it (`DecodeEgressDocument`/`EgressOf`, `ConnectionsOf`), since a layer-2 use case may not import a layer-3 command. A destination inside the checkout's `apps/` remains the command's refusal: it is a rule about this checkout, not about a use case.

**The rule, as code:** `orchestration-wires` — the use-case layer imports no `flag`, `os`, `log` or `testing`, prints nothing, calls no `panic`, and assigns no package variable; `places` holds layer 2 to `orchestration` and layer 3 to a `main` package under `cmd/`. The archtest's own fixtures were updated with the new numbering, and the plan's guardrails table with the new rule.

**One behaviour changed, deliberately:** a new instance's port is now the lowest free one in the *document's* order (environment, then application) rather than the order the snapshot happened to list, so the same facts always give the same document. Every existing instance keeps its recorded port, so `platform/egress/allocation.json` is unchanged — which CI's regenerate-and-diff confirms.

**What it holds now:** `Compile`/`CompileInto` and, since 26 October, [`Migrate`](#the-migrate-use-case-implemented-26-october-2026) — translate, encode, compile, write. **What it does not yet hold:** `resolve`, `egress`, `proxy`, `preview`, `shadow`, whose input assembly stays in the command until a second consumer needs it.

**Checks:** `make check-v2` (13 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make v2-report` (the report names the new layer and rule, and the three command functions the move removed). Offline only.

#### The `migrate` use case (implemented 26 October 2026)

The last hand-written step before a migration was the record pair in `v2-records/`: two JSON documents an operator wrote by reading v1's records, with the full source commit resolved from the app's repository by hand. `swhurl-v2 migrate` does that write, from documents the snapshot already pins.

**Two decisions, approved before implementation.**

1. **Where the translation lives.** `legacy.Migrate(records, supplied)` is a second pure function beside `Translate`: it reads the same four documents and yields the facts a record pair states (`Source`, `Workload`, `Instance`, `Database`, `Claim`), a source per fact, and every legacy fact the pair cannot carry. It cannot import `kernel` — both are layer 0, and `layers` allows only *lower* layers — so `orchestration.Migrate` maps those facts onto `kernel.ReleaseBundle`/`InstanceSpec`, encodes them with `kernel.EncodeRelease`/`EncodeInstance` (validation and identity stay the kernel's), runs the existing `Compile`, and writes once. Alternatives rejected: widening `Translate` (the preview would pay for facts it never renders, and its published `sources`/`gaps` shape would change), letting the command read v1's fields (a second legacy authority), and a new pure package (it could import neither `legacy` nor `kernel`, so it would re-implement the parsing).
2. **How the source commit is obtained.** v1 keeps only a seven-character abbreviation, twice — `image.tag`'s hex suffix and the `offers` commit — and this repository holds nothing fuller. So `--source-repository` and `--source-commit` are the operator's, and the translation **corroborates** them: the commit must begin with the tag's hex suffix when it has one and with `offers` when the record states one, and the repository's name must equal the image name's last path segment. A contradiction is a refusal naming the field; an abbreviation is never expanded, and a record with no abbreviation leaves the commit *unverified by the records* instead of inventing a check. Alternatives rejected: deriving the repository from the image name (true for all 17 instances, but a template convention that would fabricate a claim for a third-party image), a Git adapter to resolve the abbreviation (network, credentials and a new adapter for one fact), and recording the abbreviation (the kernel requires 40 hex, and a short hash is not the provenance).

**Contract.** The command takes the snapshot, the instance, the profile, the source and `--out`, plus the optional resolution and allocation exactly as `compile` does; it writes `v2-records/<app>/<env>/{release,instance}.json` and the compiled files under one root, so there is one write and one `apps/` refusal. The report is a `v2-migration` version-1 document on stdout: the snapshot revision, the source with what corroborated it, the release/instance/compilation identities, the allocation's port and grants, the resolution's edges beside the predecessor's declarations and the ones it does not connect, every file outcome, and the gaps. Exit 0 means the document was written; 2 a usage error or refusal; 1 an I/O failure. It refuses an instance the snapshot lacks or names records as missing for, a contradicted source, a release declaring `secrets` (empty-only bindings would change what the app can do), a database release whose committed output declares no volume, and a malformed input.

**The claim a database instance adopts** needs no flag and no live read: v1's release declared a data volume and named no claim, so the pinned chart generated one named after the release. The migration reads `existingClaim` when the committed output names one, and that convention when it does not — tied by rendering `apps/demo-orders/staging/helmrelease.yaml` through the pinned chart in a test.

**What it does not do:** delete v1's records, manifests or unit; copy the v1-shaped record set the handover leaves for `resolve`; edit the unit's registration; regenerate the allocation with `--previous`; apply, reconcile or observe. The handover procedure stays the operator's, as recorded in [current state](current-state.md#the-first-stateful-migration-swhurl-try-10-staging-keeps-its-volume-26-october-2026).

**Acceptance.** The two committed v1-shaped record copies are the pre-migration bytes, so the test re-runs the migration over them with the committed resolution and allocation and asserts byte-for-byte the record pair and the files Git holds — the stateful migration's adopted claim included. Both committed pairs were regenerated by the command, so their bytes are now the kernel encoder's (the asserted `releaseId`, an empty `storage` object on a stateless instance) and their identities did not move. Mutation-checked: dropping the claim, skipping the corroboration, or never reporting an unmet declaration each fails a test.

**Checks:** `make check-v2` (13 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make v2-report`, `make -s check-docs`. Coverage: `legacy` 85.8%, `orchestration` 84.0%, CLI 88.0%. Offline only.

#### The backup observation (implemented offline, 26 October 2026)

`verify-platform` asks whether each instance's newest archive is younger than 26 hours, but it builds the list of instances from `apps/`, so a v2-owned instance is invisible to it. For `swhurl-try-10` staging — the one live stateful instance, migrated on 26 October — nothing at all checked that its nightly job ran. This slice answers the half that needs no credential: **does the job run, and did its last run succeed?**

**Where the answer lives.** In `observe`, as a `backup` block beside the seven running findings, never inside the verdict: an instance can run while its backups are stale, and its job can be current for an instance that is not running. The document becomes `v2-observation` **version 2**; the only consumer is the test suite.

```json
"backup": {"state": "current", "schedule": "58 3 * * *", "lastScheduledAt": "…",
           "lastSucceededAt": "…", "maxAgeSeconds": 93600,
           "newestRun": {"name": "swhurl-try-10-backup-29345678", "outcome": "complete"}}
```

`state` is `current` (a run succeeded within the threshold), `stale` (none that recent, with `no_runs`, `no_successful_run` or `last_success_too_old`), `failing` (the newest run's Job states a `Failed` condition — it outranks an older success, because the newest data is not what was backed up), `suspended` (the CronJob is, whatever its history), `absent` (a successful 404 for the CronJob), `unavailable` (a failed read, with its reason), `not_assessed` (the caller made no such read, or a pod names no Job) or `not_applicable` (the instance renders no backup). The order is fixed and tested.

**What is read, and the convention avoided.** The CronJob, then the pods the job's runs create — selected by the labels the *pod template* carries (`platform.swhurl.com/backup=true`) — then the newest pod's **owner Job, by the name its owner reference states**. A Job created by a CronJob carries no label its own selector could find: v1's committed job puts the label on the pod template only, so `kubectl get jobs -l platform.swhurl.com/backup=true` (v1's own hint) selects nothing, and v2 must not invent a name pattern. `observe.NewestRun` is the pure function that reads the newest pod and names its Job; the command reads that Job through `kube.RefOf`, which states `batch/v1`. Two kinds join the adapter's table: `batch/v1 CronJob` and `batch/v1 Job`.

**The threshold** is `--max-age`, a duration the operator states, defaulting to 26h — v1's `BACKUP_MAX_AGE_HOURS` default, so the same miss that fails v1's check fails this one. A value the command cannot parse is a usage error, never a report judged by nothing.

**The deliberate rule change, and why it is not a weakening.** `observe` has to parse the cluster's RFC3339 timestamps and compare them with the observation time, which it previously carried as an opaque string. `time` therefore joins `PureImports`, and the clock's own functions (`Now`, `Since`, `Until`, `After`, `Tick`, `Sleep`, timers) join the refused calls, so the clock — the effect the rule exists to catch — is now *checked* where before it could not be imported at all. The archtest's own fixture asserts both halves: a pure package may parse a supplied timestamp and may not ask for the current one.

**What it refuses:** a CronJob or run document it cannot decode, a timestamp it cannot parse (in the CronJob's status, a pod's creation time, a run's, or the observation time), a threshold that is not positive, and backup evidence supplied for an instance whose target names no backup. `Target` validates the CronJob as a named role in the instance's namespace and the job's pods as a second collection of the same kind, so the instance's own pod list and the job's are two reads with two selectors.

**Scope, stated in the report.** `notCovered` gains `backup_archive` and `backup_restore`: the object store is not read, so "the job succeeded" is not "the data is in S3", and nothing about restoring is claimed. The live half ran on 26 October: one read-only observation of `swhurl-try-10` staging returned `running` with backup `current` (the newest run complete, twelve hours inside the threshold), recorded in [current state](current-state.md#the-first-backup-observation-of-a-v2-owned-instance-swhurl-try-10-staging-26-october-2026).

**Checks:** `make check-v2` (13 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make -s check-docs`. Offline only: the fixtures are recorded API documents and the fake answers them; no cluster or credential was read for this slice.

#### The object-store read (implemented 26 October 2026)

The job's answer is that it ran; it is not that an archive is there. `observe --archive` lists the instance's own prefix with the operator's own credentials and answers the bucket's question too.

**The decision, before implementation:** route 1 (v2 lists the bucket) rather than scraping the job's log or making v1's check read `v2-records`; the operator's environment for the live read only, no new Secret; a hand-written SigV4 in `stdlib-only` rather than `aws-sdk-go-v2`; the region from the bucket's own `x-amz-bucket-region` header, so no profile field and no identity moves; and an opt-in flag, so an observation stays credential-free by default.

**The shape.** `observe` owns the decision: a key is an archive when it ends `.db.age` under the instance's prefix, the newest is by S3's `LastModified`, and its age is compared with the same `--max-age`. `adapters/s3` owns S3's formats and answers in the evidence vocabulary: `ListObjectsV2`, pages, the status-to-reason mapping, and the signer. The report's `backup.archive` is beside `backup.state`, never folded into it, because a job can report success and leave no archive, and an archive can be current while the job that wrote it has since failed — that disagreement is what the check is for. The listing is confined to `<profile.backup.s3Uri><namespace>/`, and a prefix that is not the instance's own is refused.

**The signer is proved without a bucket:** AWS's published example for a signed GET Object is a test, field for field — canonical request, string to sign, signing key and signature `f0e8bdb8…` — so a wrong canonicalisation fails offline. A session token is signed when present; the canonical query is sorted and encoded once.

**Live (26 October, read-only):** `swhurl-try-10` staging reported `archive current` — `sqlite-20261026T082103Z.db.age`, `LastModified 08:21:10Z`, thirteen hours old against 26h, eleven seconds before the run that uploaded it completed. The job and the bucket agree; `notCovered` keeps only `backup_restore`.

**What it deliberately does not do:** restore, read the bucket's root, judge a v1-owned instance's archives (v1's check still builds its list from `apps/`), or hold a credential of its own.

**Checks:** `make check-v2` (15 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make -s check-docs`. Offline fixtures plus one live read.

#### The restore plan (implemented 26 October 2026)

The observation says an archive exists; it does not say the archive is restorable, and v2 cannot yet replace a database. This slice closes the first half without taking the two architectural firsts the second half needs.

**The definition.** `swhurl-v2 restore` is a *check and a command*. It lists the instance's prefix, chooses the newest archive or the one named with `--from`, reads the metadata the backup job published beside it (`sqlite-<STAMP>.json` — v1's own naming, checked in the live read), and states the instance's claim, mount, database path and uid from v2's records and the compiler's constants. The plan is `ready` only when the archive is within `--max-age`, its metadata names **this** instance (`source` is `<namespace>/<application>`) and records a checksum and a table count; otherwise `stale`, `incomplete`, `no_archive` or `unavailable`, each with a fixed reason. It hands over the v1 command and says what to verify afterwards. It restores nothing, stops nothing, and holds no credential beyond the read ones.

**Why not let v2 perform it now.** A restore is the first operation that would need a *write* path to Kubernetes (suspend a HelmRelease, scale a Deployment, run a pod) and the first that would need to decrypt age — a dependency (`filippo.io/age`) inside a `stdlib-only` module, or the age private key handed to v2 or put in a cluster Secret. Both are decisions worth their own slice, and a restore is a poor place to debut them.

**The live read found a real defect:** the metadata's `source` is `<namespace>/<application>`, not the namespace; the first live plan said `incomplete/metadata_names_another_instance`, and the fix made it `ready`. Without reading the metadata, v2 would have called an unverifiable archive ready.

**Checks:** `make check-v2` (15 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make -s check-docs`. Offline fixtures plus one read-only live plan.

#### The restore's write primitives (option 2a, part one — 26 October 2026)

A restore is the first thing v2 would *do* to a cluster, so the write path is built and tested before anything uses it.

**`adapters/kube.Writer`** is four operations — a JSON merge patch, a create, a delete, and a pod's log tail — and a second kind table (`WritableRefOf`). `Secret` is writable and still **not readable**: `kinds` refuses it for `Reader`, so creating the restore's age-key Secret cannot become a way to read Secrets, and a test asserts the reader still refuses one. A delete of an object that is already gone is done, not an error; every other refusal is `failed: write: <reason>` and carries no content. `Connection.Writer()` builds it from the same endpoint, TLS material and token as the reader.

**What it is not:** a capability. Nothing calls it yet, so no instance can be restored by v2; the adapter exists because the approved next step needs it, and the plan's "add an adapter when a slice needs it" is satisfied by the slice, not by a speculative consumer.

**What remains:** the restore Job (from the platform's backup image, which already carries `age`, the AWS CLI and SQLite), the sequence — suspend, scale to zero, wait, create the age-key Secret, create the Job, wait, read its log, scale back, resume, delete — and the **presigned read URL** the pod uses instead of a long-lived key. The open decision is where the restore's *steps* live: a `restore-job-sqlite` command in the backup image (this repository's `tools/swhurl` and `images/backup`, a v1 change and an image rebuild) or a script rendered inside the Job (no image change, and a contract to test).

**Checks:** `make check-v2` (15 packages), `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make -s check-docs`. Offline only.

#### The backup image in Go (option C part one, 27 October 2026)

The backup image is 134 MB compressed because it carries Python, a uv venv and the AWS CLI; the database
copy, the encryption and the upload need none of them. Its **SQLite job is now Go**, in a module of its own,
with the platform's contract unchanged.

**The contract it keeps**, because v1's restore, `verify-platform` and this repository's own metadata reader
all depend on it: the archive is `sqlite-<UTC>.db.age` and the record beside it `sqlite-<UTC>.json`, published
**second** (a backup counts only once its metadata is there); the metadata keys are `created`, `source`,
`path`, `tables`, `integrity_check`, `bytes`, `plain_sha256`, `age_recipient`, `sha256`; and the upload sends
the payload's SHA-256 as S3's checksum with `If-None-Match: *`, so an existing object is refused rather than
replaced. The plaintext copy is taken with SQLite's `VACUUM INTO` (a consistent copy while the app writes),
checked with `PRAGMA integrity_check`, and its table count, size and SHA-256 are what the metadata records —
the same facts the Python job records under the same names.

**Where it lives and why.** `images/backup/tool` is its own Go module: `filippo.io/age` for encryption and
`modernc.org/sqlite` for the copy are dependencies, and they must not enter `v2/`, whose `stdlib-only` rule
stands. The binary replaces `python -m swhurl backup-job-sqlite`; `go test ./...` in that module runs its tests, and CI's tooling job does so beside `make check-v2`
(there is no `make` target: the command catalogue documents every target).

**`restore-job-sqlite` is the second command**, and it is the one option 2a's Job will run: it fetches the
encrypted archive from a **read URL** (v2 will supply a presigned one, so a restore pod holds no standing
bucket credential), checks the archive's SHA-256, decrypts with the mounted age key, checks the plaintext's
SHA-256, `integrity_check` and the table count against the metadata, and only then replaces the database —
keeping the database and its `-wal`/`-shm` in `before-restore-<UTC>/` beside it. Every check refuses *before*
the live database is touched, and a refused restore keeps no folder: the tests assert both, and that no
plaintext copy is left behind.

**`backup-job-mongodb` is the third command**, and the last Python job: it pipes `mongodump`'s output straight
into age (the connection string reaches the tool on stdin, never a command line, and no plaintext copy is
made), records the server's version and each collection's count — read with `mongo-go-driver`, the tool's third
dependency — and publishes `clickstack-mongodb-<UTC>.archive.gz.age` with its metadata, the same names and keys
the Python job publishes. `mongodump` (21 MB, glibc + krb5) is the one thing the tool cannot replace, and it is
why the image base cannot yet be `scratch`.

**The image is now the Go one** ([`images/backup/Dockerfile`](../images/backup/Dockerfile)): a builder stage
compiles the tool, the runtime is `debian:bookworm-slim` + `libgssapi-krb5-2` + `mongodump` + the stripped
binary, and the entrypoint is the binary, so the CronJobs' arguments do not change. **48.1 MB compressed**
(measured by building it locally), against 134 MB today; the binary is 13.4 MB stripped. `mongodump --version`
runs inside it, the usage lists the three commands, and `backup-job-sqlite` refuses with no configuration.
Nothing is deployed: the rendered jobs still name the old digest.

**The digest moved once, deliberately:** every rendered job (five v1 instances, the MongoDB job, v2's
`try-10` CronJob) names the new tag and digest; v2's `profile.backup.digest` changed, so its profile ID moved
to `prof-sha256:e94c3d69…`, the two goldens were regenerated once, the identity fixture was recomputed
independently, and v1's pre-M6 render hash was re-captured for the one scenario (`web` with a database) whose
output carries the image. The archives, the metadata and the restore procedure are unchanged, so an archive
from either image is readable by both. **Nothing is pushed yet**: the rollout is the next act.

**The first live night found a real defect.** The rollout worked — every CronJob named the new digest, and the
MongoDB backup published through the Go image — but all six SQLite jobs failed their upload with `the upload did
not complete`. Diagnosed live: the request never reached the proxy; `GODEBUG=netdns=go+2` showed the resolver
looking up the proxy's name, and **the same binary in the same pod succeeded forty-five seconds later**, so a
new pod's first seconds cannot reach the proxy's ClusterIP. The Python image had hidden it: `aws` retries by
default. The tool now retries a **transport** failure (five attempts, five seconds apart) and names the cause
(`dns`, `refused`, `timeout`, `tls`, `dial`) with the attempt count; a status the server answers is never
retried. That fix is its own image, so the digest moves once more.

**What is not done:** the digest move to the fixed image and the live proof through it, the presigned read URL
on v2's side, and any restore.

**Checks:** `go test ./...` in `images/backup/tool` (10 tests: settings for the three jobs, the online copy,
an age round trip, the metadata keys, the S3 upload's headers and refusals, the SQLite backup end to end
against a fake bucket, the restore — replaced and kept aside, plus five refusals that leave the live database
untouched — and the MongoDB job with a stand-in `mongodump` and a facts seam, plus its two refusals),
`make check-v2`, `REQUIRE_HELM=1 make check-v2`, `make check`, `make check-lint`, `make -s check-docs`. The
binary is 19.6 MB static (10.7 MB gzipped). Offline only.

#### The restore v2 performs (option 2a, offline half — 27 October 2026)

**The definition.** `swhurl-v2 restore` keeps its plan as the default: without a flag it reads, decides and
writes `v2-restore`, and changes nothing. With `--confirm APPLICATION/ENVIRONMENT` it **performs** that plan:
v2 suspends the release, scales the app to zero, waits for its pods to go, creates a Secret holding the age
identity and a presigned read URL for that one archive, creates a Job from the backup image that runs
`restore-job-sqlite`, waits for it, reads its log, deletes the Job and the Secret, scales the app back, resumes
the release, and waits for the app to be available. It is the first use of `adapters/kube.Writer`, and the
first thing v2 does *to* a cluster.

**Five decisions, all as the brief recommended (approved 27 October 2026).**

1. **The presigned read lives in `adapters/s3`** as `Reader.PresignURL(ctx, query, key, ttl)`: query-string
   SigV4, the same canonicalisation and signing key as a signed read, refused for a key outside the instance's
   prefix and for an expiry outside SigV4's own seven-day ceiling (the CLI defaults to 1h). It is proved by
   AWS's published presigned-GET vector — canonical query, scope and signature `aeeed9bb…` — beside the
   header-signing vector, so a wrong canonicalisation fails without a bucket. The command supplies the
   operator's environment credentials and nothing else: a restore pod holds no standing bucket credential.
2. **The URL goes in the Secret**, not the Job document: the Job reads it with a `secretKeyRef`, so a copied
   Job, a log line or the report cannot carry it. The Secret holds two keys — the age identity at `age.key`
   (mounted at `/keys`) and the read URL at `archive-url` — and is deleted by the same run that created it.
3. **The Job is named `<app>-restore-<UTC second>`**, bounded to the cluster's 63 characters. Two runs of one
   instance differ, so a run interrupted by a signal is still findable by name, and there is no
   delete-before-create step that could remove a running restore's evidence.
4. **The pod reuses the instance's backup policy** by carrying `platform.swhurl.com/backup=true`: no new policy
   object, the same rules (no ingress, DNS and the instance's proxy port only). A second label,
   `platform.swhurl.com/restore=<job>`, is how v2 finds the run's own pod to read its log — v2's own label, not
   one the cluster generates.
5. **`--confirm` is the gate, and the plan stays the default.** A confirmation for another instance, a plan
   that is not `ready`, and an identity that is not an age key are refusals (exit 2) before anything is
   presigned or changed.

**Where the Job's document comes from.** `kernel.RenderRestore(spec, release, profile, egress, job)` sits beside
`backupCronJob` and shares its pod shape — a helper now builds both, so the release's uid, the instance's own
claim, the scratch and `/tmp` emptyDirs, the proxy environment and the hardening cannot drift apart. Its facts
are the instance's (namespace, application, database path, claim, port), the release's uid, the profile's
backup image and proxy address, and the restore's own facts — the archive, both checksums, the table count and
the two credentials. It refuses a release that is not `sqlite`, a profile with no backup block, an instance
with no allocation, and a restore fact that is not one (empty, a name that is not a Kubernetes name, an
identity that is not an age key, a URL that is not https, a checksum that is not SHA-256, `tables < 1`). The
two objects are **not compiled intent**: no `Claim`, no `ResourceSet`, never written under `v2-apps/` or Git,
so CI's regenerate-and-diff and `TestOwnedInstancesRecompileFromGit` are untouched.

**Where the sequence lives.** `orchestration.Restore` (layer 2), in three acts — stop, restore, put back — with
every wait bounded and every step recorded. The undo is explicit rather than a stack: the run tracks whether it
suspended, scaled, created the Secret and created the Job, and a failure unwinds in reverse order (Job,
Secret, replicas, release), attempting each undo even when an earlier one failed. **Suspend precedes scaling**
so Flux cannot start an upgrade or revert the scale; **resume follows the scale-back** so the app is not
restarted while its database is being replaced. The use case takes a `Reader` and a `Writer` as one `Cluster`
port (a restore that could not read the Deployment it scales would have to guess), plus `Sleep` and `Now` from
the command, so its tests run on a fake clock. A `Refusal` means nothing was changed; a failure after the first
step is never one, and a failed undo is recorded as such — that is what leaves an app stopped, and the report
names it.

**What restore reports.** `v2-restore` rises to version 2: the plan block unchanged, plus `action` (state
`restored`/`failed`/`not_attempted`, the failing step, start and finish, and every step with its outcome), the
`job`'s own state and its log — read whatever the Job's answer was, with `not_read` and a reason when it could
not be — and an `app` block (replicas, suspended, available, state) **beside** `action`, because "the data was
replaced" and "the app runs" are different facts. Exit 0 means restored *and* the app is back; 1 is an
attempted restore that did not complete (the document is still written); 2 is a refusal, cluster untouched. A
failed `restore-job-sqlite` still leaves the live database untouched — the tool's own five refusal tests prove
that — and `backoffLimit: 0` stops a retry on top of a database it may already have moved.

**Two deliberate semantic changes.** The plan now requires the metadata's **plaintext** checksum as well as the
archive's (`metadata_has_no_plain_checksum`), because the tool refuses to run without it: an archive whose
record lacks it is `incomplete`, not `ready` — the live try-10 record has both. And a create through
`adapters/kube.Writer` now takes only a kind the write table states (`Secret`, `Job`), while a patch or delete
still takes any kind the reader can name: readability is not permission to create.

**After the review (27 October 2026).** The independent read-only review found two blockers, four should-fix
findings and seven nits; all are fixed, and five of them are behaviour rather than wording:

- A Job delete sends **background propagation** and the run **waits until the restore pod is gone** before
  scaling the app back up. If that cannot be confirmed, the undo deletes the Secret and stops: the app stays at
  zero replicas with its release suspended, and the report says `stopped` — restarting an app over a pod that
  may still hold the claim is worse than leaving it stopped for the operator.
- The **age key file is read as `age-keygen` writes it** — comment lines, then one `AGE-SECRET-KEY-1` line —
  because the default `./age.agekey` is that shape. The command refuses a file with no key or with two.
- A wait **retries three consecutive failed reads** before failing its step: one refused request during a
  fifteen-minute wait is not evidence that the work stopped.
- The run **records its intent before each request**, so a create that timed out but was applied is still
  cleaned up, and the **undo runs on its own bounded context** so a run that has used its own can still put the
  app back. An interrupt unwinds the same way.
- The log is read **even when the Job's wait failed**, and a **restore of an app already at zero replicas
  reports `stopped`** rather than `running`. `--url-ttl` has a floor of the restore's own window, and the
  backup observation's selector **excludes the restore pod** (`!platform.swhurl.com/restore`), which otherwise
  would be read as a backup run and make the instance's protection look broken.

Each of these is pinned by a test, and each was mutation-checked in a throwaway copy: removing the retry,
loosening it, ignoring the adopted claim, reporting a zero-replica app as running, letting the undo inherit a
cancelled context, dropping the pod-gone wait, moving the read URL into the Job, removing the delete's
propagation, widening a create to any readable kind, or ignoring a failed Job each fails at least one test.

**Live, 27 October 2026.** `swhurl-try-10` staging was restored over its newest archive
(`sqlite-20261027T085907Z.db.age`, 12h8m old, 2 tables) in **30 seconds**: every step `done`, the Job complete
with `[OK] Restored the archive into /data/app.db (2 tables, integrity ok); the replaced files are in
/data/before-restore-20261027T210953Z`, the app back at one replica with its release resumed and observed
available. The bucket was read with the operator's own credentials, the pod held only a presigned read of that
one object, and the Secret that carried it was deleted by the run. [Evidence](current-state.md#the-first-restore-v2-performs-swhurl-try-10-staging-newest-archive-over-itself-27-october-2026).

**Content change, proved (27 October 2026, 21:31Z).** A second run restored an older archive on purpose (26 Oct,
`--max-age 72h`, 29 seconds). SHA-256 of the files, taken from outside v2: the live `app.db` is exactly the
archive's recorded `plain_sha256`, and the file it replaced is a *third* hash — the database the app had written
to since the previous restore — now kept in `before-restore-<UTC>/`. The content changed, and the proof does not
rest on the tool's own prose.

**The backup schedule is suspended for the window (offline, 27 October 2026).** A backup that copied the database
while the restore swapped its files would publish a stale archive, so the sequence now reads the instance's
CronJob, sets `spec.suspend` before the app stops, and puts back what it found once the Job's pod is gone: a
schedule the operator had already suspended stays suspended, and the undo waits for that pod before touching it.
A run that cannot confirm the pod is gone reports `backupSuspended` (and says so on its summary line); an
instance that renders no backup is refused before anything changes. An already-running backup is left alone —
it only reads the database. Four mutations each fail a test. The live restore above predates this, so its first
live exercise is still owed.

**What it deliberately does not do:** restore another instance's archive, restore MongoDB, retain or prune
`before-restore-<UTC>/`, or hold a credential of its own. **Residual risks, stated:** a pod killed between the tool's renames can leave a
`.restore` file and a `before-restore-<UTC>/` holding the live database; a run killed by a signal that does not
unwind leaves its Job and Secret for the next run's preflight; and a scheduled backup can still start during
the swap window — its copy fails its own integrity checks rather than uploading a bad archive, but that
night's run would be missing. Suspending the backup CronJob for the window is the next tightening.

#### First migration candidate: the decision, with the evidence (25 October 2026)

The inventory above makes the choice concrete. Of the 17 instances in this repository, **no instance is both private and a web app**: every web instance is `authenticated-web`, and every private instance is a `worker` or `job`, which v2 does not render at all. The lightest web instance is `swhurl-try-11` (no database, no events, two calls); `demo-orders` — the natural candidate — needs storage (SQLite), events, calls, routes and a backup job.

| Option | What it needs before one existing app could migrate | Cost and reusability |
| --- | --- | --- |
| **A. Routes (`authenticated-web`)** — recommended | Ingress, TLS through cert-manager, the shared sign-in middleware, host and issuer from the instance records | One platform-domain slice, needed by **every** web instance; offline-renderable and comparable, and verifiable with a throwaway instance at a real host |
| B. App kinds (`worker`, `job`) | No Service or probes, a schedule, and for most workers the events domain | A different domain, needed by the 7 private instances; avoids routes but grows two capabilities at once |
| C. Migrate `demo-orders` first | Routes, storage and backup, secrets, events, calls, offers | Six domains; the longest path to the first migration, and the one most likely to break |

**Recommendation: A, then the first migration of a database-free web instance** (`swhurl-try-11` or `-12`, whose remaining needs are calls and, for `-12`, one event), and only then B and C. Routes are the single capability that unblocks every web instance, and they are platform-owned and renderable offline; storage and events are per-app domains that the migration after that can force one at a time.

#### Domain order before more migrations: decision brief (approved 29 October 2026)

Routes, storage, backup, calls and the rest of the [first migration candidate](#first-migration-candidate-the-decision-with-the-evidence-25-october-2026) are done. The remaining question is which of v2's missing domains come next, now that two instances are v2-owned and the other 15 still cannot move.

**Method.** This brief reads the records at `ed3acc45` and the two v2-owned records, not the opening prompt's census. It ranks a domain by the instances whose migration it blocks, who owns each fact today, what v2 would own, what identity that fact would change, and whether the two v2-owned instances are already missing it.

**Census, checked.** There are 15 v1 instances under `apps/` and 2 v2-owned staging instances. The prompt's census is right that every v1 instance gets telemetry and notify variables, that 7 are workers (there are no jobs today), that every app has a team, and that 2 instances subscribe to events. It is wrong about team grants: **5** instances allow a caller by team, not 4. It also names only subscribers for events; **13** instances publish or subscribe, because publishing is an event fact too. No instance declares an app secret, no app customises alerts, and all 15 carry an offers commit.

| Domain | Fact and owner today | What v2 would own | Instances it unblocks | Identity cost | Missing in the two v2-owned instances? |
| --- | --- | --- | --- | --- | --- |
| **Telemetry** | The release states `telemetry: true` (an image need read from `swhurl.yaml`); the endpoint and protocol are platform values in `contract.py` and the `platform/otel` unit. | A release fact (`telemetry`) and the platform's endpoint/protocol (a profile block, or an interface the profile names). | All 15. | The release canonical form gains a fact, so release IDs move (all of them if encoded unconditionally) and the two v2 release records and both goldens are regenerated once. A profile endpoint moves the profile ID. No adapter or credential. | **Yes.** Their compiled HelmReleases have no `HOST_IP` or `OTEL_*`, so their images export no traces or metrics; the collector still reads their logs. |
| **Notifications** | The platform owns the address (`calling.NOTIFY_URL`); the token is a projected service-account token derived from the pod, not stored material. v1 renders both on every instance. | A profile `notify` address; the URL and the `notify` token in every instance's env and token volume. | All 15. | The profile gains a block, so its ID and every compilation ID move; no release or instance ID moves if the fact stays unconditional. No adapter or credential. | **Yes.** Neither has `SWHURL_NOTIFY_URL`; `swhurl-try-11` has a token volume for its callees only, and `swhurl-try-10` has none. Their notify helper therefore does nothing. |
| **Teams** (label and `team:` grants) | `apps/<app>/app.yaml` owns the team, written by `app-team`/the console; every instance's namespace gets the label. The instance's `allows` owns a `team:` grant, resolved against `apps/teams.yaml`. There is **no team network isolation** in v1: a team is an owner label and a call grant, not an access boundary. | An app-level team fact (a new app record, or an instance field), the registry read, and team-aware `allows` resolution. The namespace label follows. | The label: all 15. The grants: `demo-orders` prod and staging, `demo-shop` staging, and `swhurl-try-12` prod and staging (5). | A new app record is a new identity surface and compile input; putting `team` on the instance instead moves all 15 instance revisions and lets staging and production disagree. Team grants need the registry and a resolver change; `migrate` currently refuses them. | The label: **yes**, both namespaces lack `platform.swhurl.com/team`, so ClickStack has no `swhurl.team` for them. The grants: not needed by either (they allow app names). |
| **Worker and job kinds** | The release states `runtime.kind` (an image identity/need, fixed at creation); a job's schedule is an instance choice. | `worker` (no Service, probes or route) and, if wanted, `job` (a CronJob, no database). | 7 workers: `demo-notifier` staging, `demo-shopper` staging, `swhurl-try-13` staging, `swhurl-try-30` prod and staging, `swhurl-try-31` staging, `swhurl-try-32` staging. 0 jobs. | New release records for those 7, so their release IDs are new; web release IDs do not move. A job schedule adds an instance fact. No profile change. | No: both are web. |
| **Events** | The release owns `publishes` (derived from its AsyncAPI); the instance owns subscriptions and its NATS public key, with the seed in an encrypted file. `platform/nats/users.yaml` and each instance's `events.yaml` are shared/per-instance generated output. | The same, plus the shared users contribution, streams and consumers, and an encryption/credential adapter for the seed. | 13 have an event fact (12 publish, 2 subscribe; `swhurl-try-13` does both). With the kind slice, it completes 6 of the 7 workers. | The release IDs of the 12 publishers move; instance revisions move for every instance given an identity (or the 13 that declare events); the profile gains a NATS block. The seed is credential material, so this domain needs a separate approval and adapter. | **Yes.** `swhurl-try-10` staging lost its `visited` publication on migration and `swhurl-try-13` lost its consumer; v2 renders no NATS identity, stream or consumer for either. |
| **Secrets** (`envFrom`, Reloader) | The release states the names the image reads; the operator's SOPS file holds the values; the workload gets `envFrom`, a reload annotation and a Reloader namespace entry. | The names (release), encrypted values (credential adapter), `envFrom`/reload rendering, and the shared Reloader namespace list. | 0 today: no instance declares `secrets` and there is no app `secret.sops.yaml`. It is a prerequisite for a future app, not for the current fleet. | New release facts for future releases; new credential/encryption adapter and approval. Values do not belong in a release ID. | No: neither declares a secret. |
| **Alerts and dashboards** | Default dashboards and alerts belong to the platform jobs (`dashboards.py`, `app_alerts.py`), which discover live HelmReleases and namespace annotations. Only a custom threshold is an app fact (`app.yaml` `alerts`, rendered as a namespace annotation). | A custom alert setting, if it is ever used; dashboards need no v2 fact. | 0 custom settings. All 15 already get the defaults; discovery reads live HelmReleases, so v2 needs no fact for it and this is not a migration blocker. The v2-owned path is not separately verified. | An app-record field (if that record exists) or an instance field; no default identity change. | No custom setting; the default path is not separately verified for them, but it reads live HelmReleases rather than v2 records. |
| **What an app offers** | The release owns `description`, `apiPath` and the `offers` commit; the documents live in `apps/<app>/<env>/offers/`; the console and catalogue read them. None of it is a Kubernetes resource. | Release facts, the two HelmRelease annotations and `SWHURL_DESCRIPTION`; a home for the documents, or an explicit decision not to own them. | 0 for runtime. It restores the console link, description and catalogue entry for all 15; 6 have an API path and 11 a description. | Release IDs move for every release given the facts (all 15 if encoded unconditionally); no profile change. Documents are not applied, so their identity is a repository decision. | **Yes.** Both v2-owned staging instances have no offers annotation, no `SWHURL_DESCRIPTION` and no `offers/` directory, so the catalogue no longer sees them. |

**Ranking.** By breadth alone: telemetry, notifications and the team label (15 each) come first; events (13) second; worker/job (7) third; team grants (5) fourth; offers (15, but cosmetic and not a runtime blocker) fifth; secrets and custom alerts have no current user. By **what a slice completes**, the order is different: no domain completes an app until the universal facts exist, the worker kind is a prerequisite for the six event-using workers, and team grants are the last missing domain for the demo and insights web callees. Events is the largest, most credential-bearing domain and should not be the first experiment.

**Recommendation — first slice and order:**

1. **The always-on instance surface: telemetry, notifications and the team label.** They are the only facts every one of the 15 v1 instances and both v2-owned instances lack. Both v2-owned instances are missing all three live, so this slice closes real regressions as well as preparing migrations. It needs no new adapter or credential. Pay the identity cost once: the release telemetry fact, the profile notify fact and one team owner decision (recommended: an app-level fact, so staging and production cannot disagree; the alternative is an instance field, which moves 15 instance revisions). Evidence on the two v2-owned instances: both recompile with `OTEL_*`, `SWHURL_NOTIFY_URL` and the `notify` token, and both namespaces carry the team label that ClickStack reads.
2. **Worker kind** (and `job` only if the same change is cheap; no job exists today). It is small, needs no platform contribution, and makes the events slice immediately exercisable on workers. It removes the last rendering gap for `demo-shopper` staging only, because the other six workers have event facts, but it is the prerequisite for all seven.
3. **Events.** It is the largest domain and the one that needs an encryption/credential adapter and its own approval. With the kind slice it completes the six event-using workers, restores `swhurl-try-10`'s publication and `swhurl-try-13`'s consumer, and makes every publisher renderable. Do not hide the credential decision inside a migration.
4. **Team grants.** Only five instances need them, but after events they are the last missing domain for `demo-orders`, `demo-shop` and `swhurl-try-12`. This is the point at which the demo chain (`demo-shopper` → `demo-shop` → `demo-orders`, with `demo-notifier` subscribing) can move as a group instead of one caller without its callees.
5. **Offers.** Restore the console link, description and catalogue entry for every migrated instance. It is visible and cheap relative to events, but it changes no runtime behaviour, so it follows the functional domains.
6. **Secrets and custom alerts** only when an app needs them. They unblock nothing in the current fleet, and secrets carry a credential-adapter cost that should not be paid speculatively.

**First migration after the slices.** After 1–3, `swhurl-try-30` prod and staging, `swhurl-try-31` staging and `swhurl-try-32` staging are self-contained workers (they publish events, call nothing and grant no team) and can move together as the first worker group. After 4, the demo chain can move together. The first migration should not be `demo-shopper` alone: its call to `demo-shop` crosses ownership and would fail at the v1 callee, the same caller-cannot-move-alone finding as `swhurl-try-11`.

**Rejected alternatives.** Events first: broadest functional domain and the live regression, but the largest credential risk and it cannot complete a worker without the kind; it would also leave the universal gap untouched. Team grants first: only five instances, and none is complete without events and the universal facts. Offers first: visible, but no runtime and no app is unblocked. Secrets or custom alerts first: no current user.

**Approved 29 October 2026, and implemented offline.** The operator approved slice 1 and chose the app-level record for the team. The release, profile, app, instance and compilation identities moved once; the two v2-owned records, both goldens, the identity fixtures and the two rendered instances were regenerated, with the fixtures' identities recomputed independently in Python. The two v2-owned instances have **not** been applied: their compiled files are in the working tree, and the live pods still run the previous output until the operator asks for the commit, push and reconcile. The v1-caller gap remains deliberately out of scope.

**Slice 2, approved and implemented offline 29 October 2026.** The operator approved the worker kind. `runtime.kind` now accepts `worker`; the release states neither a port nor a health path for one, and the renderer omits the Service, the probes and the route while every other instance fact stays. No record field was added or moved, so no identity, golden or rendered file changed. `job` was deliberately left out: it needs a CronJob controller and an instance-level schedule, and no instance is one. No instance migrated — the six event-using workers wait for slice 3, and `demo-shopper` waits for its callee. [The slice record](v2-status.md#slice-record-the-worker-kind-29-october-2026).

**Slice 3, part one, approved and implemented offline 29 October 2026.** The operator said go on events. Because
the domain is the only one whose facts include a private key, it was split where the credential sits: v2 now
renders NATS's **user list** — the controller, the console and one entry per keyed instance, with the permissions
each needs — from a pinned snapshot, and `make check-v2` compares it with the file the platform runs, byte for
byte in meaning (17 users, identical order and permissions). No record field moved. The instance side, the seed
and the file's cutover are part two, and the seed decision is the operator's: adopt each migrated instance's
existing encrypted seed, or have v2 generate a pair and encrypt it to the platform's public age recipient.
[The slice record](v2-status.md#slice-record-events-part-one-the-servers-user-list-29-october-2026).

**Slice 3, part two, approved and implemented offline 29 October 2026**, with a standing principle from the
operator: favour a clean v2 contract over anything that preserves v1 compatibility, a one-way journey. The release
states `publishes`, the instance states its `events` block, the profile states the broker (`nats`), and the
resolution document carries subscriptions beside the pairs. A keyed instance renders its variables, its stream, its
consumers and a unit that waits for `platform-nats` and decrypts its folder; its seed is the encrypted file the
operator supplies, listed and copied and never read. The decision the operator took: **adopt** the predecessor's
pair rather than mint one, so the two instances v2 already owns wait for a minting slice. Every release, instance
and profile identity moved once. [The slice record](v2-status.md#slice-record-events-part-two-the-instance-side-29-october-2026).

**Slice 4, implemented offline 30 October 2026.** The operator said to take the next gate, which this ranking puts fourth and which slices 1–3 left as the last missing domain for the demo and insights web callees. It is smaller than the census row assumed: `v2/calls` already consented by team and the resolution already read each caller's own app record, so **no registry read was added** and the grant never holds a member list. The kernel's `allows` grammar gained `team:<team>` beside app names and `migrate`'s `allows: teams_are_not_modelled` refusal is gone; the five instances that declare a grant migrate and their records carry it unchanged. Nothing was applied, and no existing record, golden or identity moved. [The slice record](v2-status.md#slice-record-team-grants-30-october-2026).

## 6. Deliberately deferred

Do not make feature parity the first goal. This is the one list of unimplemented features and incomplete transition semantics: how v2 treats each today, and which domain or next boundary must resolve it. v1 supplies examples, not the required replacement contract. Update the list in the change that defers, implements or removes something; a phase's own classification table links here. The list does not approve removing anything from v1.

| Feature or semantic gap | v2 today | Decide by |
| --- | --- | --- |
| Egress proxy: proxy variables, port allocation, egress grants | Rendered and live since [the cutover](#the-egress-cutover-implemented-26-october-2026): a stable port per instance, the client variables, the grants annotation and policy, and the proxy's configuration with grants enforced. A database instance's backup host is [the platform's grant](#the-backup-host-is-the-platforms-grant-implemented-28-october-2026). **A v2 record cannot declare a host of its own**: a v2-owned instance's grants are the backup host only | The first v2-owned instance that must reach an outside host: an `egress` field on the instance record (an identity change) |
| The proxy's configuration as the live one | `make app-sync` still writes `platform/egress/proxy.yaml`, which Flux applies; what v2 renders is compared with it offline and applied nowhere. No instance declares a grant, so an enforced proxy would refuse every outbound host | A live slice after v1's `granted=-` history has been granted or explained ([stage E3b](plan.md#stage-5-egress-grants)); needs the operator |
| An instance that must not reach outside | Every instance in the snapshot is allocated a port, and nothing withholds one | The first kind that must not egress |
| A v2-owned instance's records | Kept in `v2-records/`: v2's own `instance.json`/`release.json`, one app-level `app.json` (its team), and the committed resolution `calls.json`, so the instance [recompiles from Git alone](#a-v2-owned-instance-recompiles-from-git-alone-implemented-26-october-2026) and CI fails on a stale file. **The translation is a use case** ([`migrate`](#the-migrate-use-case-implemented-26-october-2026)), and the record now [states its own declarations](v2-status.md#slice-record-the-record-states-its-declarations-a): `calls` and `allows` are facts of the pair, `resolve` reads them from either record shape, and **the v1-shaped YAML copies are gone**. The [always-on slice](v2-status.md#slice-record-the-always-on-instance-surface-29-october-2026) added the app record and its team label | Done for the app record, its team label and a team grant in `allows` ([slice](v2-status.md#slice-record-team-grants-30-october-2026)) |
| Freshness of a v2-owned instance's backups | **Both facts are observed**: the job (`observe` reads the CronJob, the pods its runs create and the newest run's Job) and the newest archive (`observe --archive` lists the instance's own prefix with the operator's credentials). `verify-platform` still builds its expected list from the app records in `apps/`, so a v1-side check cannot see a v2-owned instance; v2 can | Done; the archive's credential is the operator's environment, and a standing key for a console check is the next step if one is wanted |
| The egress namespace's own egress | Not restricted: the rendered policy states `Ingress` only, as v1's does, and the proxy's way out is the platform's existing state | A platform-wide decision to default-deny the platform's own namespaces (v1's E3a covers instances only) |
| DNS egress for an instance that may reach anything | Rendered: an instance with a call or an allocation admits the cluster's DNS (UDP and TCP 53) alongside its callee and the proxy | Done; it was the calls slice's defect |
| Routes: `public` exposure, a per-instance host override, route-level observation (Ingress readiness, certificate state) | `authenticated-web` renders its Ingress, TLS annotation and middleware from the profile and is `private`'s only alternative; `public` is still refused (`unsupported`), no instance stores a host, and observation does not verify a route | The first instance that needs a public route or an unusual host |
| App kinds `job` (a CronJob schedule) and a command override | Refused (`unsupported`). `worker` is modelled: a release states it, the renderer gives it no Service, probes or route, and [a migration carries it](v2-status.md#slice-record-the-worker-kind-29-october-2026) | The first instance that needs a scheduled job or a command override |
| Teams: `team:` call grants and any future team access rule | **Done 30 October 2026**: the namespace `team` label is rendered from the app record, and an instance's `allows` states `team:<team>`, which the resolver matches against each caller's own `app.json` — so no registry read is needed and a grant never holds a member list ([slice](v2-status.md#slice-record-team-grants-30-october-2026)). v1 has no team network isolation: the label is ownership and telemetry, not access. A team that no app declares is an unused grant, exactly as an absent app name is; v1's `make check-apps` rule `team` refuses an unregistered team on the v1 side, and v2 has no equivalent check | A team access rule, or a v2 check that a granted team is registered |
| Calls between apps: caller tokens and signing keys, `SWHURL_<APP>_URL`, callers in the NetworkPolicy | The [resolver, pair rendering, resolution document and delivery](#calls-between-apps-resolver-and-pair-rendering-implemented-offline-25-october-2026) exist, and the call's network rules now state the cluster's pod block with the token as the caller's identity ([why](#calls-on-the-pod-network-with-the-token-as-the-callers-identity-implemented-26-october-2026)). The bridge still reports calls as gaps. **Proved live 26 October 2026:** `swhurl-try-11` calls `swhurl-try-10` and the callee answers `caller.id=swhurl-try-11` — the network by the pod block, the identity by the audience token, whose subject requires the caller's pod to run as the service account named after the app | A policy implementation that honours selectors if network-level identity is wanted; otherwise the bridge's `calls` section |
| One platform ConfigMap for token verification, replicated into each managed namespace | **Built:** `swhurl-v2 auth` renders one identical `swhurl-auth` per managed namespace into `platform/auth/`, the `platform-auth` unit applies it, and CI regenerates and diffs it. Instances mount the fixed name and claim none of it; a rotation rewrites one folder. The allow-list stays per instance, and no app unit can depend on the platform unit (namespace creation would deadlock), so a new app's pods wait one reconcile | Done; the env forms and the `auth-revision` annotation are retired for v2-rendered callees (29 October 2026): files only |
| A v1 caller of a v2-owned callee | v2's resolution connects the pair and the callee accepts the caller's token, but v1 treats a v2-owned instance as absent and renders the caller no address and no token (`make app-calls` reports it waiting): `swhurl-try-12` staging holds such a pair and cannot call `swhurl-try-10` staging | v1's caller rendering reading v2's records, or the caller's own migration |
| A v2 caller of a v1-owned callee | v2 renders the caller's addresses and tokens, but v1 renders the callee's allowed callers from v1 instances only, so moving the caller out of v1 drops it: after `swhurl-try-11` production migrated, `swhurl-try-10` and `swhurl-try-12` production no longer list it | v1's callee rendering reading v2's records, or the callee's own migration |
| Notifications: `SWHURL_NOTIFY_URL`, the projected `notify` token | **Done:** the profile states the address and every instance renders the URL and the projected `notify` token beside its callee tokens ([slice](v2-status.md#slice-record-the-always-on-instance-surface-29-october-2026)) | Done; the address is a platform fact, not a per-app one |
| Events: NATS users, streams and consumers | **Done, live.** The server's **user list** comes from a pinned snapshot and `make check-v2` proves it is what the platform runs ([part one](v2-status.md#slice-record-events-part-one-the-servers-user-list-29-october-2026)); a keyed instance renders its `SWHURL_EVENTS_*` variables, its `Stream` and `Consumer`s, the unit's `platform-nats` dependency and `decryption`, and the folder lists its seed ([part two](v2-status.md#slice-record-events-part-two-the-instance-side-29-october-2026)); and v2 **mints** a pair with `swhurl-v2 mint`: the seed is drawn from the command, encrypted to the profile's `sops.recipient` by the one adapter that runs a command, and written beside the record, which states the public key ([slice](v2-status.md#slice-record-minting-a-broker-identity-29-october-2026)). `compile`/`migrate` still take the encrypted file as a declared input and never read it | Done; a public key cannot be checked against its seed without decrypting, so drift is proved by the workload signing in |
| Promotion of a v2-owned instance | **Implemented offline 30 October 2026 and first performed live the same day**: `kernel.SelectPromotion` and `swhurl-v2 promote` take the release staging selects into production's record pair and compiled files, with production's own facts unchanged; the refusals are above, and `swhurl-try-11` production was migrated and then promoted ([evidence](current-state.md#the-first-live-promotion-swhurl-try-11-production-moves-to-v2-30-october-2026)). **A first promotion (creating production) is not modelled**, a rollback's direction is stated rather than decided, and readiness is the caller's check | A create-instance operation for a first promotion; a rollback decision |
| Secrets (`envFrom`, Reloader) and bindings to resources | Non-empty `bindings` refused; no secrets | Phase 7, per app |
| Telemetry: the OTLP endpoint and its variables | **Done and live 29 October 2026**: the release states whether its image reports, the profile states the collector's endpoint, protocol and the block its address is in (`telemetry.nodeCIDR`), `HOST_IP` is defined before the endpoint that expands it, and a reporting instance's policy admits the collector's block on the endpoint's port ([slice](v2-status.md#slice-record-the-telemetry-export-29-october-2026)). It was found blocked the same day (the policy admitted no collector) and fixed in its own slice | Done; per-app endpoint overrides remain a future release fact, and a multi-node cluster states a block covering every node |
| SQLite storage: the volume, its path and the single writer | The [storage slice](#sqlite-storage-the-volume-implemented-and-proved-live-25-october-2026) renders a retained 1 GiB-class volume, `DATABASE_PATH`, `Recreate`, and a namespace Flux will not prune, and now a claim that already exists can be [adopted](#a-stateful-handover-an-allocation-outlives-the-records-and-a-volume-can-be-adopted-implemented-26-october-2026) instead; proved live including restart and removal survival | Done for the volume; growth is refused |
| SQLite backup and restore | The backup job is rendered, its job and its newest archive are [observed live](#the-backup-observation-implemented-offline-26-october-2026), a [restore is planned and verified](#the-restore-plan-implemented-26-october-2026), and **v2 now performs the restore**: the age-key and read-URL Secret, the Job, the sequence with its undo, and the report — [offline half implemented](#the-restore-v2-performs-option-2a-offline-half--27-october-2026), **not yet run live**. A content-changing restore, another instance's archive and a point in time remain | A live restore of `swhurl-try-10` staging over its newest archive, then a content-changing one |
| Alerts | Not generated | Phase 7, per app |
| Resources from the image's minimums; a per-instance CPU choice | CPU from the profile; one memory value for request and limit | When an app needs more |
| Quota headroom for an instance's other pods | The backup pod is counted (`replicas + 1` for a rolling update, plus one for the backup, with the job's own requests); no other second workload exists | The next feature that adds pods to an instance's namespace |
| Image automation, automatic release discovery, auto-merge | **The offline half is implemented** ([contract](#image-automation-what-an-image-states), [slice](v2-status.md#slice-record-image-automations-offline-half-29-october-2026)): a staging unit renders the `ImageRepository`/`ImagePolicy` pair and `swhurl-v2 deploy` resolves the newest tag through `adapters/registry`, reads the image's declaration label, refuses what the instance cannot take, selects and compiles, and writes records and files. **Done and live 30 October 2026**: the image states its declaration and its revision, the v2 Deploy workflow is started by the image Alert, and all three v2-owned staging instances followed their images to the running pod ([evidence](current-state.md#the-first-image-delivered-by-v2-30-october-2026)). Auto-merge is not modelled | Auto-merge; a production path for a v2-owned instance |
| An image's declaration | The build writes one JSON document into the image's config labels (`platform.swhurl.com/declaration`); v2 reads it with the manifest, and `secrets`/`memoryMiB` only to refuse them. The declaration is not the whole app: offers remain deferred | When an offer must travel with an image, or a fact the declaration omits must be applied |
| The render fingerprint in the resources | The compilation ID is in the compilation document only. Observation did not need it in the cluster: the unit's applied revision is read instead, and compared only when the caller supplies the intended commit | Phase 5, if a later observation needs an artifact identity in the cluster |
| Reloader's watch list | **Done and live 29 October 2026**: `platform/reloader/values.json` is the ConfigMap its HelmRelease merges as values, rendered from the profile's own namespaces and every instance that declares a Secret; v1's writer is deleted and the Render workflow regenerates it ([slice](v2-status.md#slice-record-reloaders-watch-list-29-october-2026)). One semantic difference is stated: a v1 Secret file whose names its release has not declared yet is not watched, because v2 reads declarations and the snapshot never reads an encrypted file | Done |
| The unit's registration in `clusters/home/kustomization.yaml` | The unit file is written at `delivery.unitsRoot` by `compile --out`; its one-line registration stays a reviewed edit by the operator ([decided with the delivery slice](#first-delivery-slice-implemented-offline-25-october-2026-operator-run)) | Decided; revisit only if per-instance registrations become noise |
| Live ownership of what a preview would create | The [fourth bridge slice](#phase-3-slice-4-live-ownership-implemented-25-october-2026) reads the preview's five claims from a live API server and reports present/absent/unavailable; the other declared objects (Secrets, CronJobs, Streams, image policies) are not read, so this is claim coverage, not a full live inventory | With the first migration, if a migration needs the wider inventory |
| v1's objects as `foreign` ownership | Empty; the [phase-3 brief](#phase-3-shadow-not-synchronise) proposes declared app-object identities from a pinned snapshot, independent of preview coverage, including unmodelled/residual apps | Phase 3 |
| Complete release/source/build relationships and artifact verification | The current descriptor requires source/type/platform claims but does not verify them. `migrate` states where every fact came from and checks the supplied commit against the record fields that abbreviate it, but the pair cannot distinguish a checked claim from an unchecked one. **An unchecked claim was wrong in practice:** `swhurl-try-10` staging's migrated release claimed an image manifest, but its digest (copied from v1, whose image automation pins the tag's digest) is an image index; the release selected on 29 October 2026 names the index's `linux/amd64` manifest, checked by hand against the registry. A read-only registry adapter (public images need no credential) would make that check part of writing a release; **for automated releases it now does** — `adapters/registry` reads the platform manifest and its labels — while a hand-written release's claims are still unchecked. Phase 3 would preview known workload facts without inventing complete releases or requiring a manual claims catalogue | Release/build domain modelling as it develops; before claiming complete or independently verified release semantics |
| Scoped workload preview/comparison independent of complete releases | The [first phase-3 slice](#first-slice-proposal-pure-http-preview-and-comparison) implements shared `RenderInstance` and pure `shadow.CompareHTTP`; the [second](#phase-3-slice-2-legacy-translation-and-a-preview-report-implemented-offline-25-october-2026) translates a predecessor's records into scoped facts with a source per fact and a gap per unmodelled fact, and `swhurl-v2 preview` composes translation, rendering and comparison into a `v2-preview` document. Snapshot acquisition, ownership inventory and foreign-object integration remain unimplemented | The next bridge slice for acquisition and ownership; before claiming the bridge complete |
| Reading a predecessor's records from its repository | `make v2-snapshot REVISION= OUT=` exports one pinned revision as JSON (records and object identities; SOPS files skipped), and v2 reads only JSON. The exporter is operator tooling, so it is also a console-image input | Unchanged unless an in-cluster or automated bridge needs the records without a host step |
| Observation of what is running (the first adapter) | The [first observation slice](#first-observation-slice-what-is-running-implemented-offline-25-october-2026) implements the pure projection and one read-only adapter for four kinds — CronJob and Job since [the backup slice](#the-backup-observation-implemented-offline-26-october-2026), which also answers a database instance's protection; the [second](#second-observation-slice-the-target-from-compiled-output-implemented-25-october-2026) derives the target's objects, selector, container, digest and replicas from the compiled HelmRelease and the unit's identity from the compiled unit. The intended source revision and the credential material stay the caller's, and there is no command. Pod-level facts (restarts, events, scheduling), ownership and every kind but those four are not read | The next observation slice (a credential source and the report envelope), then live verification with phase 6 |
| A credential source for reading a cluster | The [third observation slice](#third-observation-slice-a-credential-source-and-an-observe-command-implemented-25-october-2026) reads a kubeconfig in its **JSON** form (`kubectl config view --raw -o json`) and refuses a credential plugin. YAML, and the in-cluster service-account token, are not read | Reopened by an approved YAML dependency, or by a v2 that runs inside the cluster |
| An operator-facing observation report (envelope, exit codes, document schema) | `swhurl-v2 observe` writes `v2-observation` version 2 to stdout: source, time, verdict, findings, the protection's own answer, assessed/unassessed, not covered, target. Exit 0 means a report was produced, never that the instance is running | Changes with the next consumer of the document (a console, a checker); `version` rises with its shape |
| Observation from a live cluster: what it cannot read | Reads are the four kinds; pod phase, restarts, events, terminating and evicted pods, foreign ownership and every other kind are not read. A `--revision` is required, because the command cannot know which commit Flux should be at | `observe` now takes `--calls` and `--egress`, so it can observe what compile can render; further kinds with the first consumer that needs them |
| What an app offers (offers documents) | **Implemented offline 30 October 2026** ([slice](v2-status.md#slice-record-what-an-app-offers-30-october-2026)): the release states `description`, `apiPath` and the commit its documents were read at; an instance renders `SWHURL_DESCRIPTION` and the two annotations; `migrate` carries all of it and copies the documents as a declared input beside the record; `compile` writes them beside the manifests, where the console reads them. **v2 generates and refreshes nothing**: a document comes from the predecessor or from the operator, and no repository is read. A deploy keeps the three facts (the image's declaration cannot state them), so a new image keeps the documents of the commit they were read at | A repository read (`adapters/` for the app's Git host, its credential and approval), so a new image's documents travel without a person; until then a v2-owned instance's documents are as old as its migration |
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
| `layers` | Every v2 package has a declared layer (0 pure model, 1 adapters, 2 use cases, 3 commands) and imports only v2 packages in lower layers, except that a fake may import the adapter it fakes ([why](#4-development-layers-and-adapters)). A new package is given its layer in `Layers`. |
| `pure` | Pure packages import only the allowlisted standard packages, print nothing and assign no package variable ([why](#the-pure-model)). Time, identifiers and snapshots are arguments: `time` is allowed for parsing and comparing a *supplied* timestamp, and its clock functions (`time.Now`, `time.Since`, `time.Until`, timers and ticks) are refused by call. |
| `no-subprocess` | v2 code (not its tests) starts no process except the encryption adapter `adapters/sops`, and names no `kubectl` or `flux` command anywhere ([why](#kubernetes-the-api-server-not-kubectl)). The one exception is priced in [the minting slice](v2-status.md#slice-record-minting-a-broker-identity-29-october-2026): a second implementation of age and the SOPS envelope would be a hand-built cipher, and the adapter reads no cluster and no private key. |
| `public-api` | The exported API of every library package matches `archtest/api.txt`, so an interface change is a deliberate, reviewed diff. |
| `budgets` | No function exceeds complexity 10 or 60 lines unless `archtest/budgets.json` lists it with a reason; a listed function may shrink but not grow, and its entry goes once it fits. |
| `places` | A package's place matches its layer: layer 0 is listed in `Pure`, layer 1 is under `adapters/`, layer 2 is `orchestration`, layer 3 is a `main` package under `cmd/` ([why](#extension-recipes)). |
| `adapter-complete` | Each adapter `adapters/<name>` follows the [new-adapter recipe](#extension-recipes): it exports an interface, has raw fixtures in `testdata/`, and its fake in `adapters/<name>/<name>test` satisfies each interface and shares one `Contract` suite with the real adapter; only tests import a fake ([why](#fixtures-and-fakes)). |
| `orchestration-wires` | The use-case layer (`orchestration`) parses no arguments, prints nothing, chooses no exit status and keeps no mutable package state: it assembles, sequences and reports, and the command above it does the rest ([why](#4-development-layers-and-adapters)). |
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

The four commands are `release-id FILE`, `instance-revision FILE`, `select-release --instance FILE --release FILE --expect REVISION` and, from phase 2, `compile --instance FILE --release FILE --app FILE --profile FILE` ([compiler](#phase-2-establish-compiler-ownership)). Only explicitly named files are read; there is no root discovery, registry, external command, credential/config lookup or file-write command. Valid results go to stdout, diagnostics to stderr. Exit `0` means success/no-op, `2` means usage/validation/refusal (including an input file larger than 1 MiB, which is not read past that), and `1` means file/output I/O failure. A flag given twice is a usage error, not the last one winning; flags may also be written `-instance=FILE`, and `--` ends them; `--help` or `-h` prints the usage to stdout and exits `0`. Refusal stdout is empty. If stdout is a pipe whose reader has gone, the process ends by `SIGPIPE`, as Unix tools do, rather than exiting `1` (catching it would need `syscall`, which the `no-subprocess` rule keeps out of v2). Output failures cannot retract bytes already accepted by the destination; they must return nonzero rather than claim success.

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

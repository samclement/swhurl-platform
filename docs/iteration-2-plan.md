# Iteration 2: the Go replacement and its contract

**This page is the contract: records, identity, selection, the compiler, the refusal codes, the layers and the guardrails. The replacement is live: v2 owns every instance, the records and the checks that read them. The goal is deeper than that — one Go implementation and no Python — and its progress, remaining work and deferred features live in [v2 status](v2-status.md). [Status and the registry](workstreams.md) are separate; dated evidence is in [current state](current-state.md).**

This plan develops the recommendations in [the architecture review](architecture-review.md). [Architecture](architecture.md) remains the canonical description of deployed behaviour; [plan](plan.md) section 0 remains the index of approved work and links here for the v2 contracts.

## Goal and recommendation

Build one implementation in Go — kernel, compiler, CLI, console, checks and recovery — and retire `tools/swhurl` (Python). Bash stays for host glue. The transition happened in place, not alongside a second control plane: v2 owns every instance and the live loop today, and each remaining Python surface is replaced by its own approved slice ([the distance](v2-status.md#remaining-python)).

The foundational distinction is release construction and identity versus environment selection. The existing `Release` and `Settings` records already separate much of the information, but releases are still stored as environment snapshots rather than independently identified objects.

The first milestone proved this example without GitHub, Kubernetes or the console:

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

## 1. Decision and boundaries

**Decided 24 October 2026:** a parallel offline v2 kernel, proven before it touched the platform and then cut over selectively, rather than refactoring v1 in place or building a separate live platform. The cutover happened on 31 October 2026 and v1's writer is deleted, so there is no second control plane to retire: the remaining work is replacing the Python surfaces v2 has not yet taken over.

**Assumptions that still constrain a slice:** Git remains the authority for deployment intent; Kubernetes and Flux remain the runtime substrate ([architecture](architecture.md)); lower maintenance cost matters more than preserving every convenience; and the platform stays operational while each slice lands.

### Language direction: Go, and eventually no Python

**Operator direction, 2 November 2026: the goal is one Go implementation and no Python.** v2 becomes the platform's only implementation, and `tools/swhurl` (Python) is retired. The console is the largest remaining surface; porting it is not approved, and its own decision brief is a gate. Bash stays for host glue.

What one binary means in practice:

- The pure model, parsing, validation and offline CLI need no Python or external executables at runtime.
- A server can embed templates, CSS and HTMX and share the domain package with the CLI; CLI and console may be one binary but take separate credentials and permissions.
- Git, SOPS, Helm and Flux stay explicit adapter dependencies; do not embed large libraries only to reduce the executable count.
- Copier and scaffolding stay outside the v2 boundary.
- A container starts non-root with CA certificates and an explicit temporary directory; an empty base image is not a goal.
- The console image currently carries Python, git, kubectl, Helm and SOPS; removing Python alone does not remove the rest.

### Go-specific contracts

- Structs are not automatically immutable. Own slices/maps carefully so transitions cannot mutate their inputs through aliases.
- Strict decoding must distinguish missing values from explicit zero/false where relevant, reject unknown fields and versions, and validate combinations.
- Specify canonical identity encoding independently of Go's default serializers. Include golden vectors so another implementation can reproduce release/spec identities; account for YAML normalization and JSON escaping.
- Preserve error, secret-redaction, dry-run and failed-read contracts intentionally. Go's types do not replace these tests.
- Prefer ordinary packages/functions and narrow interfaces at actual effect boundaries. Do not introduce a provider/plugin framework.

### Future UI: Go plus HTMX

The console already uses HTMX: [base template](../v2/cmd/swhurl-v2/console/templates/base.html) and page templates contain HTMX navigation, form and refresh attributes. Go + HTMX would continue the server-rendered approach, not replace a client-side SPA.

The useful performance direction is:

```text
Browser → useful page shell → independently loaded status sections
        → bounded observation reads → HTML fragments
```

Return useful content before optional integrations finish; refresh only relevant sections; avoid duplicate expensive reads across fragments; show loading/unavailable/stale evidence honestly. Preserve ordinary links/forms, backend authentication/authorisation and CSRF protections. Polling is sufficient initially; use SSE only where streaming adds value.

HTMX does not make server work fast, and fragmenting one expensive request into many can increase work. The existing [read-performance documentation](console.md#read-performance) and architecture review identify remote historical-evidence work as a major cost. Simplifying that promise and sharing immutable reads matter more than changing template engines. The port itself is [section 11](#11-the-console-port-strangled-from-inside-approved-3-november-2026).

## 2. Phase-1 model and contract

Three value records and one pure operation live in [`v2/kernel`](../v2/kernel/records.go): the release, the instance, and the app-level record the always-on slice added. All domain members are scalars or nested value structs: copying a record cannot alias a map, slice or pointer. `PlatformProfile` is the one exception, its `delivery.dependsOn` list, which the compiler copies wherever it is used. Construction alone does not validate a Go value; identity, encoding and selection validate it. No network, filesystem, environment, clock, randomness or subprocess access belongs in this package.

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
dispatch names the app — so they are kernel constants, tied by a test to the committed objects and to
`contract.py`.

The trigger is Flux's; the facts are the image's. An image states its declaration in its config label
`platform.swhurl.com/declaration`: one strict JSON document at `schemaVersion` 2 ([the contract](#10-the-declaration-travels-with-the-image-approved-1-november-2026)). Version 1 is refused: an image either states its whole declaration or is not this platform's.

The same build states the commit it was built from, in the standard `org.opencontainers.image.revision` label. The
tag carries an abbreviation (`<run>-<7 hex>`), and a release states a full 40-character commit, so the image states
one and the tag has to agree: a tag whose suffix the revision does not start with is another build and is refused,
and a revision that is itself an abbreviation is not a commit. Neither label is optional.

`adapters/registry` is the one integration: one public repository's tags, then one tag's index resolved to its
`linux/amd64` manifest, then that manifest's config labels. It reads anonymously, takes no credential, and names no
policy: the tag ranking is the kernel's. `swhurl-v2 deploy` composes them — decode the records, read the tags, take
the newest by the platform's policy, describe it, read the declaration and the revision, refuse what the instance
cannot take, select the release against `--expect`, compile, and write the record pair and the compiled files in one
effect. A tag that matches no policy is a refusal; an image already selected is a no-op that writes nothing; a
registry it cannot read is exit 1; and a refused image leaves the running one selected. With `--records` the same
delivery runs for every `v2-records/<app>/staging`: one instance's refusal or failure does not stop the others, the
exit status is the worst outcome, and an instance takes the allocation document only when it records a grant for it.
The image Alert starts the workflow, and an hourly run catches what it missed.

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

Instance revisions identify **content, not edit history**. Changing a spec then restoring identical content restores its revision (ABA). The model has no store, sequence counter or publication CAS; a writer protects its own read/write boundary.

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
| `wrong_release` | Compiling a release the instance does not select |
| `over_quota` | An instance whose pods can never fit the profile's quota |
| `ownership_conflict` | An object claimed by two compilations, or by one and someone else |

When an input breaks several rules, the first in this order decides, so the same input always gets the same refusal: the document itself; then object by object, the top level first: missing required fields (in schema order), unsupported fields, nulls (each in key order); then types and integer ranges; then each record's fields in the order of its field table; then the identity assertion; then selection's contractual order above. An unsupported field is reported at its parent object (`unsupported: runtime: unsupported field`), because the key is input and is not echoed; a type or range error names the record (`release` or `instance`).

### Acceptance tests

[`kernel_test.go`](../v2/kernel/kernel_test.go) covers record round-trips, strict parsing, golden identities/normalisation, per-field identity changes, ID/content mismatch, environment separation, stale/no-op ordering, refusal input preservation and value-only records. Refusal precedence is tested on inputs that break several rules at once, each decoded 50 times, and selection's order on requests that break two of its checks. [`main_test.go`](../v2/cmd/swhurl-v2/main_test.go) covers CLI boundaries and a real built binary with empty PATH and no credential/config environment. It checks success, no-op, stale/wrong-app/unsupported refusals by their code, I/O errors, safe diagnostics, empty stdout on refusal and unchanged input files. [`coexistence_test.go`](../v2/coexistence_test.go) guards the Flux paths and console-image boundaries.

## 3. Coexistence boundaries

The Go module is [`v2/`](../v2/go.mod), module path `github.com/samclement/swhurl-platform/v2`. Fixtures stay in `v2/testdata/`; compiled output goes to temporary directories. The narrow exception in `AGENTS.md` and [contributing](contributing.md#operator-tooling) covers the module, Go tests and direct CLI invocation only; external-effect adapters need separate approval.

Root-level placement matters: the console Dockerfile copies `tools/`, and its content hash includes the entire `tools` tree. Putting Go under `tools/` would include it in the console image inputs. `v2/` is outside those inputs, the Docker build allowlist and Flux's active paths, and it writes the tree through v2's use cases rather than v1's writer.

- v2 is the live authority: it renders, writes and checks every instance.
- Do not reference v2 fixtures or scratch output from Flux.
- Do not import v1 orchestration into the v2 domain.
- Reuse existing infrastructure where language boundaries permit, but do not call Python merely for reuse. A Go implementation keeps the guarantees of v1's `Runner`/`FakeRunner` (an unexpected fake call fails, dry runs change nothing, Secret values are never printed, a failed read is reported as a failure) on typed adapters, not on a command runner ([development layers](#4-development-layers-and-adapters)).
- Do not build bidirectional synchronisation or maintain two authoritative specifications for an instance.
- Never let two implementations reconcile the same Kubernetes resource.

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

This is a responsibility model, and since 26 October 2026 it is also the checked layout: **layer 0** the pure model (`kernel`, `calls`, `observe`), **layer 1** one adapter per dependency (`adapters/files`, `adapters/kube`), **layer 2** the use cases (`orchestration`), **layer 3** the command (`cmd/swhurl-v2`). `orchestration` assembles the inputs a use case needs, calls the kernel's decisions in order, performs the use case's one effect through an adapter, and reports either the result or where it stopped; it parses no arguments, prints nothing, chooses no exit status and holds no mutable state (the `orchestration-wires` rule). A command is then flags, I/O, exit codes and output. Boundaries are still revised only where a use case needs it, not for the numbers' own sake.

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

Generating Kubernetes resources is pure; applying them is not. Deciding which credentials are needed is pure; generating and encrypting them is not. v1's `allocate` mixes allocation decisions, filesystem checks, key generation and encryption in one step; in v2 split it into (1) a pure calculation of the allocations and material required, (2) adapters that return keys and encrypted material, (3) a pure validation of what came back and compilation of the completed plan. Preparation failures are then testable without generating credentials or writing files. This is a v2 design rule, not a v1 change.

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

### Helm: explicit chart acquisition and local rendering

[`adapters/helm`](../v2/adapters/helm) implements `Renderer`: an explicit existing chart directory,
expected chart metadata name/version, release name, namespace and JSON values in; every rendered object as owned JSON bytes in document order,
or `refused_render` with a fixed reason and no objects out. It runs only `helm template` with
`--include-crds`, no kind filter, chart patch, dependency acquisition or cluster read. Acquisition is a separate operation (`swhurl-v2 chart`); rendering never fetches a dependency.
The adapter reads `Chart.yaml` (at most 64 KiB) and refuses a name/version mismatch, regardless of directory name. It writes values privately (0700 directory, 0600 file), removes them on
success and failure, and never returns subprocess or parser diagnostics. Values are limited to 1 MiB,
stdout to 16 MiB, stderr to 64 KiB (discarded), subprocess duration to 30 seconds and inherited-pipe waits
to another second. A missing executable, timeout, nonzero exit, oversized stream or malformed later
document returns no partial result.

The operator approved one maintained YAML parser, `go.yaml.in/yaml/v3` v3.0.6, confined to Helm, the explicitly approved [app-source adapter](#producer-first-python-retirement-approved-5-november-2026) and the [manifest decoder](#17-check-repo-in-go-the-decisions-not-the-subprocesses-approved-8-november-2026) `check-repo` reads the checkout through.
Its [README](https://github.com/yaml/go-yaml/blob/v3.0.6/README.md) identifies YAML-organization maintenance
after the original repository became unmaintained; the [tag changes](https://github.com/yaml/go-yaml/compare/v3.0.5...v3.0.6)
include removing its upstream test dependency (there is no GitHub release-note entry for the tag).
**The numeric guarantee starts at Helm stdout, not the input values.** Helm may already round a value
or template expression; the adapter cannot recover its earlier spelling and is not a replacement values
engine. A real chart test demonstrates Helm rounding an explicit float conversion. JSON values are
validated with stdlib JSON tokens/`UseNumber`, not YAML: decoded duplicate keys, non-object roots,
excess depth and out-of-range integers are refused. Validated values are then decoded with `UseNumber`
and marshalled before the private file is written, so JSON surrogate pairs never reach Helm's YAML values
reader. Input and normalized values are each bounded to 1 MiB. Real renderer tests (Helm 3.19 and 4.2)
compare escaped/literal emoji output; validator acceptance alone is not the rendering guarantee.

Stdout nodes are decoded without a float64 intermediary: signed/unsigned 64-bit integers remain exact and
decimal/exponent values keep their decimal precision as JSON numbers. Implicit plain numeric lexemes are classified independently of the YAML
resolver (which can label overflow `!!str`); quoted and explicitly tagged strings remain strings. Integers outside that range,
nonfinite numbers, timestamps, binary/custom tags, aliases/merges, non-string keys, duplicate keys,
invalid identity/type shapes, excessive nesting and malformed streams are refused. Truly empty/comment
documents are ignored, but an empty stream is a failed render. No namespace or missing object field is
invented. Identity validation is the common object/path-segment shape, not guessed per-kind Kubernetes schemas (RBAC names may contain colons). A nonempty stream with no workload is left to policy to refuse, not a successful policy check.

The fake and real adapter run one contract suite. Raw stream tests cover conversion and atomic failures;
a tiny local chart proves integer retention, unfamiliar kinds and CRD inclusion; the unmodified pinned
app-template renders compiled production values. `REQUIRE_HELM=1` turns missing Helm or chart into a
failure. Helm 3.19 and the built console image also exercise the real boundary ([evidence](current-state.md#app-policy-go-gate-4-november-2026)).

### App policy: all compiled instances

`swhurl-v2 policy --records DIR --profile PROFILE.json --out ROOT --chart LOCAL_DIR` is read-only.
`--out` is the checkout used for team/channel availability, not a write destination. The command reuses
`renderTree`, `renderFolders`, `withNotify` and orchestration `Inputs`/`Compile`, not another records reader.
It validates the entire input tree before invoking Helm, then checks every instance, including production,
in application/environment order. The chart is explicit and its metadata must match both profile and
compiled HelmRelease. No chart acquisition, Kubernetes access or writer is used.

`policy` is layer 0 and stdlib-only. Its small `Platform` projection contains policy limits and route/storage
constraints from the validated profile; its other inputs are existing compiled documents and complete
chart-rendered objects, not another instance model. It emits sorted, distinct `{rule,object,detail}` findings
without environment/Secret values. `orchestration.CheckPolicy` wires compilation, the Helm adapter and those
pure decisions. `v2-policy` version 1 reports each application/environment with `findings:[]` and optional
`renderFailure`; subprocess diagnostics are never included. Render failure is not a policy violation, and
one failed render does not skip later instances. Exit 0 means every instance passed; 1 means a render or
rule failed; 2 means usage/input validation refused. Invalid input or malformed object evaluation prints
no partial report. Empty input and a chart with no application Deployment cannot pass.

| Contract | Owner and check |
| --- | --- |
| Records, teams, channels, grants, connected calls and source intent | Existing decoder/resolver/compiler and `render --check`; no Python records compiler added |
| Actual workloads, including init containers and compiler-owned backup | Go policy checks effective non-root context, capabilities/escalation, image pins, resources, token/host access, exact runtime image/replicas/resources/UID/probes (HTTP defaults, scheme, connection host and headers) and Service ports |
| Identity, calls and events | Actual main environment/ordering and seed references match compiled resolved facts; verification/token directories are read-only whole mounts (no subPath/subPathExpr), projected audiences/paths/lifetimes exact; backup is identified by compiled CronJob identity, never a label exemption; no additional streams/consumers |
| Routes, storage and isolation | Known instance resources and main/backup relationships stay in the compiled namespace; omitted known namespaces use the Helm target without rewriting objects. Compiler-owned Flux automation may live in flux-system, and unknown kind scopes are not guessed. Chart output cannot name a foreign namespace. Services have no external IPs and their nonempty selector must select the intended main pods; profile-domain sign-in and TLS, compiled host/backend/class/issuer; profile storage and compiled claim size/mount; RWO replica/rollout rules; app labels match compiled selectors and chart-added policies cannot widen access or select all pods |
| Quota | Exactly one profile-sized quota; actual workload totals include rolling surge, regular sums, init maxima/restartable sidecars and the compiled backup; CronJobs must forbid overlap and unbounded DaemonSets fail. The retired v1 blanket solver/restore reserve is not a v2 profile field |
| Unsupported legacy settings | Public/host-override exposure, per-instance exceptions, provenance/platform-image policy and alerts are not ported; v2 refuses unsupported records rather than modelling legacy settings |

`check-apps` is a zero-argument whole-tree gate, [`v2/check-apps.sh`](../v2/check-apps.sh),
with no Python execution path. Explicit paths or flags exit 2 before file reads or subprocesses. It
acquires the chart through [`v2/chart.sh`](../v2/chart.sh), then runs `check-rendered.sh --chart DIR`.
Render-equality failure stops before policy; the gate preserves Go's report, diagnostics and nonzero
status. Validate and Deploy-v2 use the same acquisition script to populate the cache before Go tests.
Python's [`charts.py`](../tools/swhurl/charts.py) remains only for the ClickStack render test in
`tests/test_manifest_policy.py`; `check-otel` renders with `helm template --repo` ([`platform/otel/check.sh`](../platform/otel/check.sh)).

#### Chart acquisition (approved 5 November 2026)

`swhurl-v2 chart --profile PROFILE.json --repositories HELMREPOSITORIES.yaml --cache DIR` resolves
one profile pin against exactly one named HelmRepository and prints only its absolute local directory.
The command reads bounded explicit files; orchestration resolves the repository through the Helm
adapter's existing YAML boundary, then calls the separate `Acquirer`. Neither the renderer nor policy
acquires implicitly. The profile owns name/version/repository name; the registry owns its URL; the shell
owns the cache location (`${XDG_CACHE_HOME:-$HOME/.cache}/swhurl-platform/charts`). No new parser dependency.

A cached chart whose `Chart.yaml` matches is reused without Helm. Otherwise `helm pull NAME --repo URL
--version VERSION --untar --untardir TEMP --repository-cache TEMP/repository --repository-config
TEMP/repositories.yaml` extracts inside a private cache temporary directory, validates metadata and
publishes by rename. Helm's repository-index cache is private to that pull; the operator's Helm
configuration and repository cache are not used. A concurrent winner is accepted only if its chart
metadata matches; a failed pull or mismatched result publishes nothing and cleans its temporary
directory. Existing mismatched cache entries are refused, not silently overwritten. Names/versions
must be safe path segments and repository URLs HTTP(S) without userinfo. Resolution matches the
repository name, as the retired helper did; duplicate names refuse even across namespaces. Helm
output is discarded and bounded (16 MiB stdout, 64 KiB stderr), with a two-minute timeout and one-second
inherited-pipe wait. Fixed reasons distinguish timeout, missing Helm, failed pull, mismatched metadata
and cache-publication failure, without tool output. Chart/repository resolution and acquisition failures
exit 1; invalid CLI usage/profile exits 2. Only the cache is written; no credentials, cluster operation
or Helm install/upgrade.

This slice deletes the 45-line `apps/validation.py`, its command run function and both inline CI fetch
recipes. It deliberately retains Python chart acquisition for its remaining callers rather than porting
OTel/ClickStack or template checks. Contract suites cover cold/cache/concurrent pulls and failures; shell
tests prove argument refusal, acquisition → equality → policy ordering and Python-free execution.

### Legacy policy retirement (approved 4 November 2026)

The operator approved retiring the legacy Python fixture checker and `check-apps <legacy-path>` API,
keeping v1's mutable fixtures and model-oracle tests plus the existing Go policy tests. The checker and its
Python-only characterization tests are deleted; no fixture files or model evaluator tests are removed.
There is no JSON bridge, v1 compatibility mode, rule filter or second compiler. All live instances are
already v2-owned, so the retained whole-tree Go gate covers every instance, including production.

Legacy quota reserve, public/host-override exposure, alert settings, reasoned exceptions, additional storage
classes and provenance/platform-image policy (including fixture exemptions) are deliberately retired as
checker contracts, not silently ported. The earlier probe established that the legacy fixtures did not have
behavior parity with unchanged Go policy; its observations remain in the
[historical record](current-state.md#app-policy-go-gate-4-november-2026). The Go contracts above stay unchanged.
The [retirement record](current-state.md#legacy-app-policy-retirement-4-november-2026) distinguishes offline
validation from deployment; remaining Python entrypoint/chart glue is not policy ownership.

### The workstream check in Go (approved 5 November 2026)

`make check-docs` is `swhurl-v2 workstreams --repo-root .`; the Python checker and its tests are deleted. The rules
are the [registry's](workstreams.md#what-make-check-docs-enforces), unchanged but for one deliberate tightening: a
change is anything `git status` reports, so a new untracked file under a workstream's paths also needs its page.

- **Facts and owners:** Git owns what the checkout holds (`adapters/git`: tracked files, present files, uncommitted
  paths, whether the clone is shallow, every commit a ref reaches); the Markdown files own the record. Existence means
  present as Git sees it: tracked and not deleted, or untracked and not ignored, so an ignored file is absent, as on a
  fresh clone.
- **Pure core:** `workstreams.Check` takes those facts and the text of the Markdown files it uses and returns one
  result per check: an okay line, its problems, or a warning when it could not run (a shallow clone skips the
  citation check). `workstreams.Needs` names those files round by round: the registry, its pages, then the Markdown
  files status-page evidence names a heading of. An absent file is never needed; it is a finding.
- **Orchestration:** `orchestration.CheckWorkstreams` reads the checkout, then what `Needs` names until it names
  nothing, then calls the check. A Markdown file nothing uses is never opened, so a stray symlink cannot stop it. The
  command opens files through `os.Root`, so no path or symlink leaves the checkout, and bounds each to 16 MiB.
- **The Git adapter** runs `rev-parse --show-prefix --is-shallow-repository`, `ls-files -z`,
  `status --porcelain=v1 -z --untracked-files=all` and `rev-list --all`, each with `--no-optional-locks` and
  `core.fsmonitor=false`, so not even the index is refreshed, and with no `GIT_*` variable from the environment, so
  only the root names the repository. Each command has a one-minute limit, 64 MiB of output and 200 bytes of
  stderr, kept as the refusal's detail; the commands list names, never contents. Renames and copies are read in
  either status column, and an unmerged file stays present. A root that is not the top of a working tree is
  refused (`not_a_repository`, `not_the_top_level`). Its contract runs on a temporary repository built from
  `testdata/checkout` and on the fake; a test proves a read leaves every file under the checkout, `.git` included,
  byte-identical.
- **Exit status:** 0 when nothing is found; 1 for a finding, a failing Git command or an unreadable file; 2 for
  usage or a root Git refuses. CI runs it in Validate's tooling job on the full-history checkout, before any step
  that writes into it.

### Kubernetes: the API server, not `kubectl`

**Decided:** v2 reads and changes Kubernetes and Flux objects through the API server. It does not start `kubectl` or `flux` processes. v1 measured the reason: one API request from the console takes 4 to 16 ms where starting `kubectl` takes 50 to 120 ms, and each `kubectl` process uses about 56 MB, which is why v1 runs at most three reads at once ([v1 tooling](contributing.md#operator-tooling)). v1 only translates the console's reads; v2 has no `kubectl` path at all.

- Credentials: the pod's service account inside the cluster, a kubeconfig outside it (`$HOME/.kube/config`, as for v1's scripts). The CLI and a server get separate credentials (section 1).
- Flux actions are object changes, as the [v1 console already does them](console.md): reconcile sets `reconcile.fluxcd.io/requestedAt` on the source and the unit; suspend and resume set `spec.suspend`.
- Each kind's API group and version is stated, not discovered, and checked against the manifests in Git (v1's `kube.RESOURCES` does this).
- A failed request (forbidden, unavailable, timeout) is unavailable evidence; only a successful `404` means absent.
- Raw fixtures are API JSON. That is the shape `kubectl get -o json` prints, so recorded v1 outputs can be reused.

**Decided 25 October 2026: a thin `net/http` client, not `client-go`.** It is the reader [`adapters/kube`](../v2/adapters/kube/kube.go), uses only the standard library (`stdlib-only` outside the approved Helm, app-source and manifest parser boundaries), and takes the k3s material it needs as arguments: a CA bundle, a client certificate or key, and a bearer token. `client-go` would be complete but adds a large dependency tree and binary size for four kinds and two verbs. Resolving a kubeconfig or an in-cluster service-account token is **not** in that adapter: the YAML parser approval is confined to Helm, app-source and checkout-manifest reading, not kubeconfig reading; the `observe` command resolves it and hands the adapter its ingredients.

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

- `tests/fixtures/instance`: a frozen staging/production example with records and rendered resources (deleted with the v1 writer).
- [`tests/fakecluster.py`](../tests/fakecluster.py): the report `swhurl-v2 verify`'s cluster scope gives a healthy platform, as the console's tests read it.
- `tests/test_console_stages.py` (deleted with the console port): earlier targets, current intent and live observations combined; `v2/pages/changes.go` and its tests carry the intent now.
- [`v2/kernel/edit_test.go`](../v2/kernel/edit_test.go): deterministic rendering and relationship rules in the v2 model.

Copy scenarios and data, not the `FakeRunner` command matching: v2 tests name operations, not command strings. Some v1 fixtures read the current checkout or clock; freeze those values when exporting.

### Test tiers

| When | What runs | External processes |
| --- | --- | --- |
| Every edit | Pure model tests | None |
| Each completed change | Domain scenarios through fake adapters, including failures and restarts | None |
| Adapter changes | Contract tests on raw fixtures; cheap real components such as a temporary Git repository | Local only |
| Before release | A small live suite: publication, reconciliation, permissions, recovery | Live, with the usual confirmations |


### Code-derived contract views

The [visual design/change tool](visual-change-tool.md) is an **independent workstream**, using v1 code and Git history as its proving ground, with component/interface graphs, commit comparisons and user-journey overlays. It is not a v2 supporting milestone, compiler gate or part of the Go kernel. Its brief and handoff live only in that document.

The tool remains optional and independent: nothing in v2 waits on it, and no viewer implementation or placement under `v2/` is approved.

## 5. Incremental delivery

Phases 1–7 were delivered between 24 October and 1 November 2026: release selection, the compiler, Git
delivery, observation, the shadow bridge, signed-in routes, calls, storage, backup and restore, the cutover
to v2 ownership, `create`, and the declaration label version 2. The delivery record is in Git
(`git log -p -- docs/iteration-2-plan.md`); dated evidence is in [current state](current-state.md). What
remains is tracked in [v2 status](v2-status.md#remaining-work).

## 6. Deliberately deferred

The one list of features v2 does not model, and what reopens each, lives in
[v2 status](v2-status.md#deferred-features). It is not duplicated here.

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
| `stdlib-only` | v2 imports only the standard library and its own packages except `go.yaml.in/yaml/v3` **v3.0.6**, approved 4 November 2026, imported only by `adapters/helm`, `adapters/appsource`, `adapters/manifests` and `adapters/copier` (including their tests; `adapters/appsource` approved 5 November 2026, `adapters/manifests`, which decodes the checkout's Kubernetes manifests for `check-repo`, [8 November 2026](#17-check-repo-in-go-the-decisions-not-the-subprocesses-approved-8-november-2026), and `adapters/copier`, which reads a stack template's `copier.yml`, [9 November 2026](#23-the-console-server-in-go-the-pages-the-jobs-and-the-consoles-last-python-proposed-9-november-2026)). The check refuses other imports, modules, versions and module replacement directives. Pure packages remain standard-library-only; this does not approve YAML kubeconfigs. |
| `layers` | Every v2 package has a declared layer (0 pure model, 1 adapters, 2 use cases, 3 commands) and imports only v2 packages in lower layers, except that a fake may import the adapter it fakes ([why](#4-development-layers-and-adapters)). A new package is given its layer in `Layers`. |
| `pure` | Pure packages import only the allowlisted standard packages, print nothing and assign no package variable ([why](#the-pure-model)). Time, identifiers and snapshots are arguments: `time` is allowed for parsing and comparing a *supplied* timestamp, and its clock functions (`time.Now`, `time.Since`, `time.Until`, timers and ticks) are refused by call. |
| `no-subprocess` | v2 code (not its tests) starts processes only in the SOPS adapter `adapters/sops` (encryption, and local decryption with a key file the operator's command names, [§27](#27-check-secrets-in-go-approved-13-november-2026)), the approved chart-rendering adapter `adapters/helm`, the read-only checkout adapter `adapters/git` and the template-rendering adapter `adapters/copier`, names no `kubectl` or `flux` command anywhere, and imports `syscall` only in `cmd/swhurl-v2`, where it names the SIGTERM a pod is replaced with ([why](#kubernetes-the-api-server-not-kubectl)). SOPS avoids a second implementation of age and its envelope; Helm avoids a second chart engine; Git avoids a second reader of its index and object store; Copier avoids a second template renderer. The Git exception, approved 5 November 2026, is [four fixed reads of one checkout](#the-workstream-check-in-go-approved-5-november-2026), with no write, fetch or general command runner. The Helm exception, approved 4 November 2026, is `helm template` for app-policy checks and, approved 5 November 2026, explicit profile-pinned `helm pull` for the check cache ([contract](#chart-acquisition-approved-5-november-2026)): no cluster reads, install, upgrade, implicit dependency fetching or arbitrary command runner. The Copier exception, approved 9 November 2026, is [one pinned render of a public stack template](#23-the-console-server-in-go-the-pages-the-jobs-and-the-consoles-last-python-proposed-9-november-2026), with no credential in its environment or arguments. The checked allowlist admits exactly the four adapter directories, not their fakes or subpackages; boundary tests exercise the fixed Helm and Copier invocations. The signal exception starts nothing: a pod's replacement sends SIGTERM, and the console takes no new job and waits up to 840 seconds for the ones it runs, inside the pod's 900. |
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

**CI (dependency bootstrap updated 4 November 2026):** Validate's tooling job sets up Go with `actions/setup-go@v7` from `v2/go.mod` (module cache keyed by `v2/go.sum`), explicitly runs `make v2-deps` to download the pinned parser with checksum verification and runs `make check-v2` after the code graph tests, with `REQUIRE_HELM=1` and the chart the v2 profile pins fetched into the chart cache first, so the [compiler's](#2-phase-1-model-and-contract) manifest check runs; a failure there holds the console deploy like any other check. First run on the push of `be041cf4` ([current state](current-state.md#v2-and-code-graph-push-24-october-2026)): `go1.26.0 linux/amd64`, all four packages passed; Go setup took 7 s and the check 11 s. `actionlint` was not run (not installed here).

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

- **What it compares:** `v2/` in the working tree (as Git carries it: tracked files, plus untracked ones that are not ignored, so a build artifact left in `v2/` is not a change), or at `AFTER=<rev>`, against `v2/` at the merge base of `BASE` (default `origin/main`) and that revision. So it covers unpushed commits and uncommitted edits, and later commits on `main` do not show as reversals. A revision without `v2/` is absent (`present: false`) and everything is added.
- **Facts,** each read the same way from each tree's own source and baselines, never by building or running it: files added, changed or removed (by SHA-256, with their kind: module, baseline, rules, test, testdata, source or other); packages added or removed, with their declared layer and whether they are pure or tests only; imports per package (standard, local or external, test-only ones marked); exported API lines, computed from the code, and whether `api.txt` changed; function sizes and `budgets.json` entries and limits; and the rule settings (`Layers`, `Pure`, `PureImports`, rule IDs), read as literals from `archtest`.
- **Review** lists what the change must explain: v2 or the design rules being new, a changed `go.mod`, a new package and its layer, an external import, any changed rule setting, changed budget limits, a `budgets.json` entry added or raised, and a changed exported API. Lowering an entry is the ratchet and is not listed.
- **Output:** the text above, at most five items per line; `FORMAT=json` prints the document (`"kind": "v2-change-report", "version": 1`), with sorted lists that are never `null`, so identical trees give identical bytes. A change to its shape raises `version`.
- **Errors:** it reports whenever it can read both trees, whatever it finds. A missing merge base, Go that does not parse, rule settings that are not plain literals, an invalid `budgets.json` or files without `go.mod` stop it (the script exits 1), as does a wrong `FORMAT` or `AFTER` (2); through `make` either fails the target.
- **Boundaries:** [`v2/archtest/report.sh`](../v2/archtest/report.sh) resolves the revisions and extracts them with `git archive` into a temporary directory (never through the index or working tree), asks `git ls-files` for the working tree's paths, and runs `archtest`'s `TestChangeReport`, which compares the two trees offline; a listed path the working tree no longer has is a removal, and the two trees count the same files as packages (none under `testdata`, a dot directory or an underscore one). Without `-base` that test skips, so `go test ./...` is unchanged. It is a test so that it reuses `archtest`'s loader and measures without moving them into a package the rules govern.

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

An external-effect adapter needs separate approval; a read-only integration is checked by `adapter-complete` from the commit that adds it.

### Offline CLI and build

Prerequisites: Go **1.26.0** and the pinned parser downloaded with **`make v2-deps`** from the repository root (network access to the Go proxy and checksum database). `make check-v2` runs the format, vet and test steps below, [guardrails](#guardrails) included. The following disables automatic dependency access and C dependencies. `go.mod` and `go.sum` pin the one approved parser. Bootstrap downloads it explicitly with `GOTOOLCHAIN=go1.26.0`, `GOPROXY=https://proxy.golang.org` and `GOSUMDB=sum.golang.org`; missing cached dependencies fail offline builds/checks rather than enabling a network fallback. Every v2 CI job and the console image build bootstrap before an offline build; the image caches the module download separately from source. CI runs them through `make check-v2`; `make check` does **not** include them.

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

`swhurl-v2 --help` lists the commands: identity and selection (`release-id`, `instance-revision`, `select-release`), the compiler and renderers, the use cases (`create`, `deploy`, `edit`, `promote`, `remove`, `restore`), observation and `declaration`. Only explicitly named files are read; a command writes only where its own contract says. Valid results go to stdout, diagnostics to stderr. Exit `0` means success/no-op, `2` means usage/validation/refusal (including an input file larger than 1 MiB, which is not read past that), and `1` means file/output I/O failure. A flag given twice is a usage error, not the last one winning; flags may also be written `-instance=FILE`, and `--` ends them; `--help` or `-h` prints the usage to stdout and exits `0`. Refusal stdout is empty. If stdout is a pipe whose reader has gone, the process ends by `SIGPIPE`, as Unix tools do, rather than exiting `1` (catching it would need `syscall`, which the `no-subprocess` rule keeps out of v2). Output failures cannot retract bytes already accepted by the destination; they must return nonzero rather than claim success.

To repeat selection, calculate `instance-revision "$selected"` and select R42 against that revision: output is identical. Selecting it against the **old** revision exits `2` with `stale_instance` and no stdout. Wrong-app and unsupported-candidate cases are exercised by `go test` using temporary synthetic inputs, with all files compared afterwards.

Also run `make check` from the repository root and `for f in host/*.sh tests/fixtures/*.sh; do bash -n "$f"; done`. Run `make check-templates` when a template pin, an app type or the producer changes. Do not run credential or live checks for an offline change.

## 8. The cutover: one writer, v1's writer deleted (approved 31 October 2026)

**The contract.** One writer per fact; after the cutover v1 renders nothing and every v1 renderer was
deleted rather than adapted. There is no mixed-ownership rule and no compatibility layer: a fact v2 models is
written only by v2, and a fact it does not model is refused by name until a slice models it. Records are v2
JSON only. Unused features stay on the backlog as future work, not struck from the roadmap. Promotion is
one-way: a rollback is a promotion of an older release, and creating production will be a compile of
staging's release into a fresh production record. `edit` is the one settings primitive: a named change to
one instance's record, compiled and written once.

**The write primitives.**

| Primitive | Contract |
| --- | --- |
| `edit` | One named setting (`--set name=value`) with its whole new value; a setting v2 does not model (`egress`, `alerts`) is refused by name; two changes in one call are a usage error. |
| `remove` | Deletes an instance's records, compiled files, unit and auth object, and edits both registrations. It refuses one the committed resolution still connects as a caller or a subscriber. It reports the platform documents to regenerate, and `make check-v2` fails until they are run. |
| a writer that prunes | A write of a compiled folder removes only a file the renderer's own vocabulary names. A declared input (a seed, the offered documents) is never touched. `compile --out` into a fresh directory is the truth-teller. |

**Acceptance:** `make check-v2` and `make check-apps` agree; every instance is v2-owned; `verify-platform`
passes with no v1 branch; and `swhurl-v2 resolve` reads one record shape. The ownership milestone records the
result; the remaining Python is [v2 status](v2-status.md#remaining-python).

## 9. The create path (implemented 1 November 2026)

`swhurl-v2 create` writes a new app's staging records from the image its repository publishes. It reads no
app repository: the image resolves through the registry adapter a delivery already uses, and its other
inputs are the platform profile, the declaration label and the documents it carries.

- **Command:** `swhurl-v2 create --records DIR --app APPLICATION --team TEAM --image REPO:<run>-<commit> --profile PROFILE.json --out DIR`.
- **Writes:** `--records/<app>/app.json`, `<app>/staging/{release,instance}.json`, `staging/events.sops.yaml` (the minted seed, encrypted, written before the record that names its key) and `staging/offers/` (the documents the label carries). Nothing else.
- **Refuses:** an app that already has records; an image with no version 2 declaration label, or one whose runtime, memory floor or secret names the instance cannot take; a revision the tag does not abbreviate; a reference outside the tag contract; a database without a volume, or a volume for an image with no database; a memory below the image's floor; an exposure, kind or team the model does not allow.
- **Records only:** the allocation, compiled files and unit are the whole tree's, so `make v2-render` writes them next. The console's New app runs `create`, an `edit` per further choice and `render` in one held pull request.
- **Not modelled:** a first production (`promote` refuses an environment with no record) and a repository read.

## 10. The declaration travels with the image (approved 1 November 2026)

**The contract.** The image's config label `platform.swhurl.com/declaration` is the whole machine-readable
declaration: one strict JSON document at `schemaVersion` 2. Version 1 is refused. `create`, `deploy` and
`check-templates` read that one label, so no repository is read, and the document bytes are pinned by the
digest that names the release. A digest and an app name determine one release.

| Field | Rule |
| --- | --- |
| `schemaVersion` | Integer `2`. Version 1 is refused: an image either states its whole declaration or is not this platform's |
| `runtime` | `kind` `web` or `worker` (`job` unsupported), non-root `uid`, `port` and `healthPath` for a `web` image only |
| `telemetry` | Optional boolean: the image reports to the platform collector |
| `database` | Optional `sqlite` |
| `publishes` | Optional event names, each at most once: what the `events` document sends. The producer computes it and the platform check ties the two, because the deployment decoder reads no YAML |
| `secrets` | Optional names, refused when non-empty: v2 models no secret |
| `memoryMiB` | Required, integer at least 1: the memory the image runs in |
| `databaseSizeMiB` | Required when `database` is set, and refused when it is not: the volume a new database is created with |
| `description` | Optional: one line of 1–200 printable characters, served to the app as `SWHURL_DESCRIPTION` |
| `apiPath` | Optional absolute path where a `web` image serves its API description. Required with the `api` document, and refused without it |
| `documents` | Optional object of at most `api`, `events` and `flags`, each the file's bytes base64-encoded, so the record's copy is byte-identical. `events` is required when `publishes` is non-empty |

**Size.** The producer refuses a declaration whose UTF-8 encoded JSON exceeds 120 KiB, naming the documents and
their sizes, because the value reaches buildx as one argument (the kernel's `MAX_ARG_STRLEN` is 131,071
bytes; measured, not assumed). The adapter's 1 MiB body cap is the hard ceiling and is not approached.

**What reads it.** `create` takes the description, API path, volume size and documents from the label;
`deploy` replaces them on a new image, so an instance that follows its image changes what it offers in the
same commit; `check-templates` runs the producer and hands the label to `swhurl-v2 declaration` with no
image, registry or write; the console reads a role by its fixed name and the commit from the release it is
already reading.

**The records.** `release.documents` states the roles the release carries; its source commit is the image's,
because the documents travel with the image built from that commit. The release identity hashes the sorted
roles, so every release ID and compiled file identity changed once when the shape landed. An `apiPath` needs
an `api` document and a `publishes` list needs an `events` one, both refused otherwise. An instance's
`platform.swhurl.com/offers` annotation renders the source commit's first seven characters and exists only
when the release states a document; the `api-description` annotation follows `apiPath`.

**What `deploy` keeps and takes.** Takes from the image: runtime, telemetry, database, publications, the
memory the instance runs in (a rise above it is a refusal), the description, the API path and the documents.
Keeps from the record: replicas, exposure, `calls`, `allows`, subscriptions, and the broker key and its
encrypted seed — the instance's grants and choices, which no image states.

**Not modelled:** a repository read, OCI referrer artifacts, a private app repository, a first production,
secrets, alerts, a `job` kind and instance-declared egress grants.

### Producer-first Python retirement (approved 5 November 2026)

`swhurl-v2 image-declaration --app-dir DIR` (DIR defaults to the working
directory) reads only the app's `swhurl.yaml` and explicitly named documents and prints one compact,
deterministic `platform.swhurl.com/declaration=<JSON>` line. The source adapter owns YAML and explicit
checkout reads; the pure producer owns source rules, defaults, publications and encoding; orchestration
wires them and the CLI owns usage, standard I/O and exit status. No registry, image build, GitHub,
credentials, subprocess or cluster operation belongs to this command.

The implementation preserves the source contract, including truthful `job` and `secrets` output even
though deployment refuses them, channel/publication order, raw API/events bytes and **exact** Python `json.dumps(indent=2)`
flags bytes plus newline (insertion order, ASCII escaping and number formatting affect release identity).
The 120 KiB encoded JSON limit counts **UTF-8 bytes**, correcting Python's character count. Malformed
YAML, unknown fields/types, missing named files and invalid declared file paths are named refusals with no
stdout. The existing YAML v3.0.6 exception extends to exactly `adapters/appsource`, not the pure layer,
kubeconfigs or other adapters; no dependency or general rule relaxation is approved. `encoding/base64`
and `math/big` are pure stdlib primitives for document encoding and exact integer/flag-bound arithmetic; no complexity budget grows.

The ordered source mapping is YAML-independent and retains flag/channel insertion order. Source defaults
remain uid 65532, web port 8080, memory 128 MiB and SQLite volume 1 GiB (quantities round up to whole MiB).
`autoDeploy` is checked as a boolean and ignored; grants are refused. Source validation does not acquire
creation's non-root UID, probe-path or memory constraints. API bytes are opaque. AsyncAPI 3 send operations
reference existing channels, receive operations are validated without publishing, and the source's one-hop
local references and channel ordering remain unchanged. Flags retain YAML 1.1 scalar types, arbitrary-size
integers and Python float spelling, ASCII/surrogate escaping, indentation and final newline.

The adapter reads only `swhurl.yaml` and the paths the source explicitly names, with the existing declared
path grammar. It follows named symlinks, including external targets, like the original producer; it adds
no root-confinement or blanket regular-file restriction. YAML retains the original source semantics:
scalar/mapping aliases, mapping merges (including sequences), and duplicate keys are accepted. Merges precede explicit fields; the first mapping in a merge sequence wins,
and explicit or later duplicate assignments keep the key's first insertion position while replacing its
value. This order is part of flags identity. Standard safe timestamp/binary scalars remain typed in unused
AsyncAPI content. Mixed integer/float bounds compare the exact integer with the actual binary float, not
its display spelling; database-size conversion retains Python integer-prefix syntax independently of
resources' stricter quantity grammar. Ordered mappings retain typed scalar keys: event maps may use
non-string keys, numeric and string keys remain distinct, and bool/int/float equality follows Python
assignment while retaining the first key. Source/flag field names are checked by their own domain rules.
Event send membership uses channel positions, not stringified keys; colliding reference strings resolve
to the last channel, like the original reference dictionary. Cyclic aliases, unhashable keys, unsupported tags,
invalid YAML and multiple documents are named refusals, never partial output. API/events raw bytes are
not reserialized. Local files have no added per-file size cap: a large comment-only source can
still produce a fitting label; oversized declarations name every document role and size. The producer has
exit 0 on success, 2 on usage/input refusal and the CLI's exit 1 on output failure. Python's checker remains
the coordinator, calling the already named Go binary twice:
producer first, then unchanged declaration acceptance. Existing pinned external workflows continue to use
the helper in their old platform revision; rollout/pin upgrades are explicitly outside this slice.

Acceptance gates: freeze current producer cases as Go fixtures/goldens before replacement; add pure,
real/fake adapter-contract, orchestration and CLI regressions, including byte cap, source defaults,
YAML semantics, named-file reads, send references and flags identity. Switch only the producer in Python's
existing template checker to its already named Go binary, then keep Go declaration acceptance. Delete
the Python producer/internal command and producer-only tests/dead helpers, retaining shared modules.
Run focused tests, `make check-v2`, `make check`, `make check-templates`, host/fixture shell syntax and
diff whitespace checks. Delivery requires independent parent review, commit/push and live platform
verification; its evidence is in [current state](current-state.md#the-image-declaration-producer-leaves-python-offline-5-november-2026).
No template pin/workflow rollout, sibling repository or app-record change is part of this implementation.

### Go producer adoption by template workflows (approved 5 November 2026)

Both stack repositories' shared `app.yml` workflows build `swhurl-v2` from an immutable platform
revision containing the Go producer, with the Go version and YAML dependency pinned by `v2/go.mod`
and `v2/go.sum`. Only `v2/` is checked out; source and binary stay in the runner's temporary directory,
outside every app build context. A failed build or producer invocation stops before image publication;
there is no Python fallback. The app checkout remains the source of truth for the declaration.

Copier's template-only rendering still uses uv/Python; image assertions and SQLite smoke checks may
still use the runner's Python standard library. This slice does not claim Python-free template CI.
Assertions cover declaration version 2, default/minimum memory and the embedded API/events/flags.
Validate both complete template matrices (four TypeScript, three Kotlin) before immutable releases;
then advance the generated callers and platform catalogue to those tested releases and run
`make check-templates`, `make check`, reconcile and live platform verification. Existing app repositories'
workflow pins, app records, credentials, data and backup destinations are outside this approval.

### The template check leaves Python (approved 5 November 2026)

`make check-templates` is [`v2/check-templates.sh`](../v2/check-templates.sh), shell sequencing like `check-apps`, and
the Python coordinator and its tests are deleted. The one decision it made itself, the names scan, is Go:
`swhurl-v2 names --catalogue NAMES.json --app-dir DIR` prints each `SWHURL_*` or `platform.swhurl.com/*` name a file
under DIR uses that the catalogue does not list, as `NAME (PATH)` sorted by name with the first file in lexical order,
and exits 1 when it printed one; a malformed catalogue or usage is 2, an unreadable tree or a symlink leaving DIR is I/O
(1). The decision (`kernel.NameCatalogue`) is pure; the command reads the tree inside an `os.Root`. A family entry
(`SWHURL_SERVICE_<APP>_URL`) lists its upper-case members and its prefix alone. No adapter or subprocess is added.

Both catalogues are v2's files with one writer each: `v2/contract/names.json` (`platform-names` version 1: each
name's surface, meaning and availability) and `v2/contract/stacks.json` (`platform-stacks` version 1: the Copier
version, each stack's repository, pinned revision and description, and each app type's title, text, stacks, answers
and starting exposure). Until their consumers are ported, Python reads them: `interface.entries()` on first use (the
module's import still reads nothing), and `contract.py` builds `STACKS`, `STACK_REVISIONS`, `STACK_DESCRIPTIONS`,
`APP_TYPES` and `COPIER` from the file with unchanged values. The console image copies `v2/contract/`, its only
exception to copying no v2 sources.

The script clones each stack at its revision, renders each type and the answers no type has (a worker with a
database) with Copier in parallel, then runs `names`, `image-declaration` and `declaration` per rendered type,
printing equivalent `[OK]`/`[WARN]`/`[BAD]` lines: a name is reported against `v2/contract/names.json`, and an
outside render that writes no `swhurl.yaml` counts as accepted. The Python pre-check of answers against `copier.yml`
has two replacements: Copier's own refusal (its validator, or a choice the question no longer offers) is the template
refusing it, and an answer the rendered app's `.copier-answers.yml` does not record (Copier ignores one to a question
the template no longer asks) fails the type. A check that stops without a result line fails, and a catalogue with no
stack, or a stack with no type, is a failure rather than nothing checked. It takes no arguments, `SWHURL_V2` or a build names the binary (no `PATH` lookup), and Copier
runs through `uvx` unless `copier` is on `PATH`: Copier is the only Python left on this path. Acceptance: Go tests for
the decision and the command, the shell tested with fake `git`, `copier`, `go` and `swhurl-v2`, and the same verdicts
as the Python coordinator against the pinned templates.

## 11. The console port: strangled from inside (approved 3 November 2026)

**Decided 3 November 2026: option 3, status first.** The brief below is kept as the reasons and what would
reopen it. Each slice is still its own approved design.

**Decision.** How the console (`tools/swhurl/console`) leaves Python. It is the largest Python surface, and the
step after the [ownership milestone](current-state.md#the-ownership-milestone-every-instance-is-v2s-3-november-2026).

**Assumptions, measured 3 November 2026 by parsing `tools/swhurl`'s imports:**

- Python is 119 modules, 19,792 lines. The console is 25 of them (4,993 lines) and 18 Jinja templates (1,360
  lines) with HTMX, `console.css` and `console.js`. Through its imports it reaches **91 modules, 13,923 lines
  (70%)**, among them `verify` (its platform page runs `verify_platform`), `apps/checks`, `recovery`,
  `backupset`, `dashboards`, the notification checker's modules and the v1 model (`merge.py`, `proposal.py`,
  `records.py`). What it does not reach is 28 modules, 5,869 lines: `apps/policy`, `apps/teams`, the docs,
  secrets and template checks, `backup_job`, `sqlite_restore`, the notify service, `commands` and the live tests.
- The console already wrote only through the binary: `console/v2.py` (deleted with the port) ran
  `swhurl-v2 edit`, `remove`, `promote` and `render`, and the image carries `swhurl-v2`. Its reads are 16
  `kubectl` calls, and its pull requests and merge gate are 10 `gh` and 8 `git` calls.
- v2 has no Git or GitHub adapter. [`adapters/kube`](../v2/adapters/kube/kube.go) already accepts a bearer token,
  and status now resolves either the host's JSON kubeconfig or the pod's mounted token through the
  [approved credential slice](#slice-the-in-cluster-credential-approved-3-november-2026).
- "Keep the same HTMX templates" holds for the markup, CSS, JS and HTMX attributes, not the templates: Jinja does
  not run in Go's `html/template`, so all 18 are rewritten.

**Options.**

| | 1. Port it in one slice | 2. Console-free first, console last | 3. Strangle it from inside |
| --- | --- | --- | --- |
| What | A Go `net/http` server, templates rewritten, reads and the merge gate through new adapters, then cut over | Port the 5.9k lines the console does not reach; the console stays as it is until a later port (1 or 3) | Keep Starlette and the templates. Each slice moves one thing the console imports into a `swhurl-v2` subcommand it calls through `console/v2.py`, as writes already are, and deletes that Python. The last slice ports a thin server |
| Python deleted per slice | ~14k lines once, at the end | 0.2–0.6k each; the 70% stays | 0.3–1.5k each, from the 70% |
| Blast radius | The operator's whole UI switches at once | None on the console | One page or action at a time; a revert is one commit |
| New adapters | Git, GitHub, in-cluster credential, before anything ships | None | One per slice, when that slice needs it |
| Consistency | New | Current page order | The pattern the console's writes already use |
| Testing | Go tests, plus a side-by-side live comparison of every page | Existing | `FakeRunner` on the console side, Go tests on the binary's, the existing console deploy |

Rejected: keeping the console in Python, which contradicts the [goal](v2-status.md) (one Go implementation).

**Recommendation: option 3, starting with status.** `apps/ops.gather_status` is one function with three readers
(the console, `make app-status` and the promotion gate; the notification checker reuses only its image helpers). It reads eight kinds
(namespace, Flux unit, source, HelmRelease, pods, workloads, Ingress, Certificate); `observe` reads four with no
pod-level facts, so slice one widens observation (a deferred feature) and adds `swhurl-v2 status` on the host
(kubeconfig, no new adapter) in place of `make app-status`. Slice two adds the in-cluster credential and points the console at it. Later slices take the
checks; the server and templates go last. The merge gate leaves first, by being retired rather than ported
([§20](#20-the-merge-gate-is-retired-approved-8-november-2026)). It
costs a JSON contract per read that the final Go server calls as a function instead, and one process start per
read, where the console already starts `kubectl`. Option 2's modules stay worth doing in between, as fillers.

**What would change it.**

- A redesigned console is wanted anyway: then option 1, because the templates are rewritten either way.
- If the in-cluster credential cannot be used safely, take the file-only reads (the catalogue, records and
  the merge gate's rules) first instead.
- Page latency over the binary proves noticeable when measured live: port the server earlier.

### Slice one: `swhurl-v2 status` (approved 3 November 2026)

**Meaning.** Status compares the cluster with what v2's records say should run, not with what the cluster says it
wants: the expected digest is the record's release, and the expected revision is the commit the records were read
at (`make app-status` passes `HEAD` and prints it). `make app-status` leaves Python for it; `gather_status` stays
for the console and the promotion gate until slice two.

**State.** A pure function of the observation, the vocabulary the console shows, each with a reason; first match
wins:

| State | When |
| --- | --- |
| `suspended` | the unit or the release is suspended |
| `failing` | a container is not ready with a waiting or terminated reason, the unit is absent, or Flux reports a failure |
| `updating` | the revision is not applied yet, the release is reconciling, the image differs from the record, or fewer replicas are ready than wanted |
| `stopped` | every wanted replica count is 0 |
| `unknown` | anything not read or not established, naming it; a failed read is never absence |
| `healthy` | otherwise |

**Contracts.**

- `observe` (pure) adds per-container problems (pod, container, reason, the message's first 160 characters),
  routes (Ingress hosts without cert-manager's solver ingresses) and each certificate's Ready; they leave
  `NotCovered`. `adapters/kube` gains one read-only kind, cert-manager's `Certificate`, with a recorded fixture.
- `orchestration.Status` carries records → target → reads → observation → state: the flow the `observe` command
  ran itself.
- `swhurl-v2 status --records DIR --app APP --env ENV` replaces `observe`'s entry point and keeps its live flags
  (`--profile`, `--calls`, `--egress`, `--revision`, `--kubeconfig`, `--archive`, `--max-age`, `--region`). Text
  by default; `--json` prints `v2-observation` version 4 with state, currentness and observed facts (the
  [bulk/currentness contract](#slice-two-the-console-reads-v2-status-approved-3-november-2026)). Exit 0 whenever a report
  is printed, whatever the state; 2 for usage, an instance with no record, or a refused request.
- `make app-status APP= ENV=` is bash: it builds the binary into a throwaway folder and writes the kubeconfig's
  JSON form there, removed on exit. Its `commands.py` row loses its `run=`.
- Dropped on purpose: settings, variables, the secret line, the team and exposure (the records hold them), job
  runs (v2 has no job kind) and the GitRepository's revision. Right after any commit to `main` every instance is
  `updating` until Flux applies it; the promotion gate's "a commit elsewhere" exception moves with slice two.

### Slice: the in-cluster credential (approved 3 November 2026)

**Purpose.** `swhurl-v2 status` runs in the console pod, which has no kubeconfig: it has its ServiceAccount's
mounted token and CA and `KUBERNETES_SERVICE_HOST`/`PORT`, as `kubectl` uses there.

**Contracts.**

- `swhurl-v2 status --in-cluster` reads the token, `ca.crt` and `namespace` under
  `/var/run/secrets/kubernetes.io/serviceaccount/` and the two variables. `--in-cluster` and `--kubeconfig` are
  mutually exclusive and exactly one is required: no detection, no fallback. Only `status` accepts it; commands
  that write keep `--kubeconfig`.
- A missing token, CA, namespace or variable is exit 2 naming it. The token is read once per process; the
  kubelet rotates the file and every call is a new process.
- `adapters/kube.ServiceAccount(token, ca, host, port)` validates and assembles a `Connection`, and reads no
  file: the command reads them, as it reads a kubeconfig for `Connect`.
- The report's source is `serviceaccount in <namespace>`; the token is never decoded.
- The console's read ClusterRole (`platform/console/rbac.yaml`) gains `batch/jobs` `get` and `list`, the one
  kind status reads that it could not: a database instance's newest backup Job.

**Proof.** Offline: each missing part, the exclusive flags, a run against the fake API server with a temporary
token and CA. Live, read-only: the records copied into the console pod's `/tmp` and `status --in-cluster` run
there for one web and one database instance, matching `make app-status`, the backup readable.

### Slice two: the console reads v2 status (approved 3 November 2026)

**Outcome.** The app page, Apps list and promotion readiness check use the same Go decision. The existing
Starlette server, HTMX markup and PR workflow stay; this is not a server port. The operator agreed the direction:
records at main, v2 deciding whether unapplied commits matter, and one bulk observation for the Apps list.
The operator approved this contract, including the submission-time readiness check, and chose to replace the
old Python path rule here rather than patch Python separately.

```text
GitHub main → one pinned commit → immutable input bundle ─┐
GitHub compare(applied, pinned) → complete changed paths ─┼→ swhurl-v2 status → JSON → console presentation
Kubernetes → applied revisions and live object evidence ─┘
```

**Authority and time.**

- Resolve main once per request. Its SHA identifies both the records and the expected revision shown on the
  page. Never combine a record from one SHA with a profile or calls document from another.
- Reuse the existing tarball-reading path for an immutable input bundle: app/instance/release JSON under
  `v2-records/`, the profile, calls and egress allocation, plus committed HelmReleases where the page displays
  rendered variables. Share immutable bytes by SHA using the console's bounded cache; extract into a
  request-owned temporary directory for the command. Do not cache live status between requests.
- An absent record is not a failed GitHub read. Failed input reads show unavailable and disable changes;
  they never fall back to Python's judge or silently use an older main. A record awaiting its first Flux unit
  still gets the existing pending page. Removed instances remain visible as removing until live cleanup ends;
  that inventory is not a reconstructed v2 record.

**Unapplied commits: a pure v2 decision, not a Python state override.**

- The console already reads the units for its inventory and controls. Use those applied SHAs to ask GitHub's
  compare API once per distinct `(applied, pinned)` pair, then supply the results to status. There is no GitHub
  adapter in Go in this slice: Python supplies facts, Go decides what they mean.
- Optional `--changes FILE` is a strict `{"kind":"v2-changes","version":1,"comparisons":[...]}` document.
  Each comparison has exactly `from` and `to` (full lowercase 40-character SHAs), `status` (`complete`,
  `incomplete` or `unavailable`) and `paths` (an array of repository-relative changed paths, including both
  names of renames and removed paths). All fields are required; unknown, duplicate, null or malformed fields
  and duplicate endpoint pairs are refused. Unavailable/incomplete comparisons may carry known partial paths
  but never grant exemptions. A truncated, failed or non-ancestor comparison is not a complete empty diff.
  The existing compare API's 300-file cutoff remains conservative.
- `--repo-root DIR` (default `.`; requires `--changes`) names the immutable bundle's repository root, so
  temporary extraction paths are never mistaken for changed paths. With comparisons, `--records`, `--profile`
  and any supplied `--calls`/`--egress` must be inside that root. File arguments remain relative to the process
  working directory, not to `--repo-root`. Without comparisons, existing single-status invocation is unchanged.
- v2 derives each instance's relevant paths from its records and compiler/writer vocabulary: its app record,
  instance inputs, rendered instance folder and unit registration, including removed/renamed paths. Do not copy
  v1 path patterns into Python or introduce a parallel path catalogue. The record reader and writer share
  `RecordFolder`/`AppRecordPath`; generated scopes use `InstanceFolder` and the compilation's registrations.
  Supplied shared profile, calls and egress paths are conservatively relevant themselves: changing one requires
  applying even if no own output change is listed. This avoids trusting stale committed rendering, at the cost
  of sometimes holding an otherwise unaffected instance for a shared-document change.
- An unapplied revision counts as unaffected only when the comparison is complete, its endpoints match the
  expected commit and the unit revision read by this invocation, and no relevant path changed. If the unit moves
  between the console's inventory read and v2's read, a nonmatching comparison grants no exemption. Exact revision
  equality needs no comparison. Unknown relevance leaves status updating and refuses promotion.
- Preserve the literal applied revision in the report. Report revision currentness separately as applied,
  unaffected, pending or unknown with a reason; unaffected does not mean Flux applied the newest SHA. Only the
  revision check is exempted: failures, suspension, a different digest and missing evidence are never excused.
  Host status without `--changes` remains conservative as today.

**Bulk observation and its boundary.**

- `status --all` is mutually exclusive with `--app/--env`. It compiles every instance in the pinned records,
  returns stable app/environment order and uses exactly the same pure evaluator as single-instance status.
  One invocation serves each console request, including an app page that also needs the Apps summary.
- Add an explicit all-namespaces list operation to `adapters/kube`; do not reinterpret an empty namespace on
  existing `Read`/`List`, and keep nonempty selectors mandatory. Contract-test real and fake adapters together.
- Candidate selectors are existing metadata, not authority: Flux ownership labels for HelmReleases, Ingresses
  and Certificates; the app-instance label for Deployments and main pods; the backup label for CronJobs, backup
  pods. Read units in `flux-system`, selected by existence of Flux's ownership-name label. Partition successful
  answers by namespace, name and target selector, keeping only objects each compiled target names. Unrelated
  candidates neither create instances nor grant ownership. No new workload labels or manifest changes are needed.
- **Approved Job exception:** compiled CronJobs guarantee the backup label on pod templates, not on Job metadata.
  Do not list Jobs broadly or rely on incidental live Job labels. After batched backup-pod discovery, GET at most
  one newest Job per database target, using the selected newest pod's owner name and that target's namespace.
  Missing owner evidence is unassessed; a failed pod list or Job GET is unavailable, never guessed absence.
- List each required kind/selector once per request, rather than seven reads per instance. Complete list
  semantics are essential: pagination or a response exceeding the adapter's cap must not make unseen objects
  absent. An incomplete or failed list becomes unavailable evidence for every dependent instance. This slice
  does not raise response limits or claim an atomic snapshot across Kubernetes resources. The core plan is four
  lists (units, releases, Deployments, main pods), plus two for routes/certificates if needed and two for backup
  CronJobs/pods if needed, independent of instance count, plus at most one newest-owner Job GET per database.
  `--archive` retains its separate per-database object-store listing.
- Single status is `v2-observation` version **5**; bulk is `v2-observations` version **1**, with `source`,
  `observedAt`, `revision` and `instances:[{"application":"...","environment":"...","report":{...}}]`.
  Each nested report is the single-report shape; an empty record tree yields `instances:[]` and no
  Kubernetes reads. Both modes keep the same credential, source, revision, archive and JSON/text flags.
- Each report adds `currentness:{state,reason,appliedRevision}` (the revision is literal, e.g.
  `main@sha1:<sha>`), and `live:{unit,release,workload,running}`. Unit/release facts carry `status`, optional
  failure `reason`, nullable `suspended`, and nullable `conditions:[{type,status,reason,observedGeneration}]`;
  a missing observed generation is null. Workload facts carry `status`, optional `reason`, and nullable
  `readyReplicas`. Running facts carry `status`, optional `reason`, and nullable sorted/distinct `digests`:
  the selected main pods' pinned content with evidence of a started container, as used by the existing image
  finding. Unavailable/absent/not-read is distinct from a present object omitting a field, and explicit empty
  arrays/zero/false remain values. The ready count is not synthesized from available replicas. The
  `unit.revision` finding says `unaffected`, not `applied`, when exempted. Python must not infer these facts
  from the selected state or invent values.
- The pure `observe.PromotionReadiness(report, reviewedDigest)` returns `{ready,reason}`; it requires currentness
  applied/unaffected, healthy state, the reviewed digest still selected, and only that digest proved running.
  Each version-5 JSON report exposes that decision as `promotion:{ready,reason}`, for the selected digest by
  default. Single status accepts `--reviewed-digest DIGEST` to evaluate a submitted review (an explicitly empty or
  invalid digest returns `ready:false`, never defaults to the selected digest); `--all` refuses that flag.
  `EncodeReport` uses the selected digest; `EncodeReviewedReport(report, reviewedDigest)` is the explicit Go
  encoding boundary for submission. No provider adapter or Python judge is involved.

**Console and promotion.**

- Replace `gather_status`, `status_of` and the instance `verdict` with the report. Adapt templates directly,
  without recreating `InstanceStatus`. Python maps Go state/reason codes to display text only. Keep the shared
  state/colour primitive and `unit_state` for the platform page; they are not instance judges.
- Desired settings and database kind come from pinned records; rendered environment variables come from the
  same commit's HelmRelease and are labelled desired, not observed. Secrets remain unsupported; do not fabricate
  secret values or file locations. Remove dead job-kind controls. Existing connection/catalogue data and the
  non-status parts of `AppRow` stay on their current readers in this slice.
- Activity consumes Go's observation too, not a second Python verdict with revision checks bypassed. It remains
  conservative while currentness is unproved; it may briefly show applying where v1's `render_state` said live.
  Historical evidence and the broader Activity redesign are out of scope. The console validates version 5 and
  the version-1 bulk envelope, preserving null/false/zero/empty values. Its request-owned `Files` scope pins main,
  caches only filtered immutable archive bytes by SHA (60 entries), and removes extraction and the 0600 host
  kubeconfig on exit, including failures. The pod uses `--in-cluster`; local `--dev` uses committed HEAD via
  `Runner` Git reads and serializes the current explicit host kubeconfig through `Runner`. A missing binary,
  invalid report or failed input read shows unavailable (HTTP 502), without status fallback or change forms.
- Promotion requires currentness (applied or proved unaffected), healthy workload, the reviewed digest still
  selected by fresh main, and that digest running. Go owns that decision and returns the refusal reason. Check
  again on submission, not only while rendering a disabled button. Preserve the existing stale-expectation check
  when the PR clone is prepared; PRs remain held for manual review. This is a submission-time observation, not a
  guarantee that staging remains healthy until a later manual merge.
- The current Python `touches` helper checks v1 paths and can mislabel an instance's own change as elsewhere.
  This affects the page's promotion warning; it is not an automatic merge bypass. The operator chose to replace
  this rule in Go as part of this slice, with no separate Python fix.

**Delivery and acceptance.** Two implementation steps, no permanent dual judge: first bulk observation and
revision evidence in Go, then switch the console consumers together and delete their Python status/readiness
rules. Keep image helpers still imported by the notification checker. Gate each step with Go state-table and
adapter contract tests, `make check-v2`, `make check` and lint; test the console adapter with `FakeRunner`.

Required cases: unrelated change, own record/render/unit change, removed/renamed path, unavailable/truncated
comparison, unit moving between reads, missing record versus failed GitHub read, failed/partial list versus an
empty list, mixed healthy/failing instances, and a POST that refuses when staging changed or became unready.
Assert list/detail agreement and core list counts independent of instance count, plus no more than one known-owner
Job GET per database (including unlabeled Jobs, missing owners, failed versus absent Jobs and namespace collisions).
Measure cold and warm page reads against the current console before deployment; report the numbers, not a presumed speedup. Live: compare every
instance, test signed-out redirects and signed-in pages/forms, and prove a promotion is refused while its own
change is unapplied without merging a promotion or creating a repository as part of the test.

### Slice: the platform page's checks (approved 6 November 2026)

**Outcome.** The six `verify-platform` checks that only read the cluster, the ones the console's platform page and
Overview run, are `swhurl-v2 verify`. The console calls the binary and no longer imports `verify`, which took eight
modules (about 1,340 lines) into its reach. `make verify-platform` runs the same Go checks first, then the remaining
Python ones, in one report and one exit code.

```text
console (--in-cluster) ──┐
                         ├→ swhurl-v2 verify --json → v2-platform-checks → page / Python Report
make verify-platform ────┘   (kubeconfig, SWHURL_V2)
```

**Semantics.** A check is one named group (`flux-units`, `ingress`, `alerts`, `notify-service`, `dashboards`,
`notifications`, in that order; Python's `flux` is `flux-units` because v2's guardrail refuses a string starting
with a command's name) with a section title and entries, each `ok`, `warn` or `bad` with a message. The
messages and verdicts are Python's: a unit that is not Ready, a missing redirect, an alert pointing at no provider, a
notify service with no ready replica, a suspended CronJob or one with no success in five minutes are `bad`; a missing
alert and a notify service on another image than the console's are `warn`. A failed read is `bad` ("cannot read …"),
never absence; an absent object is `bad` too and says it is missing. The time is the caller's.

**Boundaries.** The decisions are pure, in `observe` (`v2/observe/platform.go`: a pure package may not import another, and
the checks judge `observe`'s reads); one orchestration use case makes the reads; the command
parses flags, connects with `--kubeconfig` or `--in-cluster` (status's credential path) and prints. The kube adapter
gains the Flux `Alert` and `Provider` kinds (`notification.toolkit.fluxcd.io/v1beta3`) and one explicit read,
`ListNamespace`: every object of a kind in one namespace, with no selector, because the root Flux units carry no label
to select them by. `List` keeps its mandatory selector. Python owns only the merge into its `Report` and the console's
presentation; the console's page no longer names the checks it skips, only that `make verify-platform` runs more.

**Contract.** `--json` prints `{"kind":"v2-platform-checks","version":1,"source":…,"observedAt":…,"passed":bool,
"checks":[{"name","section","entries":[{"level","message"}]}]}`; without it, the `== section ==` and `[OK]` lines
Python prints. Exit 0 when no entry is `bad`, 1 when one is (the report is still printed), 2 for a usage error, an
unusable credential or a request the adapter refuses. The Python consumer validates kind, version and levels and
shows unavailable on anything else. Reads per run: three namespace lists (units, alerts, providers) and five gets,
independent of the number of instances.

**Acceptance.** Go table tests for each check (ready, not ready, empty, failed and absent reads, stale, suspended,
warn cases); adapter contract tests for `ListNamespace` against the real adapter and the fake; `FakeRunner` tests for
the console and the merge; `make check-v2`, `make check`. Live: `make verify-platform` and the platform page agree
with the current results (84 OK, no BAD or WARN), and the console's image runs the binary for the page.

### Slice: the rest of `verify-platform` (approved 6 November 2026)

**Outcome.** Every `verify-platform` check is `swhurl-v2 verify`; `verify.py`, its internal `verify-run` and the
modules only it reaches are deleted, and `make verify-platform` runs the binary itself. The console keeps the six
cluster checks with its pod's credential and changes nothing.

```text
make verify-platform ──→ swhurl-v2 verify --scope platform --kubeconfig K --root . [host facts] ──→ report, exit
console (--in-cluster) ──→ swhurl-v2 verify (--scope cluster, the default) ──→ the six, unchanged
```

**Semantics.** `--scope cluster` (the default) is the six checks of the [previous slice](#slice-the-platform-pages-checks-approved-6-november-2026);
`--scope platform` is all of them, needs `--kubeconfig` (with `--in-cluster` it is a usage error, 2: the console's
ServiceAccount cannot read what the rest needs) and `--root`, the checkout whose records, compiled tree, profile and
settings the checks compare with. Each check keeps Python's name, section, verdicts and messages unless a deliberate
difference below says otherwise. An input the platform scope needs but was not given (AWS credentials, a GitHub
token, a host fact) is `bad` "… not supplied", never a skipped check; a failed read stays `bad`, never absence. The
time is the caller's. Values read from Secrets are compared, never printed, in any message or error.

**Boundaries.** Decisions are pure, in `observe`, one function per check over the evidence it needs. One use case in
`orchestration` makes the reads for the platform scope. Adapters, each with its real/fake `Contract` suite:

| Adapter | Gains |
| --- | --- |
| `adapters/kube` | `Reader` kinds ImagePolicy, Receiver, StatefulSet, Stream, Consumer, PersistentVolumeClaim, PersistentVolume; one read of the cluster's token issuer and signing keys (the OIDC discovery document and its JWKS), named in domain terms, not a raw path. Secrets and ConfigMaps only through `Data`. An `Executor` port: exec in one named pod and container through the API server over WebSocket (`v5.channel.k8s.io`, standard library only), optional stdin, bounded output, the exit status from the status channel. The commands are fixed strings in v2; the port is not reachable from `Reader` |
| `adapters/github` (new) | Read-only, `api.github.com` only: a repository's hooks with each one's latest delivery; whether a token is accepted and its `github-authentication-token-expiration`. Tokens are arguments, never logged or in errors |
| `adapters/s3`, `adapters/registry` | Unchanged: `List` and `Describe` |

**Inputs from the host.** v2 still starts no process for these: the make recipe passes them.
`--console-tag` is the checkout's console content tag, computed by one script that CI's deploy job also uses;
`--heartbeat FILE` is `systemctl show --timestamp=unix` of the heartbeat timer and service, parsed by Go; AWS
credentials come from the environment (`aws configure export-credentials`, as `restore-sqlite`); the operator's
GitHub token for listing hooks is `GH_TOKEN` (`gh auth token`). The console's GitHub tokens are read from its Secret.
The Flux version and controller arguments move to one JSON file under `clusters/home/flux-system/install/`, read by
`flux-install` and by the check; no third package parses YAML.

**Deliberate differences.** Backups' S3 location is the live backup CronJob's `BACKUP_S3_URI`, not Git's YAML;
the channel Secrets are those the notify service reads (by its label), not names alone; a console tag that is not a
content tag (`src-…`) warns "cannot compare" instead of a `git diff`; until the last slice the report shows Go's
checks before Python's remaining ones, then Python's order returns. Webhooks match the exact parsed host, not a
substring or an impostor host; untrusted delivery messages retain only standard HTTP reasons, never URLs or credentials.
An exec stream ends successfully only after a terminal status and an orderly close (code 1000/1001, or none): a
valid error close (1002, 1008, 1011, …) fails the check even after a status, and a 101 selecting an extension this
client never offered is refused, so a failed transport can never read as a passing check. `flux-controllers` reads
Flux's settings with the same convention as every other scope step: a Deployment the API does not answer for aborts
the report (exit 2) where Python warned "is not installed" for any `kubectl` failure; a Deployment that is absent
still warns, and one that cannot be parsed is bad. `tools/swhurl/flux.py` reads `flux.json` only when a function needs
it, never at import, because the console imports `WAITING_REASONS` from it and the console image carries no checkout.

**Delivery,** four slices, each committed, run live with `make verify-platform` (the platform page unchanged) and
recorded in current state, Python's `CHECKS` shrinking with each and the deleted Python's test cases kept as Go
table tests: (1) the nine checks that read the cluster, the checkout, the registry and S3 (`image-automation`,
`isolation`, `signing-keys`, `events`, `egress`, `channels`, `backups`, `sqlite-backups`, `console`) and
`--scope platform`; (2) GitHub (`push-webhook`, `image-webhooks`, `console-token`); (3) exec (`ingestion-key`,
`registration`, `retention`); (4) the host facts (`notification-heartbeat`, `flux-controllers`), then `verify.py`
and `verify-run` are deleted and the operations page's check table is checked against Go's list.

**Slice 2 settings source (approved 6 November 2026).** The webhook checks read `BASE_DOMAIN` from the installed
`flux-system/platform-settings` ConfigMap (`Data`) and the platform repository URL from the installed GitRepository
named by the checkout profile's `delivery.source` in `flux-system` (`Reader`, which gains GitRepository). This checks
the webhook configuration actually installed, not drift of those settings from the local checkout. An absent,
malformed or unreadable setting is bad, never a guessed repository or domain. No Flux manifest is changed and no
new YAML reader is added. App image-webhook repositories still come from staging release records, and both console
tokens are checked against this installed platform repository. All other slice-2 scope, and slices 3 and 4, remain
approved.

**Slice 3 report groups (approved 6 November 2026).** The ingestion checks are two result groups,
`runtime-secrets` (`Runtime Secrets`) and `ingestion-key` (`Ingestion Key Sync`), preserving both sections and their
verdicts without changing the JSON schema or repeating a check name. The final catalogue has twenty-four groups
rather than twenty-three; no additional live operation is implied. Slice 3 moves Python's three routines into
four Go groups; after it twenty-two groups are Go's and two host checks remain Python.

## 12. The notify service in Go (approved 5 November 2026)

The operator approved porting the [notify service](services.md#notify-service) to `swhurl-v2 notify-serve`, run from
the console image it already uses. **Its wire contract and its outbox stay as they are**: the checker, every app and
ClickStack's webhook keep calling it unchanged, and the Go service sends what the Python one accepted. Everything else
is v2's design.

**Kept exactly** (each case of the Python service's tests is a Go test):

- `POST /v1/notify` and `POST /v1/alert`, `GET /healthz` (`ok`); every status, `{"error": ...}` reason and
  `{"channels": [...]}` answer; the 16 KiB body, 250-character title, 3,500-character message, 300-character key and
  500-character `https://` click limits, counted in characters; the field order of validation, so the same input
  gets the same reason.
- Who a caller is: a `TokenReview` for the audience `notify` and nothing from the body; the checker
  (`system:serviceaccount:console:notification-check`) names the app, and an app's own service account sends as
  itself, with `<app>/<env>: ` on its title and a random key when it gives none, at most 30 accepted an hour per
  instance (in memory: a restart forgets). Anyone else is 403 and logged by name.
- Alerts: the secret in the body, compared in constant time with `notify/notify-alerts`; the app from
  `swhurl:<app>:<preset>` and the environment from the alert's namespace group; the preset's label; the same key.
- The outbox: `notify/notify-outbox` key `state.json`, version 1, written before 202 and before any send; one entry
  per channel, each queued once per key; at most 500 pending and 700,000 bytes; a delivered or abandoned key
  remembered two hours; a channel that fails is retried for two hours without holding up the others; a delivery pass
  on every accept and every 30 seconds; at least once. An outbox of another version is refused, never reset.
- Routes, a namespace's team label, channel addresses and the webhook secret are read when they are used, never
  cached; the service's RBAC is unchanged.

**Boundaries.**

| Layer | Owns |
| --- | --- |
| `notify` (pure) | Decoding and validating a message or an alert, the sender rule, the rate limit over supplied times, the outbox's state and its transitions (accept, expire, delivered, failed), the ntfy message. Time and the random key are arguments |
| `adapters/kube` | Two new ports beside `Reader` and `Writer`. `Reviewer` asks whose token a caller presented. `Data` lists ConfigMaps and Secrets by field or label selector in one namespace and sets one ConfigMap key under a field manager. `Reader` still never reads a Secret, so an observing use case cannot reach one |
| `adapters/ntfy` | Publishes one message to one HTTPS topic address: a JSON post, a 10-second limit, no redirect, and an error that never names the address or the answer, since the address is the credential |
| `orchestration` | The service's use cases over those ports: accept a message, accept an alert, run one delivery pass. It holds the loaded outbox and its lock, not package state |
| `cmd/swhurl-v2` | `notify-serve [--listen ADDR]`: the HTTP server, JSON log lines, the delivery loop, and a new API connection from the pod's mounted ServiceAccount for each request and pass, so the kubelet's token rotation is followed |

What the service reads is narrower than the Python one: Secrets by label (`platform.swhurl.com/notify-transport=ntfy`)
or by name (`notify-alerts`), so Helm's release record is never fetched. A `SIGTERM` ends the process without a
graceful drain (v2 code may not name `syscall`); a cut-off request was not answered 202 and is retried by its caller,
and a send cut off before its save is sent again, which at least once allows.

**Step 1 was implemented and delivered before the cutover:** the pure [`notify`](../v2/notify) package, Kubernetes
`Reviewer`/`Data` ports, [`ntfy`](../v2/adapters/ntfy) publisher with real/fake contract suites, use cases and
[`notify-serve`](../v2/cmd/swhurl-v2/notify.go) command exist. The HelmRelease selects Go and the Python
implementation, internal command and service-only tests are deleted. [Live acceptance passed](current-state.md#the-notify-service-switches-to-go-5-november-2026);
[retirement evidence](current-state.md#the-python-notify-service-is-retired-5-november-2026) records the deletion.
[Handler tests](../v2/cmd/swhurl-v2/notify_test.go) exercise the Python service's acceptance and delivery cases;
[use-case tests](../v2/orchestration/notify_test.go) verify persistence before sending, retry and restart.
The API baseline intentionally includes these ports, pure types and use cases, not budget exceptions.
`kube.NewAPI` lets the command supply one owned client for all three ports without casting a `Reader` to a
secret-reading port. The service gives that client a ten-second round-trip timeout and disables idle connection
retention; credentials are still read anew for each request/pass. ConfigMap-key writes use the Data port's
4 MiB response bound because JSON escaping can make a legal 700,000-byte outbox exceed the writer's 1 MiB bound;
other writes keep that original bound.

**Deliberate differences:** Go refuses newline-suffixed app names that Python's regex accidentally admitted;
refusal log lines replace uvicorn's access log; Go's standard handler supplies wrong-method/path answers.
A successful empty channel-Secret list is read once per pass rather than once per copy. Unlike Python's
pass-local retry list, Go keeps failed copies in every subsequent saved checkpoint, so a mid-pass stop cannot
lose an earlier retry. Live TokenReview, RBAC, ntfy delivery/readback and the existing version-1 outbox
were exercised at cutover. Forced token rotation, an in-flight SIGTERM and a real correctly signed ClickStack
webhook remain uninduced; offline tests cover the corresponding decisions and request paths.

**Delivery, separate commits.** The operator approved delivery of step 1 and the live cutover on
5 November 2026. Both are complete: the HelmRelease runs `[swhurl-v2, notify-serve]`. The acceptance test uses a
port-forward: no token and a forged token get 401; a checker token (`kubectl create token`, ten minutes) is
accepted for a team with a channel, a repeat of its key gets no channel, and the copy is read back from ntfy;
a wrong webhook secret gets 401 and the outbox empties. The unchanged checker's real `forward()` client also
sent and retried a separate notice, with delivery/readback verified; scheduled checker runs succeeded, but no
new lifecycle incident was induced. **Step 3 is implemented after operator approval:** the unused Python
service, its internal command and service-only tests are deleted. The checker and shared delivery code it still
imports remain. The Go tests retain the retired cases; `9766d4c5:tests/test_notify_service.py` is their historical
source. Python rollback now requires an image from before retirement containing the old service (for example
`src-043f8c6e6233b3ab`) **and** its former command `[python, -m, swhurl, notify-serve]`, through Git as usual.
Reverting only the command on a post-retirement image cannot work. Neither direction migrates or resets the
version-1 outbox. The [status page](v2-status.md#remaining-work) names the next gate.

## 13. The notification checker in Go (approved 7 November 2026)

The operator approved porting the [notification checker](services.md#alerts) (`console/console-notifications`, every
minute) to `swhurl-v2 notifications-check`, run from the console image it already uses. The Python checker
is retired after [live lifecycle acceptance](current-state.md#notification-checker-lifecycle-acceptance-and-python-retirement-7-november-2026). One pass reads the Flux units, HelmReleases, workloads, pods and lifecycle Events, decides lifecycle
notices (deployed, failed, rolled back, uninstalled) and health incidents (unhealthy, stalled, recovered, hourly
reminders), and delivers them to the platform topic, with a copy for the notify service when the notice is about an
app. **Its state document and its wire contracts stay as they are**; everything else is v2's design.

**Kept exactly** (each retained case of the retired Python tests has [Go coverage](../v2/incident/testdata/README.md#all-42-legacy-cases), time an argument):

- **The state:** `console/notification-state` key `state.json`, version 1, the same shape (`started`,
  `last_success`, `instances`, `seen`, `pending`, `monitor`, `notify_service`) and the same notice keys, so Go
  continues from the document Python leaves (no repeated notice, no reset incident clock) and Python can continue
  from Go's. A document of another version or shape, or one over 700,000 bytes encoded, is refused, never reset;
  written with field manager `notification-check`.
- **The rules:** targets are the units whose path is an instance folder (the current
  `./v2-apps/instances/<app>/<env>`, legacy `./apps/<app>/<env>`, and the generated fixtures' root) and the console's
  unit; grace 300 s (600 s for the console), recovery after 120 s healthy, a
  reminder every 3,600 s, a unit failure reported only after 60 s, history 7,200 s; the health rule as it is
  (a completed rollback is healthy, a repository-wide revision change is ignored, a stopped instance clears its
  incident, a suspended one silences it, a mismatched digest stalls unless Helm rolled back), the event filters
  (controller, uid, attempt token, the last sixteen failed attempts, a deployment held until the workload is
  ready), the wording, titles, priorities, tags and links.
- **Unknown is not healthy:** a failed read keeps the previous state, clears every pending recovery, raises the
  monitor's own incident (reported after its grace) and exits 1.
- **Delivery:** the state is saved before any send; pending notices go in order, each checkpointed when accepted;
  a copy for the notify service that is refused is retried for 7,200 s without holding up the platform topic, and
  one refusal ends the pass's other copies; the operator hears on the platform topic when the notify service stops
  accepting copies and when it accepts again. At least once. Failed copies remain in every saved checkpoint;
  unlike Python's temporary retry list, stopping partway through a pass cannot lose an earlier failed copy.
- **The wire:** ntfy gets the same JSON document; the notify service gets the same `POST /v1/notify` with the
  checker's projected `notify` token, read anew each pass. The platform topic comes from `NTFY_PLATFORM_URL` and is
  never logged; neither address nor any answer body appears in an error.
- **`--dry-run`** reads and evaluates, logs what it would send, and neither posts nor saves. At cutover, `make
  notifications-check` runs it from the host with the kubeconfig; the CronJob runs a real pass with the pod's
  ServiceAccount. Step 1 leaves the existing Python make target unchanged.

**Boundaries.**

| Layer | Owns |
| --- | --- |
| `incident` (pure, new) | Targets from the units, the health rule, which events qualify, the incident transitions, the outbox, and decoding and encoding the state document. A pass is `Evaluate(previous, snapshot, now, base, copies)`; delivery transitions are functions over the state. It owns the persisted notice fields without a same-layer dependency on `notify` |
| `adapters/kube` | Reads the kinds the snapshot needs across namespaces without a label selector, and core `Event` across namespaces by **field selector** `involvedObject.kind` (`HelmRelease`, then `Kustomization`). Writes the state through the existing `Data.SetConfigMapKey`; reads it with a named ConfigMap GET, not a list, preserving the checker's existing get/patch-only permission on that object |
| `adapters/ntfy` | Unchanged: publishes to the platform topic |
| `adapters/notifysvc` (new, external effect, approved with this slice) | Posts one copy to the notify service with the token it is given; reports accepted (202), refused (any other status) or unreachable, never the token or the body |
| `orchestration` | One pass: read the state, read the snapshot, evaluate, deliver, report counts and what a dry run would send. Projects the persisted notice fields to the existing `notify.Publication` for ntfy; no exception to the same-layer import rule |
| `cmd/swhurl-v2` | `notifications-check [--dry-run] (--kubeconfig PATH | --in-cluster)`: environment (`CONSOLE_URL`, `NTFY_PLATFORM_URL`, `SWHURL_NOTIFY_URL`, the token path), JSON log lines, exit status |

**Deliberate differences, approved:**

1. **Only lifecycle Events are read.** Python lists every Event (1.4 MB on 7 November 2026, almost all for Pods and
   Jobs it discards); Go lists the two kinds it uses, which keeps the read inside the adapter's bound.
2. **DaemonSets are not read**, and the checker's ClusterRole drops them: v2 renders only Deployments and
   StatefulSets for an app, and the cluster's two DaemonSets are the platform's.
3. **The scheduled-job path is dropped** (`job_health`, `run failed`): v2 refuses the `job` kind, so no instance
   reaches it. It returns with the deferred `job` kind.
4. **Direct Go invocation requires `CONSOLE_URL`.** It does not read checkout YAML for a fallback domain or
   hardcode this cluster's address. The CronJob already supplies this variable; direct callers must set it.
   This keeps console links explicit without adding a parser or profile boundary.
5. **Discover current v2 app units.** Python's `Instance.of_path` recognizes only the legacy and fixture roots;
   the current units point into `./v2-apps/instances/<app>/<env>`. Go recognizes that exact root too, tested against
   current unit paths and unrelated folders. Retaining the omission would silently skip all v2 apps. The live
   acceptance must distinguish newly discovered apps' eligible notices from duplicates of delivered keys.

**Delivery, separate commits.**

1. **Offline:** the package, the adapters (with their contract suites and raw fixtures), the use case and the
   command, the kept cases as Go tests, and a decoding test over a real version-1 state document. `make check-v2`
   and `make check`; an independent read-only review. Nothing live. **Implemented and reviewed:**
   [evidence](current-state.md#notification-checker-scheduled-in-go-7-november-2026).
   The version-1 fixture is sanitized and Python-produced, not a live export; actual live-document decoding
   remains a cutover gate.
2. **Cutover, confirmed by the operator on 7 November 2026:** the CronJob's command becomes
   `[timeout, "50", swhurl-v2, notifications-check, --in-cluster]` and the ClusterRole drops DaemonSets.
   Acceptance: the first Go pass reads the live state and sends nothing it already sent; scheduled passes succeed;
   `make verify-platform` stays clean. **Acceptance-plan correction:** `make live-test-lifecycle` uses a raw
   Deployment/PVC, not a HelmRelease or a discoverable app path, so it cannot prove checker lifecycle notices.
   The operator separately approved installation/removal of only the existing no-data `smoke-worker` Helm fixture.
   Deployed and uninstalled notices were read back on both the platform topic and the team's distinct channel;
   notify-service acceptance and delivery logs corroborate each team copy. The fixture is removed; no namespace-removal
   race prevented this test's team copy. [Scheduled cutover](current-state.md#notification-checker-scheduled-in-go-7-november-2026)
   and [lifecycle acceptance](current-state.md#notification-checker-lifecycle-acceptance-and-python-retirement-7-november-2026)
   are recorded separately. Phone display, rollback and timed health/recovery are not claimed by this fixture.
3. **Retirement:** removed `notifications/__init__.py`'s `check` and `main`, `evaluate.py`, `state.py`, `delivery.py`,
   `notice.forward` and `tests/test_notifications.py`; the command has no Python implementation. `notice.py` (`Notice`,
   `Ntfy`, `platform_topic`) and `errors.py` stay for the host heartbeat, `clickstack_ntfy.py` and `dashboards.py`, which
   leave with their own rows. The Go case map, captured goldens, wiring and failure/restart tests remain. A Python
   checker rollback requires both a pre-retirement image and its former command, not a command-only change.

**Not in this slice:** the host heartbeat, the dashboard reconciler, new alerts (backups, certificates, disk), and
any change to what raises a notice.

## 14. The dashboard reconciler in Go (approved 7 November 2026)

The operator approved porting the ClickStack dashboard reconciler (`console/console-dashboards`, every minute,
and `make clickstack-dashboards`) to `swhurl-v2 clickstack-dashboards`, **offline only**; the live cutover is
separately gated. It replaces `tools/swhurl/{dashboards,app_alerts,clickstack_ntfy}.py`. One pass finds the apps,
repairs the two reserved webhooks, keeps one dashboard per app, records each app's dashboard for the console and
keeps each app's default alerts.

**Kept exactly** (each of the 40 Python cases has [Go coverage or a stated reason](../v2/dashboards/testdata/README.md#all-40-python-cases)):

- **The objects:** dashboard names, queries and layout (Python-produced goldens); the default alerts
  `swhurl:<app>:<preset>`, 5 per 5 minutes on the `Errors` and `Error logs` tiles, and an app's own thresholds
  from the `platform.swhurl.com/alerts` annotation on its namespaces (a bad value falls back to the defaults);
  both reserved webhooks, whose bodies are the Python job's bytes, so cutover rewrites neither.
- **Stable ids:** a changed dashboard, webhook or alert is replaced in place by its id.
- **Webhooks first:** both reserved names are checked (duplicate, other service, no id, truncated listing)
  before either is written, and every alert names the notify webhook's id.
- **Deletion:** only a dashboard tagged `swhurl-app` and named exactly `App: <app>` whose app is gone; a cluster
  pass that finds no app release deletes no dashboard, alert or record entry, but still repairs the webhooks.
- **The record:** `console/app-dashboards` key `dashboards.json`, `{"<app>": "<ObjectId>"}` in the Python job's
  bytes, field manager `dashboard-sync`, written only when it changed.
- **Credentials:** the admin email and MongoDB URI, the platform topic and the notify secret are read by name
  every pass; the access key is read from MongoDB once per pass. Each travels only on an exec's stdin; no error,
  log line or argument carries one, nor any HTTP answer body.
- **`--dry-run`** reads and plans everything and writes nothing, the record included.

**Deliberate differences, approved:**

1. **Records mode reads `v2-records`**, not `apps/`; a missing or invalid release record, or a tree naming no
   instance, stops the pass instead of reading as no apps.
2. **No job dashboards.** v2 runs no scheduled job; an app release that is one stops the pass rather than
   losing its dashboard to pruning.
3. **Duplicate or incomplete listings stop the pass:** two tagged dashboards or two `swhurl:` alerts of one name,
   one without an id, or a listing whose `meta.total` exceeds its items. Python kept the last dashboard and
   deleted all but the first alert.
4. **A failed read or write of the record stops the pass** before the alerts; Python carried on and exited 1.
5. **Deletion is by exact name in both modes;** Python's records mode deleted any tagged dashboard.
6. **Discovery selects:** HelmReleases by `kustomize.toolkit.fluxcd.io/namespace=flux-system` and namespaces by
   `platform.swhurl.com/app`, within the existing list permissions. When staging and production disagree on the
   kind or alert settings, staging's decide.

**Boundaries.**

| Layer | Owns |
| --- | --- |
| `dashboards` (pure, new) | Apps from HelmReleases and namespaces, or from records; the definitions; comparison with what HyperDX lists; the webhook, dashboard and alert plans with their preflight checks; the record's bytes |
| `adapters/clickstack` (new, external effect, approved with this slice) | The external API (`/api/v2`) and the admin-key read, as Node and mongosh programs with credentials on stdin; complete listings or an error; an answer outside 2xx is an error naming its status only. A `Runner` supplied by the caller runs each program; a fake HyperDX and one `Contract` suite |
| `adapters/kube` | `Data.GetSecret`: one Secret by named GET, because the job's Roles allow `get` on named Secrets only; 404 is absence, anything else a failure |
| `orchestration` | One pass, `SyncDashboards`, in the order above, stopping at the first failure with a report of how far it got; `ClickStackRunners` puts the programs on the API server's exec (MongoDB's pod; the app Deployment's ready pod) |
| `cmd/swhurl-v2` | `clickstack-dashboards (--cluster \| --records DIR) [--dry-run] (--kubeconfig PATH \| --in-cluster)`: reads the records tree, JSON log lines, exit 1 when the pass stopped |

**Delivery, separate commits.**

1. **Offline:** the package, the adapters with their contract suites and raw fixtures, the use case, the command
   and the case map. `make check-v2` and `make check`; an independent read-only review. Nothing live.
2. **Cutover, separately gated:** the CronJob's command becomes `[timeout, "50", swhurl-v2,
   clickstack-dashboards, --cluster, --in-cluster]` and `make clickstack-dashboards` runs the Go command against
   `v2-records`. Acceptance: a dry run against the live HyperDX plans no change to the existing webhooks,
   dashboards, alerts or record; scheduled passes succeed; `make verify-platform` stays clean.
3. **Retirement:** delete the three Python modules and their tests. The console's link reader
   (`dashboards.recorded`, in `console/context.py`) is their only other importer; it moves into the console in
   this step. `app_alerts.spec_of` already has no caller.

**Not in this slice:** the host heartbeat, `clickstack-bootstrap`, new alert presets, and any change to what a
dashboard shows.

## 15. The host heartbeat in Go (approved 7 November 2026)

The operator approved porting the [host heartbeat](operations.md#notification-checker-heartbeat) to `swhurl-v2
notifications-heartbeat`. It is the watchdog for the notification checker: the host's
`swhurl-notification-heartbeat.timer` runs `make notifications-heartbeat` every five minutes, outside the cluster,
so whatever stops the checker does not stop it. Only the program changes; the timer, the target's name, the state
file, the destination, the wording and the log shape stay. It replaces `tools/swhurl/notifications/` entirely.

**Kept exactly** (each case of `tests/test_notification_heartbeat.py` and the heartbeat's use of `tests/test_notice.py`
has Go coverage or a stated reason):

- **The rule:** one watched job, `notification checker` (`console/console-notifications`, 600 s), a Go constant.
  It is stale when the CronJob is missing, suspended, has no `lastSuccessfulTime`, has one that does not parse, or
  last succeeded more than the maximum age ago (`last success was N minutes ago`); fresh otherwise. Transitions:
  alert (stale, no incident), remind (stale, 3,600 s since the last alert), recover (fresh, incident open), none.
- **The wording:** `<label> stale` with `The <name> CronJob is stale: <reason>. Check with make verify-platform.`,
  priority 4, tag `warning`; `<label> recovered` with `The <name> CronJob is healthy again.`, priority 3, tag
  `white_check_mark`; the same ntfy JSON document.
- **The state:** `~/.local/state/swhurl-platform/notification-heartbeat.json`, the first job's flag
  (`alerting_since`, `last_alert`: both null or both finite numbers) at the top level and any other job's under
  `others`, so Go continues from Python's file. An absent file is no incident; an unreadable or invalid one is
  refused, left unchanged and exits 1. Written atomically (temporary file, fsync, rename) with directory mode 0700
  and file mode 0600.
- **Delivery:** the destination is `console/notification-ntfy` key `NTFY_PLATFORM_URL`, read by named GET only when
  a notice is due; the state is saved only after ntfy accepts, so a refused send is retried next run. A failed
  CronJob read sends nothing, saves nothing and exits 1 (unknown is not stale). Each job is decided on its own; any
  failure exits 1. Neither the address nor an answer body appears in a log line or error.
- **The log:** the JSON lines `swhurl-v2`'s commands already write (`time`, `level`, `logger`, `message`), the
  shape Python wrote, appended to the unit's log file.
- **`--dry-run`** (`DRY_RUN=true`) reads and decides, logs `Would notify: <title> (priority N)`, and neither posts
  nor saves.

**Deliberate differences, approved:**

1. **`--state PATH`** selects the state file (default unchanged), so acceptance can use a throwaway file.
2. **`--max-age D`** stays (`s`, `m` or `h`, greater than zero), overriding every watched job's maximum age.
3. **The unit's `PATH` gains `/usr/local/go/bin`**, where the host's Go is; the operator reinstalls once with
   `make host-heartbeat`.

**Smaller differences found in implementation** (each in the [case map](../v2/heartbeat/testdata/README.md)): a
time Python wrote as an integer is written back as a float and `others` keys are sorted (Python reads both);
`lastSuccessfulTime` must be RFC 3339, as Kubernetes writes it; a CronJob field of the wrong type is `<label> status
is invalid` (exit 1, nothing sent); a state file over 1 MiB or containing `NaN` is refused as unreadable; a refused
send or an address that is not an HTTPS topic names the adapter's fixed code (`failed: publish: status_403`); the log's `logger` is `notifications-heartbeat`;
a pass is bounded to one minute; the temporary file is `.notification-heartbeat.json.*`, and a missing state
directory's missing parents are made 0700 too.

**Boundaries.**

| Layer | Owns |
| --- | --- |
| `heartbeat` (pure, new) | Freshness, the transition, the notice, and decoding and encoding the state document |
| `adapters/kube` | Unchanged: `Read` of one CronJob, `GetSecret` |
| `adapters/ntfy` | Unchanged |
| `adapters/hoststate` (new, external effect, approved with this slice) | Reads one local file (absent distinct from unreadable) and replaces it atomically with private modes; a contract suite and a fake |
| `orchestration` | One pass over the watched jobs: read, decide, read the destination when due, send, save; a report per job |
| `cmd/swhurl-v2` | `notifications-heartbeat [--dry-run] [--max-age D] [--state PATH] --kubeconfig PATH`: JSON log lines, exit status |

**Delivery, two commits.** The heartbeat runs from the checkout and has no image, so a rollback is one revert.

1. **Offline:** the package, the adapter, the use case, the command and the case map, plus the unit's `PATH`
   line, which the Python run ignores. `make check-v2` and `make check`; an independent read-only review. The
   operator then reinstalls the timer.
2. **Cutover and retirement:** `make notifications-heartbeat` builds and runs the Go command with a private JSON
   kubeconfig, silently (no echoed recipe or build output in the log file), passing `--dry-run` when
   `DRY_RUN=true`; `tools/swhurl/notifications/`, its two test files and its `commands.py` entry are deleted.
   Acceptance: scheduled runs log `fresh` and succeed; `--dry-run --max-age 1s` plans `notification checker stale`;
   one real alert and recovery on the platform topic with `--max-age 1s --state <throwaway>`; `make
   verify-platform` stays clean.

**Not in this slice:** `clickstack-bootstrap`, more watched jobs, and removing `httpx` (the console uses it).

## 16. ClickStack bootstrap in Go (approved 7 November 2026)

The operator approved porting `make clickstack-bootstrap` to `swhurl-v2 clickstack-bootstrap`, after weighing a bash
version (shorter, but untested and handling credentials). HyperDX has no setting for its first account or its team's
ingestion key; the command makes both the ones in `observability/clickstack-runtime-inputs` (SOPS). The target's name,
its inputs and what it writes stay; `tools/swhurl/clickstack.py` keeps only what the MongoDB restore test,
`verify-logs` and the team scenario still use, and `tests/test_clickstack.py` is deleted.

**Kept exactly** (each case of `tests/test_clickstack.py` has a Go test in `orchestration/bootstrap_test.go`): both
Secrets are read before anything else; the API is waited for (five minutes, every five seconds) before MongoDB is
touched; the admin is registered only when no team exists; an admin with no account is a warning; the one team's key
is set only when it differs, so a second run writes nothing; more than one team, a refused registration or an API that
never answers stops with exit 1; `--dry-run` (`DRY_RUN=true`) reads and decides and writes nothing; no output line,
error or argument holds the password, the key or MongoDB's address, and the current team key never leaves MongoDB's
pod (the comparison runs there).

**Deliberate differences:** a refused registration names its status, not HyperDX's message (the adapter never repeats
an answer's body); any 2xx answers `/ready` and a registration, where Python wanted 200; an `/installation` answer
without `isTeamExisting` stops the run instead of reading as no team; setting the key checks that exactly one team was
updated; an account check that cannot be answered stops the run, and a dry run warns of a missing account too; a
missing input names its first missing key; a dry run with no team says it would set the key and reads nothing more;
`TIMEOUT_SECS` is gone (it was never documented), and the whole run is bounded to seven minutes.

**Boundaries.**

| Layer | Owns |
| --- | --- |
| `adapters/clickstack` (external effect, approved with this slice) | `Installer`: readiness, whether a team exists and registration on HyperDX's internal API, which needs no key; the account check and the team key's comparison and update as mongosh programs. Same exec runners and stdin-only credentials as the dashboard adapter; `InstallerContract` runs against the fake `Installation` and the real adapter |
| `orchestration` | `BootstrapClickStack`: read, wait, register, check the account, compare and set the key; a report of lines, or the error where it stopped |
| `cmd/swhurl-v2` | `clickstack-bootstrap [--dry-run] --kubeconfig PATH`: `[OK]`/`[INFO]`/`[WARN]` lines, `[BAD]` and exit 1 where it stopped |

**Delivery, one commit:** the operator runs the command, nothing schedules it, and a rollback is one revert.
Acceptance: `make check-v2` and `make check`; a live dry run and a live run both find everything in place and write
nothing.

## 17. `check-repo` in Go: the decisions, not the subprocesses (approved 8 November 2026)

The operator approved splitting `make check-repo` by **who decides**, not by language, after weighing a full port and
a bare deletion. `validate.py`'s work has two halves: decisions about this checkout (documentation links and their
anchors, the Flux units table, the app-interface table, emitted names against `v2/contract/names.json`, SOPS Secret
structure, the settings the platform substitutes, and which paths Flux renders with what each substitutes), and three
steps where a tool verifies itself (`kubectl kustomize`, `flux-schema validate`, `bash -n`). Only the first half is
v2's. The second stays bash glue in `v2/check-repo.sh`, so v2 starts no new process, names no `kubectl` or `flux`, and
the `no-subprocess` guardrail is unchanged.

**The schema step leaves the standing check.** `flux-schema` validated 253 platform documents over 32 paths in 11.5 s,
beside 5.3 s of rendering. It catches required fields, enums, CEL rules and the one class nothing else catches:
misspelled fields, which the API server prunes silently (`/spec: additional properties 'valuez' not allowed`), leaving
the unit Ready and the platform running something other than the file says. Every other class fails on the cluster,
where Flux refuses the object and `make verify-platform` reports the unit. The operator's judgement, recorded:
platform services are stable and fail loudly in the cluster, and userland manifests are compiler output tested in Go,
so the standing check keeps the cheap render and substitution discipline and `flux-schema` becomes opt-in
(`make check-repo SCHEMA=1`). Measured while deciding, 8 November 2026: 267 commits in 120 days touched a hand-written
manifest after excluding the four v2-rendered platform folders and bot image pins, 121 of them a Flux/CRD-shaped
object — churn is not stability, but no commit in that window was a field typo.

**Boundaries.**

| Layer | Owns |
| --- | --- |
| `adapters/manifests` (new; approved with this slice, and its one guardrail diff) | `Decoder.Documents(text)` over Kubernetes YAML: decoded documents with kind, name and their fields, a plain YAML 1.1 boolean read as Kubernetes reads it; it reads no file (the command reads them inside the checkout). Raw fixtures in `testdata/`, a fake and one `Contract` suite. It is the third approved importer of `go.yaml.in/yaml/v3`, added to `archtest/rules_test.go`'s allowlist and to the guardrails table |
| `v2/checkrepo` (pure, layer 0) | Each check as a function of the documents and file text it is given: `docs-links`, `flux-units-table`, `app-interface-table`, `emitted-names`, `sops-secrets`, `platform-settings`, `render-paths`, and the substitution of one path |
| `adapters/git` (unchanged) | The tracked and untracked file lists the documentation and shell checks need |
| `orchestration` | `CheckRepo`: read, run every check, collect the failures, report |
| `cmd/swhurl-v2` | `check-repo [--root DIR] [--rendered PATH] [--json]` |
| `v2/check-repo.sh` (bash) | The three subprocess steps, over the paths Go lists |

**Contract.**

- `swhurl-v2 check-repo` prints `[OK]` for each check and then the active render paths with their substitution values;
  `--json` prints the same as one document (`kind: v2-check-repo`, version 1, checks, paths, values). Every check runs
  even when an earlier one failed; exit 1 if any failed, 2 for usage or an unreadable tree. It writes nothing, fetches
  nothing and reads no cluster.
- `--rendered PATH` reads that path's rendered output on stdin, applies the path's substitution and prints the result;
  an unresolved `${...}` is exit 1 naming the token and the escaping rule (`$${...}`). The substitution decision lives
  in Go, so bash never parses a token.
- `make check-repo` is `bash v2/check-repo.sh`: the binary, then for each path
  `kubectl kustomize PATH | swhurl-v2 check-repo --rendered PATH`, then `bash -n` on `git ls-files '*.sh'`. `SCHEMA=1`
  adds `flux-schema validate --schema-location default --schema-location ecosystem --skip-json-path /sops` on the same
  stream. The `commands.py` row loses its `run=` and gains the `SCHEMA` variable.
- The pinned `flux-schema` install leaves `validate.yml` and `render.yml`; [contributing](contributing.md)
  lists it as what `SCHEMA=1` needs, and [commands](commands.md) says what each form checks.
- Deleted with it: `tools/swhurl/validate.py` (296 lines), `tests/test_validate.py` (171) and `apps/checks.py`, which
  held only validate's compiled-tree path; `platform.py`'s `decrypted_secret_files`, `BOOTSTRAP_PATHS` and `UNITS_DOC`
  (`flux_unit_documents` stays: `platform.units()` and `tests/test_manifest_policy.py` read it); `interface.unlisted`
  and `NAME_RE` with their test (v2 reads the same catalogue, and `interface.py` stays for the console); and validate's
  last read of the v1 `apps/contract.py` constants — the backup writer, file and label are v2's own.

**Acceptance.** Go table tests carry every case of `tests/test_validate.py`: each kind of drift is named, every check
still runs after a failure, a substituting unit is resolved and a unit without substitution keeps its `${...}`, a
decrypting unit with no Secret file fails, and a missing setting fails. Parity is shown on this tree — the Python
`validate.py` and the Go command both pass — and on one injected fault per check (a broken anchor, a unit missing from
the table, an unlisted emitted name, an unencrypted Secret value, an absent setting, a path that escapes the
repository, an unresolved substitution), where both fail naming it. `SCHEMA=1` passes on the same tree and fails a
copied rendered document with one misspelled field. The `adapters/manifests` allowlist change is reviewed as a
deliberate baseline change, and `make check-v2` is the check that holds it.

**Delivery, one commit.** `make check`, `make check-repo`, `make check-repo SCHEMA=1`, `make check-v2`,
`make check-docs`, and `bash -n` over `host/*.sh tests/fixtures/*.sh`.

## 18. The snapshot export and the migration tools are retired (approved 8 November 2026)

The operator chose the larger of two options: retire `make v2-snapshot` and, with it, v2's commands that only read
its file and the migration tooling that has nothing left to act on. The Python exporter exists only to feed v2:
`tools/swhurl/v2snapshot.py` (259 lines) wrote one pinned revision's records and object identities as JSON, and seven
commands read it with `--snapshot`. Two facts made it removable. Everything it produced is already rendered by
`swhurl-v2 render`, which builds the same value in memory from `v2-records/` and calls the same Go helpers to write
the resolution, the egress allocation, `platform/auth`, NATS's users and Reloader's list; only `make v2-users` and
`make v2-reloader` still took the Python route, and their caller was the Deploy v2 bot. And the migration tools have
nothing left to convert: `apps/` holds only a README and every unit in `clusters/home/` is a v2 JSON unit, so
`shadow`, `migrate` and `preview` cannot run on anything real.

**Before and after.** The bot's step becomes one render:

| | Deploy v2's step | What writes the derived files |
| --- | --- | --- |
| before | `make v2-deploy`, then `make v2-users` and `make v2-reloader` | each target exported a snapshot of the working tree through Python, then one Go command read it |
| after | `make v2-deploy`, then `make v2-render` | one Go render reads `v2-records/` and writes every file derived from it |

**Semantics.** v2's records tree is the only input. `render` is the only producer of the files derived from it, and
`render --check` remains the freshness check. "Snapshot" survives only as `render`'s internal value: the records it
reads, named and shaped as the helpers already take them. A predecessor's record shape leaves the command surface
with the migration tools; the v1 model oracle keeps its own fixtures.

**Boundaries.** v2 owns rendering. `bots.py` stays Python glue for now (its own later slice) and calls
`make -s v2-render`; Python remains installed in `deploy-v2.yml` for that bot, and the step's comment stops claiming
`check-v2` needs PyYAML, because `check-render.sh` runs no Python. Deleting the migration tools removes v2's last
reader of a predecessor's YAML records, so `legacy` and `shadow` leave the layer map, `Pure` and the API baseline.

**Contract.** A deploy commit holds deploy's record and compiled-file changes plus whatever render derives from
them. `check-v2` already compares the result with `render --check`, so the new path cannot diverge from what the
checks expect. `render` records `--revision` in `calls.json` and the allocation, so the one observed risk was that
the bot's commit starts touching those two fields; parity is measured before the old targets are deleted, and churn
in those two files stops the slice for the operator.

**Deleted with it.** `tools/swhurl/v2snapshot.py` and `tests/test_v2_snapshot.py`; the `v2-snapshot`, `v2-users` and
`v2-reloader` targets and their `commands.py` rows; the command entries for `auth`, `users`, `reloader`, `resolve`,
`egress`, `shadow`, `migrate` and `preview`, whose helpers stay because `render` calls them; the `legacy` and `shadow`
packages and `orchestration/migrate.go` with their tests; `identity`, `notCovered` and the request fields only those
commands used. `archtest/api.txt`, the layers map and `Pure` shrink. What stays: `proxy` (render's output is its
input), `compile`/`deploy`/`promote`/`status` with `--calls` and `--egress`, and `render`'s helpers.

**Acceptance.** Parity on a scratch worktree: `make v2-deploy` followed by the old `v2-users` and `v2-reloader`
writes the same `platform/nats/users.json` and `platform/reloader/values.json` bytes as `make v2-deploy` followed by
`make v2-render`, and the comparison names any other path render touches. A `FakeRunner` test has `deploy_v2` call
`v2-render`, and the Go tests pass on the slimmer command set. `make check`, `make check-v2` (with the `api.txt`,
rule and layer changes) and `bash -n` over the scripts. A search finds `--snapshot` and `v2-snapshot` only in dated
history ([current state](current-state.md), [deleting v1](v2-deletion-plan.md)). Live: the next scheduled Deploy v2
run succeeds through the new path, which is the only live path this changes.

**Delivery, one commit after the contract.** Docs change with the code: [commands](commands.md), [services](services.md),
the egress lesson in `AGENTS.md`, [v2 status](v2-status.md) and the [workstream registry](workstreams.md), plus a
dated entry in [current state](current-state.md).

## 19. Egress grants an instance declares (approved 8 November 2026)

The operator chose this over the next retirement slice: it is the one deferred feature that blocks an ordinary request
(an app that needs Stripe), and the machinery it needs already exists and is enforced. The proxy, its configuration, the
allocation and the policy were v2's from 26 October 2026; what was missing was a way for a record to state a host.

**Semantics.** A grant is one host an instance may reach outside the cluster, held in that environment's instance record
under `grants`, beside the exposure that says who may reach it: `"grants": {"exposure": "private", "egress":
["api.stripe.com", "*.github.com:8443"]}`. It is the operator's statement of what the instance needs, never the app's:
an app's `swhurl.yaml` cannot declare one, and a grant is per environment, so a promotion never widens production.

**The one form the platform reads.** `kernel.EgressGrants` reads a record's list and writes what it read: lowercase, a
wildcard as `*.domain`, 443 written by its absence, sorted by port and then host, and nothing for an empty list (the
field is absent, not `[]`). Three refusals, at field `grants.egress`: something that is not a host and port (an IP
address, a URL, a path), a port outside 1–65535, and the same authority stated twice — a host granted twice, or a host
a wildcard grant on the same port already covers. The third is where a record differs from the allocation: the
allocation's reader reduces such a pair instead, because the platform adds a database's backup host, which a wildcard
grant may legitimately cover.

**Boundaries.** The record states outside hosts only; the backup host stays the profile's fact. The allocation decides
ports and carries the grants; the proxy's configuration and policy, and the instance's `platform.swhurl.com/egress`
annotation and proxy variables, are rendered from it. So the three writes are separate by design:

| Step | Writes | What it decides |
| --- | --- | --- |
| `swhurl-v2 edit --set egress=…` | the instance record, and the files it compiles from the committed allocation | the hosts, and only the hosts |
| `make v2-render` | the allocation, the proxy's two documents, the instance's folder and unit | the port, the proxy's ACLs, the annotation and the variables |
| `git commit` and Flux | nothing of its own | the cluster, through the proxy's ConfigMap |

`edit` therefore reports `allocationStale` and prints the render to run, exactly as a declaration change reports
`resolutionStale`. A port is given to any instance that declares a grant or keeps a database, and to nothing else.

**Identity.** The grants take part in the instance revision, last in the canonical instance array, so two records that
reach different hosts have different revisions; the record's list is cloned wherever a record is returned, like the
declaration lists. This changed every instance's revision without changing a rendered file: the revision is the
compilation's input identity, not part of a manifest, and the checks compare the tree that way (`render --check`: 157
files, 0 differ).

**Not in this slice.** A console form (the **Connect** and **Egress** pages keep showing what is rendered), grants at
creation, a per-instance "no egress" instruction, and any change to the proxy's own deployment. The first is a
console-port slice of its own.

**Verification.** Kernel and command tests: the normalised form, each refusal, the round trip, the identity, the
allocation entry, the proxy's ACLs and the annotation for a declared grant, and no allocation for an instance that
declares nothing. `make check-v2` (Go tests, guardrails, `render --check`), `make check-repo`, `make test` and
`make check-docs`. Live: one throwaway instance is granted a host it could not reach, the granted host answers 200 and
an ungranted one 403 with `granted=-` in the proxy's log, from a pod in that instance's namespace, then the grant is
cleared and re-rendered ([current state](current-state.md)).

## 20. The merge gate is retired (approved 8 November 2026)

**Decision.** The console's merge gate leaves by being deleted, not ported. Nothing it did was reachable: every kind in
its table is `gated=False` (the `new` kind since 1 November 2026, the teams and every `v2-*` kind since 2 November), so
its queue held no labelled branch and the Auto-merge workflow skipped every pull request that woke it. Porting it would
need v2's first *writing* Git adapter (merge, commit, push) and a GitHub write adapter (label, comment) for a mechanism
with no caller.

**What a console pull request is now.** A person merges every one. The console adds no label, writes no comment and has
no merge path: GitHub's **Merge** button is the only way a `console/*` branch reaches `main`. The bots that push to
`main` directly (Render, Deploy, Validate's console image pin) keep `bots.push_main` and its own
`make check-repo check-apps`. Validate's app-only fast path is unchanged; it was never the gate's.

**What stays.** `console/merge.py` becomes `console/changes.py` (654 → 318 lines): the table of console change kinds
(`Action`), the branch grammar (`parse`, `branch`, `target`), each kind's `owns`, `keeps`, `commands` and `title`,
`Connections` (what the New app and Connect forms choose, and what a new app's flags are checked against) and
`encrypted_reason` — a changed `*.sops.yaml` needs its values set, which a new app's own generated seed does not. That
rule is a statement to a person now, not a hold. `Action` loses `gated`, `again`, `kind`, `read` and `rule`; `judge` and
the two `manual_*` stand-ins go with the gate, whose ownership verdict could not fire because the file list was already
filtered by `owns`.

**What goes.** The merge engine (`eligible`, `validated`, `fetch_head`, `merge`, `merge_records`, `refuse`, `main`,
`Hold`, `Skip`, its constants), the v1 leftovers no caller reached (`recipients`, `new_app_records`, `check_events_key`,
`settings_of`, `checked_host`, `team_commands`, `channel_commands`, `SCALE_FIELDS`), the `auto-merge.yml` workflow, the
`console-merge` command, the console's label and comment plumbing (`AUTO_MERGE_LABEL`, `refusal()`,
`PullRequest.labels`/`refused`/`auto`), the "merges itself" and "Hold for manual review" wording, and
`promotion.merge_mode`'s label branches. The pull request body keeps one optional line, **Merge plan:** it <reason>.,
present only when a person must do something beyond merging it.

**Contracts.**
- No console pull request carries `auto-merge`, and no comment starting `Auto-merge refused:` is written or read.
- Activity's Merge stage reads one thing from an open pull request: it waits for you, with the reason from the body when
  there is one. The **Validate** and **Checks** facts are unchanged.
- A merge made by a person's token starts Validate on `main`, and the Render workflow renders any records it merged
  untouched. `main` stays unprotected and directly pushable, which the bots need.
- The retained rule reaches three places from one value: the pull request body, the job log and the Activity row
  (`proposal.merge_plan`).

**Verification.** `make check` (Python tests updated: no label call, no `auto`/`refused` field, the plan line read from
the body), `make check-repo` (documentation links and anchors follow the renamed section), `make test` and
`make check-docs`. Live: the deployed console opens a change on `swhurl-try-30/staging` that carries no label and says it
waits for a person; the operator merges it on GitHub, Render renders it, and the instance returns Healthy.

**Not in this slice.** Auto-merge of any kind — deferred, and if it returns it should be GitHub's required checks and
native auto-merge rather than a bot that pushes merge commits; the promotion review page's unreachable v1 branches; and
the console's remaining Python, which leaves with the server and templates (`changes.py` included).

## 21. The Apps inventory and connections in Go (approved 8 November 2026)

**Decision.** The console stops deriving an instance from the cluster. One pinned `swhurl-v2 status --all`
returns, beside each instance's unchanged version-5 report, the **declaration** the records state, and one
entry for a live unit or release that no record names. The console keeps only presentation: it draws rows,
the map and the catalogue from Go's document, and reads none of its facts from `kubectl`.

Options were put to the operator and decided: the block rides on the existing `status --all` call rather than a
second command (one process, one pinned bundle, one compile); a connection row states what the records say and
loses the *connected in Git, not yet observed live* and *removed in Git, still observed live* wording; and a live
unit or release with no record is still reported, as `removing`, so an instance stays visible until its namespace
goes.

**Why it is a projection, not a new authority.** Every fact is one the compiler already has: the host from
`routeHost`, the addresses from the names the compiler writes, the database path from `DatabasePath`, the team
from the app record, the artifact from the release, the connected pairs and refusals from the committed resolution
(`v2-calls`), and the port and grants from the committed allocation (`v2-egress`). Nothing is re-derived by a
second reader, in either language.

```text
records + profile + calls.json + allocation.json ─┐
                                                  ├→ status --all → v2-observations v2
Kubernetes (units, releases, pods, routes) ──────┘     ├ instances[]: report (v5) + inventory
                                                       └ removing[]: {application, environment, reason}
                                     console: rows, map, catalogue, egress — presentation only
```

**Contract.** `status --all --json` prints `{"kind":"v2-observations","version":2,…}`. Each `instances[]` entry
keeps `report` exactly as version 5 defines it and adds `inventory`:

- `team`, `kind`, `exposure`, `port`; `hosts` (`[]` for a private instance or a worker); `address` (the Service
  address the app's own env carries, `""` for a worker); `api` (its API document's address, `""` when it
  publishes none); `artifact {image, digest}`; `database {kind, path}` (`kind` empty when it keeps none);
  `offers` (the source commit its documents are read at, `""` when it offers none);
  `egress {port, grants}` (`port` 0 when the allocation names it none; `grants` is the allocation's list,
  so a database's backup host appears without the record naming it).
- `calls`, `allows`, `callers`, `publishes`, `subscriptions`: the declaration and the resolution's answer.
- `connections`: one row each — `{role, app, event, outcome, reason}` — `role` is `calls`, `called-by`,
  `publishes`, `subscribes`, `subscriber` or `reaches`; `outcome` is `connected`, `waiting`, `refused` or empty
  for a row that is only a declaration (`publishes`, `reaches`); `reason` is a fixed code the console renders as
  words (`callee_absent`, `no_consent`, `self_call`, `callee_not_web`, `publisher_not_ready`). A team is a grant,
  not a row, and a peer's team is the peer's own row.
- `removing`: `[{application, environment, reason}]`, sorted, from the units and releases the observation
  already lists that no record names. `reason` is `unit_and_release`, `unit` or `release`.
- Single status, `make app-status`'s text, `--changes` and every promotion decision are unchanged: only the bulk
  JSON changes version, and its only consumer is the console, which is updated in the same change.

**Deliberate changes.** A connection row no longer reports what the *running* release still does: currentness and
the page's revision line already say a change is unapplied, once, instead of on every row that differs. The Apps
list compares staging with production by digest — v2's release identity — because v2 renders no image tag.

**Deletions.** `console/records.py`; `console/cluster.py`'s read path (`apps`, `ingress_hosts`, its four
`kubectl` lists, the row assembly); the app page's own `instance.json`/`release.json` reads; the console tests
that pinned the old derivation, whose cases become Go table tests. `apps/calling|egress|events|helmrelease|ops|
connections` and `interface` stay while the bounded v1 oracle and the live tests import them; the console importers
leave with this change and `test_v1_model_bound` shrank by them.

**Verification.** Go table tests for every field and every row (connected, each waiting and refused reason,
declaration-only rows, private and worker instances, a team grant, a subscription waiting on its publisher, a
database's backup grant, a record with no release, empty records, and `removing` from a unit, a release and both);
`make check-v2`, `make check`, `make test`, `make check-docs`. Live: every instance's rows compared field by
field with the pages before the change; a deliberately pending call created on a throwaway and removed; the
`removing` row seen by removing one throwaway instance; cold and warm Apps-list latency measured and reported
rather than assumed; signed-out redirects and signed-in pages checked.

**Not in this slice.** Document parsing and the catalogue's offers (`catalogue.Documents`, `apps/offers.py`), the
map's layout, the team registry and its channels, the compiled variables table the app page prints, the Platform
page's unit list, and every write path.

## 22. Feature flags in Go: the overrides adapter and the console's card (approved 8 November 2026)

**Decision.** The console's **Feature flags** card stops being Python. The one dependency v2 lacks is NATS's
key-value store, so this slice adds `adapters/nats` — an external-effect adapter, proposed here — together with
the pure flags model, the use case, the command and the console's switch to it. The operator agreed the split
on 8 November 2026: the flags work lands first, so the console server slice contains no new external effect
and a wrong write to a live app's flag cannot hide inside a template port. The operator approved this contract,
the adapter's external effect included, on 8 November 2026.

**Meaning.** The card's own words, unchanged, with the states now stated once in Go:

- A **declaration** is the app's `offers/flags.json` beside its record: `{name: {type, description, default,
  staging?, prod?, min?, max?, enum?}}`. No file means the image declares no flags, which is not a problem; a
  file that cannot be read is a `problem` the card shows, and nothing is set from it.
- The **declared value** in an environment is the `staging` or `prod` key when the declaration has one, else
  `default`. An **override** is one key-value entry in bucket `flags-<env>` under `<env>.<app>.<flag>`, its value
  as JSON. A tombstone is not an override. A stored value that is not JSON is an override with no usable value,
  which the app ignores too. **Effective** is the override when there is one, else the declared value.
- An **undeclared override** is listed after the declared flags and can only be cleared. An override **age** is
  whole days since NATS stored it, and **stale** at 14 days or more: a mark on the card, never a refusal.
- **Set** writes a declared flag only, and only a value the declaration admits: `true`/`false` for a boolean,
  a whole number inside 32 bits and within `min`/`max`, a finite number within `min`/`max`, a string inside
  `enum` when it has one. **Clear** takes one flag name (kebab case, at most 40 characters) and needs no
  declaration. Both are idempotent, so re-running one after a lost reply is safe, and neither is retried by v2.
- The bucket, the stream and the subject are the kernel's names already (`kernel/users.go` states them for the
  app users and the console user); the adapter receives subjects and re-derives nothing.

**Boundaries.**

- `kernel/flags.go` (layer 0): decode and validate `flags.json`; a flag's declared value for an environment;
  whether a stored value fits its declaration; the form's text as the declared type; and the report's rows from
  a declaration, its overrides and an explicit time. Exported names move out of `users.go` unchanged in meaning
  (`FlagsBucket`, `FlagsStream`, `FlagSubject`, `ConsoleInbox`) so that no reader restates them.
- `kernel/nkey.go` gains the decode beside the encode it already has — `DecodeUserSeed(text) ([32]byte, error)`
  and `Sign(seed [32]byte, data []byte) []byte` — because the base32 alphabet, the prefixes and the CRC-16 the
  seed is written with are the kernel's. Nothing else of the seed is used; it is never printed, recorded or put
  in an error.
- `adapters/nats` (layer 1) is the protocol, and only the protocol: `Open(ctx, url, seed, inbox) (KV, error)`,
  then `KV.Keys(ctx, stream, prefix)`, `KV.Get(ctx, stream, key)` and `KV.Put(ctx, subject, value, headers)`, and
  `Close()`. The inbox is the caller's, because the user's permissions are what name it, and `Keys` is the
  listing named for what it returns rather than for the API's verb. The standard library covers it
  (`crypto/ed25519`, `encoding/base32`, `net`), so `stdlib-only` is untouched. One connection per `Open`, no
  pool, the same 5 s timeout: the console opens one per request, as it does today. Sign-in signs the server's
  nonce with the seed; `no_responders`, headers and protocol 1 are asked for as now, and a `503` answer means
  nothing listens on the subject (the bucket is missing).
- `orchestration/flags.go` (layer 2): records → declaration → overrides → rows, and the same decision once more
  for one write before anything is published. It prints nothing, chooses no exit status, and takes the time and
  the store as arguments.
- `cmd/swhurl-v2/flags.go` (layer 3): `swhurl-v2 flags --records DIR --app APP --env ENV [--set FLAG=TEXT |
  --clear FLAG] [--json]`. `NATS_URL` (default the in-cluster address) and `NATS_SEED` come from the
  environment, as the console's own adapter reads them; there is no kubeconfig and no `--in-cluster`, because
  NATS is not Kubernetes. `--set` takes the form's text, not a typed value, so typing and its refusals have one
  home; it splits at the first `=`.

**Contract.**

```text
swhurl-v2 flags --records v2-records --app demo-shop --env prod            # the card's rows, as text
swhurl-v2 flags … --set shout=true                                        # write, then print the rows
swhurl-v2 flags … --clear old-banner  /  … --json
{"kind":"v2-flags","version":1,"instance":{"application","environment"},
 "writable":true,"problem":"","rows":[…]}
```

- A row: `name`, `declaration` (`type`, `description`, `default`, `value` for this environment, `min`, `max`,
  `enum`, each omitted when the declaration has none), `undeclared`, `overridden`, `override` (omitted when
  none), `storedAt` (RFC 3339 or empty), `ageDays` (a number, or absent when the time could not be read),
  `stale`, `effective`. Declared flags come first, each sorted by name, then undeclared overrides sorted by name.
  `writable` is true exactly when a seed is configured — so the card disables **Set** and **Clear** for the same
  reason it does today — and does not depend on whether a read succeeded.
- `problem` is empty when the declaration and the overrides were both read. A declaration that cannot be read
  sets it and still lists every override, clearable; NATS unreachable sets it and still lists the declaration.
- Exit 0 whenever a report is printed, whatever the state; 2 for a refusal — no such record, an environment
  that is not `staging` or `prod`, a flag name, type or key the declaration refuses, an undeclared flag, a
  value outside its declaration, a write with no seed, or NATS refusing — with v2's own words on stderr, which
  the console shows unchanged.
- `--set` and `--clear` print the report they read back after the write, so the card states what is stored and
  not what was asked for.
- **Words preserved:** a refusal keeps today's wording (`choose true or false`, `give a whole number`,
  `give a number`, `must be one of …`, `outside min and max`, `does not declare …`), and a transport failure
  keeps `cannot reach NATS at …`, `NATS did not answer in time` and `nothing answers … (is the bucket there?)`.
  The audit line is the console's and is unchanged: `[AUDIT] <who> flag <app>/<env> <flag> = <json>` or `…
  cleared`.

**Deliberate changes.**

- The card reads its declaration from the pinned input bundle's `v2-records/<app>/<env>/offers/flags.json` —
  the record's own copy, which the render copies byte for byte — instead of from GitHub at the cluster's offers
  annotation. One page can no longer show a declaration from a different commit than its own rows; the bundle's
  filter gains the offers documents' fixed names.
- A malformed `flags.json` no longer empties the card: the problem is shown and the overrides are still listed
  and clearable, which is what an operator needs to remove an override of a flag the image stopped declaring.
- The browser's text is typed and checked in Go, so the route no longer carries a second copy of the rules.

**Deletions.** `tools/swhurl/console/flagstore.py` (237 lines) and `tests/test_flagstore.py`, whose fake NATS
server becomes `adapters/nats`'s raw fixtures and scripted-server contract test; `flagstore` from the
console's context, server and app-page route and their fakes; `apps/nkeys.py`'s console use (the module stays
while the live tests and `model/write.py` still sign in with it, and leaves with them).

**Verification.** Go: table tests for every row state — declared with and without an override, an undeclared
override, a tombstone, a stored value that is not JSON, an unreadable stored time, both environment keys, a
stale boundary at 13 and 14 days, no document, a malformed document, no seed, NATS unreachable — and for every
refusal; the adapter's `Contract` suite, run against the real adapter on a scripted in-process server and
against its fake, covering INFO with a nonce, the CONNECT fields, PING/PONG, `MSG` and `HMSG` replies, a
status line, `-ERR`, a closed connection and a timeout. `make check-v2` with `Layers` and `api.txt` updated in
the same change (say why in the change). `make check` for the console, whose flag tests lose their Python
store and assert their words through a fake command. Live, on a throwaway instance: `flags` lists the rows;
a set reaches the running app without a restart; a clear returns the declared value; an undeclared flag and a
value outside its declaration are refused with those words; the same two writes go through the deployed
console's card in an in-cluster request with the forward-auth header, and the audit lines appear in the pod's
log. Signed-in browser and promotion of a changed declaration stay unexercised until they are.

**Not in this slice.** A record that owns a flag's *state* (the deferred row stays), retention of overrides,
flags in a rendered variable, the console's server and its 18 templates, and `apps/nkeys.py`'s removal.

## 23. The console server in Go: the pages, the jobs and the console's last Python (proposed 9 November 2026)

**Decision.** The console stops being Starlette and Python. One command, `swhurl-v2 console`, serves the same 12
pages, the three redirects, the eight POSTs and the job stream, with the same markup, CSS, JS, URLs, identity rule
and GitHub tokens; the 18 Jinja templates are rewritten as `html/template` in which no page logic remains. The
operator decided on 9 November 2026 to keep **New app** in the console: rendering a new app's repository stays
Copier's, one pinned subprocess, and repository creation joins the GitHub write port — so `apps/repo.py`,
`apps/hooks.py`, `proposal.py`, `repos.py` and at the end the whole of `console/` leave Python. Four steps, each its
own gate, and the first adds no external effect.

**What is ported, and what is not decided again.** Every console change already runs v2's own code — `create`,
`edit`, `remove`, `promote`, `team` and `render` in the pinned tree — and the facts every page shows are already v2
documents: §21's inventory, §22's flags, `verify`'s checks, the offers and the team registry. This slice ports
**orchestration and presentation**: the pinned bundle, the publish path, the job lifecycle, the page data and the
templates. It adds no second model, no second path catalogue and no second judge.

**Measured 9 November 2026, read-only.** 22 routes — 13 GET (12 pages and `/healthz`), 3 redirects (`/jobs`,
`/services`, `/units`), 8 POST, `/jobs/{id}/events` (server-sent events) and `/static/*`. 18 Jinja templates,
1,337 lines; `console/` is 26 modules, 3,788 lines, with 3,777 lines of tests pinning it. The console still reaches
56 modules and 8,364 lines: those 26, and 21 under `apps/` (2,612). The image runs four external binaries:
`kubectl`, `swhurl-v2`, `copier`/`uvx` and, for local development only, `git`.

**Where it lives.**

- **Layer 0, `pages`**: each page's data — rows, labels, states, reasons, ages, short hashes, URL fields and the
  sorted slices the templates iterate — computed from `kernel` and `observe` values by functions that print
  nothing. No field is `template.HTML` or `template.HTMLAttr`: markup, `hx-*` attributes and escaping stay the
  templates' job, so the data cannot slip the escaper. The two places the Python console injects markup today
  (`follow()`'s `hx-*` attributes and one JSON attribute) become template helpers beside the templates that use
  them, covered by the byte-comparison tests.
- **Layer 1**: `adapters/kube` gains its write port (patch or annotate a Kustomization or GitRepository, merge-patch
  the job record); `adapters/github` gains the console's reads (a tarball at a commit, a comparison of two commits,
  open pull requests) and a write port (blobs → tree → commit → ref → pull request; create a repository, its first
  commit and its image webhook); a new `adapters/copier` runs one pinned `copier copy` and nothing else;
  `adapters/registry` and `adapters/nats` are reused unchanged.
- **Layer 2, `orchestration`**: the console's use cases — a pinned bundle, a change and its publication, a job's
  lifecycle, a page's inputs, `app-repo`.
- **Layer 3, `cmd/swhurl-v2`**: handlers, the template set and static assets (`//go:embed`), and the flags. `places`
  keeps its rule — layer 2 is `orchestration`, and a server is the command that presents its use cases — so the
  port needs no new layer and no exception.

**Contract: the server.**

- `swhurl-v2 console --port 8080 (--in-cluster | --kubeconfig FILE) [--dev]`. Exactly one credential, as `status`
  states it; `--dev` refuses every address but loopback and fixes the identity to `operator@localhost (dev)`.
- Every route, method, status code and redirect target is today's. A trailing slash answers 308 to the canonical
  path — stated rather than inherited from a framework.
- `/healthz` answers `ok` and is the only route without an identity. Identity is `X-Auth-Request-Email`, verbatim;
  without it a plain-text 401. Any method but GET or HEAD needs an `Origin` whose host equals the request's `Host`
  (`:authority` under HTTP/2, never `X-Forwarded-Host`), else 403. No cookies, no roles.
- Static assets keep their content-hashed URLs and immutable caching; CSS, JS and HTMX are moved, not rewritten.
- The four self-refreshing pages keep refreshing; a job page left open re-reads its job.
- Failures keep the Python console's meanings: 502 and the error page for a failed read, 404 for no such app or
  unit, 400 for a refused form, 409 for a conflict, 401 for no identity — never a fallback to an older `main` and
  never a fallback to Python.

**Contract: pages, one commit per request.**

- A request resolves `main` once, downloads the tarball at that SHA, filters it to what a page may show and caches
  the bytes by SHA (60 entries, first in first out); extraction is into a request-owned directory removed on exit.
  A failed read is never absence and never a newer commit.
- The filter gains the offers documents and the team registry, so a page cannot mix a declaration from one commit
  with rows from another — §22 made that true of the flag card alone.
- A read happens once per distinct input per request, with at most four in flight.
- Templates hold no page logic: no filter pipeline, no macro reading another value, no lookup that decides a word.
  Every collection the Jinja filters build (sorted teams, `dictsort`, `selectattr`, `sum(start=[])`, slices,
  `split()[1]`) is a Go field, and every label, state word, age, refusal and short hash is computed in `pages`.
- Acceptance is **byte-identical HTML** per page and per state against today's Python render, with any difference
  named in this contract. The escaping, the attribute quoting and the entity choices are the test's subject.

**Contract: reads are calls.** `status --all`, `verify --scope cluster`, `flags`, the offers documents, the team
registry, the compiled variables and the pull-request evidence are called as functions in the request's process;
what they also print stays for their other consumers (`make app-status`, the Deploy workflow, `check-docs`), so
nothing outside the console changes. This is where the Python console's missing deadlines go: `status` and `verify`
run as subprocesses with no timeout today, and afterwards every read carries the request's deadline and every job's
work carries the job's.

**Contract: a change and its publication.** Seven steps, today's, in one process: resolve the pinned commit → apply
the change by calling the use case on the extracted tree (`create`, `edit`, `remove`, `promote`, `team`, `render`) →
snapshot path, mode and SHA-256 → keep only the files the kind owns → upload blobs → create tree, commit and the
`console/<action>-<name>[-<env>]-<sha>` ref → open the pull request with the same body, author and **Merge plan**
line. The kinds and their owned paths are one table, moved from `changes.ACTIONS` with its targets, environments,
wording and refusals. Extraction preserves file modes; a mode lost makes every script look changed, so a contract
test compares the owned set for a tree holding an executable. A channel address still reaches v2 on standard input
only.

**Contract: jobs.** One running job per target; states `running`, `succeeded`, `failed`, `interrupted`; a job's lines
are its record. Records stay in the ConfigMap `console/console-jobs`, one key per job, written as the job runs and
as it ends, keeping the newest 30, each trimmed from the front to 20 kB, with the same merge patch and the same null
deletes so two pods cannot drop each other's keys; every line redacted and capped at 1,000 characters.
`/jobs/{id}/events?after=N` keeps its shape: `id` is the line number, a `done` event carries state, finish time and
link, a keep-alive every 15 seconds, `Last-Event-ID` resumes, and another pod's job is 404. On SIGTERM the server
takes no new job and waits up to 840 seconds inside the pod's 900; a job cut short is marked interrupted with its
audit line, and **Finish creating this app** resumes it from the record.

**Contract: Flux actions.** Reconcile, suspend and resume are the kube write port's: fetch, then patch or annotate,
then poll for up to 600 seconds, refusing the root units (`cluster-sources`, `cluster-stack`) in the same words. A
browser request gets the `started` fragment; a plain form a 303 to the job.

**Contract: New app (the operator's decision).** `swhurl-v2 app-repo` is one command and one implementation: read
the stack's `copier.yml` for its questions, refuse a name that exists or answers its type does not accept, run one
pinned `copier copy --vcs-ref <v2/contract/stacks.json>` in a Go-owned scratch directory under a deadline (its
template clone is anonymous, so it holds no token and no credential reaches its environment or argv), create the
public repository with its first commit and image webhook through the GitHub write port, wait for the app's own first
image through the registry adapter (600 seconds; run 1 or later, because the template's `0-<sha>` seed is a copied
build whose label states the template's commit, so it is not a release v2 creates from), then the ordinary publication
for `create`, each `edit` and `render`. `make app-repo` becomes bash over it and the console's New app calls the same
use case.
`copier` is the fourth approved subprocess, after SOPS, Helm and Git, so the `no-subprocess` allowlist, its table
row and `archtest` change with it.

**Contract: secrets.** One redactor covers the two GitHub tokens, the kubeconfig, a channel address and every job
line; a refusal, an error page and a log line all pass through it. Secrets are bytes in a header, or on a child's
standard input, never in argv.

**Deliberate changes.**

- Reads are calls, not subprocesses: the console image no longer needs `swhurl-v2`, `kubectl` or `helm`. It keeps a
  Python runtime, a pinned `copier` and `git`, because Copier is the one foreign renderer a new app needs.
- The image copies neither `tools/` nor the platform's Python, and the image's content tag and the
  `verify-platform` console check follow it.
- Three caches gain bounds (stacks and questions at five minutes, offer documents per commit), where today they
  grow without eviction.
- No page data carries trusted HTML; the two places the Python console injects markup are template helpers.
- `docs/console.md`'s token table says `/new` creates nothing while `new_app.py` creates the repository: the stale
  sentence is deleted and the section says what is.

**Deletions.** `tools/swhurl/console/` (26 modules, 4,480 lines) and its nine test files (3,777 lines, whose cases
become Go table tests); `console/templates/` and `console/static/` move into `v2/`; `apps/repo.py`, `apps/hooks.py`
and the console's last importers of `apps/*`, which is what lets the bounded v1 model shrink again;
`images/console/Dockerfile`'s tooling, venv and `tools/` layers; the `console` and `console-dev` rows in
`commands.py` become bash over the new command.

**Delivery, step by step.** Each step is validated (`make check`, `make check-v2`, `make check-docs`, Go tests) and
each has its own gate. They land in this order because the first three change nothing the operator sees:

1. **The server serves reads** — handlers, `pages`, the 18 templates, static assets, authentication, `/healthz` and
   `--dev`. No new external effect: it writes nothing. Proof: table tests per page and per state, the byte
   comparison against the Python render, and a live side-by-side — the Go server on your kubeconfig beside a
   port-forward of the deployed console, every page compared by hand. Nothing deployed.
2. **The publish path and the Flux actions** (kube write, GitHub write). Landed as `swhurl-v2 change --kind …`,
   called through `console/v2.py` exactly as `edit` and `promote` are today, so the deployed console opens real pull
   requests through Go while Python still serves the pages; `proposal.py`, `repos.py`'s publication and
   `changes.py`'s GitHub parts are deleted. Two landings: the publish path first ([§24](#24-the-publish-path-in-go-step-2a-proposed-9-november-2026)),
   its gate a real pull request; then `swhurl-v2 unit` with the job lifecycle below, so publication is proven live
   before jobs touch it ([§25](#25-the-flux-unit-actions-in-go-step-2b-proposed-9-november-2026); the command is `unit`,
   because the guardrail forbids a non-adapter naming the `flux` command). Both commands of steps 2 and 3 read their tokens from the environment,
   as `flags` reads `NATS_SEED`, and print their lines with the pull request URL last, so the console streams and
   records what it streams today.
3. **New app** — `swhurl-v2 app-repo` and `adapters/copier`, again called through `console/v2.py`, so
   `apps/repo.py` and `apps/hooks.py` are deleted and a live creation from the deployed console proves Copier, the
   repository, the first image and the pull request.
4. **Cutover** — the image's command becomes `swhurl-v2 console`; the remaining `console/` Python and its tests are
   deleted; `make console-dev` becomes bash; the docs are rewritten. Proof: signed-out and signed-in requests to
   every page, one throwaway app created from the console, a promotion refused while its own change is unapplied, a
   job surviving a pod replacement, and the operator's browser pass.

**Verification.** Go table tests for `pages` (every state, team, exposure, connection outcome and an empty record
tree), for the filter's path set, for the mode-preserving snapshot and for the owned paths of every change kind, and
for job states including a foreign pod's job and an interrupted one; contract suites for `adapters/github`'s write
port (a scripted server: blob, tree, commit, ref, pull, a refusal, and a token that must not follow a redirect to
codeload), for `adapters/kube`'s write port (patch, annotate, merge patch, null delete, a refused root unit) and for
`adapters/copier` (a local template fixture, no network; the real adapter's suite runs where Copier is installed, as
the chart tests run where Helm is); `make check-v2` with `Layers`, `api.txt` and the subprocess allowlist updated in
the same change, saying why; `make check`, `make test`, `make check-docs`; the byte comparison of every page; cold
and warm page timings against the Python console, reported as numbers rather than a presumed speedup; and the live
checks each step names.

**Not in this slice.** Any redesign: the markup, the map's layout, the Activity page's history and the catalogue's
presentation stay as they are. Also unchanged: what a promotion means, the rule that a person merges every pull
request, the tokens and their scopes, `platform/console`'s manifests and RBAC, and the deferred features list.

## 24. The publish path in Go: step 2a (proposed 9 November 2026)

**Decision.** `swhurl-v2 change` makes a console change and opens its pull request: §23's seven steps in one process,
with the kinds and their owned paths moved out of `changes.ACTIONS`. The deployed console keeps its pages, its forms and
its jobs; it stops downloading `main`, snapshotting, uploading and publishing — `console/proposal.py` becomes the runner
that streams the command's lines and records the URL it prints last. This is the first step of the console port that
writes anything, so it lands alone and is proven live before the job lifecycle touches it (§23 step 2b).

**Semantics: one table of kinds.** A kind states the environments its branch may name, the instance it changes when that
is not the branch's (a promotion changes production), the input paths its pull request may carry, and its title. The
kind names keep their `v2-` prefix: a branch name is permanent, and Activity reads the branches the console opened before
this slice as well as after. The values are the console form's, passed as v2's own words wherever v2 already decides
them — a value `edit` can carry is passed as one:

| Kind | Target | Values | What v2 runs |
| --- | --- | --- | --- |
| `v2-scale` | app, env | `--set replicas=3`, `--set memoryMiB=256` | `edit`, `render` |
| `v2-expose` | app, env | `--set exposure=private` | `edit`, `render` |
| `v2-team` | app, env (branch only) | `--set team=payments` | `edit` naming no environment, `render` |
| `v2-remove` | app, env | — | `remove`, `render` |
| `v2-promote` | app, env `staging` | — | `promote --expect <the pinned staging record's revision>`, `render` |
| `v2-connect` | app, env | `--call b`, `--subscribe b.orders` | `edit` with the record's lists plus the choice, `render` |
| `v2-disconnect` | app, env | `--drop-call b`, `--drop-subscription b.orders` | `edit` with them less the choice, `render` |
| `v2-allow` | app, env | `--allow b`, `--disallow team:payments` | `edit` with `allows` so merged, `render` |
| `team-add`, `team-edit`, `team-remove` | team | `--description`, `--rename` | `team` |
| `team-channel-set`, `team-channel-remove` | team | `--transport`, the address on standard input | `team` |

A kind that **merges a list** states the choice and v2 merges it, because the base it merges into is the pinned record
(the three connection kinds and `v2-allow`); every other kind's value is already one word v2 accepts. A merge that
changes nothing is a refusal in v2's words, as `edit` refuses one today. An addition is what the form ticked and the
record does not state, a removal what it left out and the record does state, so a list the record holds but the cluster
does not (a change merged and not yet applied) survives the next one; and only the lists a change actually alters are
written, because `edit` refuses a setting that sets what is already there.

**The command.**

    swhurl-v2 change --kind KIND (--app APPLICATION --env ENVIRONMENT | --team TEAM) --requested-by IDENTITY \
        [--repository OWNER/REPO] [--revision COMMIT] [--dry-run]

It reads `CONSOLE_TOKEN` and, unless `--repository` names one, `CONSOLE_REPO` from the environment as `flags` reads
`NATS_SEED`; neither ever appears in an argument. The platform's own layout is not a flag: the records are
`v2-records`, the profile is `v2/testdata/profile.json` and the change's output is the extracted tree, stated once
beside the kind that writes them. Exit is 0 when the pull request is open, and 2 with v2's refusal on standard error:
2 is what every other v2 command answers a refusal with, and the console reads the exit status only for "it refused". A
kind and its flags are one table (`changeShapes`): a change that states a flag its kind does not take, or omits one it
needs, is refused in the change's own words before a byte is downloaded, rather than reaching a command that would
answer with its whole usage text.

**Contract: the pinned tree.** The command resolves the base branch once (`--revision` skips that read), downloads the
tarball at that SHA and extracts it into a scratch directory it creates and removes on exit. Extraction preserves
modes, because a mode lost makes every script look changed, so a contract test takes a tree holding an executable
through the whole path. Nothing outside that directory changes except GitHub: `git`, the operator's checkout and the
console's own files are never touched. `--dry-run` resolves, extracts, applies and prints the branch and the owned
paths, and opens nothing.

**Contract: the GitHub write port** (`adapters/github`). Its read side is the existing `Console` port's `Revision` and
`Tarball`; the write side adds five calls, each taking the repository and the token explicitly and retaining neither:
`Blob` (one file's bytes as base64), `Tree` (a base tree and the changed entries; an entry with no SHA deletes its
path), `Commit` (message, tree, one parent, the console's author), `Branch` (creates `refs/heads/<branch>`, and refuses
any branch that is not `console/<kind>-<name>[-<env>]-<commit>`: the console writes no other ref) and `Pull` (title,
head, base, body, returning its URL). Step 3 adds `Repository` and `Hook` to the same port; this slice names neither. A
token is never in a URL, never formatted into a refusal, and never follows a redirect: the tarball's redirect to
codeload is read without it. A refusal names the operation and GitHub's own message, so the console shows what GitHub
said.

**Contract: what the console says.** The command prints the lines the console records today, as it goes:
`Downloading <repo> main (<sha7>)`, one `A`/`M`/`D <path>` per carried file, `Merge plan: you merge it on GitHub[; it …]`,
`Committed <sha7> [console] <title>`, `Creating branch <branch>`, and `Opened <url>` last, so the console keeps its
record and takes the job's link from the last line; under `--dry-run` it prints today's
`Dry run: would commit N file(s) to <branch>; no PR opened` instead of the last three. A failed apply makes no GitHub
write. The pull request's title, body, author (`swhurl console <console@users.noreply.github.com>`),
`Requested-by: <identity>` trailer and `**Merge plan:** it ….` line are today's character for character — including that
the body's command line shows `--revision @base` rather than the commit it resolved to, because that is the command a
person can type.

**Boundaries.** `kernel` holds the kind table and the set of platform files a change may carry, because they state what a
change is, beside `edit`, `remove`, `creation` and `promotion`; the names come from the folders `kernel` already
renders into (`usersFolder`, `reloaderFolder`, `authFolder` and the egress allocation), not from a second list. `pages`
builds Activity's branch patterns from the same table instead of its own copy. `orchestration` holds the use case
(resolve, extract, apply by calling the existing use cases, filter, publish) and prints nothing;
`adapters/github` holds the six calls; `cmd/swhurl-v2` holds the flags. The console keeps its forms, its job and its
`/jobs` pages — what it loses is `snapshot`, `download`, `publish`, `run_tool`, `open_action`'s seven steps and
`changes.py`'s command builders.

**Deliberate changes.**

1. **The merge base is the pinned record, not the cluster.** The console reads the live instance to render the form and
   to refuse "that changes nothing" with a 400; v2 merges into the record at the commit it applies to. Today a merged
   but unreconciled change lets a second connection change silently drop the first, because the console builds the whole
   list from what the cluster shows. The console's pre-checks stay until the server leaves (step 4) — they decide 400
   against a job, not what is written.
2. `keeps` is dropped: no kind uses anything but `AMD`, and an unused field in a table is a rule nobody applies.
3. The dead `python` tool of `changes.Command` goes with it: every kind runs v2's binary, so the console executes no code
   from the tree it downloads.
4. The kind table moves from Python to `pages` (which the command and the pages both already depend on, and which the
architecture check keeps at layer 0 beside `kernel`); `changes.py`'s `branchActions` becomes a reading of it, not a
second copy.
5. **A connection change writes only the lists it alters.** Today the console writes both lists from the form, so a
change that alters one of them is refused with `already_set` by the edit of the other — connecting an event without
changing the calls has never worked. The delta above is what makes the omission possible.

**Not in this slice.** The Flux actions and their polling (step 2b, [§25](#25-the-flux-unit-actions-in-go-step-2b-proposed-9-november-2026)), the job
lifecycle's live reader (also 2b, which ports it and leaves it to the cutover), `app-repo`
and repository creation (step 3, which the write port's `Repository` and `Hook` are named for but does not use yet), the
cutover (step 4), and any change to what a promotion means, to the rule that a person merges every pull request, or to
the tokens and their scopes.

**Verification.** `kernel` table tests for every kind's environments, target, owned paths (its own records, its
instances, a rendered platform file, another app's files, `apps/`) and branch round trip, including the branch names the
console already opened; `orchestration` tests with a fake GitHub for the seven steps, the mode-preserving snapshot, a
deletion entry, `--dry-run` opening nothing, a refused apply making no write, and the body, merge plan and commit
message, and a scale end to end against a tarball of one instance's records and the real disk; `adapters/github`'s
contract suite against a scripted server for blob → tree → commit → branch → pull, a
refusal, a foreign branch refused, and a token that does not follow the redirect to codeload; the console's Python
publication cases become relay cases and `tests/test_console.py` keeps asserting what the job shows and links to;
`make check-v2` with `Layers` and `api.txt` updated in the same change (no new subprocess), `make check`, `make test`,
`make check-repo`, `make check-docs`. Live: a scale on a throwaway app from the deployed console, its pull request's
body, author and merge plan compared field by field with one the console opened before this slice, and a promotion
refused while its own change is unapplied ([current state](current-state.md)).

## 25. The Flux unit actions in Go: step 2b (proposed 9 November 2026)

**Decision.** `swhurl-v2 unit --action reconcile|suspend|resume --unit NAME` makes one of the console's three cluster
changes: it reads the Kustomization, refuses the two root units and a reconcile of a suspended one, patches or annotates
through the kube write port, and waits until Flux reports the request handled. `console/actions.py`'s `Flux` class and its
`kubectl get/annotate/patch` are deleted and the three POSTs relay the command, as §24 made `proposal.py` relay
`swhurl-v2 change`. The job lifecycle is ported beside it and **not yet wired**: a Go process runs no job until the
cutover, so its records, drain, resume and stream arrive with step 4, where they have a live reader (§below).

**Semantics: one action, three changes.** An action names a unit, which is a Kustomization in `flux-system`, and one of:

- **reconcile** reads the unit first, because both refusals are facts of the unit rather than of the request: a
  suspended unit is refused in the console's words (`<unit> is suspended; resume it instead (resume also reconciles)`)
  and a unit whose `sourceRef` names no source this action can request in its (`<unit> has an unsupported source kind
  'None'`); then it requests the source and, once that is handled, the unit, because the unit applies what the source
  fetched.
- **suspend** sets `spec.suspend: true` and stops there: Flux stops applying Git, nothing is reconciled, and the job is
  done when the patch is accepted.
- **resume** sets `spec.suspend: false` and then requests the unit: a resume that did not reconcile would leave the
  cluster behind the Git the suspension missed.

A source is the object the unit's `sourceRef` names — `GitRepository`, `OCIRepository` or `Bucket` — in the unit's own
namespace, which is `flux-system`, unless the reference names one.

**Semantics: what "handled" means.** A request is the `reconcile.fluxcd.io/requestedAt` annotation set to the action's
own clock in UTC, and it is handled when the object's `status.lastHandledReconcileAt` equals that token **and**
`status.observedGeneration` equals `metadata.generation`: the second clause is what keeps "handled" from meaning an
earlier request that happened to leave the same token, or a request made before a spec change Flux has not seen. Handled
and `Ready=True` is success, reported from the object's own status (`✔ applied revision …` for a unit, `✔ fetched
revision …` for a source). Handled and `Ready=False` is a failure carrying the controller's message, because that is the
sentence that says what is wrong. Not handled within 10 minutes fails with `timed out after 10m waiting for …; it may
still finish (check the unit page)`, polling every 2 seconds, as the console's `TIMEOUT` and `POLL` did. A read that is
refused, absent or malformed fails the action rather than waiting for a timeout, and a refused action makes no write: the
root units and a suspended reconcile are refused before the first patch.

**Contract: the command.** `swhurl-v2 unit --action ACTION --unit NAME (--in-cluster | --kubeconfig FILE)`: exactly one
credential, the action and the unit, or exit 2 with the command's usage. Lines go to standard output as they happen, in
the console's words, so a job page and its tests read what they read today:

    ► suspending Kustomization gw                ► annotating GitRepository flux-system in flux-system namespace
    ✔ Kustomization gw suspended                 ◎ waiting for GitRepository reconciliation
                                                 ✔ fetched revision main@1edf370

A refusal is one line on standard error and exit 2, as every other v2 command answers one. There is no `--dry-run`: the
console's own runner is never a dry run, so the branch Python had for it was unreachable code.

**Contract: the ports.** The action reads and writes through `kube.Reader` and `kube.Writer` — the ports §25's
predecessors built — so it starts no process and names no CLI. Reading a unit, a source and a unit's status are
`Reader.Read` of one object; a patch and an annotation are `Writer.Patch` of a merge patch. The kinds table gains
`OCIRepository` and `Bucket` (they existed in the console's `SOURCES`), so a unit that applies an OCI artifact or a
bucket is reconciled as it always was. The job lifecycle's store port gains one write: `Data.SetConfigMapKeys` sets and
deletes several keys in **one** merge patch, because its record cleanup forgets old jobs while it writes a new one and a
second pod must not lose both changes; `SetConfigMapKey` is that call with one key.

**The job lifecycle, ported and unwired.** `orchestration.Jobs` is the console's job registry in Go: one running job per
target, `running`/`succeeded`/`failed`/`interrupted`, a job's lines as its record, the newest 30 kept front-trimmed to
20 kB, each line redacted and capped at 1,000 characters, every state change one `[AUDIT]` line, a returning pod calling
another pod's unfinished job interrupted, and a stopping pod draining its jobs. A job outlives the request that
started it (its context is not cancelled with the request's), and another pod's records are read again only while one
the registry remembers is running, as `jobs.py` does. Its record JSON keys are
`jobstore.py`'s, so a page and a reader cannot disagree about what a record says. It has no caller until the cutover:
the console's running jobs are still Python's, and nothing in this slice writes a record. `swhurl-v2 console`'s job-event
route now streams a job this server runs, and answers 404 while no command gives it a registry — which is what it
answers today.

**Deliberate changes.**

1. **A missing unit says so plainly.** Python asked `kubectl` for the unit and showed its NotFound text (`Error from
   server (NotFound): kustomizations… not found`); the Go command reads it and refuses with `no Kustomization named
   '<unit>'`, which is what the console's own unit page calls a missing unit.
2. **The command is `unit`, not the plan's `flux`.** The guardrail forbids v2 code outside an adapter from naming the
   `flux` or `kubectl` command, and a command name is the first place it would appear.
3. **The job lifecycle's Python stays, whole.** `jobs.py`, `jobstore.py` and the console's job pages are untouched: this
   slice changes what a job runs, not what a job is.

**Not in this slice.** The ConfigMap body of the job store, the server's job pages and its stream's one live registry, and
the drain a pod's SIGTERM runs — step 4's, once `swhurl-v2 console` runs a job itself. Also unchanged: the console's forms
and its pre-checks, `app-repo` and New app (step 3), the cutover, and the tokens and RBAC.

**Verification.** `orchestration`'s tests with a fake cluster: reconcile annotates the source before the unit and never
the reverse, a handled-but-failing request carries the controller's message, a timeout after 10 minutes' worth of polls,
a suspended reconcile and both root units refused with no write, suspend and resume each patch one field, an unsupported
source kind and a missing unit refused, and an unknown action refused; `adapters/kube`'s contract test for the one
multi-key patch (set and null-delete in one request, an invalid key refused before any request) and for the two kinds it
gained; `cmd`'s test that reconcile over a scripted API server makes exactly the reads and patches in that order and
prints the lines, that a root unit makes no request, and that a wrong credential or action is exit 2; the console's relay
cases (the argv with one credential, the lines kept, a refusal as v2's last line, `--dev` naming a kubeconfig and never
`--in-cluster`); `make check-v2` with `Layers` and `api.txt` updated in the same change, `make check`, `make test`,
`make check-docs`. Live: suspend, resume and reconcile a throwaway app's unit from the deployed console, the job page's
lines and the unit page's state compared with the same action run by Python before this slice, then `make
verify-platform` ([current state](current-state.md)).

## 26. The image webhooks in Go: `app-hooks` (approved 11 November 2026)

**Decision.** `make app-hooks` runs `swhurl-v2 app-hooks --records v2-records --owner samclement --hook-host
flux-webhook.<routing.baseDomain>`, and `imagehooks.py` with its tests is deleted; the catalogue keeps the target
without a `run` function, as `app-repo` did.

**Semantics.** An app gets the webhook when its **staging** release record publishes its own platform image,
`ghcr.io/<owner>/<app>` (Python's `APP_OWNER`, `samclement`); production and other images are never candidates. For
each, the repository's webhooks are listed and, by the URL's host: a correct one (active, the Receiver's URL derived
from the token, exactly `package` and `registry_package`) is left alone, the first stale, disabled or wrong-event one
is changed, and with none one is created. Nothing is deleted, and webhooks to other hosts are not the platform's.

**Contract.** The kernel owns the decisions (`ImageHookApps`, `PlanImageHook`, `ImageHookURL`, `ImageHookEvents`;
`app-repo` uses the same URL and events); `orchestration.AppHooks` lists and writes through the existing
`RepositoryWriter` (`Hooks`, `CreateHook`, `UpdateHook`); the command reads the records, prints one line per app and
chooses the exit status. Credentials come from the environment only: `GH_TOKEN` lists and writes, `IMAGE_WEBHOOK_TOKEN`
signs. The recipe takes `GH_TOKEN` or `gh auth token` and `stringData.token` from
`platform/flux-webhook/image-secret.sops.yaml` (`SOPS_AGE_KEY_FILE`, else `./age.agekey`), stops before building
when either is empty or unreadable, and passes both in the command's environment, never as an argument or to the
build. Only webhook creation sends GitHub's `name: web`; updating an existing hook omits that create-only field.
`--dry-run` lists the webhooks and writes nothing.

**Deliberate change.** Python skipped a malformed release record silently. Now a record that cannot be decoded or
names another application, and a listing or write GitHub refuses, are that app's `[ERROR]` line; the other apps still
run and the exit status is 1. A records tree or record that cannot be read stops before any call. The host comes from
the v2 profile, as `make app-repo`'s does, not from `platform-settings`. `verify-platform`'s own selection
(`appHookRepositories`) is unchanged.

**Verification.** Kernel tests for the URL, selection (own image, other owner or registry, renamed repository,
malformed, wrong application) and every plan case; orchestration tests with a per-repository fake for create,
repoint, in place, dry run, per-app failures that let others run, and refusals without inputs; command tests for the
lines, exit statuses, refusals before any call, no credential in output, and the platform's own records; a recipe
test with fake `gh`, `sops` and `go` proving it stops on auth, decrypt and empty-token failures and passes the tokens
only in the environment. `make check-v2`, `make check`. Live (operator): `make app-hooks DRY_RUN=true`, then
`make app-hooks`, then `make verify-platform`'s Image Webhooks ([current state](current-state.md)).

## 27. `check-secrets` in Go (approved 13 November 2026)

The first of the kept checks ([remaining work B.1](v2-status.md#remaining-work)), ported with the same behaviour. It
guards the double-base64 problem that caused an outage, and it only reads. The rest of B.1 waits for its own section.

**Decision.** `make check-secrets` runs `swhurl-v2 check-secrets --root . --age-key-file "${SOPS_AGE_KEY_FILE:-./age.agekey}"`, and refuses `DRY_RUN=true` as before. `secrets_check.py` and
`tests/test_secrets_check.py` are deleted. The catalogue keeps the target without a `run` function, as `app-hooks`
did.

**Semantics (retained).** Every `*.sops.y*ml` file Git tracks, apart from `.sops.yaml`, is decrypted in memory, and
each document of each file is checked:

- **An `[ERROR]` line** for each of these: a file that cannot be decrypted, cannot be read or holds no document; a
  document that is not a Secret; an empty value; a `REPLACE_ME` value outside `tests/fixtures/`.
- **A `[WARN]` line** for a `data` value of at least 16 bytes whose base64 decodes to printable text, which means it
  was probably encoded twice.
- **Three comparisons across files, by SHA-256 only:**
  - `CLICKSTACK_INGESTION_KEY` is in exactly the ClickStack and OTel Secrets, and both copies are equal.
  - The image webhook token and the GitHub dispatch token are each identical in their two Secrets.
  - The platform ntfy destination matches the notification checker's copy once both have lost their query and
    fragment.

The output keeps Python's lines in Python's order (`[OK] <file>: <namespace>/<name> (<n> data, <n> stringData)`,
then the comparisons, then `<e> error(s), <w> warning(s).`). Those lines name files, keys and counts, never a value or
a fingerprint. The exit status is 0, 1 when there is any error, and 2 when there is no age key (`SOPS_AGE_KEY_FILE`,
else `./age.agekey`).

**Boundaries and contract.**

| Layer | Owns | Does not own |
| --- | --- | --- |
| 0 `secretcheck` (new, pure) | The rules above and the table of copies kept equal (`INGESTION_FILES`, `SHARED` and `NTFY_DESTINATIONS`, as Python has them). Input: each file's path, whether it is a fixture, and either its parsed documents or why it could not be read. Output: the ordered findings and the counts | Reading files, keys or Git |
| 1 `adapters/sops`: a new `Decrypter` beside `Encrypter` | `sops --config /dev/null decrypt --input-type yaml --output-type yaml <path>`, with `SOPS_AGE_KEY_FILE` set from its argument in the child's environment only. Stderr is discarded. Output is bounded by `MaxDocument`. A failure is a `DecryptError` with a fixed reason (`sops_exit_N`, `sops_not_available`, `empty_output`, `output_too_large`, `no_file_or_key`) | Reading the key itself; deciding what a value means |
| 1 `adapters/git` (existing `Read`) and `adapters/manifests` (existing YAML) | The tracked list and the parse | — |
| 2 `orchestration.CheckSecrets` | The tracked list, then a decrypt and parse of each file, then the decisions | Printing, exit codes |
| 3 command | `--root`, the key file's path, the lines and the exit status | The decisions |

The plaintext crosses one boundary, the adapter's return value, and lives only in memory until the fingerprints and
findings have been made from it. A test passes known plaintext through the whole command and asserts that no value
and no fingerprint appears on stdout or stderr.

**Rule change this needs.** The `no-subprocess` allowlist is unchanged: the code stays in the same directory. What
changes is the text of the sops exception, which becomes "encryption, and local decryption with a key file the
operator's command names". The package comment loses "reads no private key" and says instead that `Encrypt` needs
none and `Decrypt` passes the key file's path only to `sops`, as the child's whole environment, so no other key is
tried. The console's encrypter is unchanged; only `check-secrets` reads the command's decrypter, and the console's
pod holds no key.

**Deliberate changes.** These are three places where Python crashes with a traceback today:

- A `data` value that is not strict base64 becomes `[ERROR] <where>: not base64`.
- A `data` or `stringData` that is not a map becomes `[ERROR] <file>: a document is not a Secret`.
- A Git listing that fails exits 2 with one line.

Nothing else changes, including the warning's threshold and the fixture exemption. Four smaller differences came
with the port, none reachable by a file sops writes from this repository: a document's value findings are in key
order (Python's was the file's); an unset namespace or name shows as `-` (Python printed `None`); a null
`stringData` value is empty (Python checked the word `None`); and a document that is a list or a scalar reads as
an empty one, so a file of nothing else has no document.

**Verification.**

- **Pure tests:** the six Python tests (destinations, double encoding, shared tokens, the ingestion key, the backup
  writer's two-document file, every document read), plus the three deliberate changes.
- **Adapter contract test** with throwaway X25519 identities the test makes in `t.TempDir()` (standard library, so CI
  needs no `age-keygen`). It is skipped without `sops`, as the encrypt test is, and `REQUIRE_SOPS=1` requires it. Plus
  missing-binary, exit and empty-answer cases.
- **Command tests:** the lines, the exit statuses 0, 1 and 2, and no value on either stream.
- **Parity, offline, scratch only:** in a scratch copy, run Python's and Go's checks and `diff` their outputs. Do
  this on today's tree, and on mutants encrypted to the platform's public recipient: `REPLACE_ME`, an empty value, a
  double-encoded value, differing ingestion copies, a missing shared copy, ntfy copies that differ only in their
  query (which must pass), and a two-document file. The outputs must be identical, apart from the three deliberate
  changes. Nothing in the scratch copy is committed or pushed.
- `make check-v2`, `make check`, then `make check-secrets` on the real tree. No live effect: the check never reaches
  the cluster.

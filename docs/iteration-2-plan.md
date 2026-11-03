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

The console already uses HTMX: [base template](../tools/swhurl/console/templates/base.html) and page templates contain HTMX navigation, form and refresh attributes. Go + HTMX would continue the server-rendered approach, not replace a client-side SPA.

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

**Decided 25 October 2026: a thin `net/http` client, not `client-go`.** It is the reader [`adapters/kube`](../v2/adapters/kube/kube.go), keeps the module free of third-party dependencies (`stdlib-only`), and takes the k3s material it needs as arguments: a CA bundle, a client certificate or key, and a bearer token. `client-go` would be complete but adds a large dependency tree and binary size for four kinds and two verbs. Resolving a kubeconfig or an in-cluster service-account token is **not** in that adapter: a kubeconfig is YAML, so reading one needs the dependency or parser decision this slice did not take; the `observe` command resolves it and hands the adapter its ingredients.

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
| `stdlib-only` | v2 imports only the standard library and its own packages; `go.mod` requires nothing ([why](#kubernetes-the-api-server-not-kubectl)). A dependency needs the operator's approval first. |
| `layers` | Every v2 package has a declared layer (0 pure model, 1 adapters, 2 use cases, 3 commands) and imports only v2 packages in lower layers, except that a fake may import the adapter it fakes ([why](#4-development-layers-and-adapters)). A new package is given its layer in `Layers`. |
| `pure` | Pure packages import only the allowlisted standard packages, print nothing and assign no package variable ([why](#the-pure-model)). Time, identifiers and snapshots are arguments: `time` is allowed for parsing and comparing a *supplied* timestamp, and its clock functions (`time.Now`, `time.Since`, `time.Until`, timers and ticks) are refused by call. |
| `no-subprocess` | v2 code (not its tests) starts no process except the encryption adapter `adapters/sops`, and names no `kubectl` or `flux` command anywhere ([why](#kubernetes-the-api-server-not-kubectl)). The one exception is priced in [v2 status](v2-status.md#where-it-stands): a second implementation of age and the SOPS envelope would be a hand-built cipher, and the adapter reads no cluster and no private key. |
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

**CI (24 October 2026):** Validate's tooling job sets up Go with `actions/setup-go@v7` from `v2/go.mod` (no cache: there is no `go.sum`) and runs `make check-v2` after the code graph tests, with `REQUIRE_HELM=1` and the chart the v2 profile pins fetched into the chart cache first, so the [compiler's](#2-phase-1-model-and-contract) manifest check runs; a failure there holds the console deploy like any other check. First run on the push of `be041cf4` ([current state](current-state.md#v2-and-code-graph-push-24-october-2026)): `go1.26.0 linux/amd64`, all four packages passed; Go setup took 7 s and the check 11 s. `actionlint` was not run (not installed here).

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

An external-effect adapter needs separate approval; a read-only integration is checked by `adapter-complete` from the commit that adds it.

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
| `publishes` | Optional event names, each at most once: what the `events` document sends. The producer computes it and the platform check ties the two, because v2 reads no YAML |
| `secrets` | Optional names, refused when non-empty: v2 models no secret |
| `memoryMiB` | Required, integer at least 1: the memory the image runs in |
| `databaseSizeMiB` | Required when `database` is set, and refused when it is not: the volume a new database is created with |
| `description` | Optional: one line of 1–200 printable characters, served to the app as `SWHURL_DESCRIPTION` |
| `apiPath` | Optional absolute path where a `web` image serves its API description. Required with the `api` document, and refused without it |
| `documents` | Optional object of at most `api`, `events` and `flags`, each the file's bytes base64-encoded, so the record's copy is byte-identical. `events` is required when `publishes` is non-empty |

**Size.** The producer refuses a declaration whose encoded JSON exceeds 120 KiB, naming the documents and
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
- The console already writes only through the binary: [`console/v2.py`](../tools/swhurl/console/v2.py) runs
  `swhurl-v2 edit`, `remove`, `promote` and `render`, and the image carries `swhurl-v2`. Its reads are 16
  `kubectl` calls, and its pull requests and merge gate are 10 `gh` and 8 `git` calls.
- v2 has no Git or GitHub adapter. [`adapters/kube`](../v2/adapters/kube/kube.go) already accepts a bearer token,
  but only the host's kubeconfig resolves one; running in the console pod needs the service-account token read
  as a credential source (a [deferred feature](v2-status.md#deferred-features) and an external-effect adapter,
  so its own approval).
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
merge gate (with the v1 model's three console importers) and the checks; the server and templates go last. It
costs a JSON contract per read that the final Go server calls as a function instead, and one process start per
read, where the console already starts `kubectl`. Option 2's modules stay worth doing in between, as fillers.

**What would change it.**

- A redesigned console is wanted anyway: then option 1, because the templates are rewritten either way.
- The in-cluster credential is not approved: option 3 still takes the file-only reads (the catalogue, records,
  the merge gate's rules), and status stays Python until it is.
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
  by default; `--json` prints `v2-observation` version 3 with `state` and the new facts. Exit 0 whenever a report
  is printed, whatever the state; 2 for usage, an instance with no record, or a refused request.
- `make app-status APP= ENV=` is bash: it builds the binary into a throwaway folder and writes the kubeconfig's
  JSON form there, removed on exit. Its `commands.py` row loses its `run=`.
- Dropped on purpose: settings, variables, the secret line, the team and exposure (the records hold them), job
  runs (v2 has no job kind) and the GitRepository's revision. Right after any commit to `main` every instance is
  `updating` until Flux applies it; the promotion gate's "a commit elsewhere" exception moves with slice two.

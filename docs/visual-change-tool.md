# Code graph: dependency chains, journeys and hotspots from code

**Status: steps 0 (place, policy, check wiring), 1 (the core, `snapshot`) and 2 (`references`, `entries`, `reach`) done and committed 24 October 2026; step 3 next.** A read-only development aid in [`devtools/codegraph/`](../devtools/codegraph) that derives a dependency graph, entry-point journeys, hotspots and changes between commits from **v1 source and its Git history**. Independent of the v2 prototype. No push or deployment is authorised by this page.

[Plan section 0](plan.md#0-where-this-paused-and-what-is-left) indexes this page; it is the canonical design, plan and handoff for this workstream. **Every change applies [section 4](#4-primitives-and-contracts) and [section 5](#5-designed-for-incremental-change).**

## 1. Goal

Answer three questions about a codebase whose design grew without written-down use cases:

1. **What did a design choice do to the structure?** Dependencies, cycles, depth and what moved or changed between two commits.
2. **What does a use case depend on?** The chain from each way into the system to everything it reaches.
3. **Where are the hotspots?** Components that change often, are depended on widely, sit on many journeys or are coupled without an explicit dependency.

Get the core right first: deterministic facts as JSON and a short text report from one CLI. A visual page, naming overlays and agent workflows are later features that read the same JSON.

## 2. Principles

- **Code is authoritative.** Everything is derived from source at an exact commit. No metadata is required; journeys are inferred, not declared.
- **Derived, not guessed.** Every result says what produced it (rule, parameters, inputs) and what the extractor could not see. A missing edge is never shown as absence of a dependency.
- **Never execute analysed code.** Source is read from Git objects and parsed with `ast`; no module, test, hook or build is imported or run. The operator's checkout is never touched.
- **Generic at the data boundary only.** The library knows Python and Git, not swhurl. The snapshot document is the portable contract: another language is another extractor writing the same document. No plugin framework, profile system or second language until one is needed.

## 3. Concepts

| Term | Meaning |
| --- | --- |
| **Revision** | A ref resolved once to a full commit ID. An unknown ref is a failure, never a fallback to `main` or the working tree. |
| **Node** | A module, class or function/method (nested functions included) at a revision. ID `py:<module>:<qualified name>`, for example `py:swhurl.apps.cli:main_promote`; a module is `py:<module>`. Carries path, line range, signature and a body hash (docstring excluded). The same ID at two revisions is the same element for comparison. |
| **Edge** | `imports` (an import statement outside any function, module to module), `lazy-imports` (an import inside a function), `calls` (a call site resolved to a node in scope, from the innermost enclosing definition, or the module for module-level code) or `references` (a function or class in scope used as a value outside call position: passed, stored, returned, a decorator, a base class, an `except` clause; annotations are not references). A call or reference inside a nested function belongs to the nested function, not its parent. |
| **External call** | Resolves outside the scanned scope (`json.dumps`, `len`). Out of scope, counted separately; not unknown. |
| **Unresolved call** | A call site the extractor cannot map, counted per caller by category: `value-attribute` (a method on a value: `runner.run()`, `self.x()`, `"".join()`), `dynamic` (calling a parameter, a variable or a computed value), `ambiguous` (a name bound in more than one way, such as a `try`/`except` import fallback), `missing` (a reference that leads nowhere in scope: an inherited method, a name the target module lacks) and `unparsed` (the target is in a file that did not parse). Anything computed over calls is therefore a **lower bound**. |
| **Unparsed file** | A file that fails to parse at that revision, or a second file for an already-named module. Recorded with the error; its module node remains, the snapshot is still produced and reports list it. |
| **Object reference** | A whole string literal in the `module:qualname` form packaging entry points use (`'swhurl.apps.cli:main_promote'`, `'cli:main_promote'`), recorded unresolved where it appears. |
| **Argument** | A function or class in scope passed directly as an argument of a call, recorded with the callee's absolute dotted name as the file binds it (`starlette.routing.Route`), the argument's position or keyword, and the call's string literal arguments. |
| **Entry point** | A function the outside world starts, found by a named rule. Records the rule and the source location of its evidence; several pieces of evidence for one function make one entry point. |
| **Reach** | The nodes reachable from a node over `calls` and `references` edges (the node included), with the external and unresolved call counts across them. |
| **Spine** | The resolved calls and references in a node's own body, in source order (within a line, by target ID): a readable outline, not an execution trace. |
| **Journey candidate** | A group of entry points that share **distinctive** reach, explained by the shared functions few other entry points reach. Evidence of shared code, not proof of a shared user goal. Names are not inferred. |
| **Change** | Between two revisions: added, removed, moved (same qualified name and body hash, different module, old gone), candidate move (same qualified name, body differs, old gone), interface changed (signature), implementation changed (body hash). "No structural change" is not "no behaviour change". |
| **Document** | A JSON value with a `kind`, `version`, `inputs` and `params`: the snapshot and every feature's output. Documents are what primitives exchange and what the CLI chains. |

## 4. Primitives and contracts

### Layers

Each layer may use only the layers above it in this table. Signatures are the intended Python API; names may be refined when implemented, but the shapes and dependencies may not change without updating this section.

| Layer | Module | Primitives | Effects |
| --- | --- | --- | --- |
| **A. Revision reader** | `git.py` | `resolve(repo, ref) -> Revision`<br>`tree(repo, revision, scope) -> tuple[Blob, ...]` (path, blob ID)<br>`blob(repo, blob_id) -> bytes`<br>`log(repo, range) -> tuple[Commit, ...]` (step 5) | The only module that runs a process. Reads Git objects; never the working tree, index or configuration it could change. |
| **B. Extraction** | `python_ast.py` | `module_name(path, import_root) -> str`<br>`extract(module, path, source: bytes) -> FileFacts`: one file's definitions, imports, call sites and module-level bindings, resolved lexically within the file and still symbolic across files<br>`link(revision, scope, import_root, files: Iterable[FileFacts]) -> Snapshot`: resolve references across files (including re-exports), count external and unresolved calls | Pure. `extract` depends only on its arguments, so its result can be cached by blob ID. |
| **C. Documents** | `documents.py`, `snapshot.py` | `documents.dump(document) -> bytes` (canonical), `documents.load(data, kind) -> dict` (the envelope), `documents.digest(document) -> str`, `documents.Input` (an entry of `inputs`)<br>`snapshot.to_document`, `snapshot.from_document` (strict), `snapshot.dump`, `snapshot.load`, `snapshot.as_input(snapshot) -> Input` | Pure. |
| **D. Graph** | `graph.py` | `graph(snapshot, kinds) -> Graph` (every node, edges of the chosen kinds)<br>`closure(graph, starts) -> frozenset[NodeId]` (the starts included)<br>`components(graph) -> tuple[frozenset[NodeId], ...]` (strongly connected)<br>`rank(graph) -> Mapping[NodeId, int]` (depth with cycles condensed) | Pure and generic: no Python or swhurl knowledge. Added with the first feature that needs each. |
| **E. Features** | one module each | `entries(snapshot) -> Entries`<br>`reach(snapshot, node) -> Reach`<br>`journeys(snapshot, entries, params) -> Journeys`<br>`diff(a, b, journeys_a=None, journeys_b=None) -> Diff`<br>`hotspots(snapshot, history, journeys=None) -> Hotspots`<br>`timeline(snapshots, params) -> Timeline` | Pure: documents in, one document out. Built from layer D and other features' public functions. |
| **F. CLI** | `cli.py` | One subcommand per primitive that produces a document; rendering as JSON or `--text` | Reads arguments, files and stdin; writes stdout and stderr; calls layer A only for `snapshot`, `history` and `timeline`. |

The snapshot is a composition, not a special case (`cli.build_snapshot`):

```python
revision = resolve(repo, ref)
import_root = posixpath.dirname(scope)  # modules are named from the scope's parent: tools/swhurl/x.py is swhurl.x
files = (extract(module_name(b.path, import_root), b.path, blob(repo, b.id))
         for b in tree(repo, revision, scope) if b.path.endswith('.py'))
snapshot = link(revision.commit, scope, import_root, files)
```

The snapshot document: `revision`, `nodes` (`id`, `kind`, `module`, `qualname`, `path`, `lines`, `externalCalls`, `unresolvedCalls` by category, and `signature`, `bodyHash`, `redefinitions`, `mainBlocks` (a module's `if __name__ == '__main__':` bodies as line ranges) where they apply), `edges` (`source`, `target`, `kind`, `lines`), `unparsed` (`path`, `error`), `objectReferences` (`source`, `line`, `value`) and `arguments` (`source`, `line`, `callee`, `slot`, `target`, `literals`), with `params` recording `scope`, `importRoot`, `extractor`, `extractorVersion` and `python`. `mainBlocks`, `objectReferences` and `arguments` came with extractor version 2 and are optional when read. Body hashes come from `ast.unparse`, so they are comparable only between snapshots with the same `python` value.

The entries document: `entries` (`node`, `evidence`: `rule`, `path`, `line`, and `value` when the rule matched one), `ambiguous` (evidence and `candidates`) and `unresolved` (evidence), with `params.rules`. The reach document: `node`, `reached` (node IDs, the node included), `externalCalls`, `unresolvedCalls` by category across everything reached, and `spine` (`line`, `target`, `kind`), with `params.node` and `params.edgeKinds`. Both name their snapshot in `inputs` (`kind`, `digest`, `revision`). Neither has a loader yet: step 3 adds one for entries when `journeys` reads it.

### Contracts every primitive keeps

1. **Values.** Frozen dataclasses with tuple and frozenset fields. Inputs are never mutated. No module-level mutable state.
2. **Effects.** Only layer A runs processes or reads Git; only layer F touches files and streams. Everything else is pure: no clock, randomness, environment, current directory or network.
3. **Determinism.** Collections are sorted by ID and ties broken by ID. Floats in documents are rounded to 4 decimal places. Identical inputs give byte-identical documents.
4. **Absence, unknowns and failure are different.** An empty result is a success. What could not be determined is recorded in the result (`unresolved`, `unparsed`, `ambiguous` with counts or lists). A failure raises `CodegraphError(code, detail)` and returns nothing partial.
5. **Error codes** are a fixed set: `unknown-ref`, `git-failed`, `invalid-document`, `unsupported-version`, `unknown-node`, `usage`. Messages name the field or input, not file contents.
6. **Documents carry provenance.** Every document has `kind` (for example `snapshot`, `journeys`), `version` (integer), `inputs` (each input document's kind, digest, and revision for snapshots; a snapshot has none) and `params` (every parameter used, defaults included). Canonical bytes: `json.dumps(sort_keys=True, separators=(',', ':'), ensure_ascii=False)` plus a newline; the digest is SHA-256 of those bytes. Loading refuses a wrong `kind`, an unknown `version`, duplicate keys, `NaN`, unknown or missing fields, wrong types (a boolean is not an integer) and dangling references.
7. **Lower bounds are labelled.** Anything computed over `calls` carries the unresolved count it rests on.
8. **Bounding is presentation.** Primitives never truncate. The CLI's `--limit` truncates lists and records how many items it left out.
9. **No hidden defaults.** A parameter that changes a result appears in `params`; a primitive does not read configuration from anywhere else.

### Chaining

Every subcommand writes exactly one document to stdout. Subcommands that analyse read documents from paths or `-` for stdin, and never re-read Git themselves:

```bash
codegraph snapshot HEAD > head.json
codegraph snapshot HEAD~20 > before.json
codegraph entries head.json > entries.json
codegraph reach head.json py:swhurl.apps.cli:main_promote
codegraph journeys head.json entries.json > journeys.json
codegraph diff before.json head.json
codegraph snapshot HEAD | codegraph entries -
```

The CLI exits 0 on success, 1 on failure and 2 on a usage error; on failure stdout is empty and stderr has one line with the error code. `--text` renders the same document for a person and contains nothing the JSON does not.

## 5. Designed for incremental change

- **Adding a fact** (a call-resolution case, an edge kind, a node attribute): extend layer B and the snapshot additively, bump the extractor version recorded in `params`, add a core fixture.
- **Adding an analysis:** a new layer E module whose public function takes documents and returns a new `kind`, plus its subcommand, hand-written snapshot fixtures and one end-to-end CLI test on a fixture repository. Use layer D and other features' public functions; nothing else changes.
- **A feature's dependency on another feature is optional where it can be:** diff works without journeys and adds journey changes when given them.
- **Removing an analysis:** delete its module, subcommand and fixtures; nothing else may break.
- **Extension points are short lists in code, not registries:** entry rules in `entries.py`, resolution cases in the extractor, edge kinds in the extractor and schema, features as modules, output views as renderers of documents.

**Schema evolution:** adding a field or edge kind keeps a document's `version`; a field added later must be optional when read, since loading refuses unknown fields and older documents lack it. Features read only the fields they need. Changing a field's meaning, removing it or changing ID syntax increments `version`, and loading refuses versions it does not know: no silent upgrade.

**Tests follow the layers:** layer A against fixture Git repositories built by the test in a temporary directory; layers B–E against hand-written sources and documents, without Git; one end-to-end CLI test per subcommand.

**Never:** call Git outside layer A; parse source outside layer B; import another feature's private names; add a parameter that is not in `params`; make an analysis subcommand read Git.

## 6. Feature roadmap

Each step ends in a usable subcommand with tests and is checked against v1 evidence (expected numbers come from a throwaway probe, section 8: explain differences, they are not thresholds). Stop after any step that does not pay off.

| Step | Delivers | Needs |
| --- | --- | --- |
| 0 | Place, policy and check wiring (**done**) | — |
| 1 | The core: layers A–C and `snapshot` (**done**) | 0 |
| 2 | `entries`, `reach` (**done**) | 1 |
| 3 | `journeys` (validation gate) | 2 |
| 4 | `diff` | 1; journey changes need 3 |
| 5 | `hotspots`, `history`, `timeline` | 1; journey counts need 3 |
| Later | Separate decisions (below) | the above |

Steps 4 and 5 are independent of each other: either may go first after step 3's gate.

### Step 0: place and policy (done)

`devtools/codegraph/` holds the package `codegraph`, its tests and its [README](../devtools/codegraph/README.md). It is outside `tools/`, so it is not in the console image or its content hash, and uses the Python standard library only (`requires-python` as the repository). `AGENTS.md` and [contributing](contributing.md#operator-tooling) carry the narrow exception: it lives outside `tools/swhurl`, reads Git through its own layer A instead of `Runner`, and is not a `swhurl` command. `make check-codegraph` runs its tests, `make check` includes it, `make check-lint` lints it, and CI runs both.

### Step 1: the core (done)

Layers A–C, the snapshot document and `codegraph snapshot <ref> --scope <dir> [--repo <dir>] [--text]`.

- Layer A: `resolve` (refuses empty and option-like refs), `tree` (literal pathspec, regular files only), `blob`. Commands run with `GIT_OPTIONAL_LOCKS=0`.
- Layer B resolves: module imports (`import a.b` binds `a`), `from` imports, relative imports from modules and packages, re-exports through a package, local and enclosing definitions (class bodies are not visible in methods), `Class.method` and constructors through the class name, lazy imports, `global`. Shadowing by assignment, parameters, loops, `except` and `match` names makes a call `dynamic`, never a guess. Decorators, defaults and bases belong to the enclosing scope. A redefinition (a property setter) merges into one node.

**Acceptance, met:** 39 tests in `devtools/codegraph/tests`, one module per layer plus the CLI. A fixture Git repository is built by the test; two runs give identical bytes; modules whose import would write a marker file or exit are never executed; `git status`, `HEAD` and the index are unchanged, with uncommitted and untracked files present and ignored; an unknown ref or a non-repository exits 1 with empty stdout and one line on stderr; an unparsable file is recorded; each resolution case and each unresolved category has a test; the snapshot round-trips, and malformed documents are refused case by case.

**v1 evidence (24 October 2026, HEAD `726b3cf4`):** 119 modules, 147 classes, 1,181 functions; 649 `imports` and 13 `lazy-imports` edges, 662 distinct module pairs (the probe's 662 exactly; it counted 17 "lazy" because it also counted imports nested in module-level `if`/`try`); 1,997 `calls` edges; 2,324 external calls; 4,574 unresolved (4,378 `value-attribute`, 192 `dynamic`, 3 `ambiguous`, 1 `missing`). Byte-identical across runs; about 7 s, mostly extraction. Across M4 (`334a0f11^` → `334a0f11`) functions (1,157), call edges (2,006) and every unresolved count are unchanged while modules go from 104 to 107 and import edges from 514 to 541: the move is visible before any diff feature exists.

**Findings that shape step 2:**

- `main_promote` itself calls only `run` and `Runner`: its work is in the nested `main_promote.change`, which it passes to `run(change, True)`. The probe's 210-function reach merged nested bodies into their parent; the snapshot does not. Reach needs the fact that a function is **passed as a value**: step 2 added the `references` edge kind for it, additively, keeping version 1.
- Of the `value-attribute` calls, only about 144 call sites in v1 are `self.`/`cls.` methods; most are methods on local values and parameters (`runner.run`, `path.read_text`). A `self` resolution case would recover little; type-aware resolution is the real lever (later features).

### Step 2: entry points, reach and spine (done)

`codegraph entries <snapshot>`, `codegraph reach <snapshot> <node>`, each with `--text`; both read a snapshot document from a path or `-`.

- Layer B (extractor version 2, snapshot version 1) adds the `references` edge kind and three facts the entry rules read, so that no rule parses source: `objectReferences`, `arguments` and a module's `mainBlocks` (section 3 and the document in section 4).
- Layer D: `graph` and `closure`. Layer E: `entries.py` with its rules in `RULES`, and `reach.py`, which follows `calls` and `references`.

Entry rules (generic Python or framework knowledge, none specific to swhurl):

1. `entry-string`: an object reference whose qualified name is a function and whose module part is that function's module or a suffix of it (`'cli:main_promote'` matches `swhurl.apps.cli`). One match is an entry point; several are reported as ambiguous and none as unresolved, never chosen.
2. `main-block`: a function called from a module's `if __name__ == '__main__':` body.
3. `starlette-route`: a function passed as argument 1 or `endpoint` of `starlette.routing.Route`; the value is the route's literal path. A handler built by a call (`Route('/x', endpoint(f))`) or a class endpoint is not a match.

**Acceptance, met:** 69 tests (30 new). Layer B: what is and is not a reference (calls, values, modules, builtins and annotations are not), references through a re-export, argument slots, callees and literals, object reference strings (not f-string fragments or docstrings), main guards either way round. Layer C: the new fields round-trip, a document without them still loads, malformed ones are refused. Layer D: kinds, cycles, unknown starts. Layer E on hand-written snapshots: each rule, ambiguous and unresolved entry strings, a class is never an entry point, reach through nested functions and methods with the unknowns it rests on, spine in source order. CLI: `snapshot | entries -`, `reach` on a file, failures exit 1 with empty stdout.

**v1 evidence (24 October 2026, HEAD `4a855098`, the same `tools/swhurl` tree as `726b3cf4`):**

- Snapshot: every step 1 count unchanged; 398 `references` edges (210 function → class, mostly `except` clauses; 115 function → function; 58 module → function), 75 object references, 201 arguments (23 to `starlette.routing.Route`), 1 main block. Byte-identical across runs; about 5 s. `entries` and `reach` take about 0.4 s each.
- **88 entry points: 64 entry strings, 23 routes, 1 main block, exactly the probe's numbers.** All 71 `commands.py` strings resolve, with none ambiguous. **The 7 explained:** they are repeats, not failures. `swhurl.apps.ops:main` backs 4 commands, `swhurl.lifecycle:main` 3, `swhurl.console.server:main` and `swhurl.settings:platform_certs` 2 each, so 71 strings name 64 functions. Unresolved: 4 strings that only look like object references (`'app:smoke'` twice in `apps/build_image.py`, the tar mode `'r:gz'` in `console/proposal.py` and `console/stages.py`).
- `main_promote` reaches **219 functions** and 36 classes in 44 modules, resting on 686 unresolved calls. Over `calls` alone it reaches 71: `references` restores the nested `change` body. The spine is 4 steps: `Runner`, `change` (referenced), `run`, `CommandError`. Across M4 it is 218 functions both before (`apps.edit:main_promote`) and after (`apps.cli:main_promote`), in 39 then 42 modules, against the probe's "about 210, 38 to 40".
- **Route reach was higher than expected.** Routes reach a median of 42 functions (range 0 to 211), not 15, with a median of 153 unresolved calls. Each handler is a nested function of `routes(ctx)` that passes a nested `read` to a context-provided `reading(...)`. Over `calls` alone the median is 1. Through `references` the `read` bodies are reached, while `ctx.*` itself stays unresolved. The probe's 15 came from merging nested bodies and is not reproducible. `/apps/{app}/staging/promotion` reaches 211 functions, close to `main_promote`'s 219, which is the overlap step 3 expects.
- Entry strings reach a median of 26 functions (up to 393). `swhurl.__main__:main` reaches only 2: it dispatches through `importlib` on the strings that the entry-string rule reads instead.

**Surprises:** besides route reach above, calling a class reaches only the class node: the class body's own calls, not `__init__` or other methods. A method called on a value stays `value-attribute`, so reach under-counts behind constructors. Recovering that needs type-aware resolution (later features), not a new rule. `references` also brings in exception classes from `except` clauses: true dependencies that add nodes, little reach.

### Step 3: journey candidates

`codegraph journeys <snapshot> <entries>`.

- Weight each function by how few entry points reach it: `w(n) = log(N / entry points reaching n)`. Plumbing reached by everything weighs nearly nothing.
- Similarity of two entry points: weighted Jaccard of their reach sets. Link pairs that meet a threshold; candidates are the connected groups. `params` records the weighting and threshold.
- Each candidate lists its entry points, its distinctive shared functions with their entry counts, and the shared and per-entry reach sizes. Ungrouped entry points are single-entry candidates.

**Tests:** entry points that share only plumbing are not grouped; entry points that share a rare function are; ties are broken deterministically.

**v1 evidence:** `main_promote` and the route `/apps/{app}/staging/promotion` are each other's nearest neighbour (0.73; next 0.63), linked by `promote`, `staging_offers` and `egress_difference`, which only 3 entry points reach. Unweighted Jaccard instead groups 27 record-editing commands by their shared 121-function core: the weighting matters.

**Validation gate:** print every candidate at HEAD; the operator marks which match a real feature and which do not. Record the result here. Tune or rethink the grouping before steps 4 and 5 rely on it; this is the main unproven idea.

### Step 4: comparison between commits

`codegraph diff <snapshot-a> <snapshot-b> [--journeys <a> <b>]`.

- Node changes per section 3, with moves grouped by source and target module ("15 functions: `apps.edit` → `apps.cli`").
- Edge changes; cycle changes (adds `components` to layer D).
- Given journey documents: candidates matched across the two sides by shared entry points, with functions reached added and removed; an entry point on one side only is reported as such.

**Tests:** each change category; a move with an edit (candidate, not move); a docstring-only edit (not an implementation change); a pair with no structural change.

**v1 evidence:** M4 (`334a0f11^..334a0f11`): about 15 moved, 3 interface changes, 36 implementation changes, import edges +35 −16, promote reach unchanged (measured in step 2: 218 functions, 39 modules before and 42 after; the probe said about 210, 38 and 40). M3 (`cd10da1d^..cd10da1d`): moves from `console.changes` to `console.merge`.

### Step 5: history and hotspots

`codegraph history <range>`, `codegraph hotspots <snapshot> <history> [--journeys <doc>]`, `codegraph timeline <range>`.

- `history` (layer A `log`, as a document): commits with the files each touched.
- `hotspots` per module: commits touching it, lines, fan-in, fan-out, cycle membership, derived depth (adds `rank` to layer D) and co-change partners (changed together in at least k commits with no import edge either way); with journeys, the number of candidates reaching it. Separate sortable columns; no combined score until the data shows one is useful.
- `timeline`: a few metrics per commit over a range (cycle sizes, top fan-ins, unresolved calls), so a jump points at the commit to diff. It builds snapshots itself, caching `extract` by blob ID within the run, since most files are unchanged between neighbouring commits.

**Tests:** fixture history with known churn, a co-change pair without an import, a cycle introduced at a known commit.

**v1 evidence:** busiest files since July include `console/server.py` (94 commits), `console/changes.py` and `apps/contract.py` (68 each). `swhurl.interface`, added at `a7b55809`, is now second in fan-in (41, after `swhurl.run` at 57). The `apps.model` cycle grows from 5 to 6 modules (`generated` joins) after M3/M4; the timeline should find that commit.

### Later features

Each needs its own decision once the core has proved useful, and each fits section 5 as a feature or a renderer of documents:

- **HTML view:** offline, a combined before/after graph with stable placement from derived depth, module overview with function drill-down. The [repository blueprint](repository-blueprint.html) renderer is a reuse candidate.
- **Names for journey candidates:** an optional overlay referring to entry point IDs, checked at each revision.
- **Agent skill:** queries the CLI and cites node IDs and commits; tested against the known v1 answers above.
- **Type-aware resolution:** pyright, or SCIP indexers for more languages, to recover `ctx.*` reach and methods behind constructors.
- **Layer rules** and **state-machine views** (v1 has no explicit transition tables).
- **A second language.**

## 7. Decisions

Settled with step 0 (24 October 2026): location `devtools/codegraph/`; tests run by `make check-codegraph` as part of `make check` and CI; the `AGENTS.md` and contributing exception as described in step 0.

Taken in step 2 (24 October 2026), open to review: entry rules read only snapshot facts. Layer B therefore records generic Python facts (object references, arguments with their callee and literals, main blocks) and `entries.py` interprets them, instead of layer B recognising entry points itself. Entries and reach documents have no loader until a feature reads them.

## 8. Evidence and limits so far

A throwaway read-only probe (`/tmp/vct-probe/`, not kept) parsed v1 from Git objects with `ast` on 24 October 2026 and produced the expected numbers above. Known limits it showed:

- Static resolution leaves most method calls on values unresolved; step 1 measured it (4,378 `value-attribute` call sites at HEAD, step 1 evidence).
- Dependency injection through a context object (`ctx.*` in the console) leaves the console's service calls unresolved; `references` recovers the nested bodies handlers pass to it (step 2 evidence).
- Calling a class reaches the class node, not its methods (step 2 surprises).
- Journey grouping was tested on one pair at one revision.
- The probe merged nested functions' calls into their parent and used breadth-first order; its reach and spine numbers for step 2 and 3 are therefore approximate. The implementation attributes calls to the innermost definition, in source order.

## 9. Handoff

Read this page, `AGENTS.md` and [contributing](contributing.md). Apply sections 4 and 5 to every change; if a change needs a different contract, update section 4 in the same change. Work one step at a time, in order; record each step's v1 evidence and surprises in section 6 before starting the next. Do not change v1 or v2 behaviour, read credentials or the cluster, add dependencies, commit or push without instruction.

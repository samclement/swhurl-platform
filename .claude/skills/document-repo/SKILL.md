---
name: document-repo
description: Create, improve, sync or assess minimal, source-grounded repository documentation for consumers and contributors. Use when asked to document a repo, write or simplify a README or contributor guide, explain its interfaces and structure, update docs after a code change, tidy agent instruction files (AGENTS.md, CLAUDE.md), or review documentation quality. Apply progressive disclosure to both well-structured and loosely organised repos; support in-repo documentation and standalone artefacts.
---

# Document a repo

Document the shortest path from a reader's question to a successful action. Include a paragraph, table or visual only when it helps the reader use the repo, change it or preserve a consequential constraint.

## Establish scope

- Follow repository instructions. Identify the mode:
  - **Author**: create or improve repository documentation in place.
  - **Artefact**: produce a standalone guide outside the repo.
  - **Sync**: update documentation to match a code change (see [Sync docs to a change](#sync-docs-to-a-change)).
  - **Assess**: review existing documentation and report findings without rewriting it.
- Honour the requested destination. For standalone artefacts, save or publish where the user asked (file path, document or page); record the reviewed revision and date, and link to revision-pinned source. For in-repo documentation, prefer relative source links and avoid snapshot metadata.
- Inspect existing documentation before creating pages. Improve canonical explanations in place; preserve useful reference material and unrelated content.
- Choose two or three primary reader tasks appropriate to the repo. Prioritise first successful use and first successful change. Include installation, integration, extension, operation or troubleshooting only when relevant.
- Ask a targeted question only when missing intent materially prevents useful work. Otherwise proceed with explicit, bounded assumptions.

## Inspect and trace

1. Read repository guidance, manifests, entry points, build/run commands, tests and existing documentation. Exclude generated/vendor trees unless needed to resolve a contract.
2. Trace a representative task from trigger through implementation to observable result. Inspect consequential configuration, state, dependencies and failure paths.
3. Identify the actual public surface: package exports, commands, network endpoints, events, files, schemas, configuration or deployment outputs.
4. Verify important claims against active source. Distinguish configured behaviour from observed runtime behaviour. When comments or documentation conflict with implementation, describe the discrepancy without silently accepting either as intent.
5. Inspect additional paths only to resolve material uncertainty or support a selected reader task. Do not catalogue the entire repo.

## Sync docs to a change

Use when documentation must follow a diff, commit or branch.

1. List what the change adds, renames or removes: paths, commands, make/script targets, flags, configuration keys, environment variables, endpoints and defaults.
2. Search all documentation (README, `docs/`, agent instruction files, runbooks, ADRs, comments that act as docs) for each item.
3. Update the one canonical explanation of each affected topic; replace duplicates elsewhere with links rather than editing every copy.
4. Delete or rewrite references to removed items. Do not leave "X was removed" notes in active docs; git history records removals. Historical records such as ADRs may keep old names when they describe a past decision, but must not present them as current.
5. If the change completes planned work, find notes that point to it (for example "until PR06" or an issue link) and replace them with the new behaviour.
6. If the change alters a recipe, re-check its prerequisites, expected result and verification step.

Keep the sync inside the scope of the change. Report unrelated stale documentation you notice instead of fixing it silently.

## Organise progressive disclosure

Separate abstraction from detail:

- Move from whole-repo purpose to a capability or flow, then to implementation.
- At each level, offer a short explanation, then an example or contract if needed, then a link to the authoritative source.

Default to a short README containing purpose, quickest useful action, expected result and task-based links. Add a focused page only when the content has an independent reader task or would obscure that entry point. Section links are sufficient for small repos. For a standalone guide, provide a short orientation and task index before focused sections.

Use a soft budget of approximately 500 words for the entry point and 300–600 per additional topic. These are ceilings to question, not targets to fill. Preserve correctness, essential operational consequences and valuable existing reference material even when that exceeds the budget. Never create empty template sections.

Example of the intended density for an entry point:

```markdown
# widget-sync

Mirrors widget records from the inventory API into Postgres every five minutes.

## Quick start

    make dev        # starts Postgres and the worker
    make sync-once  # runs one sync; expect "synced N widgets"

## Where to go next

- Change what is synced: [docs/mapping.md](docs/mapping.md)
- Operate and troubleshoot: [docs/runbook.md](docs/runbook.md)
```

Not: a feature list, badges, a directory tree, a generic contribution section and an architecture essay ahead of the first command.

## Select documentation primitives

Use these as an inspection vocabulary, not a mandatory output schema:

| Primitive | Minimum useful content |
| --- | --- |
| Concept | Repo-specific meaning and why the reader needs it |
| Responsibility | What an area does and where it is implemented |
| Interface | Consumer → provider, mechanism, input/output and consequential behaviour |
| Flow | Trigger, important transformations or hand-offs, observable result |
| Constraint | What must remain true, consequence and enforcement point |
| Recipe | Prerequisites, action, expected result and verification |

- Document shared state and implicit file/configuration contracts as relationships, not only explicit function or network calls.
- Include authentication, errors, retries, ordering, versioning and compatibility only where they affect a selected task.
- Explain a pattern through one representative implementation and important exceptions. Omit textbook definitions.
- Keep one canonical explanation per topic. Link to schemas, tests and generated reference rather than copying exhaustive fields or options.
- Expand unfamiliar acronyms at first use. Use the user's spelling conventions.

## Agent instruction files

Treat `AGENTS.md`, `CLAUDE.md` and similar files as documentation with a distinct reader: an agent that already reads code quickly but cannot see intent, history or live-system behaviour.

- Keep in them: repository rules, non-obvious constraints, commands that must or must not be run, and hard-won lessons that cannot be derived from source or docs.
- Link to the canonical doc for anything else; do not maintain a parallel copy of architecture, layout or procedures.
- Remove entries that only record that something was deleted or renamed, that restate the code, or that duplicate a canonical doc.
- Apply the same verification as human-facing docs: stale agent instructions actively cause wrong changes.

## Handle weak or unclear boundaries

- Infer navigation from executable entry points and traced behaviour, not directory names alone.
- Call a grouping an "area" or "responsibility" unless source supports a module, service or layer boundary.
- Label conceptual groupings explicitly. Never imply process, deployment, security or ownership isolation merely from a diagram or folder.
- Explain consequential overlap concretely: which task crosses which files, which state is shared, or which checks must change together.
- Mark material inferences and unverified behaviour beside the affected claim. Do not invent historical rationale, owners or contracts.
- Document existing behaviour even when untidy. Keep proposed improvements distinct and include them only if requested or needed to explain a blocked task.
- Mention planned work only where it changes what a reader should do now: one line beside the affected limitation, linking to the canonical plan or issue. Keep plans in one place (plan document or tracker), not in a documentation roadmap section.

## Choose visuals sparingly

Default to zero diagrams; usually use at most one overview. Add another only when a distinct reader question needs it.

| Reader question | Preferred representation |
| --- | --- |
| What depends on what? | Small diagram with labelled relationships |
| What happens in this operation? | Sequence diagram when ordering matters |
| Which transitions are valid? | State diagram |
| Where should I change something? | Task-to-code table |
| Which data connects responsibilities? | Small entity relationship diagram when essential |

Use the repo's established editable diagram format, otherwise Mermaid. Keep one abstraction level per visual. Distinguish creation, readiness, runtime calls and data movement. Label edges meaningfully. Omit decorative directory trees, exhaustive dependency graphs and visuals that repeat adjacent prose. Use tables or prose for simple relationships.

## Verify, prune and finish

- Check source links, paths, command names and required wiring. Check examples against active code and configuration. Before directing a reader to an existing guide or validation workflow, inspect the relevant instructions; flag broken paths or stale steps beside the link instead of endorsing them as a working route.
- Check paths mechanically where practical: extract backticked or linked repo paths from the docs and test that each exists. Expect false positives (resource names such as `namespace/name`, placeholders, intentional historical references) and review hits rather than deleting them blindly.
- Run available non-destructive syntax, render or dry-run checks relevant to the documentation. Do not assume a dry-run flag or mode is honoured by every command: inspect its implementation first.
- Do not install, tear down, deploy, rotate credentials or perform other live mutations solely to verify prose. Explain consequential destructive effects where a recipe exposes them; avoid exposing secret values.
- State precisely what was checked and what remains unverified. A render is not a successful deployment; configuration presence is not credential validity.
- Keep only gaps that affect the selected tasks. Place caveats near the action they change, with a compact consolidated list only if useful.
- Prune generic advice, speculative architecture, stale instructions and detail already maintained elsewhere.
- Stop when a reader can identify the purpose, complete a useful action, locate a likely change point and discover constraints without reading everything.

Finish according to mode:

- **Author, artefact, sync**: deliver the documentation with a brief statement of coverage, verification and material limitations. Do not turn the final response into another copy of the guide.
- **Assess**: report findings ranked by impact on the selected reader tasks. Give each finding its evidence (file and line, or the command that exposed it), the consequence for the reader, and a concrete fix. Note what the docs do well so it is preserved. Do not rewrite the docs unless asked.

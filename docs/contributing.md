# Contributing

How to change this repo without breaking the cluster or the docs. Commits go straight to `main`; CI ([`.github/workflows/validate.yml`](../.github/workflows/validate.yml)) is the gate, and Flux applies `main`.

## Agent-assisted changes

Use the [software change workflow](software-change-workflow.md) to agree an outcome, observable contracts, design boundaries, authority and evidence with a coding agent. It includes an operator quick start, a reusable task-record template and an illustrative promotion-preview proposal. Invoke it explicitly in your request; it is a procedure, not an installed command or permission to commit or deploy.

## Validation

Run before every push:

```bash
make check
```

`check` runs `check-repo` (`swhurl-v2 check-repo` decides relative Markdown links, the two tables the docs keep in step with Git, the names v2's compiled tree gives apps, SOPS structure, required Secret files and settings, and the active render paths; `v2/check-repo.sh` renders each path, substitutes it as Flux does and checks shell syntax; `SCHEMA=1` adds Kubernetes and Flux schema validation with `flux-schema`, which CI does not run; needs Go; never contacts the cluster), `test` (the unit tests in `tests/`), `check-apps` (zero-argument Go render equality and actual chart-output policy over every `v2-records` instance including production; team/channel validation uses the same whole-tree compiler path; needs Go and Helm, with explicit Go chart acquisition into the local cache; no Python on this target's path; [ownership](apps.md#the-app-policy)), `check-otel` (`platform/otel/check.sh`: renders each collector's HelmRelease with `helm template` and validates the rendered config, unmodified, in the image the render names, then runs the log fixtures through its processors in that image; only the pod's environment is stubbed; podman locally, `CONTAINER_RUNTIME=docker` in CI; no Python), `check-lint` (Ruff and ShellCheck at the versions CI pins) and `check-docs` (compares the [workstream registry](workstreams.md) with Git and checks each progress page's shape and size, so it is overwritten rather than appended to; `swhurl-v2 workstreams`, needs Go). `make check` runs them in parallel and keeps each one's output together. It leaves out `check-templates` (`v2/check-templates.sh`: every app type's rendered app through `swhurl-v2 names`, `swhurl-v2 image-declaration` and `swhurl-v2 declaration`; reads the templates from GitHub), which needs the network and runs in CI on every push: run it locally when a change touches a template pin or the app types (`v2/contract/stacks.json`), the names catalogue (`v2/contract/names.json`) or what the producer writes, so a refusal shows before the push (it builds `swhurl-v2` from `v2/` when `SWHURL_V2` names none, and CI sets `SWHURL_V2` to its own build). GitHub Actions runs the rendering and fixture checks in parallel jobs, alongside the operator tests, lint and dry runs. Every pull request and push runs every job. A pull request or push that changes only input records (paths under `v2-records/` and a team's channel Secret, `platform/notify/channels/channel-<team>.sops.yaml`; [`.github/records-only.sh`](../.github/records-only.sh) decides) is rendered with `make -s v2-render` in the workspace before `check-repo` and `check-apps`, so it is checked as `main` will hold it once the [Render workflow](../.github/workflows/render.yml) has rendered it; any other change is checked as committed, so a commit that leaves rendered files stale still fails. The offline collector fixture allows up to 30 seconds for startup and processing under parallel load, stopping as soon as its records flush. The template job caches Copier's `uvx` environment. Template sources are shallow-cloned once per stack during each check. Only Copier, run through `uvx`, is Python on the template check's path; the stack and name catalogues are v2's JSON files.

The [producer contract](iteration-2-plan.md#producer-first-python-retirement-approved-5-november-2026)
has frozen Python-produced full-label goldens under `v2/adapters/appsource/testdata/producer.json`.
Do not regenerate them from the Go implementation: flags bytes affect release identity, so compare the
whole line, not just decoded JSON. The adapter's CRLF events fixture is marked `-text` in
`.gitattributes` so Git preserves its exact bytes. `producer` owns source rules/encoding, `adapters/appsource` owns
explicitly named file/YAML reads (including the original symlink behavior), and the real/fake reader
share a contract suite. The parser
exception is exactly that adapter, Helm and `adapters/manifests` (the checkout's Kubernetes YAML, for `check-repo`); no new dependency is needed. Run `make v2-deps` once before
offline binary builds; Validate already bootstraps its template job's binary dependencies.

Run `make v2-deps` once after checkout or a v2 dependency pin change, before offline builds/checks; CI and the console image build do so explicitly ([build contract](iteration-2-plan.md#offline-cli-and-build)).

Prerequisites (CI pins the same): `kubectl`, `helm`, `uv`, `jq`, Go 1.26 for `check-v2`, `check-apps`, `check-repo` and `check-docs` (the version `v2/go.mod` states), and Python 3 with the `check` dependency group from [`pyproject.toml`](../pyproject.toml) (PyYAML). `make test` runs in the environment `uv` builds from `uv.lock` (the `check` and `console` groups); after changing a group, run `uv lock` and commit `uv.lock`. `make console-dev` builds and runs v2's Go console.

`make check-repo SCHEMA=1` also needs `flux-schema`, which no CI job installs or runs ([why](iteration-2-plan.md#17-check-repo-in-go-the-decisions-not-the-subprocesses-approved-8-november-2026)):

```bash
go install github.com/fluxcd/flux-schema/cmd/flux-schema@v0.9.0
export PATH="$(go env GOPATH)/bin:$PATH"
```

CI also runs every `DRY_RUN=true` target and `REQUIRE_HELM=1` so Helm-based tests cannot silently skip. `make check-secrets` needs the age key, so it runs locally only.

## Operator tooling

**Bounded Go exception:** the offline v2 model, compiler and preview comparator are a separate module in [`v2/`](../v2/go.mod), with Go-native tests and direct CLI invocation. External-effect adapters need separate approval; v1 retains all rules below. The [phase-1 contract and build instructions](iteration-2-plan.md#2-phase-1-model-and-contract) are canonical. `make check` does not run Go checks: run `make check-v2` (format, vet, tests and v2's [design guardrails](iteration-2-plan.md#guardrails)) as well as the applicable v1 checks. CI runs it in the tooling job, with Go set up from `v2/go.mod`, the chart v2's profile pins fetched first and `REQUIRE_HELM=1`, so its manifest check cannot skip.

**Code graph exception:** the read-only [code graph](visual-change-tool.md) development aid is a separate standard-library package in [`devtools/codegraph/`](../devtools/codegraph), not in the console image. It reads Git through its own revision reader, is not a `swhurl` command, and never imports or runs the code it analyses. `make check-codegraph` runs its tests (part of `make check` and CI); `make check-lint` lints it. Its [primitives and contracts](visual-change-tool.md#4-primitives-and-contracts) are canonical.

`make` is the operator interface, and every target is one line in the Makefile's header (below). New operator logic is a `swhurl-v2` subcommand ([Go exception](#operator-tooling) above) or bash; no target runs Python. The Python left is the tests in `tests/`, the code graph aid, the host's connectivity probe ([`host/connectivity-probe.py`](../host/connectivity-probe.py)) and Copier in the console image. Choose the language by what the code does:

- **Go** (`swhurl-v2`) for anything that parses JSON or YAML or edits structured files, makes a safety decision or refusal, handles or compares Secret values, polls or cleans up after failure, or produces a pass/fail verdict.
- **Bash** for short linear glue (the commands that are one or two `kubectl` or `flux` calls: `app-logs` and `app-reconcile` run [`host/ops.sh`](../host/ops.sh), which checks its names before any call and is tested through `make` with fake executables), a streaming pipe nothing inspects, code that runs where Python dependencies are not guaranteed (the host timer installer, [`host/install-timer.sh`](../host/install-timer.sh), and its systemd unit templates), and command lists that double as documentation. `make suspend` and `make resume` instead call `swhurl-v2 unit --target` ([lifecycle](operations.md#lifecycle)). Kept bash uses `set -Eeuo pipefail`, has a dry-run path where it changes anything, and passes `make check-lint`; when it grows real logic, the logic moves to `swhurl-v2` and the script calls it.

- v2 reaches Kubernetes through the API server, never `kubectl` ([development layers](iteration-2-plan.md#4-development-layers-and-adapters)). The console is Go, so nothing serves `kubectl` output from inside a pod.
- A new target is a recipe and one line in the Makefile's header, `## <target> | <touches> | <dry run> | <help>[ | <variables>]`, under its `##@ <section>` line: what it touches (`Local`, `Git`, `Cluster (read)`, `Cluster`, `Cluster (throwaway)`, `GitHub`, `Host`), what `DRY_RUN=true` does (`plan`: prints a plan and touches nothing, which `make test` then proves; `reads`: reads without changing; `-`: none, and the recipe refuses `DRY_RUN=true`), and its variables as help shows them, optional ones in `[]`. Add a row to [commands](commands.md); `make help` prints the lines ([`tools/make-help.awk`](../tools/make-help.awk)), and `tests/test_commands.py` fails when the lines, the Makefile's targets and the rows differ. A name starting `check` must be `Local` and one starting `verify-` must be `Cluster (read)`. A recipe refuses a missing variable with its own `Usage: make …` line.
- Words: app, environment, instance and unit are defined in the [glossary](../GLOSSARY.md); a container's settings are **variables**; the app another app calls is its **callee**; "service" is kept for what the platform provides, in the console too: one **Deployments** list serves the person using an app, the developer connecting to it and the operator (the app page's Use, Connect and Operate parts). Commands are verbs (`set_calls`) and policy rules are `check_*`.
- The console's image is [`images/console/Dockerfile`](../images/console/Dockerfile), built from the repository root (`podman build -f images/console/Dockerfile .`; `.dockerignore` admits only what it copies: `v2/` for the binary, and `pyproject.toml` and `uv.lock` for Copier). On each push to `main`, [`validate.yml`](../.github/workflows/validate.yml)'s `console-image` job builds it alongside the checks and pushes it to `ghcr.io/samclement/swhurl-console`, tagged with the commit SHA and `src-<hash of its inputs>`, and skips the build when that hash is already published. The tag is computed once, by [`images/content-tag.sh`](../images/content-tag.sh) (`console` or `backup`), which the workflow's two image jobs and `make verify-platform` run; `make console-image` ([`images/pin-console.sh`](../images/pin-console.sh)) pins the image of that tag (`content-tag.sh console --committed`, which refuses uncommitted inputs). How Render, Deploy and Validate's console image pin push to `main` is [`host/push-main.sh`](../host/push-main.sh), tested against real Git in `tests/test_push_main.py`.
- Console pages: Jinja templates in `v2/cmd/swhurl-v2/console/templates` and one stylesheet, [`static/console.css`](../v2/cmd/swhurl-v2/console/static/console.css). Use its colour roles (`--ok`, `--attention`, `--critical`, `--accent`) and type sizes rather than new values, the `state` and `icon` macros in `_status.html` for a status or an icon, and `data-confirm="what will change"` on a form that stops or removes something. No CSS framework and no build step: htmx, the fonts and the icons are files in `static/`, listed with their licences in [`static/LICENSES.md`](../v2/cmd/swhurl-v2/console/static/LICENSES.md); pages load nothing from another host (tested). A page that only shows state joins `SELF_REFRESHING` in `server.py`.
- The tests in `tests/` check what is in Git (manifests, workflows, the images) and run the make targets and scripts; each finds the repository from its own path (`ROOT = Path(__file__).resolve().parents[1]`). `make test` runs [`tests/run.py`](../tests/run.py), one test per job across worker processes, or one whole class when it has `setUpClass` (or its module `setUpModule`) so that setup runs once (`-j N` sets the count, `-v` lists jobs, `-k PATTERN` or `make test K=PATTERN` runs only the classes whose dotted name contains it) and ends with the slowest classes, or, when something failed, the `make test K=<module>` command that re-runs just the modules that failed. To run one file: `PYTHONPATH=tests uv run --frozen python -m unittest test_commands`. Fake `kubectl`, `flux` and `swhurl-v2` executables on `PATH` stand in for the cluster.
- Recipes hold sequencing, host scripts and the glue around one `swhurl-v2` call (build it, export a private kubeconfig, pass the make variables); a decision belongs in `swhurl-v2`. Use `make` functions (`$(if)`, `$(foreach)`) for plain sequencing.
- Operator commits require the full `make check`. A console pull request gets Validate, every job of it, and a person merges it. See [review and merge](console.md#review-and-merge).

## Checklist for a change

- **Where it goes:** a shared capability gets its own directory and Flux unit in `clusters/home/infra.yaml` or `platform.yaml`, with explicit `dependsOn`, `deletionPolicy: Orphan`, substitution only if its manifests use `${...}`, and decryption only if its path holds `*.sops.yaml`; add its row to the [Flux units table](architecture.md#flux-units), which `make check-repo` compares with Git (`platform.units()` is the inventory code and tests read). Apps are created by `make app-repo` and the `swhurl-v2 create` line it prints ([start a new app](apps.md#start-a-new-app)).
- **Secrets:** beside the consumer, following the [Secret rules](operations.md#secrets); add a `.sops.yaml` rule for any new path.
- **Checks:** for a live invariant, write one pure function in [`v2/observe`](../v2/observe) over the evidence it needs, read that evidence in `orchestration/platform_scope.go`, and add the group to [the catalogue](../v2/observe/platform_catalogue.go) with a sentence saying what passing means; the [operations table](operations.md#check-health) is that catalogue (a Go test fails when they differ), table-tested beside the check and in `tests/` for anything checkable offline. Prefer declarative wiring over new feature switches (`SKIP_VERIFY` is the only one).
- **Live proof:** for behaviour that only shows on the cluster, add or run a throwaway test like `make live-test-lifecycle`, then record what you saw, with the date and commit, in [current state](current-state.md).
- **Charts:** pin `version:` in the HelmRelease and keep the file and its HelmRepository under `apps/`, `clusters/`, `infra/` or `platform/`, where [Renovate](operations.md#chart-updates) finds them.
- **Root units** in `clusters/home/flux-system/kustomizations.yaml` are not reconciled by Flux; run `make flux-bootstrap` after changing them.
- **App-template fixture:** `tests/fixtures/app-template/records/` holds the records of the two throwaway instances `live-test-app-template` deploys (team `smoke`; `smoke-worker` and `smoke-web`, both `prod`, on the demo apps' images by digest). The rest of `tests/fixtures/app-template/` is their render, committed because Flux reads Git: `make v2-render` writes it beside the platform's tree, and `make check-v2` fails while it differs from what its records render. Change the records and render; never edit a rendered file by hand.

## Documentation

- Claude Code sessions can apply the repository's documentation approach with the [`document-repo` skill](../.claude/skills/document-repo/SKILL.md) (`/document-repo`), including syncing docs to a change. Draft working skills (`decision-brief`, `new-component-checklist`, `phase-handoff`) sit beside it; `AGENTS.md` says when each applies.
- Update the docs in the same commit as the behaviour. Each topic has one page; link rather than repeat:

  | Topic | Page |
  | --- | --- |
  | Everyday loop, task index | [README](../README.md) |
  | Reusable agent-assisted software change procedure | [Software change workflow](software-change-workflow.md) |
  | Where each workstream stands and what finishes it, and how one resumes | [Workstreams](workstreams.md) |
  | Go replacement: the goal, what it has done, the remaining Python, the deferred features | [v2 status](v2-status.md) |
  | Bare host to running platform | [bootstrap](bootstrap.md) |
  | Health, Secrets, certificates, lifecycle, chart updates, backups, troubleshooting | [operations](operations.md) |
  | Every `make` target | [commands](commands.md) |
  | Shared services, settings, keys | [services](services.md) |
  | App lifecycle (template to removal), generator, policy | [apps](apps.md) |
  | Web console: use, deploy, token, protection | [console](console.md) |
  | Units, dependencies, ownership, how changes reach the cluster | [architecture](architecture.md) |
  | Dated live evidence | [current state](current-state.md) |
  | Remaining planned work | [plan](plan.md) section 0 |

- Describe what is, not what was: git history records removals.
- Put a caveat next to the step it affects. Mark anything not verified on the cluster.

## Diagrams

Diagrams are Mermaid blocks in the page they explain; GitHub renders them, so there is nothing to generate or commit besides the text. Keep external systems outside the cluster subgraph, label only the edges that carry meaning, and show `Let's Encrypt (ACME)` wherever cert-manager appears and the telemetry path (app → OTel collector → ClickStack) in the container view. To check a diagram locally: `npx -y @mermaid-js/mermaid-cli -i diagram.mmd -o diagram.svg`.

The [repository blueprint](repository-blueprint.html) is the one generated diagram: a self-contained page built from a JSON model by the `repository-blueprint` skill's `build_blueprint.py`. To change it, open the page, take the model from **Edit DSL** → **Download**, edit it and rebuild; evidence links are relative to `docs/`.

# Contributing

How to change this repo without breaking the cluster or the docs. Commits go straight to `main`; CI ([`.github/workflows/validate.yml`](../.github/workflows/validate.yml)) is the gate, and Flux applies `main`.

## Validation

Run before every push:

```bash
make check
```

`check` runs `check-repo` (renders every active Flux path, validates Kubernetes and Flux schemas, SOPS structure, shell syntax, required substitutions and relative Markdown links; never contacts the cluster), `test` (the unit tests in `tests/`), `check-apps` (renders app instances with Helm against the app contract), `check-otel` (validates the rendered collector configs with the collector release they will run; `swhurl/otel.py`) and `check-lint` (Ruff and ShellCheck at the versions CI pins). `make check` runs the five in parallel and keeps each one's output together. CI runs the same steps.

Prerequisites (CI pins the same): `kubectl`, `helm`, `uv`, Python 3 with the `check` dependency group from [`pyproject.toml`](../pyproject.toml) (PyYAML), and `flux-schema`. `make test` and `make console-dev` run in the environment `uv` builds from `uv.lock` (the `check` and `console` groups); after changing a group, run `uv lock` and commit `uv.lock`:

```bash
go install github.com/fluxcd/flux-schema/cmd/flux-schema@v0.9.0
export PATH="$(go env GOPATH)/bin:$PATH"
```

CI downloads the same version's release binary and checks its SHA-256 (`.github/workflows/validate.yml`); bump the URL and checksum together.

CI also runs every `DRY_RUN=true` target and `REQUIRE_HELM=1` so Helm-based tests cannot silently skip. `make check-secrets` needs the age key, so it runs locally only.

## Operator tooling

All operator logic lives in the Python package [`tools/swhurl/`](../tools/swhurl) (live tests in `swhurl/livetests/`), run as `python3 -m swhurl <command>` with `tools/` on `PYTHONPATH`; the Makefile's `$(SWHURL)` does that, and `make` stays the operator interface. Choose the language by what the code does:

- **Python** for anything that parses JSON or YAML or edits structured files, makes a safety decision or refusal, handles or compares Secret values, polls or cleans up after failure, or produces a pass/fail verdict.
- **Bash** for short linear glue, a streaming pipe nothing inspects, code that runs where Python dependencies are not guaranteed (the host timer installer, [`host/install-timer.sh`](../host/install-timer.sh), and its systemd unit templates), and command lists that double as documentation (`tests/fixtures/apps.sh`). Kept bash uses `set -Eeuo pipefail`, has a dry-run path where it changes anything, and passes `make check-lint`; when it grows real logic, the logic moves to `swhurl` and the script calls it.

- Call external tools only through `swhurl.run.Runner`. It handles `DRY_RUN` (pass `mutating=True` for changes), redacts registered Secret values from every message, and can hide a command's output from errors (`secret_output=True`). Read output live, line by line and redacted, with `Runner.stream(args)` (closing it stops the command). Stream sensitive data with `Runner.pipe(producer, consumer)`: the processes share an OS pipe Python never reads, and either side failing raises (like `pipefail`). Pass credentials on stdin (`input=`), never in argv: `clickstack.py` sends mongosh and API scripts this way, and backups give `mongodump` its login as a config file on stdin.
- Report through `swhurl.report.Report` (`[OK]`/`[BAD]`/`[WARN]` and the exit code).
- Unit-test with `swhurl.run.FakeRunner`: it records calls and answers from argv-prefix rules, and fails on any unexpected command.
- A new command is a function `(argv) -> int`, registered in `COMMANDS` in `swhurl/__main__.py` and given a Makefile alias.
- Layout: `run.py` and `report.py` (foundation); `platform.py` (repository paths, label names, Flux unit discovery; one copy, not a constant per module); `apps/` (`contract.py` rules shared by `new.py` and `policy.py`, `ops.py`, and `edit.py` for promote, scale and remove); `verify.py`, `validate.py`, `settings.py`, `secrets_check.py`, `lifecycle.py`, `clickstack.py` (first-run bootstrap; every MongoDB script, each printing one `RESULT` line, and HyperDX API access, all over stdin), `recovery.py` (backup, S3 upload and restore test), `retention.py` (pruning); `livetests/`; `console/` (the web console: `cluster.py` reads, `actions.py` runs background jobs (reconcile, suspend, resume), `changes.py` opens PRs from a clone of `main` using that clone's tooling, `server.py` and `templates/` serve; see [plan](plan.md) section 7). Its image is [`images/console/Dockerfile`](../images/console/Dockerfile), built from the repository root (`podman build -f images/console/Dockerfile .`; `.dockerignore` admits only what it copies). After `Validate` passes on `main`, [`publish-console.yml`](../.github/workflows/publish-console.yml) pushes it to `ghcr.io/samclement/swhurl-console`, tagged with the commit SHA and `src-<hash of its inputs>`, and skips the build when that hash is already published. `make console-image` (`swhurl/images.py`) computes the same hash and pins that image; a test checks it matches the workflow's shell.
- Tests import `swhurl` directly (`from swhurl import ROOT`); `make test` puts `tools/` on `PYTHONPATH` and runs [`tests/run.py`](../tests/run.py), one test class per worker process (`-j N` sets the count, `-v` lists classes). To run one file: `PYTHONPATH=tools uv run --frozen python -m unittest tests.test_verify`. `test_command_safety.py` is the only place that uses fake executables, for what only a real process shows; everything else uses `FakeRunner`.
- Makefile recipes stay aliases and sequencing: no shell `if`/loops, `sed`, `grep` or `jq`. Use `make` functions (`$(if)`, `$(foreach)`) for plain sequencing and move anything else into `swhurl`.

## Checklist for a change

- **Where it goes:** a shared capability gets its own directory and Flux unit in `clusters/home/infra.yaml` or `platform.yaml`, with explicit `dependsOn`, `deletionPolicy: Orphan`, substitution only if its manifests use `${...}`, and decryption only if its path holds `*.sops.yaml`. Apps use `make app-new`.
- **Secrets:** beside the consumer, following the [Secret rules](operations.md#secrets); add a `.sops.yaml` rule for any new path.
- **Checks:** extend [`tools/swhurl/verify.py`](../tools/swhurl/verify.py) for live invariants (one function per check, unit-tested in `tests/test_verify.py`) and `tests/` for anything checkable offline. Prefer declarative wiring over new feature switches (`SKIP_VERIFY` is the only one).
- **Live proof:** for behaviour that only shows on the cluster, add or run a throwaway test like `make live-test-lifecycle`, then record what you saw, with the date and commit, in [current state](current-state.md).
- **Charts:** pin `version:` in the HelmRelease and keep the file and its HelmRepository under `apps/`, `clusters/`, `infra/` or `platform/`, where [Renovate](operations.md#chart-updates) finds them.
- **Root units** in `clusters/home/flux-system/kustomizations.yaml` are not reconciled by Flux; run `make flux-bootstrap` after changing them.
- **Generator changes:** regenerate the fixtures (`rm -rf tests/fixtures/apps && tests/fixtures/apps.sh`); `test` fails if they drift.

## Documentation

- Claude Code sessions can apply the repository's documentation approach with the [`document-repo` skill](../.claude/skills/document-repo/SKILL.md) (`/document-repo`), including syncing docs to a change. Draft working skills (`decision-brief`, `new-component-checklist`, `phase-handoff`) sit beside it; `AGENTS.md` says when each applies.
- Update the docs in the same commit as the behaviour. Each topic has one page; link rather than repeat:

  | Topic | Page |
  | --- | --- |
  | Everyday loop, task index | [README](../README.md) |
  | Bare host to running platform | [bootstrap](bootstrap.md) |
  | Health, Secrets, certificates, lifecycle, chart updates, backups, troubleshooting | [operations](operations.md) |
  | Every `make` target | [commands](commands.md) |
  | Shared services, settings, keys | [services](services.md) |
  | App instances, generator, policy | [apps](apps.md) |
  | Web console: use, deploy, token, protection | [console](console.md) |
  | Units, dependencies, ownership | [architecture](architecture.md) |
  | Dated live evidence | [current state](current-state.md) |
  | Remaining planned work | [plan](plan.md) section 0 |

- Describe what is, not what was: git history records removals.
- Put a caveat next to the step it affects. Mark anything not verified on the cluster.

## Diagrams

Diagrams are Mermaid blocks in the page they explain; GitHub renders them, so there is nothing to generate or commit besides the text. Keep external systems outside the cluster subgraph, label only the edges that carry meaning, and show `Let's Encrypt (ACME)` wherever cert-manager appears and the telemetry path (app → OTel collector → ClickStack) in the container view. To check a diagram locally: `npx -y @mermaid-js/mermaid-cli -i diagram.mmd -o diagram.svg`.

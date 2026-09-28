# Sub-plan: operator tooling in the right language

28 September 2026 · revised the same day to decide per piece what stays bash · sub-plan of the [implementation plan](Swhurl-platform-implementation-plan.md) · not started

## Goal

Make the operator tooling readable and testable without changing what operators type. Every `make` target keeps its name, arguments, `DRY_RUN` behaviour, exit codes and `[OK]`/`[BAD]` output. Logic moves to Python; glue that is clearest as shell stays bash and gets linted.

**Readable** means one language, named functions with docstrings, structured data instead of `jq`/`sed`/`grep` pipelines, and no `set -e` edge cases. **Testable** means every decision can be unit-tested offline with a fake cluster, including refusals, dry runs, failures and the rule that Secret values are never printed.

## Bash or Python

Choose by what the code does, not by file.

**Python** for code that:

- parses JSON or YAML, or edits structured files (`jq`, `sed`, `grep` on YAML);
- makes safety decisions or refusals (`destroy-data`, label checks before deleting);
- handles or compares Secret values;
- polls, waits, retries, or must clean up after a failure;
- has branches worth testing one by one, or produces a pass/fail verdict.

**Bash** for code that:

- is short, linear glue running CLI tools in order, with little or no branching on data;
- is a streaming pipe where the shell is the clearest expression and nothing is inspected on the way;
- runs where Python dependencies are not guaranteed (a systemd unit on the host, a bare host before bootstrap);
- is a list of commands that doubles as documentation;
- is a Makefile recipe of one or two lines.

Applied to today's code:

| Code | Verdict | Why |
| --- | --- | --- |
| `scripts/verify-platform.sh` | Python | Parses JSON, compares Secrets by bytes, many branches, produces the verdict |
| `scripts/app.sh` | Python | `status` is mostly `jq` shaping; `logs`/`reconcile` follow along to keep one CLI |
| `scripts/lifecycle.sh` | Python | `destroy-data` refusals are the most safety-critical logic in the repo |
| `scripts/*-test.sh` (lifecycle, reloader, app-template) and `restore-test-clickstack-mongodb.sh` | Python | Assertions, polling, cleanup on failure, label checks |
| `scripts/backup-clickstack-mongodb.sh` | Python (borderline) | The `mongodump \| age` stream is natural shell, but the script also parses `.sops.yaml`, hand-writes JSON metadata and prunes. A `Popen` pipe keeps the stream off disk just as well, and the restore test that checks it is Python |
| Makefile `platform-certs-*` | Python | `sed` on YAML; should be a parsed edit with a test |
| Makefile `wait-runtime-inputs-otel`, `verify-config` | Python | A polling loop, and checks that belong with `verify-platform` |
| Makefile `install`, teardown guard, `otel-collectors-restart`, `runtime-inputs-sync`, `host-dns*` | Stay in the Makefile | Sequencing, a refusal message and a few CLI calls; one or two lines each once the loops move out |
| `scripts/generate-charts.sh` | Bash | A loop running `d2` over a fixed list |
| `tests/fixtures/apps.sh` | Bash | Reads exactly like the generator commands a user types; the drift test already covers it |
| `host/dynamic-dns.sh`, `host/aws-dns-updater.sh` | Bash | Installer and systemd glue (`sudo`, `systemctl`, `curl`, `aws`) on a host where Python dependencies are not guaranteed; the branching is arguments and dry-run, not data |

Bash that stays is held to a bar: `set -Eeuo pipefail`, no `jq`/`sed` data manipulation beyond trivial trimming, `shellcheck`-clean in CI, and a `--dry-run` or `DRY_RUN=true` path exercised by CI. If a kept script grows real logic, that logic moves to Python and the script calls it.

## What exists

| Area | Files | Lines | Notes |
| --- | --- | --- | --- |
| Operator scripts | `scripts/*.sh` (10) | ~785 | Heavy `kubectl`/`flux`/`jq` use; two stream Secrets (`backup`, `restore-test`) |
| Host DNS | `host/dynamic-dns.sh`, `host/aws-dns-updater.sh` | ~383 | Run by systemd as a timer; `sudo`, `systemctl`, `aws`, `curl` |
| Makefile inline shell | `install`, `teardown`/`reinstall`, `platform-certs-*` (`sed`), `verify-config`, `wait-runtime-inputs-otel`, `otel-collectors-restart`, `host-dns*` | ~100 | Hard to test at all today |
| Fixture script | `tests/fixtures/apps.sh` | 14 | Calls the Python generator |
| Python already | `app-new.py`, `app_policy.py`, `validate-repo.py`, `secrets_check.py`, `prune-backups.py` | ~1,370 with tests | Stdlib plus PyYAML; each a standalone script |

Current tests reach bash only end to end, through fake `kubectl`/`flux` executables on `PATH` (`tests/test_operator_safety.py`). That catches regressions but makes each branch expensive to test.

## Design

**One package for the logic, one entry point.** Create `tools/swhurl/` (package `swhurl`) run as `python3 -m swhurl <command>`. The Makefile becomes a thin table of one-line aliases, for example `destroy-data: ; python3 -m swhurl destroy-data "$(TARGET)" --confirm "$(CONFIRM)"`. The existing Python scripts move into the package, leaving short shims at their old paths until the docs and CI are updated.

```
tools/swhurl/
  __main__.py      argparse subcommands mirroring the make targets
  run.py           Runner: the only place that calls kubectl, flux, helm, sops, age, mongodump
  report.py        ok/bad/info output, exit codes, dry-run plans
  kube.py          typed helpers on Runner: get_json, exec_in, flux_reconcile, wait_ready
  secrets.py       decrypt in memory, compare by hash, never stringify values
  verify.py  lifecycle.py  backup.py  apps.py  settings.py
  livetests/       lifecycle, reloader, app_template, restore: steps plus cleanup
```

**Keep the CLI tools; don't adopt the Kubernetes Python client.** `kubectl`, `flux` and `helm` already handle kubeconfig, Flux reconcile semantics, Helm rendering and SOPS the way the cluster expects. Wrapping them keeps behaviour identical and dependencies at stdlib plus PyYAML. `jq` disappears, because JSON is parsed in Python.

**The Runner is the seam that makes this testable.** Every command goes through `Runner.run(args, *, input=None, secret=False)`, which returns structured output. Tests inject a `FakeRunner` that records calls and returns scripted responses. With it, tests can assert things like "`destroy-data` without `CONFIRM` made zero cluster calls" or "the verifier never wrote the key to output", without executables on `PATH`. The Runner also:

- honours `DRY_RUN` centrally: mutating calls are printed, not run, and read-only calls still run;
- scrubs Secret values from logs, from its own errors and from `CalledProcessError` text (plain subprocess errors include command output, which would leak keys);
- supports streaming pipelines (`Popen` chains) for `mongodump | age` and `age -d | kubectl exec -i`, so plaintext still never touches disk.

**Cleanup without `trap`.** Live tests and restore checks use `contextlib.ExitStack`, so throwaway namespaces and volumes are removed on success, failure or Ctrl-C, and only when they carry the `platform.swhurl.com/<test>=true` label.

**Python version:** 3.11 minimum (the host has 3.14, CI uses 3.12). Type hints throughout; checking them is optional (see decisions).

## Phases

Each phase is one commit to `main` (or a few), converts whole targets end to end, and deletes the bash it replaces. The acceptance bar for every phase:

- make targets and their outputs unchanged;
- new unit tests cover each branch;
- the existing `test_operator_safety.py` contract tests still pass against the new code;
- CI green;
- the relevant live check run on the cluster, with evidence recorded in `docs/operations/current-state.md`;
- `docs/commands.md` and `docs/contributing.md` updated if anything user-visible moved.

| Phase | Converts | Why this order | Live check |
| --- | --- | --- | --- |
| 0. Foundation | Package skeleton, `Runner`/`FakeRunner`, `report`, `__main__`; move `prune-backups`, `secrets_check`, `validate-repo`, `app_policy`, `app-new` into the package behind shims | No behaviour change; gives every later phase its test harness | `make validate-repo`, `make secrets-check` |
| 1. Read-only | `verify-platform.sh` (absorbing `verify-config`), `app.sh` (status, logs, reconcile, check) | Cannot harm the cluster; `verify-platform` has the most branches and the secrecy rule | `make verify-platform`, `make app-status` |
| 2. Makefile logic | `platform-certs-*` (a parsed YAML edit instead of `sed`) and `wait-runtime-inputs-otel` (polling). Sequencing recipes stay in the Makefile and shrink to one or two lines | Removes the only untested logic in the Makefile | `make platform-certs-staging DRY_RUN=true`, `make runtime-inputs-refresh-otel` |
| 3. Backup and restore | `backup-clickstack-mongodb.sh`, `restore-test-clickstack-mongodb.sh` | Streaming Secrets: tests must show no plaintext reaches disk or output | `make backup-clickstack-mongodb`, `make restore-test-clickstack-mongodb` |
| 4. Lifecycle | `lifecycle.sh` (`suspend`, `resume`, `destroy-data`) | The only data-deleting command; gains the most from exhaustive refusal tests | `make lifecycle-test` |
| 5. Live tests | `lifecycle-test.sh`, `reloader-test.sh`, `app-template-test.sh` | Converted after the code they test, so each is proven against the new implementation | All four live tests |
| 6. Bash that stays | Add `shellcheck` to CI for `host/*.sh`, `scripts/generate-charts.sh`, `tests/fixtures/apps.sh` and fix findings; confirm each meets the bar above | Keeps the remaining shell honest without rewriting it | `make host-dns DRY_RUN=true`, `make charts-generate` |

Rough effort: phase 0 about a day; phases 1–5 half a day to a day each; phase 6 an hour or two. About 4–6 days in total, all of it local or throwaway on the cluster. Roughly 700 lines of bash and Makefile shell move to Python; about 450 lines stay bash.

## Testing strategy

- **Unit tests** (`tests/unit/`, offline, in `make test-safety`) with `FakeRunner`. They cover every refusal, dry-run plan, failure message and exit code, plus each output check. Every command that handles a Secret gets a test that feeds a known fake value and asserts it appears nowhere in stdout, stderr or recorded logs.
- **Contract tests:** keep the current fake-executable tests for a handful of full `make` invocations, so the interface is proven end to end, not just the Python functions.
- **Live tests:** unchanged in what they prove; run each one after the phase that touches its code.
- **Parity:** before deleting each bash script, run the old and new versions side by side on the same cluster state where that is read-only (`verify-platform`, `app-status`, dry runs), and diff their output.

## Risks

| Risk | Mitigation |
| --- | --- |
| Secret values leak via exceptions or tracebacks | All calls go through the Runner, which scrubs output and raises its own error type; secrecy tests for every Secret-handling command |
| A streaming pipe buffers plaintext in memory or on disk | `Popen` chains with pipes, never `capture_output` on decrypted streams; a test asserts no temporary files |
| Behaviour drift (exit codes, output wording, `DRY_RUN`) | Contract tests plus side-by-side diffs before deleting bash |
| Cleanup skipped on interrupt | `ExitStack` plus label checks; a test simulates a failure mid-run |
| Host Python differs from CI | Minimum 3.11, stdlib plus PyYAML only; CI pins 3.12 |
| Half-converted state confuses readers | Each phase deletes the bash it replaces and updates `docs/commands.md` in the same commit |

## Decisions needed before starting

1. **Package location and name:** `tools/swhurl/` run as `python3 -m swhurl` (recommended), or keep flat `scripts/*.py`.
2. **Developer tooling:** stdlib `unittest` only (as now), or add `ruff` (lint and format) and `mypy` through `uvx` in CI. Recommended: `ruff` yes, `mypy` optional, and `shellcheck` for the bash that stays (preinstalled on GitHub runners; locally `uvx --from shellcheck-py shellcheck`).
3. **The borderline case:** move `backup-clickstack-mongodb` to Python (recommended, for the metadata and its Python restore check) or keep it as bash that calls Python for metadata and pruning.
4. **`make` as the interface:** keep it (recommended; docs and muscle memory depend on it), or also document `python3 -m swhurl` directly.

## Done when

- The only bash left is the list in [Bash or Python](#bash-or-python) marked Bash, and it is `shellcheck`-clean in CI.
- Makefile recipes are one or two lines; no loops, parsing or `sed` remain in it.
- Every command's branches are covered by offline unit tests, including secrecy.
- The four live tests pass on the new implementation.
- `docs/commands.md`, `docs/contributing.md` and `AGENTS.md` describe the Python layout; `validate-repo` keeps its shell-syntax step for the bash that stays.

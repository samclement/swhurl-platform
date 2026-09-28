# Sub-plan: convert operator bash to Python

28 September 2026 · sub-plan of the [implementation plan](Swhurl-platform-implementation-plan.md) · not started

## Goal

Make the operator tooling readable and testable without changing what operators type. Every `make` target keeps its name, arguments, `DRY_RUN` behaviour, exit codes and `[OK]`/`[BAD]` output. Only the implementation moves from bash to Python.

**Readable** means one language, named functions with docstrings, structured data instead of `jq`/`sed`/`grep` pipelines, and no `set -e` edge cases. **Testable** means every decision can be unit-tested offline with a fake cluster, including refusals, dry runs, failures and the rule that Secret values are never printed.

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

**One package, one entry point.** Create `tools/swhurl/` (package `swhurl`) run as `python3 -m swhurl <command>`. The Makefile becomes a thin table of one-line aliases, for example `destroy-data: ; python3 -m swhurl destroy-data "$(TARGET)" --confirm "$(CONFIRM)"`. The existing Python scripts move into the package, leaving short shims at their old paths until the docs and CI are updated.

```
tools/swhurl/
  __main__.py      argparse subcommands mirroring the make targets
  run.py           Runner: the only place that calls kubectl, flux, helm, sops, age, mongodump
  report.py        ok/bad/info output, exit codes, dry-run plans
  kube.py          typed helpers on Runner: get_json, exec_in, flux_reconcile, wait_ready
  secrets.py       decrypt in memory, compare by hash, never stringify values
  verify.py  lifecycle.py  backup.py  apps.py  settings.py  host_dns.py
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
| 1. Read-only | `verify-platform.sh`, `app.sh` (status, logs, reconcile, check), `verify-config`, `generate-charts.sh` | Cannot harm the cluster; `verify-platform` has the most branches and the secrecy rule | `make verify-platform`, `make app-status` |
| 2. Settings and restarts | `platform-certs-*` (a YAML edit instead of `sed`), `runtime-inputs-sync`, `wait-runtime-inputs-otel`, `otel-collectors-restart`, `runtime-inputs-refresh-otel`, `install`, the teardown guard | Moves the untestable Makefile shell; small and well bounded | `make install DRY_RUN=true`, `make runtime-inputs-refresh-otel` |
| 3. Backup and restore | `backup-clickstack-mongodb.sh`, `restore-test-clickstack-mongodb.sh` | Streaming Secrets: tests must show no plaintext reaches disk or output | `make backup-clickstack-mongodb`, `make restore-test-clickstack-mongodb` |
| 4. Lifecycle | `lifecycle.sh` (`suspend`, `resume`, `destroy-data`) | The only data-deleting command; gains the most from exhaustive refusal tests | `make lifecycle-test` |
| 5. Live tests | `lifecycle-test.sh`, `reloader-test.sh`, `app-template-test.sh`, `tests/fixtures/apps.sh` | Converted after the code they test, so each is proven against the new implementation | All four live tests |
| 6. Host DNS (optional) | `dynamic-dns.sh`, `aws-dns-updater.sh`, systemd templates | Runs as root under systemd outside the cluster; lowest benefit, most environment risk | `make host-dns DRY_RUN=true`, then a real timer run |

Rough effort: phase 0 about a day; phases 1–5 half a day to a day each; phase 6 about a day. About 5–8 days in total, all of it local or throwaway on the cluster.

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
2. **Developer tooling:** stdlib `unittest` only (as now), or add `ruff` (lint and format) and `mypy` through `uvx` in CI. Recommended: `ruff` yes; `mypy` optional.
3. **Host DNS scripts (phase 6):** convert, or leave as bash with `bash -n` and a dry-run check.
4. **`make` as the interface:** keep it (recommended; docs and muscle memory depend on it), or also document `python3 -m swhurl` directly.

## Done when

- `scripts/` contains no bash, and `host/` none either if phase 6 is done.
- The Makefile holds only one-line aliases.
- Every command's branches are covered by offline unit tests, including secrecy.
- The four live tests pass on the new implementation.
- `docs/commands.md`, `docs/contributing.md` and `AGENTS.md` describe the Python layout; `validate-repo` no longer needs its shell-syntax step, except for any bash kept deliberately.

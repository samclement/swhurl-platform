"""Throwaway live tests: each creates its own resources on the cluster, checks
behaviour, and removes them, touching nothing it did not create.

Shared pieces live here: a :class:`LiveTest` with a report, a cleanup stack
that runs even when a step fails, and small kubectl helpers. DRY_RUN=true
prints the test's plan (its module docstring) and makes no cluster calls.
"""
from __future__ import annotations

import contextlib
import json
import sys
import time
from collections.abc import Callable
from typing import Any

import yaml

from swhurl.report import Report
from swhurl.run import CommandError, Runner


class Preflight(Exception):
    """The test cannot start safely (for example leftovers from a previous run)."""


class LiveTest:
    def __init__(self, runner: Runner, report: Report | None = None, sleep: Callable[[float], None] = time.sleep):
        self.runner = runner
        self.report = report or Report()
        self.report.redact = runner.redact
        self.sleep = sleep
        self.cleanup = contextlib.ExitStack()

    # Reporting -------------------------------------------------------------

    def step(self, title: str) -> None:
        self.report.section(title)

    def check(self, passed: bool, ok: str, bad: str) -> bool:
        (self.report.ok if passed else self.report.bad)(ok if passed else bad)
        return passed

    # kubectl helpers -------------------------------------------------------

    def kubectl(self, *args: str, check: bool = True, input: str | None = None) -> str:
        return self.runner.run(['kubectl', *args], check=check, input=input).stdout

    def succeeds(self, *args: str) -> bool:
        return self.runner.run(['kubectl', *args], check=False).returncode == 0

    def get(self, *args: str) -> Any:
        """``kubectl get ... -o json`` as data, or None when it does not exist."""
        result = self.runner.run(['kubectl', *args, '-o', 'json'], check=False)
        return json.loads(result.stdout) if result.returncode == 0 and result.stdout.strip() else None

    def apply(self, *docs: dict, namespace: str | None = None) -> None:
        args = ['-n', namespace] if namespace else []
        self.kubectl(*args, 'apply', '-f', '-', input=yaml.safe_dump_all(docs))

    def label(self, obj: Any, key: str) -> str | None:
        return (((obj or {}).get('metadata') or {}).get('labels') or {}).get(key)

    def poll(self, condition: Callable[[], bool], *, attempts: int, interval: float) -> bool:
        for _ in range(attempts):
            if condition():
                return True
            self.sleep(interval)
        return False

    def quietly(self, *args: str) -> None:
        """Best-effort cleanup command: never raises."""
        with contextlib.suppress(CommandError):
            self.runner.run(['kubectl', *args], check=False)

    def delete_namespace_if_labelled(self, name: str, label: str, value: str = 'true', *, wait: bool = False) -> bool:
        if self.label(self.get('get', 'namespace', name), label) != value:
            return False
        extra = ['--wait=true', '--timeout=3m'] if wait else ['--wait=false']
        self.quietly('delete', 'namespace', name, *extra)
        return True

    def destroy_volumes_of(self, namespace: str) -> None:
        """Delete retained PVs whose claim lived in ``namespace`` (and their data)."""
        for pv in (self.get('get', 'pv') or {}).get('items', []):
            if ((pv.get('spec') or {}).get('claimRef') or {}).get('namespace') != namespace:
                continue
            name = pv['metadata']['name']
            self.quietly('patch', 'pv', name, '-p', '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}')
            self.quietly('wait', '--for=delete', f'pv/{name}', '--timeout=2m')


def run_live_test(plan: str, body: Callable[[LiveTest], None], *, passed: str,
                  runner: Runner | None = None, report: Report | None = None,
                  sleep: Callable[[float], None] = time.sleep) -> int:
    """Run ``body`` with cleanup guaranteed; print ``passed`` when every check passed."""
    runner = runner or Runner.from_environment()
    if runner.dry_run:
        print(plan.strip())
        return 0
    test = LiveTest(runner, report, sleep)
    try:
        with test.cleanup:  # cleanup callbacks run last, even if a step raises
            try:
                body(test)
            except CommandError as error:
                test.report.bad(str(error))
            if test.report.passed:
                test.report.line(f'\n{passed}')
    except Preflight as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1
    return test.report.exit_code()

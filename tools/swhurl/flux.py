"""Wait for every Flux unit to apply the current Git revision (``make flux-reconcile``).

``cluster-stack`` does not wait for the units it defines, so a slow or failing
unit never holds it (or a new app's unit) back. This command gives the operator
the wait instead: it polls until each unit reports Ready at the revision the
GitRepository has fetched, prints units as they finish, and stops early with the
unit's message when one fails at that revision. Suspended units are listed and
skipped. Read-only.
"""
from __future__ import annotations

import argparse
import sys
import time
from collections.abc import Callable

from swhurl.report import Report
from swhurl.run import CommandError, Runner

SOURCE = 'swhurl-platform'
KUSTOMIZATIONS = 'kustomizations.kustomize.toolkit.fluxcd.io'
# Ready=False reasons that mean "not yet", not "failed at this revision".
WAITING_REASONS = {'DependencyNotReady', 'Progressing', 'ProgressingWithRetry'}


def condition(unit: dict, kind: str) -> dict:
    for item in (unit.get('status') or {}).get('conditions') or []:
        if item.get('type') == kind:
            return item
    return {}


def state(unit: dict, revision: str) -> str:
    """'suspended', 'ready', 'failed' or 'waiting' for this revision."""
    spec, status = unit.get('spec') or {}, unit.get('status') or {}
    if spec.get('suspend'):
        return 'suspended'
    ready, current = condition(unit, 'Ready'), status.get('observedGeneration') == unit['metadata'].get('generation')
    ours = (spec.get('sourceRef') or {}).get('name') == SOURCE
    applied = not ours or status.get('lastAppliedRevision') == revision
    if ready.get('status') == 'True' and applied and current:
        return 'ready'
    attempted = not ours or status.get('lastAttemptedRevision') == revision
    if (ready.get('status') == 'False' and attempted and current and ready.get('reason') not in WAITING_REASONS
            and condition(unit, 'Reconciling').get('status') != 'True'):
        return 'failed'
    return 'waiting'


def wait(runner: Runner, report: Report, timeout: float, interval: float = 2,
         clock: Callable[[], float] = time.monotonic, sleep: Callable[[float], None] = time.sleep) -> int:
    source = runner.json(['kubectl', '-n', 'flux-system', 'get', 'gitrepositories.source.toolkit.fluxcd.io', SOURCE,
                          '-o', 'json'])
    revision = ((source.get('status') or {}).get('artifact') or {}).get('revision', '')
    if not revision:
        report.bad(f'GitRepository {SOURCE} has no artifact yet')
        return report.exit_code()
    report.section(f'Flux units at {revision}')
    start, seen = clock(), set()
    while True:
        units = runner.json(['kubectl', '-n', 'flux-system', 'get', KUSTOMIZATIONS, '-o', 'json'])['items']
        states = {u['metadata']['name']: (state(u, revision), u) for u in units}
        for name, (current, unit) in sorted(states.items()):
            if name in seen or current == 'waiting':
                continue
            seen.add(name)
            if current == 'ready':
                report.ok(f'{name} ({clock() - start:.0f}s)')
            elif current == 'suspended':
                report.warn(f'{name} is suspended; skipped')
            else:
                report.bad(f'{name} failed: {condition(unit, "Ready").get("message", "no message")}')
        waiting = sorted(name for name, (current, _) in states.items() if current == 'waiting')
        if not waiting or not report.passed:
            return report.exit_code()
        if clock() - start >= timeout:
            report.bad(f'timed out after {timeout:.0f}s waiting for: {", ".join(waiting)}')
            return report.exit_code()
        sleep(interval)


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    parser = argparse.ArgumentParser(prog='swhurl flux-wait', description=__doc__.split('\n\n')[0])
    parser.add_argument('--timeout', type=float, default=1200, help='seconds (default 1200)')
    args = parser.parse_args(argv or [])
    runner = runner or Runner.from_environment()
    report = Report(redact=runner.redact)
    try:
        return wait(runner, report, args.timeout)
    except (CommandError, KeyError, TypeError, ValueError) as error:
        print(f'[ERROR] could not read Flux state: {error}', file=sys.stderr)
        return 1

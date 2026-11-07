"""Flux itself: install its controllers with our settings, and wait for its units.

``flux-install`` (``make flux-install``) renders ``flux install --export`` at the
version pinned in ``clusters/home/flux-system/install/flux.json``, applies the
patches in ``clusters/home/flux-system/install`` with kustomize, and applies the
result server-side as the ``flux`` field manager, like ``flux install`` does. A plain
``flux install`` would drop the patches; ``swhurl-v2 verify`` warns if it has, reading
the same ``flux.json``.

``flux-wait`` (``make flux-reconcile``) waits for every unit to apply the current
Git revision. ``cluster-stack`` does not wait for the units it defines, so a slow or failing
unit never holds it (or a new app's unit) back. This command gives the operator
the wait instead: it polls until each unit reports Ready at the revision the
GitRepository has fetched, prints units as they finish, and stops early with the
unit's message when one fails at that revision. Suspended units are listed and
skipped. Read-only.
"""
from __future__ import annotations

import argparse
import functools
import json
import shutil
import sys
import tempfile
import time
from collections.abc import Callable
from pathlib import Path

from swhurl import ROOT
from swhurl.report import Report
from swhurl.run import CommandError, Runner

SOURCE = 'swhurl-platform'
INSTALL = 'clusters/home/flux-system/install'
# The Flux release the cluster runs, the controllers beyond the default four and the arguments the install
# patches add: one JSON file, read here and by swhurl-v2 verify's flux-controllers check. Renovate does not
# see it; to upgrade, see docs/operations.md (chart and tool updates).
SETTINGS = INSTALL + '/flux.json'
# flux install's own components, which every installation has.
DEFAULT_COMPONENTS = ('source-controller', 'kustomize-controller', 'helm-controller', 'notification-controller')


@functools.cache
def settings(root: Path = ROOT) -> dict:
    """What ``flux.json`` states: the version, the extra and removed components and the controller arguments.

    Read when a function needs it, never when this module is imported: the console imports ``swhurl.flux`` for
    ``WAITING_REASONS`` and its image carries no checkout (``images/console/Dockerfile``).
    """
    return json.loads((root / SETTINGS).read_text())


def extra_components(root: Path = ROOT) -> tuple[str, ...]:
    """Controllers beyond flux install's default four: the image reflector picks each app's newest staging image,
    and its ImagePolicy report starts the Deploy workflow (docs/apps.md#deploy-a-new-image)."""
    return tuple(settings(root)['extraComponents'])


def removed_components(root: Path = ROOT) -> tuple[str, ...]:
    """Components this cluster ran once and no longer does: make flux-install deletes what flux install labelled
    with them (its Deployment, ServiceAccount and CRDs), since applying never prunes."""
    return tuple(settings(root)['removedComponents'])


def controllers(root: Path = ROOT) -> tuple[str, ...]:
    """Every Deployment flux-system runs: flux install's own components and the extra ones."""
    return (*DEFAULT_COMPONENTS, *extra_components(root))


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
    def source_revision() -> str:
        source = runner.json(['kubectl', '-n', 'flux-system', 'get', 'gitrepositories.source.toolkit.fluxcd.io',
                              SOURCE, '-o', 'json'])
        return ((source.get('status') or {}).get('artifact') or {}).get('revision', '')

    revision = source_revision()
    if not revision:
        report.bad(f'GitRepository {SOURCE} has no artifact yet')
        return report.exit_code()
    report.section(f'Flux units at {revision}')
    start, seen = clock(), set()
    while True:
        # A commit can land while we wait (the console publish bot pushes after every tools/ change); units
        # then move on to it and never report the old revision, so every unit must reach the newest one.
        latest = source_revision()
        if latest and latest != revision:
            report.info(f'source moved to {latest}; waiting for every unit at that')
            revision, seen = latest, set()
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


def reconcile_main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    """``reconcile <unit>``: fetch Git and reconcile one Flux unit."""
    if not argv or len(argv) != 1:
        print('usage: reconcile <unit> (see flux get kustomizations)', file=sys.stderr)
        return 2
    return (runner or Runner.from_environment()).attached(
        ['flux', 'reconcile', 'kustomization', argv[0], '-n', 'flux-system', '--with-source', '--timeout=20m'], mutating=True)


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


class InstallError(Exception):
    pass


def pinned_version(root: Path = ROOT) -> str:
    """The Flux version the cluster must run (``flux.json``'s ``version``)."""
    return settings(root)['version']


def required_args(root: Path = ROOT) -> dict[str, list[str]]:
    """Controller arguments the install patches add, by Deployment name (``flux.json``'s ``args``)."""
    return {name: list(args) for name, args in settings(root)['args'].items()}


def render(runner: Runner, root: Path = ROOT) -> str:
    version = pinned_version(root)
    client = runner.output(['flux', 'version', '--client'])
    if f'v{version}' not in client.split():
        raise InstallError(f'flux CLI is {client.strip() or "unknown"}, but {SETTINGS} pins {version}; '
                           f'install flux {version} (or bump its version to upgrade)')
    components = runner.output(['flux', 'install', '--export', '--namespace', 'flux-system',
                                f'--components-extra={",".join(extra_components(root))}'])
    with tempfile.TemporaryDirectory() as tmp:
        shutil.copytree(root / INSTALL, tmp, dirs_exist_ok=True)
        (Path(tmp) / 'gotk-components.yaml').write_text(components)
        rendered = runner.output(['kubectl', 'kustomize', tmp])
    for name, args in required_args(root).items():
        missing = [a for a in args if a not in rendered]
        if missing:
            raise InstallError(f'rendered {name} lacks {", ".join(missing)}')
    return rendered


def install_main(argv: list[str] | None = None, runner: Runner | None = None, root: Path = ROOT) -> int:
    runner = runner or Runner.from_environment()
    try:
        rendered = render(runner, root)
        print(f'[INFO] Flux {pinned_version(root)} with the patches in {INSTALL}')
        if runner.dry_run:
            diff = runner.run(['kubectl', 'diff', '--server-side', '--field-manager=flux', '-f', '-'],
                              input=rendered, check=False)
            print(diff.stdout or '[OK] no changes')
        runner.run(['kubectl', 'apply', '--server-side', '--field-manager=flux', '--force-conflicts', '-f', '-'],
                   input=rendered, mutating=True)
        for name in controllers(root):
            runner.run(['kubectl', '-n', 'flux-system', 'rollout', 'status', f'deployment/{name}', '--timeout=5m'],
                       mutating=True)
        for name in removed_components(root):
            label = f'app.kubernetes.io/component={name},app.kubernetes.io/part-of=flux'
            runner.run(['kubectl', '-n', 'flux-system', 'delete', 'deployment,serviceaccount', '-l', label,
                        '--ignore-not-found'], mutating=True)
            runner.run(['kubectl', 'delete', 'customresourcedefinitions', '-l', label, '--ignore-not-found'], mutating=True)
        if not runner.dry_run:
            print('[OK] Flux controllers applied and rolled out')
        return 0
    except (InstallError, CommandError, OSError, KeyError, TypeError) as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1

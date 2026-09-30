"""Flux itself: install its controllers with our settings, and wait for its units.

``flux-install`` (``make flux-install``) renders ``flux install --export`` at the
version pinned in the console Dockerfile, applies the patches in
``clusters/home/flux-system/install`` with kustomize, and applies the result
server-side as the ``flux`` field manager, like ``flux install`` does. A plain
``flux install`` would drop the patches; ``verify-platform`` warns if it has.

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
import shutil
import sys
import tempfile
import time
from collections.abc import Callable
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.report import Report
from swhurl.run import CommandError, Runner

SOURCE = 'swhurl-platform'
INSTALL = 'clusters/home/flux-system/install'
# The Flux release the cluster runs: make flux-install installs it and refuses any other flux CLI.
# Renovate does not see it; to upgrade, see docs/operations.md (chart and tool updates).
FLUX_VERSION = '2.8.1'
# Controllers beyond flux install's default four: image automation deploys newer app images to
# staging (platform/image-automation, docs/apps.md#deploy-a-new-image).
EXTRA_COMPONENTS = ('image-reflector-controller', 'image-automation-controller')
CONTROLLERS = ('source-controller', 'kustomize-controller', 'helm-controller', 'notification-controller',
               *EXTRA_COMPONENTS)
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


class InstallError(Exception):
    pass


def pinned_version(root: Path = ROOT) -> str:
    """The Flux version the cluster must run (``FLUX_VERSION``)."""
    return FLUX_VERSION


def required_args(root: Path = ROOT) -> dict[str, list[str]]:
    """Controller arguments the install patches add, by Deployment name."""
    kustomization = yaml.safe_load((root / INSTALL / 'kustomization.yaml').read_text())
    required: dict[str, list[str]] = {}
    for patch in kustomization.get('patches') or []:
        name = patch['target']['name']
        for op in yaml.safe_load(patch['patch']) or []:
            if op.get('op') == 'add' and op.get('path', '').endswith('/args/-'):
                required.setdefault(name, []).append(op['value'])
    return required


def render(runner: Runner, root: Path = ROOT) -> str:
    version = pinned_version(root)
    client = runner.output(['flux', 'version', '--client'])
    if f'v{version}' not in client.split():
        raise InstallError(f'flux CLI is {client.strip() or "unknown"}, but tools/swhurl/flux.py pins {version}; '
                           f'install flux {version} (or bump FLUX_VERSION to upgrade)')
    components = runner.output(['flux', 'install', '--export', '--namespace', 'flux-system',
                                f'--components-extra={",".join(EXTRA_COMPONENTS)}'])
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
        for name in CONTROLLERS:
            runner.run(['kubectl', '-n', 'flux-system', 'rollout', 'status', f'deployment/{name}', '--timeout=5m'],
                       mutating=True)
        if not runner.dry_run:
            print('[OK] Flux controllers applied and rolled out')
        return 0
    except (InstallError, CommandError, OSError, KeyError, TypeError) as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1

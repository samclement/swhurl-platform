"""Flux itself: install its controllers with our settings, and wait for its units.

``flux-install`` (``make flux-install``) renders ``flux install --export`` at the
version pinned here (``FLUX_VERSION``), applies the patches in
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

from swhurl import ROOT, platform, yamlio
from swhurl.kube import ready_condition
from swhurl.report import Report
from swhurl.run import CommandError, Runner

SOURCE = 'swhurl-platform'
INSTALL = 'clusters/home/flux-system/install'
# The Flux release the cluster runs: make flux-install installs it and refuses any other flux CLI.
# Renovate does not see it; to upgrade, see docs/operations.md (chart and tool updates).
FLUX_VERSION = '2.8.1'
# Controllers beyond flux install's default four: the image reflector picks each app's newest staging
# image, and its ImagePolicy report starts the Deploy workflow (docs/apps.md#deploy-a-new-image).
EXTRA_COMPONENTS = ('image-reflector-controller',)
# Components this cluster ran once and no longer does: make flux-install deletes what flux install
# labelled with them (its Deployment, ServiceAccount and CRDs), since applying never prunes.
REMOVED_COMPONENTS = ('image-automation-controller',)
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
    """The Flux version the cluster must run (``FLUX_VERSION``)."""
    return FLUX_VERSION


def required_args(root: Path = ROOT) -> dict[str, list[str]]:
    """Controller arguments the install patches add, by Deployment name."""
    kustomization = yamlio.load((root / INSTALL / 'kustomization.yaml').read_text())
    required: dict[str, list[str]] = {}
    for patch in kustomization.get('patches') or []:
        name = patch['target']['name']
        for op in yamlio.load(patch['patch']) or []:
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
        for name in REMOVED_COMPONENTS:
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


def check_units(runner: Runner, report: Report) -> None:
    report.section('Flux Kustomizations')
    try:
        units = runner.json(['kubectl', '-n', 'flux-system', 'get', 'kustomizations.kustomize.toolkit.fluxcd.io',
                             '-o', 'json'])['items']
    except (CommandError, KeyError, TypeError):
        report.bad('cannot read Flux kustomizations (kubectl -n flux-system get kustomizations)')
        return
    if not units:
        report.bad('no Flux kustomizations in flux-system, so nothing applies Git: make flux-bootstrap')
        return
    for unit in sorted(units, key=lambda u: u['metadata']['name']):
        name = unit['metadata']['name']
        status, message = ready_condition(unit)
        if status == 'True':
            report.ok(name)
        else:
            report.bad(f'{name} is not Ready' + (f': {message}' if message else ''))


def check_controllers(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """The controllers run the pinned version with the settings in Git (a plain `flux install` drops them)."""
    report.section('Flux Controllers')
    version = pinned_version(root)
    required = required_args(root)
    for name in CONTROLLERS:
        args = required.get(name, [])
        try:
            deployment = runner.json(['kubectl', '-n', 'flux-system', 'get', 'deployment', name, '-o', 'json'])
        except CommandError:
            report.warn(f'{name} is not installed (run: make flux-install)')
            continue
        try:
            live = deployment['metadata'].get('labels', {}).get('app.kubernetes.io/version', '')
            running = [a for c in deployment['spec']['template']['spec']['containers'] for a in c.get('args') or []]
        except (KeyError, TypeError):
            report.bad(f'cannot read flux-system/{name} (kubectl -n flux-system get deploy {name})')
            continue
        missing = [a for a in args if a not in running]
        if missing or live != f'v{version}':
            expected = f'v{version}' + (f' with {", ".join(args)}' if args else '')
            report.warn(f'{name} is {live or "unknown"}' + (f' with {len(args) - len(missing)}/{len(args)} Git settings'
                        if args else '') + f'; expected {expected} (run: make flux-install)')
        else:
            report.ok(f'{name} {live}' + (' with the settings in Git' if args else ''))


def check_push_webhook(runner: Runner, report: Report, root: Path = ROOT) -> None:
    """Flux's GitHub Receiver is Ready and GitHub's latest delivery to it succeeded (docs/services.md#push-webhook)."""
    report.section('Push Webhook')
    try:
        receiver = runner.json(['kubectl', '-n', 'flux-system', 'get', 'receivers.notification.toolkit.fluxcd.io',
                                'github', '-o', 'json'])
        status, message = ready_condition(receiver)
    except (CommandError, TypeError):
        status, message = 'Unknown', 'could not read it'
    if status == 'True':
        report.ok('Flux receiver flux-system/github is Ready')
    else:
        report.bad(f'Flux receiver flux-system/github is not Ready: {message} (make reconcile UNIT=platform-flux-webhook)')
    host = f'flux-webhook.{platform.base_domain(root)}'
    repository = platform.github_repository(root)
    try:
        hooks = runner.json(['gh', 'api', f'repos/{repository}/hooks'])
    except CommandError as error:
        report.warn(f'could not list the GitHub webhooks of {repository} with gh: {error}')
        return
    hook = next((h for h in hooks or [] if host in (h.get('config') or {}).get('url', '')), None)
    if not hook:
        report.warn(f'no GitHub webhook on {repository} calls {host}; Flux falls back to polling every minute')
        return
    last = hook.get('last_response') or {}
    if not hook.get('active'):
        report.warn(f'the GitHub webhook to {host} is disabled; Flux falls back to polling every minute')
    elif last.get('code') == 200:
        report.ok(f'GitHub\'s latest delivery to {host} returned 200')
    else:
        report.warn(f'GitHub\'s latest delivery to {host} returned {last.get("code")} {last.get("message", "")}'.rstrip()
                    + '; see Recent Deliveries on the webhook (Flux still polls every minute)')


ALERTS = ('failures',)  # platform/alerts/alerts.yaml


def check_alerts(runner: Runner, report: Report) -> None:
    """The ntfy alerts exist and point at existing providers (v1beta3 objects report no Ready status)."""
    report.section('Alerts')
    base = ['kubectl', '-n', 'flux-system', 'get']
    try:
        alerts = {a['metadata']['name']: a for a in runner.json([*base, 'alerts.notification.toolkit.fluxcd.io',
                                                                 '-o', 'json'])['items']}
        providers = {p['metadata']['name'] for p in runner.json([*base, 'providers.notification.toolkit.fluxcd.io',
                                                                 '-o', 'json'])['items']}
    except (CommandError, KeyError, TypeError):
        report.bad('cannot read Flux alerts and providers (make reconcile UNIT=platform-alerts)')
        return
    for name in ALERTS:
        alert = alerts.get(name)
        provider = ((alert or {}).get('spec') or {}).get('providerRef', {}).get('name')
        if alert is None:
            report.warn(f'alert {name} is missing: no push notifications for it (make reconcile UNIT=platform-alerts)')
        elif provider not in providers:
            report.bad(f'alert {name} points at provider {provider}, which does not exist, so its notifications are '
                       'lost (make reconcile UNIT=platform-alerts)')
        else:
            report.ok(f'alert {name} sends to {provider}')

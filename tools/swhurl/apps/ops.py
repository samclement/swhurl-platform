"""Operate one generated app instance (namespace ``<app>-<env>``, Flux unit
``app-<app>-<env>``, HelmRelease ``<app>``).

    app status    APP ENV   desired vs applied revision and image, replicas, route, failure reason
    app logs      APP ENV   recent logs from the instance's workload (FOLLOW=true to stream, TAIL=N)
    app reconcile APP ENV   fetch Git and reconcile only this instance
    app check     APP ENV   render and check this instance against the app contract (offline)
"""
from __future__ import annotations

import os
import sys
from dataclasses import dataclass, field

from swhurl import ROOT
from swhurl.apps import policy
from swhurl.apps.contract import INSTANCE_ROOTS
from swhurl.run import CommandError, Runner
from swhurl.verify import ready_condition

ACTIONS = ('status', 'logs', 'reconcile', 'check')
WORKLOAD_KINDS = 'deploy,statefulset,daemonset'


@dataclass(frozen=True)
class Instance:
    app: str
    env: str

    @property
    def namespace(self) -> str:
        return f'{self.app}-{self.env}'

    @property
    def unit(self) -> str:
        return f'app-{self.app}-{self.env}'


def get(runner: Runner, *args: str) -> dict | None:
    """``kubectl get ... -o json``, or None if it does not exist."""
    try:
        return runner.json(['kubectl', *args, '-o', 'json'])
    except CommandError:
        return None


def desired_image(release: dict | None) -> str:
    image = (((((release or {}).get('spec') or {}).get('values') or {}).get('controllers') or {})
             .get('main', {}).get('containers', {}).get('main', {}).get('image') or {})
    text = f"{image.get('repository', '?')}:{image.get('tag', '')}"
    return text + (f"@{image['digest']}" if image.get('digest') else '')


def running_images(pods: dict | None) -> list[str]:
    ids = {status['imageID'].removeprefix('docker-pullable://')
           for pod in (pods or {}).get('items', [])
           for status in (pod.get('status') or {}).get('containerStatuses') or []
           if status.get('imageID')}
    return sorted(ids)


@dataclass(frozen=True)
class Replicas:
    workload: str  # <Kind>/<name>
    ready: int
    wanted: int | None


@dataclass(frozen=True)
class Problem:
    pod: str
    reason: str
    message: str


@dataclass(frozen=True)
class InstanceStatus:
    """What ``app status`` shows, gathered once so the CLI and the console read the same facts."""
    instance: Instance
    desired_revision: str
    applied_revision: str
    unit: tuple[str, str]          # Ready status and message
    release: tuple[str, str] | None
    desired_image: str
    running_images: list[str]
    replicas: list[Replicas]
    routes: list[str]
    certificates: list[tuple[str, str]]  # name, Ready status
    problems: list[Problem]
    settings: dict[str, str] = field(default_factory=dict)  # replicas, cpu, memory, memory_limit as in Git

    @property
    def applied(self) -> bool:
        return self.desired_revision == self.applied_revision


def replicas(workloads: dict | None) -> list[Replicas]:
    found = []
    for item in (workloads or {}).get('items', []):
        status, spec = item.get('status') or {}, item.get('spec') or {}
        found.append(Replicas(f"{item['kind']}/{item['metadata']['name']}",
                              status.get('readyReplicas', status.get('numberReady', 0)),
                              spec.get('replicas', status.get('desiredNumberScheduled'))))
    return found


def problems(pods: dict | None) -> list[Problem]:
    found = []
    for pod in (pods or {}).get('items', []):
        for status in (pod.get('status') or {}).get('containerStatuses') or []:
            if status.get('ready'):
                continue
            state = status.get('state') or {}
            waiting, terminated = state.get('waiting') or {}, state.get('terminated') or {}
            found.append(Problem(pod['metadata']['name'], waiting.get('reason') or terminated.get('reason') or 'not ready',
                                 (waiting.get('message') or '')[:160]))
    return found


def release_settings(release: dict | None) -> dict[str, str]:
    """Replicas and resources as the HelmRelease sets them (what app-scale edits)."""
    controller = ((((release or {}).get('spec') or {}).get('values') or {}).get('controllers') or {}).get('main') or {}
    resources = ((controller.get('containers') or {}).get('main') or {}).get('resources') or {}
    found = {'replicas': str(controller.get('replicas', 1)),
             'cpu': (resources.get('requests') or {}).get('cpu'),
             'memory': (resources.get('requests') or {}).get('memory'),
             'memory_limit': (resources.get('limits') or {}).get('memory')}
    return {k: str(v) for k, v in found.items() if v is not None} if release else {}


def gather_status(runner: Runner, instance: Instance) -> InstanceStatus | None:
    """Read the instance from the cluster; None if its Flux unit does not exist."""
    ns = instance.namespace
    unit = get(runner, '-n', 'flux-system', 'get', 'kustomization', instance.unit)
    if unit is None:
        return None
    source = get(runner, '-n', 'flux-system', 'get', 'gitrepository', 'swhurl-platform') or {}
    release = get(runner, '-n', ns, 'get', 'helmrelease', instance.app)
    selector = f'app.kubernetes.io/instance={instance.app}'
    pods = get(runner, '-n', ns, 'get', 'pods', '-l', selector)
    workloads = get(runner, '-n', ns, 'get', WORKLOAD_KINDS, '-l', selector)
    ingresses = (get(runner, '-n', ns, 'get', 'ingress') or {}).get('items', [])
    certificates = (get(runner, '-n', ns, 'get', 'certificate') or {}).get('items', [])
    return InstanceStatus(
        instance=instance,
        desired_revision=((source.get('status') or {}).get('artifact') or {}).get('revision', ''),
        applied_revision=(unit.get('status') or {}).get('lastAppliedRevision', ''),
        unit=ready_condition(unit),
        release=ready_condition(release) if release else None,
        desired_image=desired_image(release),
        running_images=running_images(pods),
        replicas=replicas(workloads),
        routes=[rule.get('host') for ingress in ingresses for rule in ingress['spec'].get('rules') or []],
        certificates=[(cert['metadata']['name'], ready_condition(cert)[0]) for cert in certificates],
        problems=problems(pods),
        settings=release_settings(release),
    )


def status_lines(found: InstanceStatus) -> list[str]:
    instance = found.instance
    note = '' if found.applied else '  <- not yet applied'
    lines = [
        f'Instance   {instance.app}/{instance.env} (namespace {instance.namespace})',
        f"Git        desired {found.desired_revision.split(':')[-1]}, "
        f"applied {found.applied_revision.split(':')[-1]}{note}",
        f'Flux unit  {found.unit[0]}: {found.unit[1]}',
        'Release    ' + (f'{found.release[0]}: {found.release[1]}' if found.release else 'Unknown: not found'),
        f'Image      desired {found.desired_image}',
        f"           running {', '.join(found.running_images) or 'none'}",
    ]
    lines += [f'Replicas   {r.workload}: {r.ready}/{r.wanted} ready' for r in found.replicas]
    lines += [f'Route      https://{host}' for host in found.routes]
    lines += [f'TLS        {name}: {ready}' for name, ready in found.certificates]
    if found.problems:
        lines.append('Problems')
        lines += [f'  {p.pod}: {p.reason} {p.message}'.rstrip() for p in found.problems]
    return lines


def status(runner: Runner, instance: Instance) -> int:
    found = gather_status(runner, instance)
    if found is None:
        print(f'[ERROR] Flux unit {instance.unit} not found', file=sys.stderr)
        return 1
    for line in status_lines(found):
        print(line)
    return 0


def logs(runner: Runner, instance: Instance, *, tail: str = '100', follow: bool = False) -> int:
    args = ['kubectl', '-n', instance.namespace, 'logs', f'deploy/{instance.app}', '--all-containers',
            f'--tail={tail}']
    return runner.attached([*args, '--follow'] if follow else args)


def reconcile(runner: Runner, instance: Instance) -> int:
    for args in (['flux', 'reconcile', 'source', 'git', 'swhurl-platform', '-n', 'flux-system', '--timeout=5m'],
                 ['flux', 'reconcile', 'kustomization', instance.unit, '-n', 'flux-system', '--timeout=10m']):
        code = runner.attached(args, mutating=True)
        if code:
            return code
    return 0


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    argv = argv or []
    if len(argv) != 3 or argv[0] not in ACTIONS:
        print(__doc__.split('\n\n', 1)[1].rstrip(), file=sys.stderr)
        return 2
    action, instance = argv[0], Instance(argv[1], argv[2])
    if action == 'check':
        return policy.main([str(ROOT / INSTANCE_ROOTS[0] / instance.app / instance.env)])
    runner = runner or Runner.from_environment()
    try:
        if action == 'status':
            return status(runner, instance)
        if action == 'logs':
            return logs(runner, instance, tail=os.environ.get('TAIL', '100'),
                        follow=os.environ.get('FOLLOW', 'false') == 'true')
        return reconcile(runner, instance)
    except CommandError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1

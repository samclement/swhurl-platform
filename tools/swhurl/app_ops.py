"""Operate one generated app instance (namespace ``<app>-<env>``, Flux unit
``homelab-app-<app>-<env>``, HelmRelease ``<app>``).

    app status    APP ENV   desired vs applied revision and image, replicas, route, failure reason
    app logs      APP ENV   recent logs from the instance's workload (FOLLOW=true to stream, TAIL=N)
    app reconcile APP ENV   fetch Git and reconcile only this instance
    app check     APP ENV   render and check this instance against the app contract (offline)
"""
from __future__ import annotations

import os
import sys
from dataclasses import dataclass

from swhurl import ROOT, app_policy
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
        return f'homelab-app-{self.app}-{self.env}'


def condition_line(obj: dict | None) -> str:
    if not obj:
        return 'Unknown: not found'
    status, message = ready_condition(obj)
    return f'{status}: {message}'


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


def replica_lines(workloads: dict | None) -> list[str]:
    lines = []
    for item in (workloads or {}).get('items', []):
        status, spec = item.get('status') or {}, item.get('spec') or {}
        ready = status.get('readyReplicas', status.get('numberReady', 0))
        wanted = spec.get('replicas', status.get('desiredNumberScheduled'))
        lines.append(f"Replicas   {item['kind']}/{item['metadata']['name']}: {ready}/{wanted} ready")
    return lines


def problem_lines(pods: dict | None) -> list[str]:
    lines = []
    for pod in (pods or {}).get('items', []):
        for status in (pod.get('status') or {}).get('containerStatuses') or []:
            if status.get('ready'):
                continue
            state = status.get('state') or {}
            waiting, terminated = state.get('waiting') or {}, state.get('terminated') or {}
            reason = waiting.get('reason') or terminated.get('reason') or 'not ready'
            message = (waiting.get('message') or '')[:160]
            lines.append(f"  {pod['metadata']['name']}: {reason} {message}".rstrip())
    return lines


def status(runner: Runner, instance: Instance) -> int:
    ns = instance.namespace
    unit = get(runner, '-n', 'flux-system', 'get', 'kustomization', instance.unit)
    if unit is None:
        print(f'[ERROR] Flux unit {instance.unit} not found', file=sys.stderr)
        return 1
    source = get(runner, '-n', 'flux-system', 'get', 'gitrepository', 'swhurl-platform') or {}
    desired = ((source.get('status') or {}).get('artifact') or {}).get('revision', '')
    applied = (unit.get('status') or {}).get('lastAppliedRevision', '')
    release = get(runner, '-n', ns, 'get', 'helmrelease', instance.app)
    pods = get(runner, '-n', ns, 'get', 'pods', '-l', f'app.kubernetes.io/instance={instance.app}')

    print(f'Instance   {instance.app}/{instance.env} (namespace {ns})')
    note = '' if desired == applied else '  <- not yet applied'
    print(f"Git        desired {desired.split(':')[-1]}, applied {applied.split(':')[-1]}{note}")
    print(f'Flux unit  {condition_line(unit)}')
    print(f'Release    {condition_line(release)}')
    images = running_images(pods)
    print(f'Image      desired {desired_image(release)}')
    print(f"           running {', '.join(images) or 'none'}")
    for line in replica_lines(get(runner, '-n', ns, 'get', WORKLOAD_KINDS,
                                  '-l', f'app.kubernetes.io/instance={instance.app}')):
        print(line)
    for ingress in (get(runner, '-n', ns, 'get', 'ingress') or {}).get('items', []):
        for rule in ingress['spec'].get('rules') or []:
            print(f"Route      https://{rule.get('host')}")
    for cert in (get(runner, '-n', ns, 'get', 'certificate') or {}).get('items', []):
        print(f"TLS        {cert['metadata']['name']}: {ready_condition(cert)[0]}")
    problems = problem_lines(pods)
    if problems:
        print('Problems')
        for line in problems:
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
        return app_policy.main([str(ROOT / 'tenants/apps' / instance.app / instance.env)])
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

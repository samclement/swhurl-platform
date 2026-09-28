"""Explicit lifecycle operations. Deployment and uninstall stay Git-driven;
these commands cover what Git cannot express safely.

    suspend      kustomization/<name> | helmrelease/<ns>/<name>
    resume       kustomization/<name> | helmrelease/<ns>/<name>
    destroy-data pvc/<ns>/<name> | pv/<name>     (CONFIRM=<same target>)

``destroy-data`` is the only command that deletes data. It refuses unless
CONFIRM repeats the target exactly, and while a pod mounts the claim or a Helm
release or Flux unit still manages it (either would recreate it). DRY_RUN=true
runs every check and prints the commands it would run.
"""
from __future__ import annotations

import os
import sys
from dataclasses import dataclass

from swhurl.run import CommandError, Runner

FLUX_NS = 'flux-system'
USAGE_EXIT = 2


class LifecycleError(Exception):
    """A refusal: printed as ``[ERROR] ...`` with exit code 2."""


class UsageError(LifecycleError):
    """Unknown action or target kind: prints the usage text, exit code 2."""


@dataclass(frozen=True)
class Target:
    kind: str
    namespace: str
    name: str
    text: str


SHAPES = {
    ('suspend', 'kustomization'): (1, 'Use kustomization/<name>'),
    ('resume', 'kustomization'): (1, 'Use kustomization/<name>'),
    ('suspend', 'helmrelease'): (2, 'Use helmrelease/<namespace>/<name>'),
    ('resume', 'helmrelease'): (2, 'Use helmrelease/<namespace>/<name>'),
    ('destroy-data', 'pvc'): (2, 'Use pvc/<namespace>/<name>'),
    ('destroy-data', 'pv'): (1, 'Use pv/<name>'),
}


def parse_target(action: str, text: str) -> Target:
    """Validate the target's shape for the action before any cluster call."""
    kind, *parts = text.split('/')
    if len(parts) > 2:
        raise LifecycleError(f'Invalid target: {text}')
    if (action, kind) not in SHAPES:
        raise UsageError(__doc__.split('\n\n')[1])
    count, hint = SHAPES[(action, kind)]
    if len(parts) != count or not all(parts):
        raise LifecycleError(hint)
    if kind == 'kustomization':
        return Target(kind, FLUX_NS, parts[0], text)
    if count == 1:
        return Target(kind, '', parts[0], text)
    return Target(kind, parts[0], parts[1], text)


def suspend_resume(runner: Runner, action: str, target: Target, out=print, *, quiet: bool = False) -> int:
    """``quiet`` captures flux's progress output instead of streaming it (used by live tests)."""
    found = runner.run(['kubectl', '-n', target.namespace, 'get', target.kind, target.name], check=False)
    if found.returncode:
        raise LifecycleError(f'{target.kind} {target.namespace}/{target.name} not found')
    if runner.dry_run:
        out(f'Plan ({action} {target.text}):')
    flux = ['flux', action, target.kind, target.name, '-n', target.namespace]
    code = runner.run(flux, mutating=True).returncode if quiet else runner.attached(flux, mutating=True)
    if code:
        return code
    if action == 'suspend':
        out(f'[INFO] Git changes stop applying to {target.text}; running workloads and data are untouched.')
        if target.kind == 'kustomization':
            out('[INFO] HelmReleases it created keep reconciling their last applied spec; '
                'suspend them separately to freeze Helm.')
    return 0


def mounted_by_active_pod(pods: dict, claim: str) -> bool:
    """Running, pending or terminating pods hold the volume; finished ones do not."""
    for pod in pods.get('items', []):
        if (pod.get('status') or {}).get('phase') in ('Succeeded', 'Failed'):
            continue
        for volume in (pod.get('spec') or {}).get('volumes') or []:
            if (volume.get('persistentVolumeClaim') or {}).get('claimName') == claim:
                return True
    return False


def check_claim_released(runner: Runner, namespace: str, claim: str) -> dict:
    """Refuse while anything is using or would recreate the claim; return the claim."""
    try:
        pvc = runner.json(['kubectl', '-n', namespace, 'get', 'pvc', claim, '-o', 'json'])
    except CommandError:
        raise LifecycleError(f'PVC {namespace}/{claim} not found') from None
    if mounted_by_active_pod(runner.json(['kubectl', '-n', namespace, 'get', 'pods', '-o', 'json']) or {}, claim):
        raise LifecycleError(f'PVC {namespace}/{claim} is mounted by a pod; stop or uninstall the workload first')
    metadata = pvc.get('metadata') or {}
    release = (metadata.get('annotations') or {}).get('meta.helm.sh/release-name')
    if release and runner.output(['kubectl', '-n', namespace, 'get', 'secret', '-l', f'owner=helm,name={release}',
                                  '-o', 'name']).strip():
        raise LifecycleError(f'Helm release {namespace}/{release} still exists and would recreate the claim; '
                             'uninstall it first')
    labels = metadata.get('labels') or {}
    owner = labels.get('kustomize.toolkit.fluxcd.io/name')
    if owner and runner.run(['kubectl', '-n', labels.get('kustomize.toolkit.fluxcd.io/namespace', FLUX_NS),
                             'get', 'kustomization', owner], check=False).returncode == 0:
        raise LifecycleError(f'Flux Kustomization {owner} still manages the claim; remove it from Git first')
    return pvc


def destroy_data(runner: Runner, target: Target, out=print) -> int:
    if target.kind == 'pvc':
        pvc = check_claim_released(runner, target.namespace, target.name)
        volume = (pvc.get('spec') or {}).get('volumeName', '')
    else:
        volume = target.name
        try:
            pv = runner.json(['kubectl', 'get', 'pv', volume, '-o', 'json'])
        except CommandError:
            raise LifecycleError(f'PV {volume} not found') from None
        if ((pv or {}).get('status') or {}).get('phase') != 'Released':
            raise LifecycleError(f'PV {volume} is not Released; destroy its claim with pvc/<namespace>/<name> instead')
    if runner.dry_run:
        out(f'Plan (destroy-data {target.text}): all checks passed')
    # Switching to Delete makes the provisioner remove the host directory once
    # the volume is released; with Retain it would be left behind.
    if volume:
        runner.run(['kubectl', 'patch', 'pv', volume, '-p', '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}'],
                   mutating=True)
    if target.kind == 'pvc':
        runner.run(['kubectl', '-n', target.namespace, 'delete', 'pvc', target.name, '--wait=true', '--timeout=2m'],
                   mutating=True)
    if volume:
        runner.run(['kubectl', 'wait', '--for=delete', f'pv/{volume}', '--timeout=2m'], mutating=True)
    if not runner.dry_run:
        out(f'[OK] Destroyed {target.text}' + (f' (PV {volume} and its data)' if volume else ''))
    return 0


def run_action(action: str, target_text: str, *, confirm: str, runner: Runner) -> int:
    target = parse_target(action, target_text)
    if action == 'destroy-data' and confirm != target_text:
        raise LifecycleError(f'destroy-data permanently deletes {target_text} and its data. '
                             f'Re-run with CONFIRM={target_text}')
    if action == 'destroy-data':
        return destroy_data(runner, target)
    return suspend_resume(runner, action, target)


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    argv = argv or []
    if len(argv) != 2 or not argv[1]:
        print(__doc__.split('\n\n')[1], file=sys.stderr)
        return USAGE_EXIT
    try:
        return run_action(argv[0], argv[1], confirm=os.environ.get('CONFIRM', ''),
                          runner=runner or Runner.from_environment())
    except UsageError as error:
        print(error, file=sys.stderr)
        return USAGE_EXIT
    except LifecycleError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return USAGE_EXIT
    except CommandError as error:
        print(f'[ERROR] {error}', file=sys.stderr)
        return 1

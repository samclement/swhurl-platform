"""
Prove the lifecycle contract on a disposable app (tests/fixtures/lifecycle-app,
reconciled from the pushed Git revision). Touches only resources it creates:
Flux Kustomizations named lifecycle-test*, namespace lifecycle-test and its PVs.

  1. suspend leaves the workload and data running; resume reconciles again
  2. uninstall (deleting an app-style unit) removes the workload but keeps
     the prune-protected namespace, claim and data
  3. destroy-data refuses without CONFIRM, then deletes the claim, PV and data
  4. a unit with deletionPolicy: Orphan (as the shared units use) leaves its
     resources running when the unit itself is deleted
"""
from __future__ import annotations

import json

from swhurl import lifecycle
from swhurl.livetests import LiveTest, Preflight, run_live_test

NS = 'lifecycle-test'
UNIT = 'lifecycle-test'
ORPHAN_UNIT = 'lifecycle-test-orphan'
LABEL = 'platform.swhurl.com/lifecycle-test'
READER_OVERRIDES = json.dumps({'spec': {
    'automountServiceAccountToken': False,
    'volumes': [{'name': 'd', 'persistentVolumeClaim': {'claimName': 'data'}}],
    'containers': [{'name': 'reader', 'image': 'busybox:1.37', 'command': ['cat', '/data/marker'],
                    'volumeMounts': [{'name': 'd', 'mountPath': '/data'}]}]}})


def unit(name: str, deletion_policy: str) -> dict:
    return {'apiVersion': 'kustomize.toolkit.fluxcd.io/v1', 'kind': 'Kustomization',
            'metadata': {'name': name, 'namespace': 'flux-system'},
            'spec': {'interval': '10m', 'sourceRef': {'kind': 'GitRepository', 'name': 'swhurl-platform'},
                     'path': './tests/fixtures/lifecycle-app', 'prune': True, 'deletionPolicy': deletion_policy,
                     'wait': True, 'timeout': '5m'}}


def apply_unit(t: LiveTest, name: str, deletion_policy: str) -> None:
    t.apply(unit(name, deletion_policy))
    t.kubectl('-n', 'flux-system', 'wait', '--for=condition=Ready', f'kustomization/{name}', '--timeout=6m')


def read_marker(t: LiveTest) -> str:
    """Read the data file through a short-lived pod mounting the claim."""
    t.quietly('-n', NS, 'delete', 'pod', 'reader', '--ignore-not-found')
    t.kubectl('-n', NS, 'run', 'reader', '--image=busybox:1.37', '--restart=Never', '--quiet',
              f'--overrides={READER_OVERRIDES}')
    t.kubectl('-n', NS, 'wait', '--for=jsonpath={.status.phase}=Succeeded', 'pod/reader', '--timeout=2m')
    marker = t.kubectl('-n', NS, 'logs', 'reader')
    t.kubectl('-n', NS, 'delete', 'pod', 'reader', '--wait=true')
    return marker


def available(t: LiveTest) -> bool:
    deploy = t.get('-n', NS, 'get', 'deploy', 'writer')
    return ((deploy or {}).get('status') or {}).get('availableReplicas') == 1


def cleanup(t: LiveTest) -> None:
    t.quietly('-n', 'flux-system', 'delete', 'kustomization', UNIT, ORPHAN_UNIT, '--ignore-not-found', '--wait=true')
    t.delete_namespace_if_labelled(NS, LABEL, wait=True)
    t.destroy_volumes_of(NS)
    t.report.info(f'Cleaned up {NS} test resources')


def body(t: LiveTest) -> None:
    if t.get('get', 'namespace', NS) or any(t.get('-n', 'flux-system', 'get', 'kustomization', u)
                                            for u in (UNIT, ORPHAN_UNIT)):
        raise Preflight(f'{NS} namespace or {UNIT} Kustomizations already exist; clean up first')
    t.cleanup.callback(cleanup, t)

    t.step('Install disposable app')
    apply_unit(t, UNIT, 'MirrorPrune')
    t.kubectl('-n', NS, 'rollout', 'status', 'deploy/writer', '--timeout=3m')
    original = t.kubectl('-n', NS, 'exec', 'deploy/writer', '--', 'cat', '/data/marker')
    pv = t.get('-n', NS, 'get', 'pvc', 'data')['spec']['volumeName']
    t.check(bool(original), f'workload wrote data to {pv}', 'no marker written')

    t.step('Suspend and resume')
    quiet = dict(out=lambda _line: None, quiet=True)
    lifecycle.suspend_resume(t.runner, 'suspend', lifecycle.parse_target('suspend', f'kustomization/{UNIT}'), **quiet)
    suspended = (t.get('-n', 'flux-system', 'get', 'kustomization', UNIT) or {}).get('spec', {}).get('suspend')
    t.check(suspended is True, 'unit suspended', 'unit not suspended')
    t.check(available(t), 'workload still running while suspended', 'workload stopped while suspended')
    lifecycle.suspend_resume(t.runner, 'resume', lifecycle.parse_target('resume', f'kustomization/{UNIT}'), **quiet)
    t.check(t.succeeds('-n', 'flux-system', 'wait', '--for=condition=Ready', f'kustomization/{UNIT}', '--timeout=3m'),
            'unit resumed and Ready', 'unit not Ready after resume')

    t.step('Uninstall (delete app unit)')
    t.kubectl('-n', 'flux-system', 'delete', 'kustomization', UNIT, '--wait=true')
    t.check(t.succeeds('-n', NS, 'wait', '--for=delete', 'deploy/writer', '--timeout=2m'),
            'workload pruned', 'workload not pruned')
    t.succeeds('-n', NS, 'wait', '--for=delete', 'pod', '-l', 'app=writer', '--timeout=2m')
    t.check(t.get('get', 'namespace', NS) is not None, 'prune-protected namespace kept', 'namespace deleted')
    claim = t.get('-n', NS, 'get', 'pvc', 'data') or {}
    t.check((claim.get('status') or {}).get('phase') == 'Bound', 'prune-protected claim kept', 'claim deleted')
    t.check(read_marker(t) == original, 'data intact after uninstall', 'data changed or missing after uninstall')

    t.step('Destroy data')
    target = f'pvc/{NS}/data'
    try:
        lifecycle.run_action('destroy-data', target, confirm='', runner=t.runner)
        refused = False
    except lifecycle.LifecycleError:
        refused = True
    t.check(refused, 'destroy-data refuses without CONFIRM', 'destroy-data ran without CONFIRM')
    t.check(t.get('-n', NS, 'get', 'pvc', 'data') is not None,
            'claim untouched by refused destroy', 'claim removed by refused destroy')
    lifecycle.destroy_data(t.runner, lifecycle.parse_target('destroy-data', target), out=lambda _line: None)
    t.check(t.get('-n', NS, 'get', 'pvc', 'data') is None, 'claim destroyed', 'claim still exists')
    t.check(t.get('get', 'pv', pv) is None, f'PV {pv} and its data destroyed', f'PV {pv} still exists')

    t.step('Orphan deletion policy (shared units)')
    apply_unit(t, ORPHAN_UNIT, 'Orphan')
    t.kubectl('-n', NS, 'rollout', 'status', 'deploy/writer', '--timeout=3m')
    t.kubectl('-n', 'flux-system', 'delete', 'kustomization', ORPHAN_UNIT, '--wait=true')
    t.sleep(10)
    t.check(available(t), 'workload kept running after its Orphan unit was deleted',
            'Orphan unit deletion removed the workload')


def main(argv: list[str] | None = None, runner=None, report=None, sleep=None) -> int:
    kwargs = {'sleep': sleep} if sleep else {}
    return run_live_test(__doc__, body, passed='Lifecycle test passed.', runner=runner, report=report, **kwargs)

"""
Prove Reloader's opt-in and namespace scope with disposable workloads:
  - an annotated Deployment in a watched namespace restarts when its Secret changes
  - an identical Deployment without the annotation does not
  - an annotated Deployment in an unwatched namespace does not
Creates only resources labelled platform.swhurl.com/reloader-test=true and removes them.
"""
from __future__ import annotations

import json
import os

from swhurl.livetests import LiveTest, Preflight, run_live_test
from swhurl.platform import label

UNWATCHED_NS = 'reloader-test'
LABEL = label('reloader-test')
RELOAD = 'secret.reloader.stakater.com/reload'


def secret() -> dict:
    return {'apiVersion': 'v1', 'kind': 'Secret',
            'metadata': {'name': 'reloader-test', 'labels': {LABEL: 'true'}}, 'stringData': {'value': 'one'}}


def deployment(name: str, opt_in: bool) -> dict:
    return {'apiVersion': 'apps/v1', 'kind': 'Deployment',
            'metadata': {'name': name, 'labels': {LABEL: 'true'},
                         'annotations': {RELOAD: 'reloader-test'} if opt_in else {}},
            'spec': {'replicas': 1, 'selector': {'matchLabels': {'app': name}},
                     'template': {'metadata': {'labels': {'app': name, LABEL: 'true'}},
                                  'spec': {'automountServiceAccountToken': False, 'containers': [{
                                      'name': 'c', 'image': 'busybox:1.37', 'command': ['sleep', '3600000'],
                                      'envFrom': [{'secretRef': {'name': 'reloader-test'}}],
                                      'resources': {'requests': {'cpu': '1m', 'memory': '8Mi'},
                                                    'limits': {'memory': '16Mi'}}}]}}}}


def template_annotations(t: LiveTest, namespace: str, name: str) -> str:
    """Reloader restarts a workload by changing its pod-template annotations."""
    deploy = t.get('-n', namespace, 'get', 'deploy', name) or {}
    annotations = ((deploy.get('spec') or {}).get('template') or {}).get('metadata', {}).get('annotations') or {}
    return json.dumps(annotations, sort_keys=True)


def body(t: LiveTest, watched: str) -> None:
    t.kubectl('-n', 'platform-system', 'rollout', 'status', 'deploy/reloader-reloader', '--timeout=2m')
    if t.get('get', 'namespace', UNWATCHED_NS) or t.get('-n', watched, 'get', 'secret', 'reloader-test'):
        raise Preflight('Test resources already exist; clean up first')

    def cleanup() -> None:
        t.quietly('-n', watched, 'delete', 'deploy,secret', '-l', f'{LABEL}=true', '--ignore-not-found', '--wait=false')
        t.delete_namespace_if_labelled(UNWATCHED_NS, LABEL)
        t.report.info('Cleaned up reloader test resources')
    t.cleanup.callback(cleanup)

    t.kubectl('create', 'namespace', UNWATCHED_NS)
    t.kubectl('label', 'namespace', UNWATCHED_NS, f'{LABEL}=true')
    workloads = [(watched, 'reloader-test-optin', True), (watched, 'reloader-test-control', False),
                 (UNWATCHED_NS, 'reloader-test-unwatched', True)]
    for namespace, name, opt_in in workloads:
        t.apply(secret(), namespace=namespace)
        t.apply(deployment(name, opt_in), namespace=namespace)
    for namespace, name, _ in workloads:
        t.kubectl('-n', namespace, 'rollout', 'status', f'deploy/{name}', '--timeout=3m')
    t.sleep(10)
    before = {name: template_annotations(t, namespace, name) for namespace, name, _ in workloads}

    for namespace in (watched, UNWATCHED_NS):
        t.kubectl('-n', namespace, 'patch', 'secret', 'reloader-test', '-p', '{"stringData":{"value":"two"}}')
    t.report.info('Rotated test Secrets; waiting for Reloader')

    restarted = t.poll(lambda: template_annotations(t, watched, 'reloader-test-optin') != before['reloader-test-optin'],
                       attempts=30, interval=2)
    t.sleep(15)
    t.check(restarted and t.succeeds('-n', watched, 'rollout', 'status', 'deploy/reloader-test-optin', '--timeout=2m'),
            f'opted-in workload in {watched} restarted after its Secret changed',
            f'opted-in workload in {watched} did not restart')
    t.check(template_annotations(t, watched, 'reloader-test-control') == before['reloader-test-control'],
            'workload without the annotation was not restarted', 'unannotated workload was restarted')
    t.check(template_annotations(t, UNWATCHED_NS, 'reloader-test-unwatched') == before['reloader-test-unwatched'],
            f'workload in unwatched namespace {UNWATCHED_NS} was not restarted',
            'workload outside the watched namespaces was restarted')


def main(argv: list[str] | None = None, runner=None, report=None, sleep=None) -> int:
    watched = os.environ.get('WATCHED_NS') or 'logging'
    kwargs = {'sleep': sleep} if sleep else {}
    return run_live_test(__doc__, lambda t: body(t, watched), passed='Reloader test passed.',
                         runner=runner, report=report, **kwargs)

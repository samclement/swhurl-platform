"""Version-5 Go report fixtures. Scenario selection belongs to tests, not a Python status evaluator."""
import io
import json
import tarfile
from pathlib import Path

from swhurl.apps.instance import Instance
from swhurl.console import v2
from swhurl.run import Result

HEAD = 'a' * 40
DIGEST = 'sha256:' + '1' * 64


def report(app='web', env='prod', state='healthy', reason='', revision=HEAD):
    flux = {'status': 'present', 'suspended': False, 'conditions': []}
    return {'kind': 'v2-observation', 'version': 5, 'source': 'console', 'observedAt': '2026-11-03T12:00:00Z',
            'state': {'name': state, 'reason': reason}, 'verdict': 'running', 'backup': {'state': 'not_required', 'archive': {'state': 'not_assessed'}},
            'currentness': {'state': 'applied', 'reason': 'revision_matches', 'appliedRevision': f'main@sha1:{revision}'},
            'live': {'unit': dict(flux), 'release': dict(flux),
                     'workload': {'status': 'present', 'readyReplicas': 1},
                     'running': {'status': 'present', 'digests': [DIGEST]}},
            'promotion': {'ready': True, 'reason': 'ready'},
            'findings': [], 'problems': [], 'routes': [], 'certificates': [],
            'assessed': [], 'unassessed': [], 'notCovered': [],
            'target': {'namespace': f'{app}-{env}', 'unit': {'apiVersion': 'v1', 'kind': 'Kustomization'}, 'release': {'apiVersion': 'v1', 'kind': 'HelmRelease'}, 'workload': {'apiVersion': 'apps/v1', 'kind': 'Deployment'}, 'pods': {'apiVersion': 'v1', 'kind': 'Pod'},
                       'selector': f'app.kubernetes.io/instance={app}', 'container': 'main',
                       'digest': DIGEST, 'replicas': 1, 'revision': revision}}


def archive(inputs, *, prefix='root/'):
    out = io.BytesIO()
    with tarfile.open(fileobj=out, mode='w:gz') as tar:
        for path, value in inputs.items():
            content = value.encode() if isinstance(value, str) else value
            entry = tarfile.TarInfo(prefix + path)
            entry.size = len(content)
            tar.addfile(entry, io.BytesIO(content))
    return out.getvalue()


def install(runner, units, releases):
    """Default page scenarios; individual tests replace the report for a deliberate Go answer."""
    runner.status_reports, runner.status_inputs = {}, {}
    for unit in units:
        instance = Instance.of_unit(unit['metadata']['name'])
        if instance is None:
            continue
        app, env = instance.app, instance.env
        condition = unit['status']['conditions'][0]
        state = 'suspended' if unit['spec'].get('suspend') else ('healthy' if condition['status'] == 'True' else 'failing')
        found = report(app, env, state, condition.get('message', ''))
        found['live']['unit']['suspended'] = bool(unit['spec'].get('suspend'))
        runner.status_reports[app, env] = found
        release = next((r for r in releases if (r['metadata']['name'], r['metadata']['namespace']) == (app, instance.namespace)), None)
        if release is None and state == 'healthy':
            found['state'] = {'name': 'unknown', 'reason': 'release_missing'}
        from swhurl.apps import helmrelease
        hosts = helmrelease.hosts(release)
        found['routes'] = [{'name': app, 'outcome': 'present', 'hosts': list(hosts)}] if hosts else []
        runner.status_inputs.update({
            f'v2-records/{app}/app.json': json.dumps({'application': app, 'team': 'demo'}),
            f'v2-records/{app}/{env}/instance.json': json.dumps({'configuration': {'replicas': 1, 'memoryMiB': 128}, 'grants': {'exposure': 'private'}}),
            f'v2-records/{app}/{env}/release.json': json.dumps({'runtime': {'kind': 'web'}, 'artifact': {'image': 'docker.io/x/web', 'digest': DIGEST}}),
            f'v2-apps/instances/{app}/{env}/helmrelease.json': json.dumps(release or {})})
    runner.on('git', 'rev-parse', 'HEAD', stdout=HEAD)
    runner.on('git', 'archive', handler=lambda args, _input: write_archive(args, runner.status_inputs))
    runner.on('kubectl', 'config', 'view', stdout='{"kind":"Config"}')
    runner.on(v2.BINARY, 'status', handler=lambda args, _input: answer(args, runner.status_reports))


def write_archive(args, inputs):
    path = next(arg.partition('=')[2] for arg in args if arg.startswith('--output='))
    Path(path).write_bytes(archive(inputs, prefix=''))
    return Result(args)


def answer(args, reports):
    revision = args[args.index('--revision') + 1]
    if '--all' not in args:
        selected = reports[args[args.index('--app') + 1], 'staging']
        return Result(args, stdout=json.dumps(selected))
    return Result(args, stdout=json.dumps({'kind': 'v2-observations', 'version': 1, 'source': 'console',
                                          'observedAt': '2026-11-03T12:00:00Z', 'revision': revision,
                                          'instances': [{'application': app, 'environment': env, 'report': value}
                                                        for (app, env), value in reports.items()]}))

"""Version-5 Go report fixtures. Scenario selection belongs to tests, not a Python status evaluator."""
import hashlib
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


def inventory(app='web', env='prod', hosts=(), team='demo', kind='web', exposure='private', port=8080,
              image='docker.io/x/web', digest='', database=('', ''), api='', offers='', egress=(0, ()), **columns):
    """What Go reports for an instance's records, as the page reads it: a test sets the columns it is about."""
    return {'team': team, 'kind': kind, 'exposure': exposure, 'port': port, 'hosts': list(hosts),
            'address': f'http://{app}.{app}-{env}.svc.cluster.local:{port}' if kind == 'web' else '',
            'api': api, 'artifact': {'image': image, 'digest': digest or DIGEST},
            'database': {'kind': database[0], 'path': database[1]}, 'offers': offers,
            'egress': {'port': egress[0], 'grants': list(egress[1])},
            'calls': [], 'allows': [], 'callers': [], 'publishes': [], 'subscriptions': [], 'connections': [],
            **columns}


def declared(app, env, release, team='demo'):
    """The block Go reports for one instance's records: the fixture stands in for the binary, deriving the columns
    from the records it already fabricates. `v2/kernel` derives the same from the same records in Go's tests."""
    values = ((release or {}).get('spec') or {}).get('values') or {}
    tag = ((((values.get('controllers') or {}).get('main') or {}).get('containers') or {}).get('main') or {}).get('image', {}).get('tag', '')
    variables = (((values.get('controllers') or {}).get('main') or {}).get('containers') or {}).get('main', {}).get('env') or {}
    ports = (((values.get('service') or {}).get('main') or {}).get('ports') or {}).get('http') or {}
    annotations = ((release or {}).get('metadata') or {}).get('annotations') or {}
    hosts = [host['host'] for ingress in (values.get('ingress') or {}).values() for host in (ingress.get('hosts') or [])]
    kind, port = ('web', int(ports.get('port', 8080))) if ports or hosts else ('worker', 0)
    address = f'http://{app}.{app}-{env}.svc.cluster.local:{port}' if kind == 'web' else ''
    api_path = str(annotations.get('platform.swhurl.com/api-description', ''))
    api = f'https://{hosts[0]}{api_path}' if api_path and hosts else (f'{address}{api_path}' if api_path else '')
    rows = [{'role': role, 'app': peer} for role, peer in _pairs(variables)]
    return inventory(app, env, hosts=hosts, team=team, kind=kind, port=port, api=api,
                     image=str(variables.get('SWHURL_IMAGE') or 'docker.io/x/web'),
                     digest='sha256:' + hashlib.sha256(tag.encode()).hexdigest() if tag else '',
                     database=('sqlite', variables['DATABASE_PATH']) if variables.get('DATABASE_PATH') else ('', ''),
                     offers=str(annotations.get('platform.swhurl.com/offers', '')),
                     egress=(int(str(variables.get('HTTPS_PROXY', '')).rsplit(':', 1)[-1] or 0) if variables.get('HTTPS_PROXY') else 0,
                             tuple(v for v in str(annotations.get('platform.swhurl.com/egress', '')).split(',') if v)),
                     calls=[peer for role, peer in _pairs(variables)],
                     allows=[name for name in str(variables.get('SWHURL_ALLOWED_CALLERS', '')).split(',') if name],
                     publishes=[v for v in str(variables.get('SWHURL_EVENTS_PUBLISH', '')).split(',') if v],
                     subscriptions=[v for v in str(variables.get('SWHURL_EVENTS_SUBSCRIBE', '')).split(',') if v],
                     connections=rows)


def _pairs(variables):
    """The rows a scenario's variables state: what it calls, and what it subscribes to."""
    import re
    found = [(re.compile(r'^SWHURL_SERVICE_([A-Z0-9_]+)_URL$').match(name), value)
             for name, value in variables.items() if name.startswith('SWHURL_SERVICE_')]
    return [('calls', match.group(1).lower().replace('_', '-')) for match, _ in found if match]


def install(runner, units, releases):
    """Default page scenarios; individual tests replace the report for a deliberate Go answer."""
    runner.status_reports, runner.status_inputs, runner.inventories, runner.removing = {}, {}, {}, []
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
        if release is None:
            found['live']['release']['status'] = 'absent'
            if state == 'healthy':
                found['state'] = {'name': 'unknown', 'reason': 'release_missing'}
        from swhurl.apps import helmrelease
        hosts = helmrelease.hosts(release)
        found['routes'] = [{'name': app, 'outcome': 'present', 'hosts': list(hosts)}] if hosts else []
        runner.inventories[app, env] = declared(app, env, release)
        runner.status_inputs.update({
            f'v2-records/{app}/app.json': json.dumps({'application': app, 'team': 'demo'}),
            f'v2-records/{app}/{env}/instance.json': json.dumps({'configuration': {'replicas': 1, 'memoryMiB': 128}, 'grants': {'exposure': 'private'}}),
            f'v2-records/{app}/{env}/release.json': json.dumps({'runtime': {'kind': 'web'}, 'artifact': {'image': 'docker.io/x/web', 'digest': DIGEST}}),
            f'v2-apps/instances/{app}/{env}/helmrelease.json': json.dumps(release or {})})
    # What Go derives from both records: the callee's inbound row and its caller list.
    for (app, env), declared_here in runner.inventories.items():
        for peer in declared_here['calls']:
            callee = runner.inventories.get((peer, env))
            if callee is not None:
                callee['callers'] = [*callee['callers'], app]
                callee['connections'] = [*callee['connections'],
                                         {'role': 'called-by', 'app': app, 'outcome': 'connected'}]
    runner.on('git', 'rev-parse', 'HEAD', stdout=HEAD)
    runner.on('git', 'archive', handler=lambda args, _input: write_archive(args, runner.status_inputs))
    runner.on('kubectl', 'config', 'view', stdout='{"kind":"Config"}')
    runner.on(v2.BINARY, 'status', handler=lambda args, _input: answer(args, runner.status_reports, runner.inventories, runner.removing))
    runner.on(v2.BINARY, 'flags', stdout=flags_card())


def flags_card(rows=(), *, problem='', writable=True, app='', env=''):
    """The document `swhurl-v2 flags --json` prints: a page with no override stored, unless a test says more."""
    return json.dumps({'kind': 'v2-flags', 'version': 1, 'instance': {'application': app, 'environment': env},
                       'writable': writable, 'problem': problem, 'rows': list(rows)})


def rows(runner):
    """The pages' rows for a scenario, from the document the fake binary answers with (tests that read rows)."""
    from swhurl.console import observation, status
    text = answer(['status', '--all', f'--revision={HEAD}'], runner.status_reports, runner.inventories,
                  runner.removing).stdout
    return status.rows(observation.decode(text, HEAD))


def write_archive(args, inputs):
    path = next(arg.partition('=')[2] for arg in args if arg.startswith('--output='))
    Path(path).write_bytes(archive(inputs, prefix=''))
    return Result(args)


def answer(args, reports, inventories=None, removing=()):
    inventories, removing = inventories or {}, removing or ()
    revision = next((arg.partition('=')[2] for arg in args if arg.startswith('--revision=')),
                    args[args.index('--revision') + 1] if '--revision' in args else HEAD)
    if '--all' not in args:
        selected = reports[args[args.index('--app') + 1], 'staging']
        return Result(args, stdout=json.dumps(selected))
    return Result(args, stdout=json.dumps({'kind': 'v2-observations', 'version': 2, 'source': 'console',
                                          'observedAt': '2026-11-03T12:00:00Z', 'revision': revision,
                                          'instances': [{'application': app, 'environment': env, 'report': value,
                                                         'inventory': inventories.get((app, env)) or inventory(app, env)}
                                                        for (app, env), value in reports.items()],
                                          'removing': [{'application': app, 'environment': env, 'reason': reason}
                                                       for app, env, reason in removing]}))

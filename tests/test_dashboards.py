"""make clickstack-dashboards, offline: one dashboard per app in Git, created, kept, updated or deleted; secrecy."""
import io
import json
import re
import tempfile
import unittest
from pathlib import Path

from test_clickstack import EMAIL, URI, b64

from swhurl import clickstack, dashboards
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

ACCESS_KEY = 'admin-access-key-0123'
SOURCES = [{'id': 't1', 'kind': 'trace', 'from': {'tableName': 'otel_traces'}},
           {'id': 'l1', 'kind': 'log', 'from': {'tableName': 'otel_logs'}},
           {'id': 'm1', 'kind': 'metric', 'from': {'tableName': ''}}]
WEB, WORKER = dashboards.App('web', 'web', ('staging', 'prod')), dashboards.App('job', 'worker', ('staging',))


class HyperDX:
    """Scripted external API and MongoDB; records each API call as (method, path, body)."""

    def __init__(self, dashboards_live=(), key=ACCESS_KEY):
        self.live, self.key, self.calls = list(dashboards_live), key, []

    def runner(self, dry_run=False):
        return (FakeRunner(dry_run=dry_run)
                .on('kubectl', '-n', 'observability', 'get', 'secret', clickstack.INPUTS_SECRET,
                    stdout=json.dumps({'data': {'CLICKSTACK_ADMIN_EMAIL': b64(EMAIL)}}))
                .on('kubectl', '-n', 'observability', 'get', 'secret', clickstack.MONGO_URI_SECRET,
                    stdout=json.dumps({'data': {'connectionString.standard': b64(URI)}}))
                .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.MONGO_POD,
                    stdout='RESULT ' + json.dumps({'key': self.key}))
                .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.APP, handler=self.api))

    def api(self, argv, program):
        url, request = re.search(r'fetch\(("[^"]*"), (\{.*\})\);', program).groups()
        path, request = json.loads(url).removeprefix('http://127.0.0.1:8000/api/v2'), json.loads(request)
        assert request['headers']['authorization'] == f'Bearer {ACCESS_KEY}'
        body = json.loads(request['body']) if 'body' in request else None
        self.calls.append((request['method'], path, body))
        answer = {('GET', '/sources'): {'data': SOURCES}, ('GET', '/dashboards'): {'data': self.live}}.get(
            (request['method'], path), {'data': {}})
        return Result(argv, 0, 'RESULT ' + json.dumps({'status': 200, 'body': answer}), '')

    def writes(self):
        return [(m, p) for m, p, _ in self.calls if m != 'GET']


def run(api, apps, dry_run=False):
    out = io.StringIO()
    runner = api.runner(dry_run)
    code = dashboards.sync(runner, Report(out, redact=runner.redact), apps)
    return code, out.getvalue()


class SyncTests(unittest.TestCase):
    def test_creates_keeps_updates_and_deletes_by_app(self):
        kept = {**dashboards.dashboard(WEB, 't1', 'l1'), 'id': 'd1'}
        changed = {**dashboards.dashboard(WORKER, 't1', 'l1'), 'id': 'd2', 'tiles': []}
        stale = {'id': 'd3', 'name': 'App: gone', 'tags': ['swhurl-app'], 'tiles': []}
        mine_by_hand = {'id': 'd4', 'name': 'App: gone', 'tags': [], 'tiles': []}
        api = HyperDX([kept, changed, stale, mine_by_hand])
        code, out = run(api, [WEB, WORKER, dashboards.App('new', 'web', ('staging',))])
        self.assertEqual(code, 0, out)
        self.assertEqual(api.writes(), [('PUT', '/dashboards/d2'), ('POST', '/dashboards'), ('DELETE', '/dashboards/d3')])
        self.assertIn('[OK] App: web is up to date', out)
        self.assertIn('[OK] deleted App: gone (its app is not in Git)', out)
        self.assertNotIn(ACCESS_KEY, out)

    def test_dry_run_reads_but_writes_nothing(self):
        api = HyperDX([{'id': 'd3', 'name': 'App: gone', 'tags': ['swhurl-app'], 'tiles': []}])
        code, out = run(api, [WEB], dry_run=True)
        self.assertEqual((code, api.writes()), (0, []))
        self.assertIn('would create App: web', out)
        self.assertIn('would delete App: gone', out)

    def test_no_access_key_stops_before_the_api(self):
        api = HyperDX(key='')
        code, out = run(api, [WEB])
        self.assertEqual((code, api.calls), (1, []))
        self.assertIn('has no access key', out)


class DefinitionTests(unittest.TestCase):
    def test_tiles_filter_on_the_apps_namespaces_and_split_by_environment(self):
        web, worker = dashboards.dashboard(WEB, 't1', 'l1'), dashboards.dashboard(WORKER, 't1', 'l1')
        self.assertEqual([t['name'] for t in web['tiles']][:1], ['Requests per minute'])
        self.assertEqual([t['name'] for t in worker['tiles']][:1], ['Runs per minute'])
        for tile in web['tiles']:
            where = tile['config'].get('where') or tile['config']['select'][0]['where']
            self.assertIn("IN ('web-staging', 'web-prod')", where)
        self.assertIn("ParentSpanId = ''", worker['tiles'][0]['config']['select'][0]['where'])
        self.assertTrue(all(t['x'] + t['w'] <= 24 for t in web['tiles']))
        self.assertEqual({t['h'] for t in web['tiles'] if t['config']['displayType'] == 'line'}, {dashboards.CHART_HEIGHT})
        self.assertEqual([t['y'] for t in web['tiles']], [0, 0, 0, 9, 9, 18], 'rows stack without overlapping')

    def test_server_defaults_do_not_count_as_a_change(self):
        wanted = dashboards.dashboard(WEB, 't1', 'l1')
        live = json.loads(json.dumps(wanted))
        for tile in live['tiles']:
            tile['id'], tile['config']['fillNulls'] = 'x', True
            if isinstance(tile['config']['select'], list):
                tile['config']['select'][0].setdefault('valueExpression', '')
        self.assertTrue(dashboards.same(live, wanted))
        live['tiles'][0]['config']['groupBy'] = 'ServiceName'
        self.assertFalse(dashboards.same(live, wanted))

    def test_apps_come_from_instances_with_web_meaning_a_service(self):
        root = Path(tempfile.mkdtemp())
        for app, env, values in (('a', 'prod', {'service': {}}), ('a', 'staging', {'service': {}}), ('b', 'staging', {})):
            (root / app / env).mkdir(parents=True)
            (root / app / env / 'helmrelease.yaml').write_text(json.dumps({'spec': {'values': values}}))
        found = dashboards.apps(sorted(p.parent for p in root.glob('*/*/helmrelease.yaml')))
        self.assertEqual(found, [dashboards.App('a', 'web', ('staging', 'prod')), dashboards.App('b', 'worker', ('staging',))])


class AutomaticSyncTests(unittest.TestCase):
    def release(self, name, env, *, kind='web', unit=None):
        return {'metadata': {'name': name, 'namespace': f'{name}-{env}', 'labels': {
            'kustomize.toolkit.fluxcd.io/name': unit or f'app-{name}-{env}',
            'kustomize.toolkit.fluxcd.io/namespace': 'flux-system'}},
            'spec': {'values': {'service': {}} if kind == 'web' else {}}}

    def test_creation_and_first_promotion_update_the_existing_dashboard(self):
        staging = self.release('new-app', 'staging')
        production = self.release('new-app', 'prod')
        api = HyperDX()
        runner = api.runner().on(*dashboards.RELEASES, stdout=json.dumps({'items': [staging]}))
        self.assertEqual(dashboards.main(['--cluster'], runner, Report(io.StringIO())), 0)
        self.assertEqual(api.writes(), [('POST', '/dashboards')])
        api = HyperDX([{**dashboards.dashboard(dashboards.App('new-app', 'web', ('staging',)), 't1', 'l1'), 'id': 'd1'}])
        runner = api.runner().on(*dashboards.RELEASES, stdout=json.dumps({'items': [production, staging]}))
        self.assertEqual(dashboards.main(['--cluster'], runner, Report(io.StringIO())), 0)
        self.assertEqual(api.writes(), [('PUT', '/dashboards/d1')])
        self.assertIn("'new-app-prod'", api.calls[-1][2]['tiles'][0]['config']['select'][0]['where'])

    def test_discovery_excludes_shared_services_and_mismatched_names(self):
        mismatched = self.release('wrong', 'prod', unit='app-real-prod')
        unowned = self.release('manual', 'prod')
        unowned['metadata']['labels'] = {}
        runner = FakeRunner().on(*dashboards.RELEASES, stdout=json.dumps({'items': [
            self.release('job', 'staging', kind='worker'), self.release('console', 'prod', unit='platform-console'),
            mismatched, unowned]}))
        self.assertEqual(dashboards.cluster_apps(runner), [dashboards.App('job', 'worker', ('staging',))])

    def test_empty_discovery_never_deletes_dashboards(self):
        api = HyperDX([{'id': 'old', 'name': 'App: old', 'tags': [dashboards.TAG]}])
        runner = api.runner().on(*dashboards.RELEASES, stdout=json.dumps({'items': []}))
        self.assertEqual(dashboards.main(['--cluster'], runner, Report(io.StringIO())), 0)
        self.assertEqual(api.writes(), [])

    def test_discovery_failure_stops_before_api_writes(self):
        from swhurl.run import CommandError
        api = HyperDX()
        runner = api.runner().on(*dashboards.RELEASES, returncode=1, stderr='discovery failed')
        with self.assertRaises(CommandError):
            dashboards.main(['--cluster'], runner, Report(io.StringIO()))
        self.assertEqual(api.calls, [])

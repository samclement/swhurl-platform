"""make clickstack-dashboards, offline: one dashboard per app in Git, created, kept, updated or deleted; secrecy."""
import io
import json
import re
import shutil
import tempfile
import unittest
from pathlib import Path

from test_clickstack import EMAIL, URI, b64

from swhurl import ROOT, app_alerts, clickstack, dashboards, yamlio
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

ACCESS_KEY = 'admin-access-key-0123'
SOURCES = [{'id': 't1', 'kind': 'trace', 'from': {'tableName': 'otel_traces'}},
           {'id': 'l1', 'kind': 'log', 'from': {'tableName': 'otel_logs'}},
           {'id': 'm1', 'kind': 'metric', 'from': {'tableName': ''}}]
WEB, WORKER = dashboards.App('web', 'web', ('staging', 'prod')), dashboards.App('job', 'worker', ('staging',))


class HyperDX:
    """Scripted external API and MongoDB; records each API call as (method, path, body)."""

    def __init__(self, dashboards_live=(), key=ACCESS_KEY, recorded=None, writable=True,
                 webhooks=({'id': 'w1', 'name': app_alerts.WEBHOOK},), alerts=(), namespaces=()):
        self.live, self.key, self.calls = list(dashboards_live), key, []
        self.webhooks, self.alerts, self.namespaces = list(webhooks), list(alerts), list(namespaces)
        self.recorded, self.writable, self.patches = recorded, writable, []  # the console's record (a ConfigMap)

    def patch(self, argv, patch):
        self.patches.append(json.loads(patch))
        return Result(argv, 0 if self.writable else 1, '', '' if self.writable else 'forbidden')

    def runner(self, dry_run=False):
        return (FakeRunner(dry_run=dry_run)
                .on('kubectl', '-n', 'observability', 'get', 'secret', clickstack.INPUTS_SECRET,
                    stdout=json.dumps({'data': {'CLICKSTACK_ADMIN_EMAIL': b64(EMAIL)}}))
                .on('kubectl', '-n', 'observability', 'get', 'secret', clickstack.MONGO_URI_SECRET,
                    stdout=json.dumps({'data': {'connectionString.standard': b64(URI)}}))
                .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.MONGO_POD,
                    stdout='RESULT ' + json.dumps({'key': self.key}))
                .on('kubectl', '-n', 'observability', 'exec', '-i', clickstack.APP, handler=self.api)
                .on('kubectl', '-n', 'console', 'get', 'configmap', 'app-dashboards',
                    stdout=json.dumps({'data': {'dashboards.json': self.recorded}} if self.recorded else {}))
                .on('kubectl', '-n', 'console', 'patch', 'configmap', 'app-dashboards', handler=self.patch)
                .on(*dashboards.NAMESPACES, stdout=json.dumps({'items': self.namespaces})))

    def api(self, argv, program):
        url, request = re.search(r'fetch\(("[^"]*"), (\{.*\})\);', program).groups()
        path, request = json.loads(url).removeprefix('http://127.0.0.1:8000/api/v2'), json.loads(request)
        assert request['headers']['authorization'] == f'Bearer {ACCESS_KEY}'
        body = json.loads(request['body']) if 'body' in request else None
        self.calls.append((request['method'], path, body))
        answer = {('GET', '/sources'): {'data': SOURCES}, ('GET', '/dashboards'): {'data': self.live},
                  ('GET', '/webhooks'): {'data': self.webhooks}, ('GET', '/alerts'): {'data': self.alerts}}.get(
            (request['method'], path), {'data': {}})
        return Result(argv, 0, 'RESULT ' + json.dumps({'status': 200, 'body': answer}), '')

    def writes(self):
        return [(m, p) for m, p, _ in self.calls if m != 'GET']


def run(api, apps, dry_run=False):
    out = io.StringIO()
    runner = api.runner(dry_run)
    code = dashboards.sync(runner, Report(out, redact=runner.redact), apps)
    return code, out.getvalue()


def with_ids(dashboard, ident):
    """A dashboard as HyperDX returns it: with its id and an id on every tile."""
    return {**dashboard, 'id': ident, 'tiles': [{**tile, 'id': f"{ident}-{tile['name']}"} for tile in dashboard['tiles']]}


class AlertTests(unittest.TestCase):
    """Every app's default alerts follow its dashboard, through the notify service's webhook."""
    HOOK = [{'id': 'w1', 'name': app_alerts.WEBHOOK}, {'id': 'w0', 'name': 'ntfy: platform'}]

    def alert(self, app, preset, tile, dashboard='d1', **changes):
        return {'id': f'a-{app}-{preset}', 'name': f'swhurl:{app}:{preset}', 'tags': ['swhurl-app', app], 'source': 'tile',
                'dashboardId': dashboard,
                'tileId': f'{dashboard}-{tile}', 'threshold': 5, 'thresholdType': 'above', 'interval': '5m',
                'channel': {'type': 'webhook', 'webhookId': 'w1'}, 'state': 'OK', **changes}

    def test_an_app_gets_one_alert_per_preset_on_its_dashboards_tiles(self):
        api = HyperDX([with_ids(dashboards.dashboard(WEB, 't1', 'l1'), 'd1')], webhooks=self.HOOK)
        code, out = run(api, [WEB])
        self.assertEqual(code, 0, out)
        created = [body for method, path, body in api.calls if (method, path) == ('POST', '/alerts')]
        self.assertEqual([(a['name'], a['tileId'], a['threshold'], a['interval'], a['channel']) for a in created],
                         [('swhurl:web:error-logs', 'd1-Error logs', 5, '5m', {'type': 'webhook', 'webhookId': 'w1'}),
                          ('swhurl:web:errors', 'd1-Errors', 5, '5m', {'type': 'webhook', 'webhookId': 'w1'})])
        self.assertTrue(all(a['source'] == 'tile' and a['dashboardId'] == 'd1' and a['thresholdType'] == 'above' for a in created))
        self.assertTrue(all(a['tags'] == ['swhurl-app', 'web'] for a in created), 'the app\'s name is a tag: filter the alerts page by it')
        self.assertIn('[OK] created swhurl:web:error-logs', out)

    def test_a_job_has_its_error_logs_alert_but_no_errors_alert(self):
        job = dashboards.App('nightly', 'job', ('staging',))
        api = HyperDX([with_ids(dashboards.dashboard(job, 't1', 'l1'), 'd1')], webhooks=self.HOOK)
        code, out = run(api, [job])
        self.assertEqual(code, 0, out)
        created = [body for method, path, body in api.calls if (method, path) == ('POST', '/alerts')]
        self.assertEqual([(a['name'], a['tileId']) for a in created], [('swhurl:nightly:error-logs', 'd1-Error logs')],
                         'a failed run is already one "run failed" message from the notification checker')

    def test_alerts_that_match_are_left_and_the_rest_are_updated_in_place_or_deleted(self):
        live = [self.alert('web', 'error-logs', 'Error logs', tags=['swhurl-app']),  # made before alerts carried their app's tag
                self.alert('web', 'errors', 'Errors', tileId='old-tile'),            # the dashboard was rebuilt
                self.alert('gone', 'errors', 'Errors', dashboard='d9'),              # its app is gone
                {'id': 'mine', 'name': 'my own alert', 'threshold': 1}]              # not ours: untouched
        api = HyperDX([with_ids(dashboards.dashboard(WEB, 't1', 'l1'), 'd1')], webhooks=self.HOOK, alerts=live)
        code, out = run(api, [WEB])
        self.assertEqual(code, 0, out)
        self.assertEqual([call[:2] for call in api.calls if call[0] != 'GET'],
                         [('PUT', '/alerts/a-web-error-logs'), ('PUT', '/alerts/a-web-errors'), ('DELETE', '/alerts/a-gone-errors')])
        self.assertEqual(next(body for m, p, body in api.calls if (m, p) == ('PUT', '/alerts/a-web-error-logs'))['tags'], ['swhurl-app', 'web'])
        self.assertIn('[OK] updated swhurl:web:errors', out)
        self.assertIn('[OK] deleted swhurl:gone:errors: its app or tile is gone', out)
        api = HyperDX([with_ids(dashboards.dashboard(WEB, 't1', 'l1'), 'd1')], webhooks=self.HOOK,
                      alerts=[self.alert('web', 'error-logs', 'Error logs'), self.alert('web', 'errors', 'Errors')])
        code, out = run(api, [WEB])
        self.assertEqual(api.writes(), [])
        self.assertIn('[OK] 1 app(s) have their default alerts', out)

    def test_without_the_webhook_nothing_is_created_and_it_says_how_to_make_it(self):
        api = HyperDX([with_ids(dashboards.dashboard(WEB, 't1', 'l1'), 'd1')], webhooks=self.HOOK[1:])
        code, out = run(api, [WEB])
        self.assertEqual((code, api.writes()), (1, []), 'a failure: the job\'s check then shows that no app has alerts')
        self.assertIn('[BAD] no "swhurl notify" webhook in HyperDX, so apps have no default alerts: make clickstack-notify-webhook', out)

    def test_a_dry_run_and_a_cluster_with_no_app_change_nothing(self):
        live = [self.alert('gone', 'errors', 'Errors', dashboard='d9')]
        api = HyperDX([with_ids(dashboards.dashboard(WEB, 't1', 'l1'), 'd1')], webhooks=self.HOOK, alerts=live)
        code, out = run(api, [WEB], dry_run=True)
        self.assertEqual(api.writes(), [])
        self.assertIn('would create swhurl:web:error-logs', out)
        self.assertIn('would delete swhurl:gone:errors', out)
        api, out = HyperDX(webhooks=self.HOOK, alerts=live), io.StringIO()
        runner = api.runner().on(*dashboards.RELEASES, stdout=json.dumps({'items': []}))
        self.assertEqual(dashboards.main(['--cluster'], runner, Report(out, redact=runner.redact)), 0)
        self.assertEqual(api.writes(), [], 'no app release found: neither dashboards nor alerts are deleted')

    def test_an_app_sets_its_own_thresholds_or_switches_alerts_off(self):
        board = with_ids(dashboards.dashboard(WEB, 't1', 'l1'), 'd1')
        tuned = dashboards.App('web', 'web', ('staging', 'prod'), 'error-logs=20,errors=none')
        api = HyperDX([board], webhooks=self.HOOK,
                      alerts=[self.alert('web', 'error-logs', 'Error logs'), self.alert('web', 'errors', 'Errors')])
        code, out = run(api, [tuned])
        self.assertEqual([call[:2] for call in api.calls if call[0] != 'GET'],
                         [('PUT', '/alerts/a-web-error-logs'), ('DELETE', '/alerts/a-web-errors')])
        self.assertEqual(next(body for m, p, body in api.calls if m == 'PUT')['threshold'], 20)
        self.assertIn('deleted swhurl:web:errors: its app or tile is gone, or the app switched it off', out)
        api = HyperDX([board], webhooks=self.HOOK, alerts=[self.alert('web', 'error-logs', 'Error logs')])
        run(api, [dashboards.App('web', 'web', ('staging', 'prod'), 'none')])
        self.assertEqual(api.writes(), [('DELETE', '/alerts/a-web-error-logs')])
        api = HyperDX([board], webhooks=self.HOOK)
        run(api, [dashboards.App('web', 'web', ('staging', 'prod'), 'nonsense=1')])
        self.assertEqual(len(api.writes()), 2, 'a bad value on the cluster falls back to the defaults')

    def test_settings_are_parsed_strictly_and_written_as_short_as_they_can_be(self):
        self.assertEqual(app_alerts.thresholds(''), {'error-logs': 5, 'errors': 5})
        self.assertEqual(app_alerts.thresholds(' error-logs=20 , errors=none '), {'error-logs': 20, 'errors': None})
        self.assertEqual(app_alerts.thresholds('none'), {'error-logs': None, 'errors': None})
        for bad, message in (('latency=5', "'latency' is not an alert: choose from error-logs, errors"),
                             ('errors=0', "errors='0': give a number from 1 to 1000000, or none"),
                             ('errors', "errors='': give a number"), ('errors=-1', 'give a number'), ('errors=1.5', 'give a number')):
            with self.subTest(bad=bad), self.assertRaisesRegex(ValueError, re.escape(message)):
                app_alerts.thresholds(bad)
        self.assertEqual(app_alerts.spec_of({'error-logs': 5, 'errors': 5}), '')
        self.assertEqual(app_alerts.spec_of({'error-logs': 20, 'errors': None}), 'error-logs=20,errors=none')
        self.assertEqual(app_alerts.spec_of({'error-logs': None, 'errors': None}), 'none')

    def test_the_cluster_job_reads_an_apps_settings_from_its_namespaces(self):
        release = {'metadata': {'name': 'web', 'namespace': 'web-staging', 'labels': {
            'kustomize.toolkit.fluxcd.io/name': 'app-web-staging', 'kustomize.toolkit.fluxcd.io/namespace': 'flux-system'}},
            'spec': {'values': {'service': {}}}}
        api = HyperDX(namespaces=[{'metadata': {'name': 'web-staging', 'annotations': {app_alerts.ANNOTATION: 'errors=none'}}},
                                  {'metadata': {'name': 'other'}}])
        runner = api.runner().on(*dashboards.RELEASES, stdout=json.dumps({'items': [release]}))
        self.assertEqual(dashboards.cluster_apps(runner), [dashboards.App('web', 'web', ('staging',), 'errors=none')])

    def test_an_alerts_name_says_which_app_and_preset_it_is(self):
        found = app_alerts.NAME.search('🚨 swhurl:orders-api:error-logs')
        self.assertEqual((found['app'], found['preset']), ('orders-api', 'error-logs'))
        self.assertIsNone(app_alerts.NAME.search('🚨 my own alert'))
        self.assertEqual(app_alerts.LABELS['errors'], 'failed requests or runs')


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

    def test_each_apps_dashboard_is_recorded_for_the_console_and_written_only_when_it_changed(self):
        ids = {'web': 'a' * 24, 'job': 'b' * 24}
        raw = json.dumps(ids, sort_keys=True)
        live = [{**dashboards.dashboard(WEB, 't1', 'l1'), 'id': ids['web']}, {**dashboards.dashboard(WORKER, 't1', 'l1'), 'id': ids['job']}]
        api = HyperDX(live)
        code, out = run(api, [WEB, WORKER, dashboards.App('new', 'web', ('staging',))])
        self.assertEqual(code, 0, out)
        self.assertEqual(api.patches, [{'data': {'dashboards.json': raw}}],
                         'a dashboard created in this run has no id yet: the next run records it')
        self.assertIn('[OK] recorded 2 dashboard addresses for the console', out)
        unchanged = HyperDX(live, recorded=raw)
        self.assertEqual(run(unchanged, [WEB, WORKER])[0], 0)
        self.assertEqual(unchanged.patches, [], 'unchanged: nothing is written')
        self.assertEqual(dashboards.recorded(unchanged.runner()), ids)
        dry = HyperDX(live)
        run(dry, [WEB], dry_run=True)
        self.assertEqual(dry.patches, [])

    def test_only_an_app_name_and_a_dashboard_id_are_read_back(self):
        def stored(raw):
            return dashboards.recorded(HyperDX(recorded=raw).runner())
        self.assertEqual(stored(json.dumps({'web': 'a' * 24, 'x': '../../evil', 'Bad Name': 'b' * 24, 'n': 7})), {'web': 'a' * 24})
        self.assertEqual(stored('not json'), {})
        self.assertEqual(stored('[1]'), {})
        self.assertEqual(stored(None), {}, 'never written yet')
        refused = FakeRunner().on('kubectl', '-n', 'console', 'get', 'configmap', returncode=1, stderr='forbidden')
        self.assertEqual(dashboards.recorded(refused), {}, 'unreadable: the console leaves the link out')

    def test_a_record_that_cannot_be_written_fails_the_run(self):
        api = HyperDX([{**dashboards.dashboard(WEB, 't1', 'l1'), 'id': 'a' * 24}], writable=False)
        code, out = run(api, [WEB])
        self.assertEqual(code, 1)
        self.assertIn('could not record dashboard addresses in console/app-dashboards', out)

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

    def test_a_jobs_dashboard_shows_its_runs(self):
        board = dashboards.dashboard(dashboards.App('nightly', 'job', ('staging', 'prod')), 't1', 'l1')
        tiles = {t['name']: t for t in board['tiles']}
        self.assertEqual(list(tiles), ['Runs', 'Failed runs', 'Run duration p95 (s)', 'Last runs', 'Error logs', 'Latest logs'])
        runs = tiles['Runs']['config']['select'][0]['where']
        self.assertIn("IN ('nightly-staging', 'nightly-prod')", runs)
        self.assertIn("ParentSpanId = '' AND SpanName = 'run'", runs)
        self.assertIn("StatusCode = 'Error'", tiles['Failed runs']['config']['select'][0]['where'])
        self.assertEqual((tiles['Last runs']['config']['displayType'], tiles['Last runs']['config']['sourceId']), ('search', 't1'))
        self.assertTrue(all(t['x'] + t['w'] <= 24 for t in board['tiles']))
        self.assertEqual([t['y'] for t in board['tiles']], [0, 0, 0, 9, 9, 18], 'rows stack without overlapping')

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

    def test_apps_come_from_records_even_without_valid_rendered_output(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            shutil.copytree(ROOT / 'tests/fixtures/instance/apps', root / 'apps')
            (root / 'apps/hello/staging/helmrelease.yaml').write_text('invalid: [')
            (root / 'apps/hello/prod/helmrelease.yaml').unlink()
            record = root / 'apps/hello/app.yaml'
            doc = yamlio.load(record.read_text())
            doc['alerts'] = 'none'
            record.write_text(yamlio.dump(doc))
            self.assertEqual(dashboards.apps(root), [dashboards.App('hello', 'web', ('staging', 'prod'), 'none')])
            record.write_text('invalid: [')
            with self.assertRaisesRegex(ValueError, 'cannot read its records'):
                dashboards.apps(root)


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
        put = next(body for method, _path, body in api.calls if method == 'PUT')
        self.assertIn("'new-app-prod'", put['tiles'][0]['config']['select'][0]['where'])

    def test_discovery_excludes_shared_services_and_mismatched_names(self):
        mismatched = self.release('wrong', 'prod', unit='app-real-prod')
        unowned = self.release('manual', 'prod')
        unowned['metadata']['labels'] = {}
        runner = FakeRunner().on(*dashboards.NAMESPACES, stdout=json.dumps({'items': []})).on(*dashboards.RELEASES, stdout=json.dumps({'items': [
            self.release('job', 'staging', kind='worker'), self.release('console', 'prod', unit='platform-console'),
            mismatched, unowned]}))
        self.assertEqual(dashboards.cluster_apps(runner), [dashboards.App('job', 'worker', ('staging',))])

    def cluster(self, live, releases, dry_run=False):
        api, out = HyperDX(live), io.StringIO()
        runner = api.runner(dry_run).on(*dashboards.RELEASES, stdout=json.dumps({'items': releases}))
        code = dashboards.main(['--cluster'], runner, Report(out, redact=runner.redact))
        return code, api, out.getvalue()

    def live(self, name, *envs, id):
        return {**dashboards.dashboard(dashboards.App(name, 'web', envs), 't1', 'l1'), 'id': id}

    def test_removed_app_loses_its_dashboard_and_others_are_untouched(self):
        code, api, out = self.cluster([self.live('kept', 'staging', id='d1'), self.live('gone', 'staging', 'prod', id='d2')],
                                      [self.release('kept', 'staging')])
        self.assertEqual((code, api.writes()), (0, [('DELETE', '/dashboards/d2')]), out)
        self.assertIn('[OK] deleted App: gone (its app has no HelmRelease on the cluster)', out)

    def test_removing_one_environment_updates_and_never_deletes(self):
        code, api, out = self.cluster([self.live('app', 'staging', 'prod', id='d1')], [self.release('app', 'staging')])
        self.assertEqual((code, api.writes()), (0, [('PUT', '/dashboards/d1')]), out)
        self.assertNotIn("'app-prod'", json.dumps(api.calls[-1][2]))

    def test_scheduled_sync_deletes_only_tagged_dashboards_named_for_an_app(self):
        copy = {'id': 'c1', 'name': 'App: kept (copy)', 'tags': [dashboards.TAG], 'tiles': []}
        by_hand = {'id': 'h1', 'name': 'App: gone', 'tags': [], 'tiles': []}
        code, api, out = self.cluster([self.live('kept', 'staging', id='d1'), copy, by_hand], [self.release('kept', 'staging')])
        self.assertEqual((code, api.writes()), (0, []), out)
        self.assertIn('left App: kept (copy): tagged swhurl-app but not named for an app', out)

    def test_scheduled_dry_run_lists_the_deletion_and_writes_nothing(self):
        code, api, out = self.cluster([self.live('kept', 'staging', id='d1'), self.live('gone', 'staging', id='d2')],
                                      [self.release('kept', 'staging')], dry_run=True)
        self.assertEqual((code, api.writes()), (0, []), out)
        self.assertIn('would delete App: gone (its app has no HelmRelease on the cluster)', out)

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

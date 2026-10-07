"""The collector: fixed queries, pre-filter outcomes, the bundle and every failure it reports."""
from __future__ import annotations

import copy
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import httpx

from swhurl.incident_review import allowlist, collect, state
from swhurl.incident_review.errors import ReviewFailure
from swhurl.run import FakeRunner

FIXTURE = json.loads((Path(__file__).parent / 'fixtures/incident-review/sweep.json').read_text())
NOW = FIXTURE['now']
ALLOW = allowlist.current()
STATE_GET = ('kubectl', '-n', 'incident-review', 'get', 'configmap', 'incident-review-state', '-o', 'json')
IMAGES = {('hello-ts-staging', 'hello-ts'): {'tag': '35-54d7918', 'digest': 'sha256:abc'}}


def telemetry(**changes):
    return collect.FixtureTelemetry({**copy.deepcopy(FIXTURE['telemetry']), **changes})


def sweep(source=None, current=None, now=NOW):
    return collect.collect(allow=ALLOW, current=current or state.empty_state(), telemetry=source or telemetry(),
                           images=IMAGES, now=now)


class CollectTests(unittest.TestCase):
    def test_fired_sweep_writes_one_bundle_and_defers_the_other_finding(self):
        source = telemetry()
        report, bundle = sweep(source)
        self.assertEqual(report['status'], 'fired')
        self.assertEqual(report['window'], {'start': '2026-10-07T02:00:00+00:00', 'end': '2026-10-07T03:00:00+00:00'})
        decisions = {f['signal']: (f['decision'], f['reason']) for f in report['findings']}
        self.assertEqual(decisions, {'error-logs': ('fired', 'error-rate-change'),
                                     'error-spans': ('fired-deferred', 'error-rate-change'),
                                     'missing-telemetry': ('quiet', 'quiet-window')})
        self.assertEqual(report['counts'], {'hello-ts/staging/error-logs': 6, 'hello-ts/staging/error-spans': 6,
                                            'hello-ts/staging/unexpected-service': 0,
                                            'hello-ts/staging/missing-telemetry': 336})
        chosen = next(f for f in report['findings'] if f['decision'] == 'fired')
        self.assertEqual((bundle['fingerprint'], bundle['key']), (chosen['fingerprint'], 'hello-ts RangeError'))
        self.assertEqual(bundle['image'], IMAGES[('hello-ts-staging', 'hello-ts')])
        self.assertEqual({f['coverage'] for f in report['findings']}, {'alert-gap'})
        self.assertNotIn('base_revision', bundle)
        names = [name for name, _ in source.calls]
        self.assertEqual(names[-2:], ['log-sample:error-logs', 'trace-sample:error-logs'])
        for _, params in source.calls:
            self.assertEqual(params, {'namespace': 'hello-ts-staging', 'start': NOW - NOW % 3600 - 3600,
                                      'end': NOW - NOW % 3600, 'expected': 'hello-ts'})

    def test_bundle_holds_only_approved_fields_and_is_redacted(self):
        _, bundle = sweep()
        for record in bundle['logs']:
            self.assertEqual(set(record), set(collect.LOG_FIELDS))
            self.assertLessEqual(len(record['stack']), 10)
        self.assertEqual(bundle['logs'][1]['path'], '/repeat')
        self.assertNotIn('abc123', json.dumps(bundle))
        self.assertEqual(set(bundle['traces'][0]), set(collect.TRACE_FIELDS))
        self.assertLess(len(json.dumps(bundle).encode()), 64_000)

    def test_no_query_contains_a_caller_value(self):
        for sql in collect.query_names():
            self.assertNotIn('hello-ts', sql)
            self.assertIn('{namespace:String}', sql)
            self.assertIn('toDateTime({start:UInt32})', sql)
        self.assertEqual(len(collect.query_names()), 10)

    def test_quiet_sweep_has_no_bundle_and_runs_no_sample_query(self):
        source = telemetry(**{'error-logs': [], 'error-spans': []})
        report, bundle = sweep(source)
        self.assertEqual((report['status'], bundle), ('quiet', None))
        self.assertFalse([name for name, _ in source.calls if 'sample' in name])

    def test_cooldown_and_budget_stop_the_bundle(self):
        first, _ = sweep()
        current = state.empty_state()
        for finding in first['findings']:
            current['incidents'][finding['fingerprint']] = {
                'app': 'hello-ts', 'env': 'staging', 'signal': finding['signal'], 'first_seen': NOW, 'last_seen': NOW,
                'last_analysis': NOW, 'cooldown_until': NOW + 86400, 'status': 'notified', 'coverage': 'alert-gap',
                'cli': '', 'model': '', 'notified': NOW, 'pr': None, 'check': None}
        report, bundle = sweep(current=current, now=NOW + 3600)
        self.assertEqual((report['status'], bundle), ('quiet', None))
        self.assertEqual({f['reason'] for f in report['findings'] if f['signal'] != 'missing-telemetry'},
                         {'repeat-inside-cooldown'})
        for field, value, expected in (('cents', 1600, 'monthly'), ('analyses_today', 4, 'daily')):
            current = state.empty_state()
            current['spend'].update(month='2026-10', day='2026-10-07', **{field: value})
            source = telemetry()
            report, bundle = sweep(source, current)
            self.assertEqual((report['status'], report['budget'], bundle), ('budget', expected, None))
            self.assertFalse([name for name, _ in source.calls if 'sample' in name])

    def test_metrics_stopping_fires_only_with_a_baseline(self):
        source = telemetry(**{'error-logs': [], 'error-spans': [], 'missing-telemetry': [{'value': 0}],
                              'log-sample:missing-telemetry': [], 'trace-sample:missing-telemetry': []})
        self.assertEqual(sweep(source)[0]['status'], 'quiet')
        current = state.empty_state()
        current['baselines']['hello-ts/staging/missing-telemetry'] = [336, 336]
        report, bundle = sweep(source, current)
        self.assertEqual((report['status'], bundle['signal'], bundle['key']), ('fired', 'missing-telemetry', 'no-metrics'))

    def test_unexpected_service_sums_logs_and_traces(self):
        source = telemetry(**{'error-logs': [], 'error-spans': [],
                              'unexpected-service:0': [{'service': 'swhurl-app', 'value': 3}],
                              'unexpected-service:1': [{'service': 'swhurl-app', 'value': 4}],
                              'log-sample:unexpected-service': [], 'trace-sample:unexpected-service': []})
        report, bundle = sweep(source)
        finding = next(f for f in report['findings'] if f['signal'] == 'unexpected-service')
        self.assertEqual((finding['key'], finding['count'], bundle['key']), ('swhurl-app', 7, 'swhurl-app'))

    def test_bad_rows_are_query_or_redaction_failures_never_partial_bundles(self):
        cases = [
            ({'error-logs': [{'service': 'hello-ts', 'exception_type': 'E', 'body_prefix': '', 'value': -1}]}, 'query'),
            ({'error-logs': [{'service': 'hello-ts', 'value': 6}]}, 'query'),
            ({'error-spans': [{'service': 7, 'span': 'GET', 'value': 6}]}, 'query'),
            ({'log-sample:error-logs': [{**FIXTURE['telemetry']['log-sample:error-logs'][0], 'email': 'a@b.c'}]},
             'redaction'),
            ({'log-sample:error-logs': [{**FIXTURE['telemetry']['log-sample:error-logs'][0], 'message': 7}]},
             'redaction'),
            ({'trace-sample:error-logs': [{'trace_id': 'only'}]}, 'redaction'),
            ({'log-sample:error-logs': [{**FIXTURE['telemetry']['log-sample:error-logs'][0], 'message': 'x' * 2000,
                                         'stack': '\n'.join(['y' * 290] * 10)}] * 20}, 'redaction'),
        ]
        for change, reason in cases:
            with self.subTest(change=list(change)):
                with self.assertRaises(ReviewFailure) as caught:
                    sweep(telemetry(**change))
                self.assertEqual(caught.exception.reason, reason)
        with self.assertRaises(ReviewFailure):
            collect.FixtureTelemetry({}).query('SELECT 1', {})

    def test_release_images(self):
        runner = FakeRunner().on(*collect.RELEASES, stdout=json.dumps(FIXTURE['releases']))
        images = collect.release_images(runner)
        self.assertEqual(images[('hello-ts-staging', 'hello-ts')]['tag'], '35-54d7918')
        self.assertEqual(images[('flux-system', 'no-image')], {'tag': '', 'digest': ''})
        for bad in (FakeRunner().on(*collect.RELEASES, returncode=1), FakeRunner().on(*collect.RELEASES, stdout='[]')):
            with self.assertRaises(ReviewFailure) as caught:
                collect.release_images(bad)
            self.assertEqual(caught.exception.reason, 'query')


class ClickHouseTests(unittest.TestCase):
    def client(self, handler):
        return collect.ClickHouseHTTP('http://clickhouse.invalid:8123', 'app', 'fixture-password',
                                      transport=httpx.MockTransport(handler))

    def test_values_travel_as_parameters_with_server_limits(self):
        seen = {}

        def handler(request):
            seen.update(params=dict(request.url.params), body=request.content.decode(), headers=request.headers)
            return httpx.Response(200, text='{"value":"6"}\n{"value":"1"}\n')

        rows = self.client(handler).query('SELECT 1 AS value WHERE x = {namespace:String}',
                                          {'namespace': "x' OR 1=1", 'start': 5})
        self.assertEqual(rows, [{'value': '6'}, {'value': '1'}])
        self.assertEqual(seen['params']['param_namespace'], "x' OR 1=1")
        self.assertNotIn('OR 1=1', seen['body'])
        self.assertEqual((seen['params']['max_result_rows'], seen['params']['max_result_bytes'],
                          seen['params']['result_overflow_mode']), ('20', '64000', 'throw'))
        self.assertEqual(seen['headers']['x-clickhouse-user'], 'app')
        self.assertNotIn('fixture-password', str(request_url := seen['params']))
        del request_url

    def test_errors_limits_and_malformed_responses_are_query_failures(self):
        def raises(request):
            raise httpx.ConnectError('fixture-password in a connection error')

        responses = [httpx.Response(500, text='Code: 62. Syntax error near hello-ts'),
                     httpx.Response(200, content=b'x' * 64_001), httpx.Response(200, text='{not json'),
                     httpx.Response(200, text='[1]\n'), httpx.Response(200, text='{"value":1}\n' * 21),
                     httpx.Response(200, content=b'\xff\xfe')]
        for handler in [raises, *[(lambda request, response=response: response) for response in responses]]:
            with self.assertRaises(ReviewFailure) as caught:
                self.client(handler).query('SELECT 1', {})
            self.assertEqual((caught.exception.reason, str(caught.exception)), ('query', 'query'))
        self.assertEqual(self.client(lambda request: httpx.Response(200, text='')).query('SELECT 1', {}), [])


class CollectMainTests(unittest.TestCase):
    def run_main(self, runner, source, env=None):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp)
            for name in ('collect', 'bundle', 'diagnosis'):
                (work / name).mkdir()
            (work / 'bundle/bundle.json').write_text('{"stale": true}')
            with mock.patch.dict('os.environ', env or {}, clear=False):
                code = collect.main(['--work', tmp], runner, telemetry=source, now=NOW)
            report = json.loads((work / 'collect/report.json').read_text())
            bundle = work / 'bundle/bundle.json'
            return code, report, json.loads(bundle.read_text()) if bundle.exists() else None

    def runner(self, raw_state=None):
        data = {} if raw_state is None else {'data': {'state.json': raw_state}}
        return FakeRunner().on(*STATE_GET, stdout=json.dumps(data)).on(
            *collect.RELEASES, stdout=json.dumps(FIXTURE['releases']))

    def test_main_writes_report_and_bundle_and_never_patches(self):
        runner = self.runner()
        code, report, bundle = self.run_main(runner, telemetry())
        self.assertEqual((code, report['status'], bundle['image']['tag']), (0, 'fired', '35-54d7918'))
        self.assertFalse([call for call in runner.calls if 'patch' in call])

    def test_handled_failures_exit_zero_with_a_reason_and_remove_any_stale_bundle(self):
        class Broken:
            def query(self, sql, params):
                raise ReviewFailure('query')

        cases = [(self.runner(), Broken(), 'query'), (self.runner('{corrupt'), telemetry(), 'state-corrupt'),
                 (FakeRunner().on(*STATE_GET, returncode=1), telemetry(), 'state-unavailable'),
                 (FakeRunner().on(*STATE_GET, stdout='{}').on(*collect.RELEASES, returncode=1), telemetry(), 'query'),
                 (self.runner(), None, 'query')]
        for runner, source, reason in cases:
            with self.subTest(reason=reason):
                code, report, bundle = self.run_main(runner, source, {'CLICKHOUSE_PASSWORD': '', 'CLICKHOUSE_USER': ''})
                self.assertEqual((code, report['status'], report['reason'], bundle), (0, 'failed', reason, None))
                self.assertEqual(report['findings'], [])

    def test_quiet_main_removes_a_stale_bundle(self):
        code, report, bundle = self.run_main(self.runner(), telemetry(**{'error-logs': [], 'error-spans': []}))
        self.assertEqual((code, report['status'], bundle), (0, 'quiet', None))


if __name__ == '__main__':
    unittest.main()

"""Operator views of the incident reviewer: read-only status and the privacy-review bundle."""
from __future__ import annotations

import contextlib
import io
import json
import unittest
from pathlib import Path

from swhurl.incident_review import collect, inspect, state
from swhurl.run import FakeRunner, Result

FIXTURE = json.loads((Path(__file__).parent / 'fixtures/incident-review/sweep.json').read_text())
NOW = FIXTURE['now']
GET = ('kubectl', '-n', 'incident-review', 'get', 'configmap', 'incident-review-state', '-o', 'json')
EXEC = ('kubectl', '-n', 'observability', 'exec')


def run(main, argv, runner):
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        code = main(argv, runner, now=NOW)
    return code, out.getvalue(), err.getvalue()


class StatusTests(unittest.TestCase):
    def test_status_summarises_state_without_log_text(self):
        current = state.empty_state()
        current.update(last_sweep=NOW - 600, last_result='analysed')
        current['baselines']['hello-ts/staging/error-logs'] = [0, 0, 6]
        current['spend'].update(month='2026-10', cents=50, analyses=1, day='2026-10-07', analyses_today=1)
        current['incidents']['3a7a05c6d917bdc6f74b4f0d'] = {
            'app': 'hello-ts', 'env': 'staging', 'signal': 'error-logs', 'first_seen': NOW, 'last_seen': NOW,
            'last_analysis': NOW, 'cooldown_until': NOW + 3600, 'status': 'notified', 'coverage': 'alert-gap',
            'cli': 'c', 'model': 'm', 'notified': NOW, 'pr': None, 'check': None}
        runner = FakeRunner().on(*GET, stdout=json.dumps({'data': {'state.json': json.dumps(current)}}))
        code, out, _ = run(inspect.status_main, [], runner)
        self.assertEqual(code, 0)
        for expected in ('result: analysed', 'Spend 2026-10: 50 of 1600 cents, 1 analyses (1 of 4 today)',
                         'hello-ts/staging/error-logs: 0 0 6 (mean 2.0)',
                         '3a7a05c6d917bdc6f74b4f0d hello-ts/staging error-logs: notified, alert-gap', 'cooling down'):
            self.assertIn(expected, out)
        self.assertEqual(runner.calls, [GET])

    def test_status_reports_unreadable_state(self):
        code, _, err = run(inspect.status_main, [], FakeRunner().on(*GET, stdout='{"data":{"state.json":"{bad"}}'))
        self.assertEqual((code, err.strip()), (1, '[ERROR] cannot read the reviewer state: state-corrupt'))


class BundleTests(unittest.TestCase):
    def runner(self, rows=None):
        names = collect.query_names()
        rows = FIXTURE['telemetry'] if rows is None else rows

        def answer(argv, _input):
            return Result(argv, 0, '\n'.join(json.dumps(row) for row in rows.get(names[argv[-1]], [])), '')

        return FakeRunner().on(*EXEC, handler=answer).on(*collect.RELEASES, stdout=json.dumps(FIXTURE['releases']))

    def test_bundle_is_read_only_and_passes_values_as_parameters(self):
        runner = self.runner()
        code, out, err = run(inspect.bundle_main, ['--summary'], runner)
        self.assertEqual(code, 0, err)
        summary = json.loads(out)
        self.assertEqual(summary['logs'], {'records': 2, 'fields': sorted(collect.LOG_FIELDS)})
        self.assertLess(summary['bytes'], 64_000)
        self.assertNotIn('request failed', out)
        self.assertIn("hello-ts/staging error-logs 'hello-ts RangeError': 6 (fired, error-rate-change)", err)
        for call in runner.calls:
            self.assertNotIn('patch', call)
            if call[:4] == EXEC:
                self.assertIn('--param_namespace=hello-ts-staging', call)
                self.assertNotIn('hello-ts', call[-1])
                self.assertIn('--max_result_rows=20', call)
        self.assertFalse([call for call in runner.calls if 'configmap' in call], 'the bundle preview uses an empty state')

    def test_full_bundle_is_the_redacted_one(self):
        code, out, _ = run(inspect.bundle_main, [], self.runner())
        bundle = json.loads(out)
        self.assertEqual((code, bundle['key'], bundle['logs'][1]['path']), (0, 'hello-ts RangeError', '/repeat'))
        self.assertNotIn('abc123', out)

    def test_quiet_hour_and_query_failure(self):
        quiet = {**FIXTURE['telemetry'], 'error-logs': [], 'error-spans': []}
        code, out, err = run(inspect.bundle_main, ['--hours-ago', '2'], self.runner(quiet))
        self.assertEqual((code, out), (0, ''))
        self.assertIn('nothing fired', err)
        broken = FakeRunner().on(*EXEC, returncode=1).on(*collect.RELEASES, stdout=json.dumps(FIXTURE['releases']))
        code, _, err = run(inspect.bundle_main, [], broken)
        self.assertEqual((code, err.strip()), (1, '[ERROR] collection failed: query'))


if __name__ == '__main__':
    unittest.main()

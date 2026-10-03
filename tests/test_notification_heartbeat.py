"""Host-side freshness alerts for the in-cluster notification checker."""
from __future__ import annotations

import argparse
import base64
import contextlib
import datetime as dt
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from swhurl.notifications import heartbeat as h
from swhurl.notifications.errors import NotificationError
from swhurl.run import FakeRunner, Result

NOW = dt.datetime(2026, 10, 3, 12, tzinfo=dt.UTC).timestamp()
URL = 'https://ntfy.sh/private-heartbeat-sentinel'
_DEFAULT = object()


def cronjob(*, age=60, suspended=False):
    status = {}
    if age is not None:
        status['lastSuccessfulTime'] = dt.datetime.fromtimestamp(NOW-age, dt.UTC).isoformat()
    return {'spec': {'suspend': suspended}, 'status': status}


def runner_for(job=_DEFAULT, *, api_error=None):
    job = cronjob() if job is _DEFAULT else job
    runner = FakeRunner()
    def job_result(args, _input):
        if api_error:
            return Result(args, 1, '', api_error)
        if job is None:
            return Result(args, 1, '', 'Error from server (NotFound): cronjobs.batch "console-notifications" not found')
        return Result(args, 0, json.dumps(job))
    runner.on('kubectl', '-n', 'console', 'get', 'cronjob', handler=job_result)
    secret = {'data': {h.SECRET_KEY: base64.b64encode(URL.encode()).decode()}}
    runner.on('kubectl', '-n', 'console', 'get', 'secret', stdout=json.dumps(secret))
    return runner


class HeartbeatActionTests(unittest.TestCase):
    def test_fresh_missing_suspended_and_old_checker(self):
        empty = {'alerting_since': None, 'last_alert': None}
        self.assertEqual(h.heartbeat_action(cronjob(), empty, NOW, 600)[0], 'none')
        self.assertEqual(h.heartbeat_action(None, empty, NOW, 600)[:2], ('alert', 'CronJob is missing'))
        self.assertEqual(h.heartbeat_action(cronjob(suspended=True), empty, NOW, 600)[0], 'alert')
        self.assertEqual(h.heartbeat_action(cronjob(age=601), empty, NOW, 600)[0], 'alert')

    def test_reminder_is_hourly_and_recovery_requires_a_prior_alert(self):
        empty = {'alerting_since': None, 'last_alert': None}
        state = {'alerting_since': NOW, 'last_alert': NOW}
        self.assertEqual(h.heartbeat_action(cronjob(age=None), state, NOW+1800, 600)[0], 'none')
        action, _reason, reminded = h.heartbeat_action(cronjob(age=None), state, NOW+3600, 600)
        self.assertEqual(action, 'remind')
        self.assertEqual(reminded['last_alert'], NOW+3600)
        action, _reason, recovered = h.heartbeat_action(cronjob(), state, NOW+120, 600)
        self.assertEqual((action, recovered), ('recover', empty))
        self.assertEqual(h.heartbeat_action(cronjob(), empty, NOW+120, 600)[0], 'none')

    def test_duration_parser(self):
        self.assertEqual(h.parse_duration('1s'), 1)
        self.assertEqual(h.parse_duration('10m'), 600)
        self.assertEqual(h.parse_duration('2h'), 7200)
        with self.assertRaises(argparse.ArgumentTypeError):
            h.parse_duration('0s')


class HeartbeatCommandTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.state_path = Path(self.temp.name) / 'notification-heartbeat.json'

    def tearDown(self):
        self.temp.cleanup()

    def run_main(self, runner, *args):
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            result = h.main(list(args), runner, now=NOW, state_path=self.state_path)
        return result, output.getvalue()

    def test_dry_run_does_not_read_secret_post_or_save(self):
        runner = runner_for(cronjob(age=601))
        result, output = self.run_main(runner, '--dry-run')
        self.assertEqual(result, 0)
        self.assertIn('Would notify: notification checker stale', output)
        self.assertEqual(len(runner.calls), 1)
        self.assertFalse(self.state_path.exists())

    def test_stale_alert_persists_state_only_after_ntfy_accepts(self):
        runner = runner_for(cronjob(age=601))
        with mock.patch.object(h, 'publish') as send:
            result, output = self.run_main(runner)
        self.assertEqual(result, 0)
        send.assert_called_once()
        self.assertEqual(send.call_args.args[0], URL)
        self.assertEqual(send.call_args.args[1]['title'], 'notification checker stale')
        self.assertEqual(json.loads(self.state_path.read_text()),
                         {'alerting_since': NOW, 'last_alert': NOW})
        self.assertEqual(self.state_path.stat().st_mode & 0o777, 0o600)
        self.assertNotIn(URL, output)

    def test_failed_cluster_read_leaves_existing_state_unchanged(self):
        original = b'{"alerting_since":12,"last_alert":13}\n'
        self.state_path.write_bytes(original)
        runner = runner_for(api_error='the Kubernetes API is unavailable')
        result, output = self.run_main(runner)
        self.assertEqual(result, 1)
        self.assertIn('cannot read notification checker', output)
        self.assertEqual(self.state_path.read_bytes(), original)

    def test_missing_cronjob_alerts_and_secret_never_appears_in_errors(self):
        runner = runner_for(job=None)
        with mock.patch.object(h, 'publish', side_effect=NotificationError(f'failed for {URL}')):
            result, output = self.run_main(runner)
        self.assertEqual(result, 1)
        self.assertNotIn(URL, output)
        self.assertFalse(self.state_path.exists())


if __name__ == '__main__':
    unittest.main()

"""Console logs stay one-line JSON and audit records retain operator/action identity."""
import datetime as dt
import json
import logging
import sys
import unittest
from unittest import mock

from swhurl.console import actions, logconfig, server
from swhurl.run import FakeRunner


class ConsoleLoggingTests(unittest.TestCase):
    def test_access_log_has_queryable_fields_and_escaped_newlines(self):
        record = logging.LogRecord('uvicorn.access', logging.INFO, '', 0,
                                   '%s - "%s %s HTTP/%s" %d',
                                   ('127.0.0.1:123', 'GET', '/healthz', '1.1', 200), None)
        text = logconfig.JsonFormatter().format(record)
        self.assertNotIn('\n', text)
        data = json.loads(text)
        self.assertEqual((data['method'], data['path'], data['status']), ('GET', '/healthz', 200))
        self.assertEqual((data['level'], data['logger']), ('INFO', 'uvicorn.access'))

    def test_exception_stays_in_a_single_json_line(self):
        try:
            raise ValueError('first\nsecond')
        except ValueError:
            record = logging.LogRecord('swhurl.console', logging.ERROR, '', 0, 'failed\nrequest', (), sys.exc_info())
        text = logconfig.JsonFormatter().format(record)
        self.assertNotIn('\n', text)
        self.assertIn('ValueError: first\nsecond', json.loads(text)['exception'])

    def test_default_audit_logs_include_fields_on_start_success_and_failure(self):
        jobs = actions.Jobs(FakeRunner(), inline=True, now=lambda: dt.datetime(2026, 10, 2, tzinfo=dt.UTC))
        with self.assertLogs('swhurl.console.audit', level='INFO') as captured:
            jobs.submit('test action', 'app/staging', 'operator@example.test', lambda job: None)
            jobs.submit('failing action', 'app/staging', 'operator@example.test',
                        lambda job: (_ for _ in ()).throw(actions.ActionError('refused')))
        records = [json.loads(logconfig.JsonFormatter().format(record)) for record in captured.records]
        self.assertEqual([r['state'] for r in records], ['started', 'succeeded', 'started', 'failed'])
        self.assertEqual(records[-1]['level'], 'ERROR')
        self.assertEqual(records[0]['identity'], 'operator@example.test')
        self.assertEqual(records[0]['action'], 'test action')
        self.assertEqual(records[0]['unit'], 'app/staging')
        self.assertEqual(records[0]['job_id'], 1)

    def test_server_installs_json_logging_for_uvicorn(self):
        with mock.patch('uvicorn.run') as run:
            self.assertEqual(server.main(['--dev', '--port', '8088']), 0)
        self.assertIs(run.call_args.kwargs['log_config'], logconfig.CONFIG)


if __name__ == '__main__':
    unittest.main()

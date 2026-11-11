"""Live log coverage cannot expose payloads; the fixtures are platform/otel's (tests/test_check_otel.py)."""
import io
import json
import unittest

from swhurl import logs
from swhurl.report import Report
from swhurl.run import FakeRunner


class LogsTests(unittest.TestCase):
    def test_live_coverage_reports_fallback_and_quiet_container_without_payloads(self):
        rows = [{'namespace': 'apps', 'pod': 'web', 'container': 'main', 'service': '', 'parser': 'json',
                 'records': 4, 'with_severity': 4, 'with_trace': 2, 'with_original': 4},
                {'namespace': 'apps', 'pod': 'web', 'container': 'main', 'service': '', 'parser': 'plain',
                 'records': 1, 'with_severity': 0, 'with_trace': 0, 'with_original': 0}]
        pods = {'items': [{'metadata': {'namespace': 'apps', 'name': name}, 'status': {'phase': 'Running'},
                          'spec': {'containers': [{'name': 'main'}]}} for name in ['web', 'quiet']]}
        runner = FakeRunner().on('kubectl', '-n', stdout='\n'.join(json.dumps(r) for r in rows)).on(
            'kubectl', 'get', 'pods', stdout=json.dumps(pods))
        report = Report(io.StringIO())
        self.assertEqual(logs.verify(runner, report), 0)
        self.assertIn('apps/quiet/main: no fresh records', report.out.getvalue())
        self.assertIn('plain; 1 records', report.out.getvalue())

    def test_missing_logs_or_failed_query_fails(self):
        for runner in [FakeRunner().on('kubectl', '-n').on('kubectl', 'get', stdout='{"items":[]}'),
                       FakeRunner().on('kubectl', '-n', returncode=1, stderr='sensitive log content')]:
            report = Report(io.StringIO())
            self.assertEqual(logs.verify(runner, report), 1)
            self.assertNotIn('sensitive log content', report.out.getvalue())


if __name__ == '__main__':
    unittest.main()

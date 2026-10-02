"""Log coverage checks cannot expose payloads and collector fixture failures fail validation."""
import io
import json
import unittest
from pathlib import Path
from unittest import mock
from urllib.error import URLError

import yaml

from swhurl import logs
from swhurl.report import Report
from swhurl.run import FakeRunner, Result


class LogsTests(unittest.TestCase):
    def test_otlp_values_round_trip(self):
        for value in ['text', 42, 3.5, True, ['one', 2], {'nested': {'key': 'value'}}]:
            with self.subTest(value=value):
                self.assertEqual(logs.unpack(logs.any_value(value)), value)

    def test_fixture_comparison_detects_drop_duplicate_field_loss_and_overwrites(self):
        case = {'name': 'fixture', 'input': 'original', 'expected': {'body': 'message', 'severityNumber': 9,
                'attributes': {'status': 200}}, 'attributes': {'existing': 'retained'},
                'record': {'traceId': '0123456789abcdef0123456789abcdef'}}
        record = {'body': logs.any_value('message'), 'severityNumber': 9, **case['record'],
                  'attributes': logs.attributes({'test.case': 'fixture', 'status': 200, 'existing': 'retained',
                                                 'log.record.original': 'original'})}
        self.assertEqual(logs.check_records([case], [record]), [])
        self.assertTrue(logs.check_records([case], []))
        self.assertTrue(logs.check_records([case], [record, record]))
        self.assertTrue(logs.check_records([case], [{**record, 'attributes': []}]))
        self.assertTrue(logs.check_records([case], [{**record, 'traceId': 'changed'}]))

    def test_fixture_runtime_uses_runner_and_exact_processors_and_flags(self):
        seen = {}
        case = {'name': 'fixture', 'input': 'message', 'attributes': {'log.parser': 'plain'},
                'expected': {'body': 'message', 'attributes': {'log.parser': 'plain'}}}

        def run(args, _):
            config = yaml.safe_load(Path(args[-1].removeprefix('--config=file:')).read_text())
            seen.update(config)
            Path(config['exporters']['file']['path']).write_text(json.dumps(logs.fixture_payload([case])) + '\n')
            return Result(args, 124)

        config = {'processors': {'transform/test': {'log_statements': ['set(body, body)']}, 'batch': {}},
                  'service': {'pipelines': {'logs': {'processors': ['transform/test', 'batch']}}}}
        runner = FakeRunner().on('timeout', handler=run)
        with mock.patch.object(logs, 'urlopen', return_value=io.BytesIO(b'{}')):
            errors = logs.exercise(runner, Path('/collector'), config,
                                   ['--feature-gates=ottl.functions.enableLambda'], [case])
        self.assertEqual(errors, [])
        self.assertEqual(seen['processors'], {'transform/test': config['processors']['transform/test']})
        self.assertIn('--feature-gates=ottl.functions.enableLambda', runner.calls[0])

    def test_fixture_collector_failure_is_reported(self):
        runner = FakeRunner().on('timeout', returncode=1)
        config = {'processors': {'transform/test': {}},
                  'service': {'pipelines': {'logs': {'processors': ['transform/test']}}}}
        with mock.patch.object(logs, 'urlopen', side_effect=URLError('refused')):
            self.assertTrue(logs.exercise(runner, Path('/collector'), config, [], []))

    def test_fixture_failures_fail_report_without_printing_bodies(self):
        report = Report(io.StringIO())
        with mock.patch.object(logs, 'exercise', return_value=['fixture: incorrect severityNumber']):
            logs.check_fixtures(FakeRunner(), Path('/collector'), {'processors': {}}, [], report)
        self.assertEqual(report.exit_code(), 1)
        self.assertNotIn('request completed', report.out.getvalue())

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

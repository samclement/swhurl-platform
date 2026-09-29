"""The swhurl foundation: Runner/FakeRunner, Report and the command dispatcher."""
import io
import os
import subprocess
import sys
import unittest

from swhurl import ROOT
from swhurl import __main__ as cli
from swhurl.report import Report
from swhurl.run import REDACTED, CommandError, FakeRunner, Result, Runner

SECRET = 'fixture-secret-token-value'


class RunnerTests(unittest.TestCase):
    def test_runs_real_commands_and_parses_json(self):
        runner = Runner()
        self.assertEqual(runner.output(['printf', 'hello']), 'hello')
        self.assertEqual(runner.json(['printf', '{"a": 1}']), {'a': 1})
        self.assertIsNone(runner.json(['true']))

    def test_failures_raise_with_redacted_detail(self):
        runner = Runner()
        runner.add_secret(SECRET)
        with self.assertRaises(CommandError) as ctx:
            runner.run(['sh', '-c', f'echo "bad {SECRET}" >&2; exit 3'])
        self.assertEqual(ctx.exception.returncode, 3)
        self.assertNotIn(SECRET, str(ctx.exception))
        self.assertIn(REDACTED, str(ctx.exception))

    def test_secret_output_is_never_quoted_in_errors(self):
        with self.assertRaises(CommandError) as ctx:
            Runner().run(['sh', '-c', 'cat; cat >&2 </dev/null; exit 1'], input=SECRET, secret_output=True)
        self.assertNotIn(SECRET, str(ctx.exception))
        with self.assertRaises(CommandError) as ctx:
            Runner().run(['sh', '-c', 'cat; exit 1'], input=SECRET)
        self.assertIn(SECRET, str(ctx.exception), 'control: without secret_output the output is quoted')

    def test_missing_command_is_a_clear_error(self):
        with self.assertRaisesRegex(CommandError, 'missing required command: no-such-tool-xyz'):
            Runner().run(['no-such-tool-xyz'])

    def test_unchecked_failure_returns_result(self):
        self.assertEqual(Runner().run(['false'], check=False).returncode, 1)

    def test_dry_run_plans_mutations_but_runs_reads(self):
        echoed = []
        runner = Runner(dry_run=True, echo=echoed.append)
        runner.add_secret(SECRET)
        self.assertEqual(runner.run(['touch', f'/nonexistent/{SECRET}'], mutating=True).returncode, 0)
        self.assertEqual(echoed, [f'  would run: touch /nonexistent/{REDACTED}'])
        self.assertEqual(runner.output(['printf', 'read']), 'read')

    def test_from_environment_honours_dry_run(self):
        old = os.environ.get('DRY_RUN')
        try:
            os.environ['DRY_RUN'] = 'true'
            self.assertTrue(Runner.from_environment().dry_run)
            os.environ['DRY_RUN'] = 'false'
            self.assertFalse(Runner.from_environment().dry_run)
        finally:
            os.environ.pop('DRY_RUN', None) if old is None else os.environ.__setitem__('DRY_RUN', old)


class PipeTests(unittest.TestCase):
    def test_streams_between_processes(self):
        self.assertEqual(Runner().pipe(['printf', 'plaintext-stream'], ['wc', '-c']).stdout.strip(), '16')

    def test_input_feeds_the_producer(self):
        self.assertEqual(Runner().pipe(['cat'], ['tr', 'a-z', 'A-Z'], input='config-on-stdin').stdout, 'CONFIG-ON-STDIN')

    def test_either_side_failing_raises_like_pipefail(self):
        with self.assertRaisesRegex(CommandError, 'exited 3, 0: producer broke'):
            Runner().pipe(['sh', '-c', 'echo producer broke >&2; exit 3'], ['cat'])
        with self.assertRaisesRegex(CommandError, 'exited 0, 4: consumer broke'):
            Runner().pipe(['printf', 'x'], ['sh', '-c', 'cat >/dev/null; echo consumer broke >&2; exit 4'])

    def test_stream_contents_never_reach_errors(self):
        with self.assertRaises(CommandError) as ctx:
            Runner().pipe(['sh', '-c', 'printf "$STREAM"'], ['sh', '-c', 'cat >/dev/null; exit 1'],
                          env={'STREAM': SECRET})
        self.assertNotIn(SECRET, str(ctx.exception))

    def test_large_stream_and_noisy_stderr_do_not_deadlock(self):
        out = Runner().pipe(['sh', '-c', 'head -c 3000000 /dev/zero; head -c 200000 /dev/zero | tr "\\0" x >&2'],
                            ['wc', '-c']).stdout
        self.assertEqual(out.strip(), '3000000')

    def test_missing_command_on_either_side(self):
        with self.assertRaisesRegex(CommandError, 'missing required command: no-such-producer'):
            Runner().pipe(['no-such-producer'], ['cat'])
        with self.assertRaisesRegex(CommandError, 'missing required command: no-such-consumer'):
            Runner().pipe(['printf', 'x'], ['no-such-consumer'])

    def test_dry_run_plans_the_pipe(self):
        fake = FakeRunner(dry_run=True)
        fake.pipe(['age', '-d'], ['kubectl', 'exec'], mutating=True)
        self.assertEqual(fake.calls, [])
        self.assertEqual(fake.echoed, ['  would run: age -d | kubectl exec'])

    def test_fake_pipe_feeds_producer_output_to_consumer(self):
        fake = FakeRunner().on('producer', stdout='dump').on(
            'consumer', handler=lambda args, stdin: Result(args, 0, f'got {stdin}'))
        self.assertEqual(fake.pipe(['producer'], ['consumer']).stdout, 'got dump')
        self.assertEqual(fake.calls, [('producer',), ('consumer',)])


class FakeRunnerTests(unittest.TestCase):
    def test_records_calls_and_answers_by_prefix(self):
        fake = FakeRunner().on('kubectl', 'get', stdout='{"kind": "List"}').on('flux', returncode=1, stderr='nope')
        self.assertEqual(fake.json(['kubectl', 'get', 'pods', '-o', 'json']), {'kind': 'List'})
        with self.assertRaisesRegex(CommandError, 'flux reconcile exited 1: nope'):
            fake.run(['flux', 'reconcile'])
        self.assertEqual(fake.calls, [('kubectl', 'get', 'pods', '-o', 'json'), ('flux', 'reconcile')])

    def test_unexpected_command_fails_the_test(self):
        with self.assertRaisesRegex(AssertionError, 'unexpected command: kubectl delete ns x'):
            FakeRunner().run(['kubectl', 'delete', 'ns', 'x'])

    def test_handler_sees_args_and_input(self):
        fake = FakeRunner().on('sops', handler=lambda args, stdin: __import__('swhurl.run').run.Result(args, 0, stdin.upper()))
        self.assertEqual(fake.output(['sops', 'x'], input='abc'), 'ABC')

    def test_dry_run_records_plans_not_calls(self):
        fake = FakeRunner(dry_run=True)
        fake.run(['kubectl', 'delete', 'pvc', 'data'], mutating=True)
        self.assertEqual(fake.calls, [])
        self.assertEqual(fake.planned, [('kubectl', 'delete', 'pvc', 'data')])
        self.assertEqual(fake.echoed, ['  would run: kubectl delete pvc data'])


class ReportTests(unittest.TestCase):
    def test_counts_and_exit_code(self):
        out = io.StringIO()
        report = Report(out)
        report.section('Flux')
        report.ok('unit Ready')
        report.warn('slow')
        self.assertEqual(report.exit_code(), 0)
        report.bad('unit failed')
        self.assertEqual((report.failures, report.warnings, report.exit_code()), (1, 1, 1))
        self.assertEqual(out.getvalue(), '\n== Flux ==\n[OK] unit Ready\n[WARN] slow\n[BAD] unit failed\n')


class ReportRedactionTests(unittest.TestCase):
    def test_report_redacts_through_the_runner(self):
        runner = Runner()
        runner.add_secret(SECRET)
        out = io.StringIO()
        report = Report(out, redact=runner.redact)
        report.bad(f'mismatch against {SECRET}')
        self.assertNotIn(SECRET, out.getvalue())
        self.assertEqual(report.lines, [f'[BAD] mismatch against {REDACTED}'])


class CliTests(unittest.TestCase):
    def run_cli(self, *args):
        return subprocess.run([sys.executable, '-m', 'swhurl', *args], cwd=ROOT, capture_output=True, text=True,
                              env={**os.environ, 'PYTHONPATH': str(ROOT / 'tools')})

    def test_help_lists_every_command(self):
        result = self.run_cli('--help')
        self.assertEqual(result.returncode, 0)
        for command in cli.COMMANDS:
            self.assertIn(command, result.stdout)

    def test_unknown_or_missing_command_exits_2(self):
        self.assertEqual(self.run_cli('nope').returncode, 2)
        self.assertEqual(self.run_cli().returncode, 2)

    def test_every_command_module_imports_and_has_main(self):
        import importlib
        for name, (module, function, _) in cli.COMMANDS.items():
            with self.subTest(command=name):
                self.assertTrue(callable(getattr(importlib.import_module(module), function)))


if __name__ == '__main__':
    unittest.main()

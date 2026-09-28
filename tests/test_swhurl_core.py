"""The swhurl foundation: Runner/FakeRunner, Report and the command dispatcher."""
import io
import os
import subprocess
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'tools'))

from swhurl import __main__ as cli  # noqa: E402
from swhurl.report import Report  # noqa: E402
from swhurl.run import REDACTED, CommandError, FakeRunner, Runner  # noqa: E402

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
        for name, (module, _) in cli.COMMANDS.items():
            with self.subTest(command=name):
                self.assertTrue(callable(importlib.import_module(module).main))


if __name__ == '__main__':
    unittest.main()

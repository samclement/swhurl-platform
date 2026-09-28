"""Live-test framework guarantees, offline: dry run, preflight, cleanup, label guards."""
import io
import json
import unittest
from contextlib import redirect_stderr, redirect_stdout

from swhurl.livetests import (
    LiveTest,
    Preflight,
    app_template,
    lifecycle,
    reloader,
    run_live_test,
)
from swhurl.report import Report
from swhurl.run import FakeRunner, Result


def kube(existing=None, fail_on=None):
    """A FakeRunner answering every kubectl call; ``existing`` maps 'kind/name' to objects."""
    existing = existing or {}

    def handler(args, _input):
        joined = ' '.join(args)
        if fail_on and fail_on in joined:
            return Result(args, 1, '', f'fixture failure in {fail_on}')
        if 'get' in args and '-o' in args and 'json' in args:
            kind, name = args[args.index('get') + 1], (args[args.index('get') + 2] if len(args) > args.index('get') + 2 else '')
            obj = existing.get(f'{kind}/{name}')
            if obj is None and kind == 'pv' and name == '-o':
                return Result(args, 0, json.dumps({'items': []}))
            return Result(args, 0, json.dumps(obj)) if obj is not None else Result(args, 1, '', 'NotFound')
        return Result(args, 0, '')
    return FakeRunner().on('kubectl', handler=handler).on('curl', stdout='302 https://accounts.google.com/x')


def mutating(runner):
    verbs = {'apply', 'create', 'label', 'delete', 'patch', 'run'}
    return [c for c in runner.calls if verbs & set(c)]


def run(module, runner):
    out, err = io.StringIO(), io.StringIO()
    with redirect_stdout(out), redirect_stderr(err):
        code = module.main([], runner=runner, report=Report(out), sleep=lambda _s: None)
    return code, out.getvalue() + err.getvalue()


class FrameworkTests(unittest.TestCase):
    def test_dry_run_prints_plan_and_calls_nothing(self):
        for module in (lifecycle, reloader, app_template):
            with self.subTest(module=module.__name__):
                runner = FakeRunner(dry_run=True)
                code, text = run(module, runner)
                self.assertEqual(code, 0)
                self.assertEqual(runner.calls, [])
                self.assertIn(module.__doc__.strip().splitlines()[0], text)

    def test_cleanup_runs_after_a_failing_step_and_verdict_precedes_it(self):
        events = []

        def body(t):
            t.cleanup.callback(lambda: events.append('cleanup'))
            t.check(True, 'first', 'x')
            t.runner.run(['kubectl', 'boom'])
        out = io.StringIO()
        runner = FakeRunner().on('kubectl', 'boom', returncode=1, stderr='exploded')
        with redirect_stdout(out):
            code = run_live_test('plan', body, passed='Passed.', runner=runner, report=Report(out))
        self.assertEqual(code, 1)
        self.assertEqual(events, ['cleanup'])
        self.assertIn('[BAD] kubectl boom exited 1: exploded', out.getvalue())
        self.assertNotIn('Passed.', out.getvalue())

    def test_preflight_refusal_exits_1_without_cleanup(self):
        events = []

        def body(t):
            raise Preflight('leftovers')
        err = io.StringIO()
        with redirect_stderr(err):
            code = run_live_test('plan', body, passed='x', runner=FakeRunner())
        self.assertEqual((code, events), (1, []))
        self.assertIn('[ERROR] leftovers', err.getvalue())

    def test_namespace_deletion_requires_the_label(self):
        runner = kube({'namespace/foreign': {'metadata': {'labels': {}}},
                       'namespace/mine': {'metadata': {'labels': {'l': 'true'}}}})
        t = LiveTest(runner)
        self.assertFalse(t.delete_namespace_if_labelled('foreign', 'l'))
        self.assertFalse(t.delete_namespace_if_labelled('absent', 'l'))
        self.assertTrue(t.delete_namespace_if_labelled('mine', 'l'))
        deletes = [c for c in runner.calls if 'delete' in c]
        self.assertEqual(deletes, [('kubectl', 'delete', 'namespace', 'mine', '--wait=false')])


class PreflightTests(unittest.TestCase):
    def test_each_test_refuses_leftovers_without_mutating(self):
        cases = {
            lifecycle: {'namespace/lifecycle-test': {'metadata': {}}},
            reloader: {'namespace/reloader-test': {'metadata': {}}},
            app_template: {'namespace/smoke-web-staging': {'metadata': {}}},
        }
        for module, existing in cases.items():
            with self.subTest(module=module.__name__):
                runner = kube(existing)
                code, text = run(module, runner)
                self.assertEqual(code, 1)
                self.assertIn('[ERROR]', text)
                self.assertEqual(mutating(runner), [])

    def test_a_failure_mid_test_still_cleans_up(self):
        runner = kube(fail_on='rollout status deploy/writer')
        code, text = run(lifecycle, runner)
        self.assertEqual(code, 1)
        self.assertIn('[INFO] Cleaned up lifecycle-test test resources', text)
        self.assertIn(('kubectl', '-n', 'flux-system', 'delete', 'kustomization', 'lifecycle-test',
                       'lifecycle-test-orphan', '--ignore-not-found', '--wait=true'), runner.calls)


if __name__ == '__main__':
    unittest.main()

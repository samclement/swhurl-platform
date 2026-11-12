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
    run_live_test,
    team_scenario,
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
        for module in (lifecycle, app_template, team_scenario):
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


class TeamScenarioTests(unittest.TestCase):
    """swhurl-v2 status, the cluster and ClickStack are scripted; the test must turn them into the right verdicts."""
    SPANS = [('reports', 'insights'), ('orders', 'payments'), ('notifier', 'payments')]

    @staticmethod
    def instance(app, env='staging', *, team='payments', calls=(), publishes=(), subscriptions=()):
        return {'application': app, 'environment': env, 'report': {},
                'inventory': {'team': team, 'calls': list(calls), 'publishes': list(publishes),
                              'subscriptions': list(subscriptions)}}

    def instances(self, linked=True):
        return [self.instance('reports', team='insights', calls=['orders'] if linked else []),
                self.instance('orders', publishes=['created']),
                self.instance('notifier', subscriptions=['orders.created']),
                self.instance('zeta', subscriptions=['orders.created']),
                self.instance('reports', 'prod', team='insights', calls=['orders'])]

    def runner(self, *, trace='t1\t3', spans=None, instances=None, logged='1', status=None):
        spans = self.SPANS if spans is None else spans
        instances = self.instances() if instances is None else instances
        # status --all exits 1 when an instance is bad, after printing the report
        status = status or Result((), 1, json.dumps({'version': 2, 'instances': instances}))
        queries = self.queries = []

        def handler(args, _input):
            if args[0] in ('go', 'git'):
                return Result(args, 0, 'abc123\n')
            if args[1:3] == ('status', '--all'):
                self.status = args
                return Result(args, status.returncode, status.stdout, status.stderr)
            if args[:3] == ('kubectl', 'config', 'view'):
                return Result(args, 0, '{"kind": "Config"}')
            if 'clickhouse-client' in args:
                query = args[-1]
                queries.append(query)
                if 'GROUP BY TraceId' in query:
                    return Result(args, 0, trace + '\n')
                if 'otel_logs' in query:
                    return Result(args, 0, logged + '\n')
                return Result(args, 0, '\n'.join('\t'.join(span) for span in spans) + '\n')
            return Result(args, 1, '', 'unexpected')
        return FakeRunner().on(handler=handler)

    def run_it(self, runner, env='staging'):
        from unittest import mock
        with mock.patch.dict('os.environ', {'ENV': env}):
            return run(team_scenario, runner)

    def test_passes_on_the_chain_it_found_and_only_reads(self):
        runner = self.runner()
        code, text = self.run_it(runner)
        self.assertEqual(code, 0, text)
        self.assertIn('Team scenario passed.', text)
        self.assertIn('The newest trace in which orders served reports (staging, last 10 minutes)', text)
        self.assertIn("SpanName = 'process orders.created'", self.queries[0])
        self.assertIn("ServiceName = 'notifier'", self.queries[0])
        self.assertIn("TraceId = 't1'", self.queries[1])
        self.assertIn('--kubeconfig', self.status)
        self.assertEqual(mutating(runner), [])
        self.assertTrue(all(call[0] in ('go', 'git') or call[1:3] in (('status', '--all'), ('config', 'view'))
                            or 'clickhouse-client' in call for call in runner.calls), runner.calls)

    def test_each_missing_piece_fails(self):
        cases = (({'trace': ''}, '[BAD] no trace in the last 10 minutes in which orders served reports'),
                 ({'trace': 't1\t1'}, '[BAD] no publish span from orders or no "process orders.created" span from notifier'),
                 ({'spans': [('reports', 'platform'), *self.SPANS[1:]]}, "[BAD] reports's spans carry team ['platform'], its records say insights"),
                 ({'spans': self.SPANS[:2]}, "[BAD] notifier's spans carry team none, its records say payments"),
                 ({'logged': '0'}, '[BAD] orders logged no line in this trace with caller.id = reports'))
        for kwargs, expected in cases:
            with self.subTest(expected=expected):
                code, text = self.run_it(self.runner(**kwargs))
                self.assertEqual(code, 1)
                self.assertIn(expected, text)

    def test_no_chain_or_no_status_stops_before_any_query(self):
        cases = ((self.runner(instances=self.instances(linked=False)), 'staging', '[ERROR] no staging instances form a chain'),
                 (self.runner(), 'prod', '[ERROR] no prod instances form a chain'),
                 (self.runner(status=Result((), 2, '', 'usage')), 'staging', '[ERROR] swhurl-v2 status --all exited 2: usage'))
        for runner, env, expected in cases:
            with self.subTest(expected=expected):
                code, text = self.run_it(runner, env)
                self.assertEqual(code, 1)
                self.assertIn(expected, text)
                self.assertEqual(self.queries, [])


if __name__ == '__main__':
    unittest.main()

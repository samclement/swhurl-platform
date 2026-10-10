"""Live-test framework guarantees, offline: dry run, preflight, cleanup, label guards."""
import io
import json
import unittest
from contextlib import redirect_stderr, redirect_stdout

from swhurl.livetests import (
    LiveTest,
    Preflight,
    app_template,
    call_identity,
    lifecycle,
    network_policy,
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
        for module in (lifecycle, reloader, app_template, call_identity, network_policy):
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
            call_identity: {'namespace/call-identity-callee': {'metadata': {}}},
            network_policy: {'namespace/network-policy-test-client': {'metadata': {}}},
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


class CallIdentityTests(unittest.TestCase):
    """The callee's answers are scripted; the test must turn them into the right verdicts."""

    GOOD = {'accepted': True, 'anonymousKeysStatus': 401, 'sub': call_identity.SUBJECT, 'aud': ['callee'],
            'exp': 1, 'apiStatus': 401}

    def runner(self, good=None, rotated_exp=2):
        good = good or self.GOOD
        state = {'calls': 0}

        def handler(args, _input):
            if 'caller.mjs' in ' '.join(args):
                audience = args[-1]
                if audience == 'callee':
                    state['calls'] += 1
                    answer = dict(good, exp=good.get('exp') if state['calls'] == 1 else rotated_exp)
                else:
                    answer = {'accepted': False, 'reason': 'wrong audience' if audience == 'other' else 'no token'}
                return Result(args, 0, json.dumps(answer))
            if '--raw' in args:
                return Result(args, 0, '{"keys": []}')
            if 'get' in args or ('exec' in args and 'test' in args):
                return Result(args, 1, '', 'NotFound')
            return Result(args, 0, '')
        return FakeRunner().on('kubectl', handler=handler)

    def test_passes_and_removes_only_what_it_created(self):
        runner = self.runner()
        code, text = run(call_identity, runner)
        self.assertEqual(code, 0, text)
        self.assertIn('Call identity test passed.', text)
        self.assertIn('the API refuses an anonymous request for the keys (HTTP 401)', text)
        self.assertIn('token file replaced before expiry', text)
        # Cleanup found nothing carrying the label in this fake, so it deleted nothing.
        self.assertEqual([c for c in runner.calls if 'delete' in c], [])

    def test_a_token_the_api_accepts_fails_the_test(self):
        code, text = run(call_identity, self.runner(dict(self.GOOD, apiStatus=200)))
        self.assertEqual(code, 1)
        self.assertIn('[BAD] the Kubernetes API answered 200 to the token', text)

    def test_a_refused_token_stops_before_the_long_wait(self):
        runner = self.runner({'accepted': False, 'reason': 'bad signature'})
        code, text = run(call_identity, runner)
        self.assertEqual(code, 1)
        self.assertIn("[BAD] token not accepted", text)
        self.assertNotIn('Rotation', text)

    def test_a_token_that_never_rotates_fails_the_test(self):
        code, text = run(call_identity, self.runner(rotated_exp=1))
        self.assertEqual(code, 1)
        self.assertIn('[BAD] token not replaced within 10 minutes', text)


class NetworkPolicyTests(unittest.TestCase):
    def runner(self, enforced=True, traefik_blocked=False):
        state = {'policy': False}

        def kubectl(args, _input):
            if 'apply' in args and 'NetworkPolicy' in (_input or ''):
                state['policy'] = True
            if 'delete' in args and 'networkpolicy' in args:
                state['policy'] = False
            if 'exec' in args:
                blocked = state['policy'] and enforced and ':8080/' in args[-1]
                return Result(args, 0, 'blocked' if blocked else 'ok')
            if 'get' in args and 'deploy' in args:
                return Result(args, 0, json.dumps({'status': {'readyReplicas': 1}}))
            if 'get' in args:
                return Result(args, 1, '', 'NotFound')
            return Result(args, 0, '')

        def curl(args, _input):
            return Result(args, 0, '000' if state['policy'] and traefik_blocked else '200')
        return FakeRunner().on('kubectl', handler=kubectl).on('curl', handler=curl)

    def test_passes_when_only_the_other_namespace_is_blocked(self):
        code, text = run(network_policy, self.runner())
        self.assertEqual(code, 0, text)
        self.assertIn('Network policy test passed.', text)

    def test_fails_when_the_policy_is_not_enforced(self):
        code, text = run(network_policy, self.runner(enforced=False))
        self.assertEqual(code, 1)
        self.assertIn('[BAD] another namespace still reaches the app: the policy is not enforced', text)

    def test_fails_when_traefik_is_blocked_too(self):
        code, text = run(network_policy, self.runner(traefik_blocked=True))
        self.assertEqual(code, 1)
        self.assertIn('[BAD] Traefik is blocked too: HTTP 000', text)


if __name__ == '__main__':
    unittest.main()

"""app status/logs/reconcile/check: output and commands, offline, with FakeRunner."""
import io
import json
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock

from swhurl import ROOT
from swhurl.apps import ops as app_ops
from swhurl.run import FakeRunner, Result

REV = 'main@sha1:abc123'
DIGEST = 'sha256:' + 'a' * 64


def ready(status='True', message='ok'):
    return {'status': {'conditions': [{'type': 'Ready', 'status': status, 'message': message}]}}


def cluster(**overrides):
    objects = {
        'kustomization': {**ready(message=f'Applied revision: {REV}'),
                          'status': {**ready(message=f'Applied revision: {REV}')['status'], 'lastAppliedRevision': REV}},
        'gitrepository': {'status': {'artifact': {'revision': REV}}},
        'helmrelease': {**ready(message='Helm upgrade succeeded'), 'spec': {'values': {'controllers': {'main': {
            'containers': {'main': {'image': {'repository': 'docker.io/x/web', 'tag': '1.0', 'digest': DIGEST}}}}}}}},
        'pods': {'items': [{'metadata': {'name': 'web-1'}, 'status': {'containerStatuses': [
            {'ready': True, 'imageID': f'docker-pullable://docker.io/x/web@{DIGEST}'}]}}]},
        'deploy,statefulset,daemonset': {'items': [{'kind': 'Deployment', 'metadata': {'name': 'web'},
                                                    'spec': {'replicas': 1}, 'status': {'readyReplicas': 1}}]},
        'ingress': {'items': [{'spec': {'rules': [{'host': 'web.homelab.swhurl.com'}]}}]},
        'certificate': {'items': [{'metadata': {'name': 'web-tls'}, **ready()}]},
    }
    objects.update(overrides)
    fake = FakeRunner()
    for kind, value in objects.items():
        def handler(args, _input, value=value):
            if value is None:
                return Result(args, 1, '', 'NotFound')
            return Result(args, 0, json.dumps(value))
        fake.on('kubectl', '-n', 'flux-system' if kind in ('kustomization', 'gitrepository') else 'web-prod',
                'get', kind, handler=handler)
    return fake.on('kubectl', '-n', 'flux-system', 'logs').on('kubectl', '-n', 'web-prod', 'logs').on('flux')


def run(runner, *argv, env=None):
    out, err = io.StringIO(), io.StringIO()
    with mock.patch.dict('os.environ', env or {}), redirect_stdout(out), redirect_stderr(err):
        code = app_ops.main(list(argv), runner)
    return code, out.getvalue(), err.getvalue()


class StatusTests(unittest.TestCase):
    def test_healthy_instance(self):
        code, out, _ = run(cluster(), 'status', 'web', 'prod')
        self.assertEqual(code, 0)
        self.assertEqual(out.splitlines(), [
            'Instance   web/prod (namespace web-prod)',
            'Git        desired abc123, applied abc123',
            f'Flux unit  True: Applied revision: {REV}',
            'Release    True: Helm upgrade succeeded',
            f'Image      desired docker.io/x/web:1.0@{DIGEST}',
            f'           running docker.io/x/web@{DIGEST}',
            'Replicas   Deployment/web: 1/1 ready',
            'Route      https://web.homelab.swhurl.com',
            'TLS        web-tls: True',
        ])

    def test_unapplied_revision_and_failing_container(self):
        runner = cluster(
            gitrepository={'status': {'artifact': {'revision': 'main@sha1:def456'}}},
            pods={'items': [{'metadata': {'name': 'web-2'}, 'status': {'containerStatuses': [{
                'ready': False, 'state': {'waiting': {'reason': 'ImagePullBackOff', 'message': 'not found ' * 40}}}]}}]})
        _, out, _ = run(runner, 'status', 'web', 'prod')
        self.assertIn('Git        desired def456, applied abc123  <- not yet applied', out)
        self.assertIn('           running none', out)
        problem = out.split('Problems\n')[1].splitlines()[0]
        self.assertTrue(problem.startswith('  web-2: ImagePullBackOff not found'))
        self.assertLessEqual(len(problem), len('  web-2: ImagePullBackOff ') + 160)

    def test_missing_release_is_reported_not_fatal(self):
        code, out, _ = run(cluster(helmrelease=None), 'status', 'web', 'prod')
        self.assertEqual(code, 0)
        self.assertIn('Release    Unknown: not found', out)

    def test_missing_unit_fails(self):
        code, _, err = run(cluster(kustomization=None), 'status', 'web', 'prod')
        self.assertEqual(code, 1)
        self.assertIn('Flux unit app-web-prod not found', err)


class GatherStatusTests(unittest.TestCase):
    def test_structured_status_matches_what_is_printed(self):
        found = app_ops.gather_status(cluster(), app_ops.Instance('web', 'prod'))
        self.assertTrue(found.applied)
        self.assertEqual(found.unit, ('True', f'Applied revision: {REV}'))
        self.assertEqual(found.running_images, [f'docker.io/x/web@{DIGEST}'])
        self.assertEqual(found.replicas, [app_ops.Replicas('Deployment/web', 1, 1)])
        self.assertEqual((found.routes, found.certificates, found.problems),
                         (['web.homelab.swhurl.com'], [('web-tls', 'True')], []))

    def test_missing_unit_is_none_and_missing_release_is_none(self):
        self.assertIsNone(app_ops.gather_status(cluster(kustomization=None), app_ops.Instance('web', 'prod')))
        found = app_ops.gather_status(cluster(helmrelease=None), app_ops.Instance('web', 'prod'))
        self.assertIsNone(found.release)


class LogsAndReconcileTests(unittest.TestCase):
    def test_logs_default_and_follow(self):
        runner = cluster()
        run(runner, 'logs', 'web', 'prod')
        run(runner, 'logs', 'web', 'prod', env={'FOLLOW': 'true', 'TAIL': '5'})
        self.assertEqual(runner.calls, [
            ('kubectl', '-n', 'web-prod', 'logs', 'deploy/web', '--all-containers', '--tail=100'),
            ('kubectl', '-n', 'web-prod', 'logs', 'deploy/web', '--all-containers', '--tail=5', '--follow'),
        ])

    def test_reconcile_fetches_git_then_the_one_unit(self):
        runner = cluster()
        self.assertEqual(run(runner, 'reconcile', 'web', 'prod')[0], 0)
        self.assertEqual([c[:4] for c in runner.calls], [('flux', 'reconcile', 'source', 'git'),
                                                        ('flux', 'reconcile', 'kustomization', 'app-web-prod')])

    def test_reconcile_dry_run_changes_nothing(self):
        runner = FakeRunner(dry_run=True)
        self.assertEqual(run(runner, 'reconcile', 'web', 'prod')[0], 0)
        self.assertEqual(runner.calls, [])
        self.assertEqual(len(runner.planned), 2)

    def test_reconcile_stops_at_first_failure(self):
        runner = FakeRunner().on('flux', 'reconcile', 'source', returncode=1)
        self.assertEqual(run(runner, 'reconcile', 'web', 'prod')[0], 1)
        self.assertEqual(len(runner.calls), 1)


class UsageTests(unittest.TestCase):
    def test_bad_usage_exits_2_without_cluster_calls(self):
        for argv in ([], ['status', 'web'], ['delete', 'web', 'prod'], ['status', 'web', 'prod', 'extra']):
            with self.subTest(argv=argv):
                runner = FakeRunner()
                self.assertEqual(run(runner, *argv)[0], 2)
                self.assertEqual(runner.calls, [])

    def test_check_runs_the_policy_for_that_instance(self):
        with mock.patch.object(app_ops.policy, 'main', return_value=0) as policy:
            self.assertEqual(run(FakeRunner(), 'check', 'hello', 'prod')[0], 0)
        policy.assert_called_once_with([str(ROOT / 'apps/hello/prod')])


if __name__ == '__main__':
    unittest.main()

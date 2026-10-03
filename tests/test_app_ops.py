"""app status/logs/reconcile/check: output and commands, offline, with FakeRunner."""
import io
import json
import unittest
from contextlib import redirect_stderr, redirect_stdout
from unittest import mock

from swhurl import ROOT
from swhurl.apps import ops as app_ops
from swhurl.run import CommandError, FakeRunner, Result

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
        'ingress': {'items': [{'metadata': {'annotations': {
            'traefik.ingress.kubernetes.io/router.middlewares': 'ingress-oauth-auth-shared@kubernetescrd'}},
            'spec': {'rules': [{'host': 'web.homelab.swhurl.com'}]}}]},
        'certificate': {'items': [{'metadata': {'name': 'web-tls'}, **ready()}]},
    }
    objects.update(overrides)
    fake = FakeRunner()
    for kind, value in objects.items():
        def handler(args, _input, value=value):
            if isinstance(value, Result):
                return Result(args, value.returncode, value.stdout, value.stderr)
            if value is None:
                return Result(args, 0, '')
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
            f'Image      docker.io/x/web:1.0@{DIGEST}',
            '           running: matches desired',
            'Replicas   Deployment/web: 1/1 ready',
            'Exposure   signed-in (Google sign-in)',
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

    def test_read_failures_are_errors_not_absence(self):
        for detail in ('Forbidden', 'connection refused', 'missing required command: kubectl'):
            with self.subTest(detail=detail):
                runner = FakeRunner().on('kubectl', returncode=1, stderr=detail)
                code, out, err = run(runner, 'status', 'web', 'prod')
                self.assertEqual(code, 1)
                self.assertIn(detail, err)
                self.assertNotIn('not found', err)
                self.assertEqual(out, '')
                with self.assertRaises(CommandError):
                    app_ops.get(runner, 'get', 'pods')

    def test_secondary_read_failure_does_not_print_partial_success(self):
        runner = cluster(pods=Result((), 1, '', 'Forbidden'))
        code, out, err = run(runner, 'status', 'web', 'prod')
        self.assertEqual((code, out), (1, ''))
        self.assertIn('Forbidden', err)


class ImageStateTests(unittest.TestCase):
    def found(self, desired, running):
        instance = app_ops.Instance('web', 'prod')
        return app_ops.InstanceStatus(instance, REV, REV, ('True', ''), None, desired, running, [], [], [], [])

    def test_compared_by_digest_because_running_images_have_no_tag(self):
        want = f'docker.io/x/web:1.0@{DIGEST}'
        other = 'sha256:' + 'f' * 64
        cases = {
            'matches': (want, [f'docker.io/x/web@{DIGEST}']),
            'different': (want, [f'docker.io/x/web@{DIGEST}', f'docker.io/x/web@{other}']),  # mid-rollout
            'none': (want, []),
            'unpinned': ('docker.io/x/web:1.0', [f'docker.io/x/web@{DIGEST}']),
        }
        for state, (desired, running) in cases.items():
            with self.subTest(state=state):
                self.assertEqual(self.found(desired, running).image_state, state)

    def test_lines_say_it_plainly(self):
        want = f'docker.io/x/web:1.0@{DIGEST}'
        same = app_ops.status_lines(self.found(want, [f'docker.io/x/web@{DIGEST}']))
        self.assertIn(f'Image      {want}', same)
        self.assertIn('           running: matches desired', same)
        other = 'sha256:' + 'f' * 64
        moved = app_ops.status_lines(self.found(want, [f'docker.io/x/web@{other}']))
        self.assertIn(f'Image      desired {want}', moved)
        self.assertTrue(any('different image' in line for line in moved))


class RunningImagesTests(unittest.TestCase):
    def pod(self, spec_image, image_id, name='main'):
        return {'spec': {'containers': [{'name': name, 'image': spec_image}]},
                'status': {'containerStatuses': [{'name': name, 'imageID': image_id}]}}

    def test_sidecars_and_other_controllers_do_not_mask_the_main_image_rollout(self):
        main = self.pod(f'ghcr.io/x/app@{DIGEST}', 'pulled')
        main['spec']['containers'].append({'name': 'sidecar', 'image': 'ghcr.io/x/helper@sha256:' + 'f' * 64})
        main['status']['containerStatuses'].append({'name': 'sidecar', 'imageID': 'helper-pulled'})
        other = self.pod('ghcr.io/x/cache@sha256:' + 'e' * 64, 'cache-pulled')
        other['metadata'] = {'labels': {'app.kubernetes.io/controller': 'cache'}}
        self.assertEqual(app_ops.running_images({'items': [main, other]}), [f'ghcr.io/x/app@{DIGEST}'])
        old = self.pod('ghcr.io/x/app@sha256:' + '0' * 64, 'old-main')
        self.assertEqual(len(app_ops.running_images({'items': [main, other, old]})), 2,
                         'old main pods still prevent a matching-image verdict during rollout')

    def test_a_pinned_digest_is_what_runs_even_when_the_node_names_another(self):
        index = 'sha256:' + '1' * 64
        alias = 'sha256:' + '0' * 64  # the same content, first pulled under another index digest
        pods = {'items': [self.pod(f'ghcr.io/x/app:15-74b90c4@{index}', f'ghcr.io/x/app@{alias}')]}
        self.assertEqual(app_ops.running_images(pods), [f'ghcr.io/x/app@{index}'])
        found = app_ops.InstanceStatus(app_ops.Instance('app', 'staging'), REV, REV, ('True', ''), None,
                                       f'ghcr.io/x/app:15-74b90c4@{index}', app_ops.running_images(pods), [], [], [], [])
        self.assertEqual(found.image_state, 'matches')

    def test_unpinned_or_mid_rollout_still_read_correctly(self):
        old, new = 'sha256:' + 'a' * 64, 'sha256:' + 'b' * 64
        self.assertEqual(app_ops.running_images({'items': [self.pod('docker.io/x/web:1.0', f'docker-pullable://docker.io/x/web@{old}')]}),
                         [f'docker.io/x/web@{old}'], 'no pinned digest: the node is all there is')
        rollout = {'items': [self.pod(f'docker.io/x/web:1@{old}', 'x'), self.pod(f'docker.io/x/web:2@{new}', 'y')]}
        self.assertEqual(app_ops.running_images(rollout), [f'docker.io/x/web@{old}', f'docker.io/x/web@{new}'])
        self.assertEqual(app_ops.running_images({'items': [self.pod(f'localhost:5000/web@{new}', 'z')]}),
                         [f'localhost:5000/web@{new}'], 'a registry port is not a tag')
        waiting = {'items': [{'spec': {'containers': [{'name': 'main', 'image': f'x@{new}'}]},
                              'status': {'containerStatuses': [{'name': 'main', 'imageID': ''}]}}]}
        self.assertEqual(app_ops.running_images(waiting), [], 'not pulled yet: nothing runs')


class HintTests(unittest.TestCase):
    SETTINGS = {'memory_limit': '128Mi', 'health_path': '/healthz', 'port': '8080'}

    def problem(self, state, last=None):
        status = {'ready': False, 'state': state}
        if last:
            status['lastState'] = {'terminated': {'reason': last}}
        pods = {'items': [{'metadata': {'name': 'web-1'}, 'status': {'containerStatuses': [status]}}]}
        (found,) = app_ops.problems(pods, app_ops.Instance('web', 'prod'), self.SETTINGS)
        return found

    def test_each_common_failure_names_its_fix(self):
        cases = {
            'oom': (self.problem({'waiting': {'reason': 'CrashLoopBackOff'}}, last='OOMKilled'),
                    'ran out of memory (limit 128Mi)', 'make app-scale APP=web ENV=prod'),
            'pull': (self.problem({'waiting': {'reason': 'ImagePullBackOff'}}), 'cannot pull the image', 'public'),
            'secret': (self.problem({'waiting': {'reason': 'CreateContainerConfigError'}}),
                       'apps/web/prod/secret.sops.yaml', 'check-secrets'),
            'crash': (self.problem({'waiting': {'reason': 'CrashLoopBackOff'}}, last='Error'),
                      'PREVIOUS=true', 'port other than 8080'),
            'probe': (self.problem({'running': {}}), '/healthz on port 8080', '--health-path'),
        }
        for name, (found, *expected) in cases.items():
            with self.subTest(name=name):
                for text in expected:
                    self.assertIn(text, found.hint)
        self.assertEqual(cases['oom'][0].message, 'last exit: OOMKilled')

    def test_unknown_reasons_have_no_hint_and_status_prints_hints(self):
        self.assertEqual(self.problem({'waiting': {'reason': 'SomethingNew'}}).hint, '')
        instance = app_ops.Instance('web', 'prod')
        found = app_ops.InstanceStatus(instance, REV, REV, ('True', ''), None, 'x:1', [], [], [], [],
                                       [self.problem({'waiting': {'reason': 'ImagePullBackOff'}})])
        self.assertTrue(any(line.startswith('    fix: The node cannot pull') for line in app_ops.status_lines(found)))


class LiveExposureTests(unittest.TestCase):
    def test_read_from_the_routes(self):
        signed_in = {'metadata': {'annotations': {'traefik.ingress.kubernetes.io/router.middlewares':
                                                  'ingress-oauth-auth-shared@kubernetescrd'}}}
        self.assertEqual(app_ops.live_exposure([]), 'private')
        self.assertEqual(app_ops.live_exposure([signed_in]), 'authenticated-web')
        self.assertEqual(app_ops.live_exposure([{'metadata': {}}]), 'public')
        self.assertEqual(app_ops.live_exposure([signed_in, {}]), 'public', 'any open route makes it public')
        solver = {'metadata': {'labels': {'acme.cert-manager.io/http01-solver': 'true'}}}
        self.assertEqual(app_ops.live_exposure(app_ops.app_routes([signed_in, solver])), 'authenticated-web',
                         "cert-manager's challenge route is not the app's")


class GatherStatusTests(unittest.TestCase):
    def test_structured_status_matches_what_is_printed(self):
        found = app_ops.gather_status(cluster(), app_ops.Instance('web', 'prod'))
        self.assertTrue(found.applied)
        self.assertEqual(found.unit, ('True', f'Applied revision: {REV}'))
        self.assertEqual(found.running_images, [f'docker.io/x/web@{DIGEST}'])
        self.assertEqual(found.replicas, [app_ops.Replicas('Deployment/web', 1, 1)])
        self.assertEqual((found.routes, found.certificates, found.problems),
                         (['web.homelab.swhurl.com'], [('web-tls', 'True')], []))

    def test_environment_lists_platform_variables_and_the_secret_by_name(self):
        container = {'env': {'HOST_IP': {'valueFrom': {'fieldRef': {'fieldPath': 'status.hostIP'}}},
                             'OTEL_SERVICE_NAME': 'web'},
                     'envFrom': [{'secretRef': {'name': 'web-secret'}}]}
        release = {'spec': {'values': {'controllers': {'main': {'containers': {'main': container}}}}}}
        self.assertEqual(app_ops.release_env(release),
                         ({'HOST_IP': 'from the pod (status.hostIP)', 'OTEL_SERVICE_NAME': 'web'}, 'web-secret'))
        container['env'] = [{'name': 'A', 'value': '1'}]
        self.assertEqual(app_ops.release_env(release)[0], {'A': '1'})
        self.assertEqual(app_ops.release_env(None), ({}, ''))

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

"""An instance's live status as the console and the promotion gate gather it, offline, with FakeRunner."""
import json
import unittest

from swhurl.apps import ops as app_ops
from swhurl.apps.instance import Instance
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
        'deploy,statefulset,daemonset,cronjob': {'items': [{'kind': 'Deployment', 'metadata': {'name': 'web'},
                                                    'spec': {'replicas': 1}, 'status': {'readyReplicas': 1}}]},
        'ingress': {'items': [{'metadata': {'annotations': {
            'traefik.ingress.kubernetes.io/router.middlewares': 'ingress-oauth-auth-shared@kubernetescrd'}},
            'spec': {'rules': [{'host': 'web.homelab.swhurl.com'}]}}]},
        'certificate': {'items': [{'metadata': {'name': 'web-tls'}, **ready()}]},
    }
    objects.update(overrides)
    team = objects.pop('namespace', {'metadata': {'labels': {'platform.swhurl.com/team': 'payments'}}})
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
    fake.on('kubectl', 'get', 'namespace', 'web-prod', stdout=json.dumps(team) if isinstance(team, dict) else '',
            returncode=0 if isinstance(team, dict) else 1)
    return fake.on('kubectl', '-n', 'flux-system', 'logs').on('kubectl', '-n', 'web-prod', 'logs').on('flux')


class GatheredFactsTests(unittest.TestCase):
    def test_an_unapplied_revision_and_a_failing_container(self):
        runner = cluster(
            gitrepository={'status': {'artifact': {'revision': 'main@sha1:def456'}}},
            pods={'items': [{'metadata': {'name': 'web-2'}, 'status': {'containerStatuses': [{
                'ready': False, 'state': {'waiting': {'reason': 'ImagePullBackOff', 'message': 'not found ' * 40}}}]}}]})
        found = app_ops.gather_status(runner, Instance('web', 'prod'))
        self.assertFalse(found.at_newest)
        self.assertEqual(found.running_images, [])
        (problem,) = found.problems
        self.assertEqual((problem.pod, problem.reason), ('web-2', 'ImagePullBackOff'))
        self.assertEqual(len(problem.message), 160)

    def test_an_unreadable_namespace_has_no_team(self):
        self.assertEqual(app_ops.team_of(cluster(), Instance('web', 'prod')), 'payments')
        self.assertEqual(app_ops.team_of(cluster(namespace=None), Instance('web', 'prod')), '')

    def test_read_failures_are_errors_not_absence(self):
        for detail in ('Forbidden', 'connection refused', 'missing required command: kubectl'):
            with self.subTest(detail=detail):
                runner = FakeRunner().on('kubectl', returncode=1, stderr=detail)
                with self.assertRaises(CommandError):
                    app_ops.gather_status(runner, Instance('web', 'prod'))
                with self.assertRaises(CommandError):
                    app_ops.get(runner, 'get', 'pods')

    def test_a_secondary_read_failure_is_an_error_not_a_partial_status(self):
        with self.assertRaises(CommandError):
            app_ops.gather_status(cluster(pods=Result((), 1, '', 'Forbidden')), Instance('web', 'prod'))


class ImageStateTests(unittest.TestCase):
    def found(self, desired, running):
        instance = Instance('web', 'prod')
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
        found = app_ops.InstanceStatus(Instance('app', 'staging'), REV, REV, ('True', ''), None,
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
        (found,) = app_ops.problems(pods, Instance('web', 'prod'), self.SETTINGS)
        return found

    def test_each_common_failure_names_its_fix(self):
        cases = {
            'oom': (self.problem({'waiting': {'reason': 'CrashLoopBackOff'}}, last='OOMKilled'),
                    'ran out of memory (limit 128Mi)', 'swhurl-v2 edit --set memoryMiB=256'),
            'pull': (self.problem({'waiting': {'reason': 'ImagePullBackOff'}}), 'cannot pull the image', 'public'),
            'secret': (self.problem({'waiting': {'reason': 'CreateContainerConfigError'}}),
                       'set the value by hand', 'check-secrets'),
            'crash': (self.problem({'waiting': {'reason': 'CrashLoopBackOff'}}, last='Error'),
                      'PREVIOUS=true', 'port other than 8080'),
            'probe': (self.problem({'running': {}}), '/healthz on port 8080', '--health-path'),
        }
        for name, (found, *expected) in cases.items():
            with self.subTest(name=name):
                for text in expected:
                    self.assertIn(text, found.hint)
        self.assertEqual(cases['oom'][0].message, 'last exit: OOMKilled')

    def test_unknown_reasons_have_no_hint(self):
        self.assertEqual(self.problem({'waiting': {'reason': 'SomethingNew'}}).hint, '')


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
        found = app_ops.gather_status(cluster(), Instance('web', 'prod'))
        self.assertTrue(found.at_newest)
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
        self.assertEqual(app_ops.shown_variables(release),
                         ({'HOST_IP': 'from the pod (status.hostIP)', 'OTEL_SERVICE_NAME': 'web'}, 'web-secret'))
        container['env'] = [{'name': 'A', 'value': '1'}]
        self.assertEqual(app_ops.shown_variables(release)[0], {'A': '1'})
        self.assertEqual(app_ops.shown_variables(None), ({}, ''))

    def test_missing_unit_is_none_and_missing_release_is_none(self):
        self.assertIsNone(app_ops.gather_status(cluster(kustomization=None), Instance('web', 'prod')))
        found = app_ops.gather_status(cluster(helmrelease=None), Instance('web', 'prod'))
        self.assertIsNone(found.release)


if __name__ == '__main__':
    unittest.main()

"""Retained notification/image helpers and desired-variable presentation.

Live status/failure/absence/replica/route tests moved to Go and test_console_status;
legacy pod-fix guesses are deliberately removed rather than ported as policy.
"""
import json
import unittest

from swhurl.apps import ops as app_ops
from swhurl.apps.instance import Instance
from swhurl.run import CommandError, FakeRunner

DIGEST = 'sha256:' + 'a' * 64


def namespace_reader(namespace):
    return FakeRunner().on('kubectl', 'get', 'namespace', 'web-prod',
                           stdout=json.dumps(namespace) if namespace is not None else '',
                           returncode=0 if namespace is not None else 1)


class NamespaceReadTests(unittest.TestCase):
    def test_an_unreadable_namespace_has_no_team(self):
        self.assertEqual(app_ops.team_of(namespace_reader({'metadata': {'labels': {'platform.swhurl.com/team': 'payments'}}}), Instance('web', 'prod')), 'payments')
        self.assertEqual(app_ops.team_of(namespace_reader(None), Instance('web', 'prod')), '')

    def test_read_failures_are_errors_not_absence(self):
        for detail in ('Forbidden', 'connection refused', 'missing required command: kubectl'):
            with self.subTest(detail=detail):
                runner = FakeRunner().on('kubectl', returncode=1, stderr=detail)
                with self.assertRaises(CommandError):
                    app_ops.get(runner, 'get', 'pods')


class ImageStateTests(unittest.TestCase):
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
                self.assertEqual(app_ops.image_state(desired, running), state)


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
        self.assertEqual(app_ops.image_state(f'ghcr.io/x/app:15-74b90c4@{index}', app_ops.running_images(pods)), 'matches')

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


class DesiredVariablesTests(unittest.TestCase):
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

if __name__ == '__main__':
    unittest.main()

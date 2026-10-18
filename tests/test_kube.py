"""Reads of the API server in place of ``kubectl get``: what is translated, what is left to kubectl, and
that an answer and a failure look to a caller as kubectl's do."""
import re
import tempfile
import unittest
import unittest.mock
from pathlib import Path

import httpx

from swhurl import ROOT, kube
from swhurl.run import CommandError

UNITS = '/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations'


class TranslationTests(unittest.TestCase):
    def read(self, command):
        return kube.api_read(tuple(command.split()))

    def test_a_get_is_the_path_of_its_kind_in_its_namespace(self):
        for command, path in {
            'kubectl -n flux-system get kustomizations.kustomize.toolkit.fluxcd.io -o json': UNITS,
            'kubectl -n flux-system get kustomization app-web-prod -o json --ignore-not-found': f'{UNITS}/app-web-prod',
            'kubectl -n flux-system get kustomizations.kustomize.toolkit.fluxcd.io/platform-otel -o json': f'{UNITS}/platform-otel',
            'kubectl get helmreleases.helm.toolkit.fluxcd.io --all-namespaces -o json': '/apis/helm.toolkit.fluxcd.io/v2/helmreleases',
            'kubectl get namespaces -l platform.swhurl.com/app -o json': '/api/v1/namespaces',
            'kubectl get namespace web-prod --ignore-not-found -o json': '/api/v1/namespaces/web-prod',
            'kubectl -n web-prod get pods -l app.kubernetes.io/instance=web -o json': '/api/v1/namespaces/web-prod/pods',
            'kubectl -n console get cronjob console-dashboards -o json': '/apis/batch/v1/namespaces/console/cronjobs/console-dashboards',
            'kubectl -n web-prod get ingress -o json': '/apis/networking.k8s.io/v1/namespaces/web-prod/ingresses',
            'kubectl get --raw /openid/v1/jwks': '/openid/v1/jwks',
            'kubectl get --raw=/version': '/version',
        }.items():
            with self.subTest(command):
                self.assertEqual([path for path, _ in self.read(command).paths], [path])

    def test_several_kinds_are_one_request_each_and_a_selector_goes_with_every_one(self):
        read = self.read('kubectl get deploy,statefulset,daemonset,cronjob --all-namespaces -l app.kubernetes.io/instance -o json')
        self.assertEqual([path for path, _ in read.paths],
                         ['/apis/apps/v1/deployments', '/apis/apps/v1/statefulsets', '/apis/apps/v1/daemonsets',
                          '/apis/batch/v1/cronjobs'])
        self.assertEqual((read.selector, read.named), ('app.kubernetes.io/instance', False))
        named = self.read('kubectl -n console get pod console-0 --ignore-not-found -o json')
        self.assertEqual((named.named, named.ignore_not_found), (True, True))

    def test_everything_else_is_left_to_kubectl(self):
        for command in ('kubectl -n console get secret console-github -o json',  # Secrets are never read here
                        'kubectl -n notify get secrets -o name',  # not JSON
                        'kubectl -n flux-system get kustomizations.kustomize.toolkit.fluxcd.io',  # a table
                        'kubectl get pods -o json',  # the namespace of kubectl's context
                        'kubectl get deployment notify -o json',
                        'kubectl get pods,secrets --all-namespaces -o json',
                        'kubectl get pod console-0 --all-namespaces -o json',
                        'kubectl get widgets.example.com --all-namespaces -o json',  # a kind the table does not have
                        'kubectl -n web-prod get pods -o json --field-selector status.phase=Running',  # an unknown flag
                        'kubectl -n web-prod get pods -o json -o yaml',
                        'kubectl -n web-prod get pods -o',
                        'kubectl -n flux-system patch kustomization x --type=merge -p {}',
                        'kubectl -n web-prod logs deploy/web',
                        'kubectl get --raw /version -o json',
                        'kubectl -n',
                        'flux get kustomizations'):
            with self.subTest(command):
                self.assertIsNone(self.read(command))

    def test_each_kind_is_read_at_the_version_the_manifests_in_git_use(self):
        """kubectl asks the server which version to read; the table states it, so it has to follow Git."""
        kinds = {resource.kind: resource for resource in kube.RESOURCES if resource.group}
        seen = set()
        for folder in ('clusters', 'platform', 'apps', 'tests/fixtures'):
            for path in (ROOT / folder).rglob('*.yaml'):
                for version, kind in re.findall(r'^apiVersion: (\S+)\nkind: (\S+)$', path.read_text(), re.MULTILINE):
                    resource = kinds.get(kind)
                    if resource and version.startswith(resource.group + '/'):
                        seen.add(kind)
                        self.assertEqual(version, resource.api_version, f'{kind} in {path.relative_to(ROOT)}')
        self.assertLessEqual({'Kustomization', 'HelmRelease', 'GitRepository', 'Alert', 'Provider', 'ImagePolicy'}, seen)


class ApiReadsTests(unittest.TestCase):
    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        self.token = Path(folder.name) / 'token'
        self.token.write_text('fixture-token-1\n')
        self.requests = []
        self.answers = {}

    def runner(self, handler=None):
        def answer(request):
            self.requests.append(request)
            status, body = self.answers.get(request.url.path, (404, {'reason': 'NotFound', 'message': 'pods "x" not found'}))
            return httpx.Response(status, json=body)
        return kube.ApiReads('https://cluster.test', self.token, transport=httpx.MockTransport(handler or answer))

    def test_a_list_is_kubectls_list_and_every_item_says_its_kind(self):
        self.answers['/apis/apps/v1/deployments'] = (200, {'kind': 'DeploymentList', 'items': [{'metadata': {'name': 'web'}}]})
        self.answers['/apis/apps/v1/statefulsets'] = (200, {'kind': 'StatefulSetList', 'items': [{'metadata': {'name': 'db'}}]})
        found = self.runner().json(['kubectl', 'get', 'deployments,statefulsets', '--all-namespaces', '-l', 'app=web', '-o', 'json'])
        self.assertEqual(found['kind'], 'List')
        self.assertEqual([(i['kind'], i['apiVersion'], i['metadata']['name']) for i in found['items']],
                         [('Deployment', 'apps/v1', 'web'), ('StatefulSet', 'apps/v1', 'db')])
        self.assertEqual([r.url.params['labelSelector'] for r in self.requests], ['app=web', 'app=web'])

    def test_the_token_is_read_for_every_request_because_the_kubelet_replaces_it(self):
        self.answers['/api/v1/namespaces'] = (200, {'items': []})
        runner = self.runner()
        runner.json(['kubectl', 'get', 'namespaces', '-o', 'json'])
        self.token.write_text('fixture-token-2\n')
        runner.json(['kubectl', 'get', 'namespaces', '-o', 'json'])
        self.assertEqual([r.headers['Authorization'] for r in self.requests], ['Bearer fixture-token-1', 'Bearer fixture-token-2'])

    def test_an_object_is_itself_and_an_absent_one_is_nothing_only_when_asked(self):
        self.answers[f'{UNITS}/platform-otel'] = (200, {'kind': 'Kustomization', 'spec': {'suspend': True}})
        runner = self.runner()
        self.assertTrue(runner.json(['kubectl', '-n', 'flux-system', 'get', 'kustomization', 'platform-otel', '-o', 'json'])['spec']['suspend'])
        self.assertIsNone(runner.json(['kubectl', '-n', 'web-prod', 'get', 'pod', 'x', '-o', 'json', '--ignore-not-found']))
        with self.assertRaises(CommandError) as raised:
            runner.json(['kubectl', '-n', 'web-prod', 'get', 'pod', 'x', '-o', 'json'])
        self.assertIn('Error from server (NotFound): pods "x" not found', str(raised.exception))

    def test_a_refusal_and_an_unreachable_server_fail_as_kubectl_does(self):
        self.answers['/api/v1/namespaces'] = (403, {'reason': 'Forbidden', 'message': 'namespaces is forbidden'})
        with self.assertRaises(CommandError) as raised:
            self.runner().json(['kubectl', 'get', 'namespaces', '-o', 'json'])
        self.assertEqual((raised.exception.returncode, str(raised.exception)),
                         (1, 'kubectl get namespaces -o json exited 1: Error from server (Forbidden): namespaces is forbidden'))
        self.assertEqual(self.runner().run(['kubectl', 'get', 'namespaces', '-o', 'json'], check=False).returncode, 1)

        def refuse(request):
            raise httpx.ConnectError('connection refused', request=request)
        with self.assertRaises(CommandError) as raised:
            self.runner(refuse).json(['kubectl', 'get', 'namespaces', '-o', 'json'])
        self.assertIn('Unable to connect to the server: connection refused', str(raised.exception))
        self.assertNotIn('fixture-token', str(raised.exception))

    def test_one_refused_kind_fails_the_whole_read(self):
        self.answers['/apis/apps/v1/deployments'] = (200, {'items': [{'metadata': {'name': 'web'}}]})
        self.answers['/apis/apps/v1/statefulsets'] = (500, 'not json')
        with self.assertRaises(CommandError) as raised:
            self.runner().json(['kubectl', 'get', 'deployments,statefulsets', '--all-namespaces', '-o', 'json'])
        self.assertIn('Error from server: HTTP 500', str(raised.exception))

    def test_what_is_not_such_a_read_runs_as_a_command(self):
        self.assertEqual(self.runner().output(['printf', 'ran']), 'ran')
        self.assertEqual(self.requests, [])

    def test_outside_a_pod_the_runner_is_a_plain_one(self):
        with unittest.mock.patch.dict('os.environ', {'KUBERNETES_SERVICE_HOST': ''}):
            self.assertIs(type(kube.runner()), kube.Runner)
        with unittest.mock.patch.dict('os.environ', {'KUBERNETES_SERVICE_HOST': 'fd00::1', 'KUBERNETES_SERVICE_PORT': '6443'}), \
                unittest.mock.patch.object(kube, 'SERVICE_ACCOUNT', self.token.parent):
            (self.token.parent / 'ca.crt').write_text('')
            with unittest.mock.patch('ssl.create_default_context'):
                found = kube.runner()
        self.assertIsInstance(found, kube.ApiReads)
        self.assertEqual(str(found._client.base_url), 'https://[fd00::1]:6443')


if __name__ == '__main__':
    unittest.main()

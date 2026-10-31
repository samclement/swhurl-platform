"""The app page of a v2 instance: its records are its declaration, and the console writes none of them.

The migrations are done (31 October 2026), so this is what every instance page renders: facts from the
cluster, the declaration from ``v2-records/``, and no form, because the console cannot write v2 yet
(deletion plan, phase 1).
"""
import json
import unittest

from test_console import WHO, client, fake, release, unit

from swhurl.console import cluster
from swhurl.run import Result

ORIGIN = {**WHO, 'Origin': 'http://testserver'}

HEAD = 'a' * 40

APP = json.dumps({'schemaVersion': 1, 'application': 'hello', 'team': 'demo', 'appId': 7})
INSTANCE = json.dumps({'schemaVersion': 1, 'application': 'hello', 'environment': 'staging',
                       'release': 'rel-sha256:ab', 'configuration': {'replicas': 1, 'memoryMiB': 128},
                       'grants': {'exposure': 'authenticated-web'}, 'bindings': {}, 'storage': {'claimName': 'hello'},
                       'calls': ['other'], 'allows': ['team:demo'],
                       'events': {'key': 'UKEY', 'subscriptions': ['other.created']}})
RELEASE = json.dumps({'schemaVersion': 1, 'runtime': {'kind': 'web', 'port': 8080, 'healthPath': '/healthz'},
                      'database': 'sqlite', 'publishes': ['visited'], 'offers': 'abc1234',
                      'source': {'commit': 'abc1234'}, 'artifact': {'image': 'ghcr.io/x/hello', 'digest': 'sha256:1'}})
OTHER = json.dumps({**json.loads(INSTANCE), 'application': 'other', 'calls': [], 'allows': ['someone-else'],
                    'events': {'key': 'OKEY', 'subscriptions': []}})
OTHER_RELEASE = json.dumps({**json.loads(RELEASE), 'publishes': ['created']})
OTHER_APP = json.dumps({**json.loads(APP), 'application': 'other', 'team': 'payments'})

RECORDS = {'v2-records/hello/app.json': APP, 'v2-records/hello/staging/instance.json': INSTANCE,
           'v2-records/hello/staging/release.json': RELEASE, 'v2-records/other/app.json': OTHER_APP,
           'v2-records/other/staging/instance.json': OTHER, 'v2-records/other/staging/release.json': OTHER_RELEASE}


def records(extra=None):
    """The repository's files: v2's records, and whatever a test adds."""
    return {**RECORDS, **(extra or {})}.get


def helmrelease():
    """hello's release as v2 renders it: the image pinned by digest, the exposure an annotation, no Ingress."""
    found = release('hello-staging', 'hello')
    found['metadata'] = {'namespace': 'hello-staging', 'name': 'hello',
                         'annotations': {'platform.swhurl.com/exposure': 'authenticated-web',
                                         'platform.swhurl.com/offers': 'abc1234'}}
    values = found['spec']['values']
    values['controllers']['main']['containers']['main'] = {
        'image': {'repository': 'ghcr.io/x/hello', 'digest': 'sha256:1'}, 'env': {'SWHURL_APP': 'hello'}}
    values['service'] = {'main': {'ports': {'http': {'port': 8080}}}}
    return found


def running():
    """A cluster running hello/staging, with its Ingress apart from the HelmRelease as v2 renders it."""
    kind_of = {'kustomization': 'flux-system', 'gitrepository': 'flux-system'}
    answers = {f'kubectl -n {kind_of.get(kind, "hello-staging")} get {kind}': json.dumps(value) for kind, value in
               (('kustomization', unit('app-hello-staging')),
                ('gitrepository', {'status': {'artifact': {'revision': f'main@sha1:{HEAD}'}}}),
                ('helmrelease', helmrelease()),
                ('pods', {'items': []}), ('deploy,statefulset,daemonset,cronjob', {'items': []}),
                ('ingress', {'items': [{'spec': {'rules': [{'host': 'staging-hello.homelab.swhurl.com'}]}}]}),
                ('certificate', {'items': []}))}
    answers['kubectl get namespaces -l platform.swhurl.com/app'] = json.dumps({'items': [
        {'metadata': {'name': 'hello-staging', 'labels': {'platform.swhurl.com/team': 'demo'}}}]})
    return fake(units=[unit('app-hello-staging')], releases=[helmrelease()], **answers)


class PageTests(unittest.TestCase):
    def page(self, **kwargs):
        return client(running(), documents=records(), **kwargs).get('/apps/hello/staging', headers=WHO).text

    def test_the_page_reads_v2_records_and_offers_no_form_that_writes(self):
        text = self.page(github=None)
        self.assertIn('href="https://staging-hello.homelab.swhurl.com"', text, "the Ingress publishes the address")
        self.assertIn('<div><span>Team</span><a href="/apps?team=demo"', text)
        self.assertNotIn('<form class="wizard"', text, 'the console writes neither v2 nor v1: no change form is offered')
        self.assertIn('Its records are v2\'s (<code>v2-records/hello/staging/</code>)', text)
        self.assertIn('<code>swhurl-v2 edit hello/staging --set &lt;field&gt;=&lt;value&gt;</code>', text)
        self.assertIn('<code>swhurl-v2 promote hello</code>', text)
        self.assertIn('<code>swhurl-v2 remove hello/staging</code>', text)
        self.assertIn('Promotion is v2\'s: <code>swhurl-v2 promote hello</code>', text)
        self.assertIn('<button class="danger" disabled>', text, 'the uninstall form is there, its button is not usable')

    def test_what_it_declares_is_a_row_with_why_it_is_not_connected_yet(self):
        text = self.page()
        self.assertIn('waiting for other to allow it', text, "other's record does not allow hello (or its team)")
        self.assertIn('receives, ', text)
        self.assertIn('<code>created</code>', text, 'other publishes it, so the subscription connects')
        self.assertNotIn('waiting for other to publish created', text)
        self.assertIn('<span class="pill">team:demo</span>', text, 'the grant as it is recorded, not expanded')
        self.assertNotIn("image's minimum", text, 'v2 states no minimum for a release')

    def test_v2_holds_no_secret_value_so_the_page_says_so(self):
        text = self.page()
        self.assertNotIn('secret.sops.yaml', text)
        self.assertNotIn('make app-secret', text)
        self.assertIn('none: v2 does not model secrets', text)

    def test_records_that_are_not_there_say_so_instead_of_claiming_none(self):
        text = client(running(), documents={}.get).get('/apps/hello/staging', headers=WHO).text
        self.assertIn('its records are not in Git, so what it declares is unknown', text)
        self.assertNotIn('Allowed callers', text, 'nothing declares who may call it')


class PermissionTests(unittest.TestCase):
    def test_the_page_renders_when_the_console_may_not_list_configmaps(self):
        """The console's account reads no ConfigMaps (platform/console/rbac.yaml), so the rendered allow-lists are
        unknown: every other fact is still shown, and the grants the records declare are still a row each."""
        runner = running()
        runner._rules.insert(0, (tuple(cluster.CALLERS[:4]), Result((), 1, '', 'Error from server (Forbidden)')))
        text = client(runner, documents=records()).get('/apps/hello/staging', headers=WHO).text
        self.assertIn('href="https://staging-hello.homelab.swhurl.com"', text)
        self.assertIn('waiting for other to allow it', text)
        self.assertIn('connected in Git, not yet observed live', text)


class WriteRefusalTests(unittest.TestCase):
    def post(self, path, data=None):
        return client(running(), documents=records(), github=GH()).post(path, data=data or {}, headers=ORIGIN,
                                                                           follow_redirects=False)

    def test_a_change_names_the_command_that_writes_it(self):
        # The page escapes the text: `--set <field>=<value>` reaches the operator as `--set &lt;field&gt;=&lt;value&gt;`.
        for path, command in (('/apps/hello/staging/scale', 'swhurl-v2 edit hello/staging --set &lt;field&gt;=&lt;value&gt;'),
                              ('/apps/hello/staging/remove', 'swhurl-v2 remove hello/staging'),
                              ('/apps/hello/staging/promote', 'swhurl-v2 promote hello')):
            with self.subTest(path=path):
                response = self.post(path, {'replicas': '2'})
                self.assertEqual(response.status_code, 409)
                self.assertIn(f'v2 writes hello/staging, and the console cannot write v2 yet: run `{command}`', response.text)

    def test_reviewing_a_promotion_names_the_command_too(self):
        response = client(running(), documents=records(), github=GH()).get('/apps/hello/staging/promotion', headers=WHO)
        self.assertEqual(response.status_code, 409)
        self.assertIn('run `swhurl-v2 promote hello`', response.text)


def GH():
    """A token-shaped value: enough for the console to consider a pull request possible."""
    from swhurl.console import github as gh
    return gh.GitHub('owner/repo', 'token')

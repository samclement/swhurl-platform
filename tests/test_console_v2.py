"""A v2 instance through the console: its records are its declaration, and every write is v2's.

The console runs ``swhurl-v2 edit|remove|promote`` and then ``render`` in the pull request's clone, shows
what the binary refused verbatim, and holds every v2 pull request for a person (deletion plan, phase 2).
"""
import io
import json
import tarfile
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import httpx
from test_console import WHO, client, fake, release, unit

from swhurl.console import actions, cluster, merge, proposal, v2
from swhurl.console import github as gh
from swhurl.run import FakeRunner, Result

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
PROD_INSTANCE = json.dumps({**json.loads(INSTANCE), 'environment': 'prod'})
PROD_RELEASE = json.dumps({**json.loads(RELEASE), 'artifact': {'image': 'ghcr.io/x/hello', 'digest': 'sha256:0'}})

RECORDS = {'v2-records/hello/app.json': APP, 'v2-records/hello/staging/instance.json': INSTANCE,
           'v2-records/hello/staging/release.json': RELEASE, 'v2-records/hello/prod/instance.json': PROD_INSTANCE,
           'v2-records/hello/prod/release.json': PROD_RELEASE, 'v2-records/other/app.json': OTHER_APP,
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
    runner = fake(units=[unit('app-hello-staging')], releases=[helmrelease()], **answers)
    runner.on(v2.BINARY, stdout='usage\n')
    return runner


class PageTests(unittest.TestCase):
    def page(self, **kwargs):
        return client(running(), documents=records(), **kwargs).get('/apps/hello/staging', headers=WHO).text

    def test_the_page_reads_v2_records_and_offers_no_form_that_writes(self):
        text = self.page(github=None)
        self.assertIn('href="https://staging-hello.homelab.swhurl.com"', text, "the Ingress publishes the address")
        self.assertIn('<div><span>Team</span><a href="/apps?team=demo"', text)
        self.assertIn('action="/apps/hello/staging/scale"', text, 'the console writes v2')
        self.assertIn('name="replicas"', text)
        self.assertNotIn('name="host"', text, 'v2 derives the address')
        self.assertIn('v2 derives the address from the app name and the exposure', text)
        self.assertIn('action="/apps/hello/staging/remove"', text)
        self.assertIn('<button class="danger" disabled>', text, 'no GitHub token: the button is not usable')
        self.assertIn('href="/apps/hello/staging/promotion"', text)

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


class CallerTests(unittest.TestCase):
    def test_who_calls_an_instance_comes_from_the_callers_own_variables(self):
        """v2 renders each allow-list as a resolved pair, so the caller's variables carry the callee's address
        and the callee's page needs no ConfigMap to say who calls it."""
        caller = release('caller-staging', 'caller')
        caller['spec']['values']['controllers']['main']['containers']['main']['env'] = {
            'SWHURL_SERVICE_HELLO_URL': 'http://hello.hello-staging.svc.cluster.local:8080'}
        runner = fake(units=[unit('app-hello-staging'), unit('app-caller-staging')],
                      releases=[helmrelease(), caller])
        rows = {row.instance.app: row for row in cluster.apps(runner, states=False)}
        self.assertEqual(rows['hello'].connections.callers, ('caller',))
        self.assertEqual(rows['caller'].connections.calls, ('hello',))
        self.assertNotIn('configmaps', ' '.join(' '.join(call) for call in runner.calls),
                         'the console reads no ConfigMap for this')


class WriteRefusalTests(unittest.TestCase):
    """A console image without the binary is the phase-1 read-only fallback: it refuses with the command."""

    def test_a_v2_change_without_the_binary_is_the_phase_1_refusal(self):
        with mock.patch.object(v2, 'available', return_value=False):
            for path, command in (('/apps/hello/staging/scale', 'swhurl-v2 edit hello/staging --set &lt;field&gt;=&lt;value&gt;'),
                                  ('/apps/hello/staging/remove', 'swhurl-v2 remove hello/staging'),
                                  ('/apps/hello/staging/promote', 'swhurl-v2 promote hello')):
                with self.subTest(path=path):
                    response = client(running(), documents=records(), github=GHTransport().github).post(
                        path, data={'replicas': '2', 'image': 'ghcr.io/x/hello@sha256:1'}, headers=ORIGIN,
                        follow_redirects=False)
                    self.assertEqual(response.status_code, 409)
                    self.assertIn(f'run `{command}` in your own terminal', response.text)

    def test_reviewing_a_promotion_without_the_binary_is_the_phase_1_refusal(self):
        with mock.patch.object(v2, 'available', return_value=False):
            response = client(running(), documents=records(), github=GHTransport().github).get(
                '/apps/hello/staging/promotion', headers=WHO)
        self.assertEqual(response.status_code, 409)
        self.assertIn('run `swhurl-v2 promote hello` in your own terminal', response.text)

    def test_a_v2_promotion_review_is_held_and_names_v2(self):
        response = client(running(), documents=records(), github=GHTransport().github).get(
            '/apps/hello/staging/promotion', headers=WHO)
        self.assertEqual(response.status_code, 200, response.text)
        self.assertIn('This PR does not merge itself: it needs manual review.', response.text)
        self.assertIn('ghcr.io/x/hello@sha256:1', response.text)


# --- The console writes v2: the adapter, the action table and one prepared pull request -----------------

def v2_client(runner, github):
    return client(runner, documents=records(), github=github,
                  jobs=actions.Jobs(runner, audit=lambda line: None, inline=True))


class CommandTests(unittest.TestCase):
    def test_edit_is_one_argv_and_names_every_input_the_binary_needs(self):
        self.assertEqual(v2.edit_argv('hello', 'staging', 'replicas=2'), [
            'edit', '--records', 'v2-records', '--app', 'hello', '--env=staging', '--set', 'replicas=2',
            '--profile', 'v2/testdata/profile.json', '--out', '.', '--calls', 'v2-records/calls.json',
            '--egress', 'platform/egress/allocation.json'])
        self.assertEqual(v2.remove_argv('hello', 'staging'), [
            'remove', '--records', 'v2-records', '--app', 'hello', '--env=staging',
            '--profile', 'v2/testdata/profile.json', '--calls', 'v2-records/calls.json', '--out', '.'])
        self.assertEqual(v2.promote_argv('hello'), [
            'promote', '--records', 'v2-records', '--app', 'hello', '--env=prod',
            '--profile', 'v2/testdata/profile.json', '--out', '.', '--calls', 'v2-records/calls.json',
            '--egress', 'platform/egress/allocation.json'])

    def test_a_change_is_the_edit_then_the_render_that_writes_what_it_derives(self):
        commands = merge.ACTIONS['v2-scale'].commands('hello', 'staging', {'replicas': '2'})
        self.assertEqual([command.tool for command in commands], ['v2', 'v2'])
        self.assertEqual([command.name for command in commands], ['edit', 'render'])
        self.assertEqual(commands[0].argv[:8], ('edit', '--records', 'v2-records', '--app', 'hello',
                                                '--env=staging', '--set', 'replicas=2'))
        self.assertEqual(commands[1].argv, ('render', '--records', 'v2-records', '--profile',
                                            'v2/testdata/profile.json', '--out', '.', '--revision', v2.BASE))
        self.assertEqual(v2.resolve(commands[1].argv, revision=HEAD)[-1], HEAD,
                         'the render records the commit the pull request is built on')

    def test_a_whole_list_is_written_once_and_the_team_names_no_environment(self):
        self.assertEqual(merge.v2_connection_settings({'calls': 'other, billing', 'subscriptions': ''}),
                         ('calls=other,billing', 'subscriptions='))
        self.assertEqual(merge.v2_connection_settings({'calls': '', 'subscriptions': 'other.created'}),
                         ('calls=', 'subscriptions=other.created'))
        self.assertIn('--set', merge.v2_commands('hello', '', ('team=payments',))[0].argv)
        self.assertEqual(merge.v2_commands('hello', '', ('team=payments',))[0].argv[5], '--set')
        self.assertNotIn('--env=', merge.v2_commands('hello', '', ('team=payments',))[0].argv)


class RouteTests(unittest.TestCase):
    def post(self, path, data=None):
        captured = []

        def open_action(runner, github, job, action, name, env, commands, **kwargs):
            captured.append((action, commands, kwargs))
            return 'https://github.com/x/pull/7'

        with mock.patch.object(proposal, 'open_action', side_effect=open_action):
            response = v2_client(running(), GHTransport().github).post(
                path, data=data or {}, headers=ORIGIN, follow_redirects=False)
        return response, captured

    def test_each_v2_change_routes_to_a_held_edit_and_render_or_remove(self):
        cases = {
            '/apps/hello/staging/scale': ({'replicas': '2'}, 'v2-scale', ['edit', 'render']),
            '/apps/hello/staging/expose': ({'exposure': 'private'}, 'v2-expose', ['edit', 'render']),
            '/apps/hello/staging/disconnect': ({'role': 'calls', 'app': 'other'}, 'v2-disconnect', ['edit', 'render']),
            '/apps/hello/staging/allow': ({'add': 'someone-else'}, 'v2-allow', ['edit', 'render']),
            '/apps/hello/staging/remove': ({}, 'v2-remove', ['remove', 'render']),
            '/apps/hello/staging/promote': ({'image': 'ghcr.io/x/hello@sha256:1'}, 'v2-promote', ['promote', 'render']),
        }
        for path, (data, action, names) in cases.items():
            with self.subTest(path=path):
                response, captured = self.post(path, data)
                self.assertEqual(response.status_code, 303, response.text)
                found_action, commands, kwargs = captured[0]
                self.assertEqual(found_action, action)
                self.assertEqual([command.name for command in commands], names)
                self.assertTrue(all(command.tool == 'v2' for command in commands))
                self.assertFalse(kwargs['auto_merge'], 'every v2 pull request waits for a person')
                self.assertEqual(response.headers['location'], '/jobs/1')

    def test_scale_writes_memory_in_v2s_unit_and_refuses_v1_only_fields(self):
        response, captured = self.post('/apps/hello/staging/scale', {'memory': '256Mi'})
        self.assertEqual(response.status_code, 303)
        self.assertIn('--set', captured[0][1][0].argv)
        self.assertIn('memoryMiB=256', captured[0][1][0].argv)
        for form, message in (({'cpu': '100m'}, 'v2 does not model CPU'), ({'memory_limit': '256Mi'}, 'v2 does not model CPU')):
            with self.subTest(form=form):
                response, captured = self.post('/apps/hello/staging/scale', form)
                self.assertEqual(response.status_code, 400)
                self.assertIn(message, response.text)
                self.assertEqual(captured, [])

    def test_expose_refuses_a_host_and_an_unknown_v2_fact(self):
        response, captured = self.post('/apps/hello/staging/expose', {'exposure': 'public', 'host': 'hello.example.com'})
        self.assertEqual(response.status_code, 400)
        self.assertIn('v2 derives the address', response.text)
        self.assertEqual(captured, [])
        response, _ = self.post('/apps/hello/staging/egress', {'hosts': 'api.example.com'})
        self.assertEqual(response.status_code, 400)
        self.assertIn('v2 does not model egress', response.text)


class PullRequestTests(unittest.TestCase):
    """One prepared v2 pull request: its diff is v2's records and what render derives, never a v1 record."""

    def prepare(self, fail=False):
        work = Path(tempfile.mkdtemp())
        for path, text in RECORDS.items():
            (work / path).parent.mkdir(parents=True, exist_ok=True)
            (work / path).write_text(text)
        runner = FakeRunner()

        def run_v2(argv, _input):
            tree = runner.tree
            if fail:
                return Result(argv, 2, '', 'invalid: replicas: expected_a_non_negative_integer\n')
            command = argv[1]
            if command == 'edit':
                path = tree / 'v2-records/hello/staging/instance.json'
                doc = json.loads(path.read_text())
                for value in argv:
                    name, _, number = value.partition('=')
                    if name == 'replicas':
                        doc['configuration']['replicas'] = int(number)
                path.write_text(json.dumps(doc))
            elif command == 'render':
                (tree / 'v2-records/calls.json').write_text('{"version": 1}\n')
                (tree / 'v2-apps/instances/hello/staging/helmrelease.json').parent.mkdir(parents=True, exist_ok=True)
                (tree / 'v2-apps/instances/hello/staging/helmrelease.json').write_text('{"kind": "HelmRelease"}\n')
            return Result(argv, 0, f'{command}: written\n')

        runner.on(v2.BINARY, handler=run_v2)
        return runner, work

    def open(self, runner, api, action='v2-scale', form=None):
        commands = merge.ACTIONS[action].commands('hello', 'staging', form or {'replicas': '2'})
        job = actions.Job(1, action, merge.target(action, 'hello', 'staging'), 'sam@swhurl.com', None)
        with mock.patch.object(proposal, 'download', side_effect=lambda _api, _revision, _workdir: runner.tree):
            return proposal.open_action(runner, api.github, job, action, 'hello', 'staging', commands,
                                        auto_merge=True), job

    def test_a_change_is_held_and_carries_only_v2_records_and_rendered_files(self):
        runner, work = self.prepare()
        runner.tree = work
        api = GHTransport()
        url, job = self.open(runner, api)
        self.assertEqual(url, 'https://github.com/x/pull/7')
        paths = [entry['path'] for entry in api.trees[0]['tree']]
        self.assertEqual(paths, ['v2-apps/instances/hello/staging/helmrelease.json', 'v2-records/calls.json',
                                 'v2-records/hello/staging/instance.json'])
        self.assertFalse([path for path in paths if path.startswith('apps/')], 'a v2 pull request carries no v1 record')
        self.assertEqual(api.labels, [], 'every v2 pull request waits for a person')
        self.assertTrue(any('needs manual review' in line for line in job.lines), job.lines)
        self.assertIn('swhurl-v2', api.commits[0]['message'])

    def test_v2s_refusal_is_the_job_s_refusal_and_nothing_is_published(self):
        runner, work = self.prepare(fail=True)
        runner.tree = work
        api = GHTransport()
        with self.assertRaises(actions.ActionError) as caught:
            self.open(runner, api)
        self.assertEqual(str(caught.exception), 'invalid: replicas: expected_a_non_negative_integer')
        self.assertEqual(api.trees, [])
        self.assertEqual(api.pulls, [])


class GHTransport:
    """GitHub's REST endpoints one prepared pull request uses, with a tarball of the v2 records."""

    def __init__(self, files=None):
        self.files = files or RECORDS
        self.requests, self.trees, self.blobs, self.pulls, self.labels, self.commits = [], [], [], [], [], []
        self.github = gh.GitHub('owner/repo', 'token', transport=httpx.MockTransport(self.handle))

    def tarball(self):
        out = io.BytesIO()
        with tarfile.open(fileobj=out, mode='w:gz') as tar:
            for path, data in self.files.items():
                raw = data.encode() if isinstance(data, str) else data
                info = tarfile.TarInfo(f'owner-repo-{HEAD[:7]}/{path}')
                info.size = len(raw)
                tar.addfile(info, io.BytesIO(raw))
        return out.getvalue()

    def handle(self, request):
        self.requests.append(request)
        path = request.url.path.removeprefix('/repos/owner/repo')
        key = f'{request.method} {path}'
        if key == 'GET /git/ref/heads/main':
            return httpx.Response(200, json={'object': {'sha': HEAD}})
        if key == f'GET /tarball/{HEAD}':
            return httpx.Response(200, content=self.tarball())
        if key == f'GET /git/commits/{HEAD}':
            return httpx.Response(200, json={'tree': {'sha': 'tree0'}, 'parents': []})
        if key == 'POST /git/blobs':
            body = json.loads(request.content)
            self.blobs.append(body)
            return httpx.Response(201, json={'sha': f'blob{len(self.blobs) - 1}'})
        if key == 'POST /git/trees':
            self.trees.append(json.loads(request.content))
            return httpx.Response(201, json={'sha': 'tree1'})
        if key == 'POST /git/commits':
            self.commits.append(json.loads(request.content))
            return httpx.Response(201, json={'sha': 'd' * 40})
        if key == 'POST /git/refs':
            return httpx.Response(201, json={'ref': 'x'})
        if key == 'POST /pulls':
            self.pulls.append(json.loads(request.content))
            return httpx.Response(201, json={'html_url': 'https://github.com/x/pull/7', 'number': 7})
        if key == 'POST /issues/7/labels':
            self.labels.append(json.loads(request.content))
            return httpx.Response(201, json=[])
        if key == 'GET /pulls':
            return httpx.Response(200, json=[])
        return httpx.Response(404, json={'message': 'Not Found'})

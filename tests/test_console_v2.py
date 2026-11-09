"""A v2 instance through the console: its records are its declaration, and every write is v2's.

The console runs ``swhurl-v2 edit|remove|promote`` and then ``render`` in the pull request's clone, shows
what the binary refused verbatim, and holds every v2 pull request for a person (deletion plan, phase 2).
"""
import io
import json
import tarfile
import unittest
from unittest import mock

import httpx
from console_observation import inventory, report
from test_console import WHO, client, fake, release, unit

from swhurl.console import actions, changes, proposal, v2
from swhurl.console import github as gh
from swhurl.run import CommandError, FakeRunner, Result

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

TEAMS = '{"schemaVersion": 1, "teams": {"demo": {"description": "The demo apps"}, "payments": {}}}'

RECORDS = {'v2-records/hello/app.json': APP, 'v2-records/hello/staging/instance.json': INSTANCE,
           'v2-records/hello/staging/release.json': RELEASE, 'v2-records/hello/prod/instance.json': PROD_INSTANCE,
           'v2-records/hello/prod/release.json': PROD_RELEASE, 'v2-records/other/app.json': OTHER_APP,
           'v2-records/other/staging/instance.json': OTHER, 'v2-records/other/staging/release.json': OTHER_RELEASE,
           'v2-records/teams.json': TEAMS}


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
    runner.status_reports['hello', 'staging']['routes'] = [{'name': 'hello', 'outcome': 'present', 'hosts': ['staging-hello.homelab.swhurl.com']}]
    # What Go reports for the three instances these records state: hello in both environments, and the app it calls.
    runner.inventories['hello', 'staging'].update({
        'hosts': ['staging-hello.homelab.swhurl.com'], 'exposure': 'authenticated-web', 'offers': 'abc1234',
        'api': 'https://staging-hello.homelab.swhurl.com', 'calls': ['other'], 'allows': ['team:demo'],
        'publishes': ['visited'], 'subscriptions': ['other.created'],
        'connections': [{'role': 'publishes', 'event': 'visited'},
                        {'role': 'calls', 'app': 'other', 'outcome': 'waiting', 'reason': 'no_consent'},
                        {'role': 'subscribes', 'app': 'other', 'event': 'created', 'outcome': 'connected'}]})
    for app, env, team, columns in (('hello', 'prod', 'demo', {}),
                                    ('other', 'staging', 'payments', {'publishes': ['created']})):
        runner.status_reports[app, env] = report(app, env)
        runner.inventories[app, env] = inventory(app, env, team=team, **columns)
    runner.inventories['hello', 'prod']['artifact'] = {'image': 'ghcr.io/x/hello', 'digest': 'sha256:' + '0' * 64}
    runner.inventories['hello', 'staging']['artifact'] = {'image': 'ghcr.io/x/hello', 'digest': 'sha256:1'}
    runner.on(v2.BINARY, '--help', stdout='usage\n')
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

    def test_the_team_form_offers_the_registered_teams_and_marks_the_current_one(self):
        text = self.page()
        self.assertIn('action="/apps/hello/staging/team"', text)
        self.assertIn('value="payments" required >', text)
        self.assertIn('value="demo" required checked disabled>', text)
        text = client(running(), documents=records({'v2-records/teams.json': None})).get('/apps/hello/staging', headers=WHO).text
        self.assertIn('v2-records/teams.json is not in Git', text)

    def test_what_it_declares_is_a_row_with_why_it_is_not_connected_yet(self):
        text = self.page()
        self.assertIn('waiting for other to allow it', text, "other's record does not allow hello (or its team)")
        self.assertIn('receives', text)
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
        """Go read the records at the pinned commit: the page states them with no Git reader of its own."""
        text = client(running(), documents={}.get).get('/apps/hello/staging', headers=WHO).text
        self.assertIn('waiting for other to allow it', text, "the block carries other's refusal")
        self.assertIn('<span class="pill">team:demo</span>', text)
        self.assertNotIn('its records are not in Git', text)


class CallerTests(unittest.TestCase):
    def test_who_calls_an_instance_comes_from_the_callers_own_variables(self):
        """v2 renders each allow-list as a resolved pair, so the caller's variables carry the callee's address
        and the callee's page needs no ConfigMap to say who calls it."""
        caller = release('caller-staging', 'caller')
        caller['spec']['values']['controllers']['main']['containers']['main']['env'] = {
            'SWHURL_SERVICE_HELLO_URL': 'http://hello.hello-staging.svc.cluster.local:8080'}
        runner = fake(units=[unit('app-hello-staging'), unit('app-caller-staging')],
                      releases=[helmrelease(), caller])
        # Go resolves the pair from both records: the caller's call, and the callee's inbound row.
        from console_observation import rows as scenario_rows
        rows = {row.instance.app: row for row in scenario_rows(runner)}
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

    def test_a_v2_promotion_review_says_a_person_merges_it(self):
        response = client(running(), documents=records(), github=GHTransport().github).get(
            '/apps/hello/staging/promotion', headers=WHO)
        self.assertEqual(response.status_code, 200, response.text)
        self.assertIn('You merge it on GitHub: no console pull request merges itself.', response.text)
        self.assertIn('ghcr.io/x/hello@sha256:1', response.text)


# --- The console writes v2: the kinds' flags, the routes and the relay ---------------------------------

def v2_client(runner, github):
    return client(runner, documents=records(), github=github,
                  jobs=actions.Jobs(runner, audit=lambda line: None, inline=True))


class ChangeArgumentsTests(unittest.TestCase):
    """A form's values as v2's flags: v2 owns the branch, the title and the files its pull request carries; the
    console owns the words its form offers and the checks that refuse a change before a job."""

    def test_a_change_states_what_to_add_or_remove_never_a_whole_list(self):
        cases = {
            'v2-scale': ({'replicas': '2', 'memory': '256Mi'}, ('--set', 'replicas=2', '--set', 'memoryMiB=256')),
            'v2-expose': ({'exposure': 'private'}, ('--set', 'exposure=private')),
            'v2-team': ({'team': 'payments'}, ('--set', 'team=payments')),
            'v2-connect': ({'calls': 'other,billing', 'subscribe': 'other.created'},
                           ('--call', 'other', '--call', 'billing', '--subscribe', 'other.created')),
            'v2-disconnect': ({'role': 'calls', 'app': 'other'}, ('--drop-call', 'other')),
            'v2-allow': ({'add': 'someone-else', 'remove': 'team:demo'},
                         ('--allow', 'someone-else', '--disallow', 'team:demo')),
            'v2-remove': ({}, ()),
            'v2-promote': ({'image': 'ghcr.io/x/hello@sha256:1'}, ()),
        }
        for action, (form, flags) in cases.items():
            with self.subTest(action=action):
                self.assertEqual(changes.change_arguments(action, 'hello', form), (flags, None))

    def test_a_new_app_states_its_image_and_settings_and_a_channel_address_is_standard_input(self):
        self.assertEqual(
            changes.change_arguments('new', 'hello', {'image': 'ghcr.io/x/hello:1-ab',
                                                      'flags': ('team=demo', 'exposure=private')}),
            (('--set', 'team=demo', '--set', 'exposure=private', '--image', 'ghcr.io/x/hello:1-ab'), None))
        self.assertEqual(
            changes.change_arguments('team-channel-set', 'payments',
                                     {'transport': 'ntfy', 'address': 'https://ntfy.sh/x'}),
            (('--transport', 'ntfy'), 'https://ntfy.sh/x\n'))
        self.assertEqual(changes.change_arguments('team-edit', 'payments',
                                                  {'description': 'Orders', 'rename': 'orders'}),
                         (('--description', 'Orders', '--rename', 'orders'), None))
        self.assertEqual(changes.change_arguments('team-remove', 'payments', {}), ((), None))

    def test_the_forms_checks_are_the_consoles(self):
        for action, form, message in (
                ('v2-scale', {'memory': 'lots'}, 'not a whole number of MiB'),
                ('v2-connect', {}, 'choose an app to call'),
                ('v2-disconnect', {'role': 'publishes'}, 'publications come from the release'),
                ('v2-promote', {}, 'refresh the staging app page'),
                ('team-add', {'description': 'x'}, 'lowercase letters')):
            with self.subTest(action=action):
                with self.assertRaisesRegex(actions.ActionError, message):
                    changes.change_arguments(action, 'Not A Team' if action == 'team-add' else 'hello', form)

    def test_a_branch_is_read_back_as_its_kind_and_its_target(self):
        self.assertEqual(changes.parse(f'console/v2-scale-hello-staging-{HEAD[:7]}'), ('v2-scale', 'hello', 'staging'))
        self.assertEqual(changes.parse(f'console/team-add-payments-{HEAD[:7]}'), ('team-add', 'payments', ''))
        self.assertEqual(changes.target('v2-promote', 'hello', 'staging'), 'hello/prod')
        self.assertEqual(changes.target('team-add', 'payments', ''), 'team:payments')
        self.assertIsNone(changes.parse('console/nothing-hello-staging-abc1234'))


class RouteTests(unittest.TestCase):
    def post(self, path, data=None):
        captured = []

        def open_change(runner, github, job, action, name, env, flags, input=None, **kwargs):
            captured.append((action, flags, input, kwargs))
            return 'https://github.com/x/pull/7'

        with mock.patch.object(proposal, 'open_change', side_effect=open_change):
            response = v2_client(running(), GHTransport().github).post(
                path, data=data or {}, headers=ORIGIN, follow_redirects=False)
        return response, captured

    def test_each_v2_change_routes_to_its_kind_and_its_flags(self):
        cases = {
            '/apps/hello/staging/scale': ({'replicas': '2'}, 'v2-scale', ('--set', 'replicas=2')),
            '/apps/hello/staging/expose': ({'exposure': 'private'}, 'v2-expose', ('--set', 'exposure=private')),
            '/apps/hello/staging/disconnect': ({'role': 'calls', 'app': 'other'}, 'v2-disconnect',
                                               ('--drop-call', 'other')),
            '/apps/hello/staging/allow': ({'add': 'someone-else'}, 'v2-allow', ('--allow', 'someone-else')),
            '/apps/hello/staging/team': ({'team': 'payments'}, 'v2-team', ('--set', 'team=payments')),
            '/apps/hello/staging/remove': ({}, 'v2-remove', ()),
            '/apps/hello/staging/promote': ({'image': 'ghcr.io/x/hello@sha256:1'}, 'v2-promote', ()),
        }
        for path, (data, action, flags) in cases.items():
            with self.subTest(path=path):
                response, captured = self.post(path, data)
                self.assertEqual(response.status_code, 303, response.text)
                found_action, found_flags, found_input, _ = captured[0]
                self.assertEqual((found_action, found_flags, found_input), (action, flags, None))
                self.assertEqual(response.headers['location'], '/jobs/1')

    def test_scale_writes_memory_in_v2s_unit_and_refuses_v1_only_fields(self):
        response, captured = self.post('/apps/hello/staging/scale', {'memory': '256Mi'})
        self.assertEqual(response.status_code, 303)
        self.assertEqual(captured[0][1], ('--set', 'memoryMiB=256'))
        for form, message in (({'cpu': '100m'}, 'v2 does not model CPU'),
                              ({'memory_limit': '256Mi'}, 'v2 does not model CPU')):
            with self.subTest(form=form):
                response, captured = self.post('/apps/hello/staging/scale', form)
                self.assertEqual(response.status_code, 400)
                self.assertIn(message, response.text)
                self.assertEqual(captured, [])

    def test_a_team_change_names_only_a_registered_new_team(self):
        response, captured = self.post('/apps/hello/prod/team', {'team': 'payments'})
        self.assertEqual(response.status_code, 303, response.text)
        self.assertEqual(captured[0][1], ('--set', 'team=payments'))
        for form, message in (({'team': 'nobody'}, 'choose a registered team: one of demo, payments'),
                              ({'team': 'demo'}, 'hello already belongs to demo'),
                              ({}, 'choose a registered team')):
            with self.subTest(form=form):
                response, captured = self.post('/apps/hello/staging/team', form)
                self.assertEqual(response.status_code, 400)
                self.assertIn(message, response.text)
                self.assertEqual(captured, [])

    def test_expose_refuses_a_host_and_an_unknown_v2_fact(self):
        response, captured = self.post('/apps/hello/staging/expose',
                                       {'exposure': 'public', 'host': 'hello.example.com'})
        self.assertEqual(response.status_code, 400)
        self.assertIn('v2 derives the address', response.text)
        self.assertEqual(captured, [])
        response, _ = self.post('/apps/hello/staging/egress', {'hosts': 'api.example.com'})
        self.assertEqual(response.status_code, 400)
        self.assertIn('the console offers no egress form', response.text)
        response, _ = self.post('/apps/hello/staging/schedule', {'cron': '0 3 * * *'})
        self.assertEqual(response.status_code, 400)
        self.assertIn('v2 does not model a schedule', response.text)


class RelayTests(unittest.TestCase):
    """The console's publication is one ``swhurl-v2 change`` run: the job gets its lines and its link is the last
    one. Nothing is downloaded, snapshotted or published here any more."""

    def runner(self, output, returncode=0, handler=None):
        fake = FakeRunner().on('swhurl-v2', 'change', stdout=output, returncode=returncode, handler=handler)
        fake.add_secret('token')
        return fake

    def job(self):
        return actions.Job(1, 'v2-scale', 'hello/staging', 'sam@swhurl.com', None)

    def test_the_change_streams_into_the_job_and_its_link_is_the_last_line(self):
        lines = ['Downloading owner/repo main (aaaaaaa)', 'M v2-records/hello/staging/instance.json',
                 'Merge plan: you merge it on GitHub', 'Committed ddddddd [console] apps: scale hello/staging',
                 'Creating branch console/v2-scale-hello-staging-aaaaaaa', 'Opened https://github.com/x/pull/7']
        runner = self.runner('\n'.join(lines) + '\n')
        job = self.job()
        url = proposal.open_change(runner, gh.GitHub('owner/repo', 'token'), job, 'v2-scale', 'hello', 'staging',
                                   ('--set', 'replicas=2'))
        self.assertEqual(url, 'https://github.com/x/pull/7')
        self.assertEqual(job.lines, lines)
        argv = runner.calls[0]
        self.assertEqual(argv[:8], ('swhurl-v2', 'change', '--kind', 'v2-scale', '--app', 'hello', '--env', 'staging'))
        self.assertEqual(argv[8:], ('--requested-by', 'sam@swhurl.com', '--repository', 'owner/repo',
                                    '--set', 'replicas=2'))
        self.assertEqual(runner.environments[0][v2.TOKEN_ENV], 'token')

    def test_v2s_refusal_is_the_jobs_last_line_and_opens_nothing(self):
        runner = self.runner('Downloading owner/repo main (aaaaaaa)\n'
                             'invalid: replicas: expected_a_non_negative_integer\n', returncode=2)
        job = self.job()
        with self.assertRaises(Exception) as caught:
            proposal.open_change(runner, gh.GitHub('owner/repo', 'token'), job, 'v2-scale', 'hello', 'staging',
                                 ('--set', 'replicas=-1'))
        self.assertIn('invalid: replicas: expected_a_non_negative_integer', str(caught.exception))
        self.assertEqual(job.lines[-1], 'invalid: replicas: expected_a_non_negative_integer')
        self.assertEqual(job.link, '')

    def test_a_channel_address_goes_to_standard_input_and_a_team_change_names_no_environment(self):
        seen = {}

        def handler(argv, stdin):
            seen['argv'], seen['stdin'] = argv, stdin
            return Result(argv, 0, 'Opened https://github.com/x/pull/8\n')

        runner = self.runner('', handler=handler)
        proposal.open_change(runner, gh.GitHub('owner/repo', 'token'), actions.Job(2, 'channel set', 'team:payments',
                                                                                   'sam@swhurl.com', None),
                             'team-channel-set', 'payments', '', ('--transport', 'ntfy'),
                             'https://ntfy.sh/x\n')
        self.assertEqual(seen['argv'][:6], ('swhurl-v2', 'change', '--kind', 'team-channel-set', '--team',
                                            'payments'))
        self.assertNotIn('https://ntfy.sh/x', ' '.join(seen['argv']))
        self.assertEqual(seen['stdin'], 'https://ntfy.sh/x\n')

    def test_a_new_app_quotes_the_repository_and_template_before_its_commands(self):
        seen = {}

        def handler(argv, stdin):
            seen['argv'] = argv
            return Result(argv, 0, 'Opened https://github.com/x/pull/9\n')

        chosen = proposal.NewApp('hello', 'typescript', '', {}, ('team=demo', 'exposure=private'))
        runner = self.runner('', handler=handler)
        proposal.open_new_app(runner, gh.GitHub('owner/repo', 'token'), self.job(), chosen, 'ghcr.io/x/hello:1-ab')
        argv = seen['argv']
        self.assertIn('--image', argv)
        self.assertEqual(argv[argv.index('--image') + 1], 'ghcr.io/x/hello:1-ab')
        preface = argv[argv.index('--preface') + 1]
        self.assertTrue(preface.startswith('The console created https://github.com/samclement/hello from '))
        self.assertIn('and waited for its first image.', preface)
        self.assertEqual(argv[:4], ('swhurl-v2', 'change', '--kind', 'new'))


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


class UnitActionTests(unittest.TestCase):
    """A Flux unit action is one ``swhurl-v2 unit`` run in the pod: the console names the credential and keeps the
    lines, and a refusal is v2's own last line with the command that printed it."""

    def test_a_unit_action_is_one_command_with_one_credential(self):
        runner = FakeRunner().on('swhurl-v2', 'unit', stdout='► annotating Kustomization gw in flux-system namespace\n'
                                                            '✔ applied revision main@1edf370\n')
        lines = list(v2.unit(runner, 'reconcile', 'gw', in_cluster=True))
        self.assertEqual(runner.calls, [('swhurl-v2', 'unit', '--action', 'reconcile', '--unit', 'gw', '--in-cluster')])
        self.assertEqual(lines, ['► annotating Kustomization gw in flux-system namespace',
                                 '✔ applied revision main@1edf370'])

    def test_a_refusal_is_v2s_last_line_and_its_exit(self):
        runner = FakeRunner().on('swhurl-v2', 'unit', returncode=2,
                                 stderr='gw is suspended; resume it instead (resume also reconciles)')
        with self.assertRaises(CommandError) as raised:
            list(v2.unit(runner, 'reconcile', 'gw', in_cluster=True))
        self.assertIn('gw is suspended; resume it instead (resume also reconciles)', str(raised.exception))
        self.assertIn('exited 2', str(raised.exception))

    def test_dev_names_the_hosts_kubeconfig_and_never_in_cluster(self):
        runner = FakeRunner().on('kubectl', 'config', 'view', stdout='{"kind":"Config"}').on('swhurl-v2', 'unit')
        list(v2.unit(runner, 'suspend', 'gw', in_cluster=False))
        argv = runner.calls[1]
        self.assertEqual(argv[:6], ('swhurl-v2', 'unit', '--action', 'suspend', '--unit', 'gw'))
        self.assertIn('--kubeconfig', argv)
        self.assertNotIn('--in-cluster', argv)

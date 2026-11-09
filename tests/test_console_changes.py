"""Console changes through GitHub: form to tooling argv, download, commit, branch, PR; offline, with
FakeRunner for the tree's tooling and a fake GitHub API."""
import base64
import io
import json
import tarfile
import tempfile
import unittest
from pathlib import Path

import httpx
import yaml
from starlette.testclient import TestClient

from swhurl import ROOT
from swhurl.apps import calling, channels
from swhurl.apps.model import write as model_sync
from swhurl.console import actions, cluster, proposal, repos, server
from swhurl.console import github as gh
from swhurl.run import FakeRunner, Result

TOKEN = 'github_pat_fixture_0123456789abcdef'
HEAD = 'abc1234' + '0' * 33
WHO = {'X-Auth-Request-Email': 'sam@swhurl.com', 'Origin': 'http://testserver'}
NEW = {'name': 'weather-api', 'type': 'web-app', 'stack': 'typescript', 'team': 'platform', 'exposure': 'authenticated-web', 'host': ''}
IMAGE = 'ghcr.io/me/hello:2.0@sha256:' + 'a' * 64


def parse_image(image: str) -> dict:
    """REPO:TAG@sha256:... as the Helm values and the record state it."""
    name, _, digest = image.partition('@')
    repository, _, tag = name.rpartition(':')
    return {'repository': repository, 'tag': tag, **({'digest': digest} if digest else {})}
REVISION = 'main@sha1:' + HEAD
SAMPLE = ROOT / 'tests/fixtures/instance'  # frozen sample instance; see its README
MAIN = {'README.md': b'# repo\n', calling.SIGNING_KEYS_FILE.as_posix(): (ROOT / calling.SIGNING_KEYS_FILE).read_bytes()}
for env in ('staging', 'prod'):
    for path in (SAMPLE / 'apps/hello' / env).glob('*.yaml'):
        MAIN[path.relative_to(SAMPLE).as_posix()] = path.read_bytes()
    rel = f'apps/hello/{env}/helmrelease.yaml'
    doc = yaml.safe_load(MAIN[rel])
    doc['spec']['values']['controllers']['main']['containers']['main']['image'] = parse_image(
        IMAGE if env == 'staging' else 'ghcr.io/me/hello:1.0@sha256:' + 'b' * 64)
    MAIN[rel] = yaml.safe_dump(doc, sort_keys=False).encode()
    record = f'apps/hello/{env}/release.yaml'  # the Release record says the same image
    doc = yaml.safe_load(MAIN[record])
    doc['image'] = parse_image(IMAGE if env == 'staging' else 'ghcr.io/me/hello:1.0@sha256:' + 'b' * 64)
    MAIN[record] = yaml.safe_dump(doc, sort_keys=False).encode()
MAIN['apps/hello/app.yaml'] = (SAMPLE / 'apps/hello/app.yaml').read_bytes()
for rel in ('clusters/home/app-hello-staging.yaml', 'clusters/home/app-hello-prod.yaml'):
    MAIN[rel] = (SAMPLE / rel).read_bytes()
with tempfile.TemporaryDirectory() as tmp:  # rendered from its records, as main always is
    root = Path(tmp)
    for rel, data in MAIN.items():
        (root / rel).parent.mkdir(parents=True, exist_ok=True)
        (root / rel).write_bytes(data)
    model_sync.sync(root)
    MAIN = {path.relative_to(root).as_posix(): path.read_bytes() for path in sorted(root.rglob('*')) if path.is_file()}



ROUTES = json.dumps({'apiVersion': 'v1', 'kind': 'ConfigMap', 'metadata': {'name': channels.ROUTES_NAME, 'namespace': 'notify'},
                     'data': {channels.ROUTES_KEY: json.dumps({'channels': {'payments-phone': 'ntfy'},
                                                               'teams': {'payments': ['payments-phone']}})}})
"""The routes v2 rendered on main: team payments notifies its channel."""


class FakeGitHub:
    """GitHub's REST API for one repository whose main is ``MAIN``; records every request."""

    def __init__(self, fail: dict[str, tuple[int, str]] | None = None):
        self.requests: list[httpx.Request] = []
        self.fail = fail or {}
        self.also: dict[str, object] = {}  # further answers, by "METHOD /path"
        self.github = gh.GitHub('samclement/swhurl-platform', TOKEN, transport=httpx.MockTransport(self.handle))

    def tarball(self) -> bytes:
        out = io.BytesIO()
        with tarfile.open(fileobj=out, mode='w:gz') as tar:
            for path, data in MAIN.items():
                info = tarfile.TarInfo(f'samclement-swhurl-platform-abc1234/{path}')
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
        return out.getvalue()

    def handle(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        path = request.url.path.removeprefix('/repos/samclement/swhurl-platform')
        key = f'{request.method} {path}'
        if key in self.fail:
            code, message = self.fail[key]
            return httpx.Response(code, json={'message': message})
        answers = {
            'GET /git/ref/heads/main': {'object': {'sha': HEAD}},
            f'GET /git/commits/{HEAD}': {'tree': {'sha': 'tree0'}},
            'POST /git/blobs': {'sha': f'blob{len(self.sent("POST /git/blobs")) - 1}'},
            'POST /git/trees': {'sha': 'tree1'},
            'POST /git/commits': {'sha': 'def5678' + '0' * 33},
            'POST /git/refs': {'ref': 'x'},
            'GET /contents/v2-records/teams.json': {'content': base64.b64encode(json.dumps({'schemaVersion': 1, 'teams': {
                'platform': {'description': 'The platform itself'}, 'payments': {}}}).encode()).decode()},
            'GET /contents/platform/notify/routes.json': {'content': base64.b64encode(ROUTES.encode()).decode()},
            'POST /pulls': {'html_url': 'https://github.com/x/pull/7', 'number': 7},
            'GET /pulls': [{'number': 7, 'title': '[console] apps: add weather-api staging', 'html_url': 'https://github.com/x/pull/7',
                            'head': {'ref': 'console/new-weather-api-staging-abc1234'}, 'created_at': '2026-10-01T07:00:00Z'},
                           {'number': 8, 'title': 'chore(deps): bump', 'html_url': 'https://github.com/x/pull/8',
                            'head': {'ref': 'renovate/x'}, 'created_at': '2026-10-01T07:00:00Z'}],
        }
        answers.update(self.also)
        if key.startswith('GET /contents/') and key not in answers:
            # The Git reader for a page: main's file, exactly as the tarball holds it. Raw bytes, because
            # the console asks for the raw media type and uses the response text.
            data = MAIN.get(key.removeprefix('GET /contents/'))
            if data is not None:
                if 'raw' in request.headers.get('accept', ''):
                    return httpx.Response(200, content=data)
                return httpx.Response(200, json={'content': base64.b64encode(data).decode(), 'encoding': 'base64'})
        if key == f'GET /tarball/{HEAD}' and key not in answers:  # built only when asked: compressing is the cost
            answers[key] = self.tarball()
        if key not in answers:
            return httpx.Response(404, json={'message': 'Not Found'})
        answer = answers[key]
        return httpx.Response(200, content=answer) if isinstance(answer, bytes) else httpx.Response(201, json=answer)

    def sent(self, key: str) -> list[dict]:
        return [json.loads(r.content) for r in self.requests if f'{r.method} {r.url.path}'.endswith(key.split(' ', 1)[1])
                and r.method == key.split(' ', 1)[0]]


V2_CREATE = ('v2-records/demo/app.json', 'v2-records/demo/staging/release.json', 'v2-records/demo/staging/instance.json',
             'v2-records/demo/staging/events.sops.yaml')
V2_RENDER = ('v2-apps/instances/demo/staging/helmrelease.json', 'clusters/home/app-demo-staging.json',
             'v2-records/calls.json')


def v2_create(tree: Path, app: str) -> None:
    """What ``swhurl-v2 create`` writes for a new app: its records, with the seed v2 encrypted itself."""
    for name in V2_CREATE:
        path = tree / name.replace('demo', app, 1)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(f'{{"application": "{app}", "environment": "staging"}}\n' if 'instance' in name else '{}\n')


def v2_render(tree: Path, app: str) -> None:
    """What ``swhurl-v2 render`` writes for it: the compiled folder, the unit and the platform documents."""
    for name in V2_RENDER:
        path = tree / name.replace('demo', app, 1)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('{}\n')


def staging_app(name, variables=None, port=8080):
    """A unit and release as the cluster lists them, for the apps the New app form offers."""
    values = {'controllers': {'main': {'containers': {'main': {'env': variables or {}}}}}}
    if port:
        values['service'] = {'main': {'ports': {'http': {'port': port}}}}
    ready = {'conditions': [{'type': 'Ready', 'status': 'True'}]}
    return ({'metadata': {'name': f'app-{name}-staging'}, 'spec': {}, 'status': ready},
            {'metadata': {'namespace': f'{name}-staging', 'name': name}, 'spec': {'values': values}})


def tree_fake(staging=()):
    """``staging``: the staging_app()s that run (none by default)."""
    runner = FakeRunner()
    # Nonstatus inventory lists; Teams receives its instance decisions from Go report fixtures;
    # any other kubectl call is still unexpected.
    for prefix, found in ((cluster.UNITS[:5], [unit for unit, _ in staging]),
                          (('kubectl', 'get', 'namespaces'), [])):
        runner.on(*prefix, stdout=json.dumps({'items': found}))
    runner.on('kubectl', '-n', 'console', 'get', 'configmap', 'app-dashboards', stdout='')  # no dashboard recorded
    from console_observation import install
    install(runner, [unit for unit, _ in staging], [release for _, release in staging])
    for report in runner.status_reports.values():
        report['target']['revision'] = HEAD
    runner.on('swhurl-v2', 'change', stdout=CHANGE_OUTPUT)
    runner.on('swhurl-v2', stdout='{}')
    return runner


CHANGE_OUTPUT = ('Downloading samclement/swhurl-platform main (abc1234)\n'
                 'M v2-records/payments/staging/instance.json\n'
                 'Merge plan: you merge it on GitHub\n'
                 'Committed def5678 [console] teams: add billing\n'
                 'Creating branch console/team-add-billing-abc1234\n'
                 'Opened https://github.com/x/pull/7\n')
"""What ``swhurl-v2 change`` prints: the console takes the job's lines and its link from the last one."""


def app_under_test(runner, **kwargs):
    """The console app, the declaration a repository would serve given inline: no test here reaches GitHub."""
    kwargs.setdefault('repo_opener', lambda request: b'version: 1\nkind: worker\nstack: typescript\n')
    return server.create_app(runner, **kwargs)








class NewAppRouteTests(unittest.TestCase):
    def client(self, runner, github=True):
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        self.api = FakeGitHub()
        github = self.api.github if github else None
        return TestClient(app_under_test(runner, jobs=jobs, github=github,
                                         app_repos=repos.AppReposToken('github_pat_app_repos_fixture_0123',
                                                                       image_webhook_token='fixture-image-webhook-token'))), jobs

    def test_the_page_offers_the_form_and_asks_for_no_host(self):
        runner = tree_fake()
        c, jobs = self.client(runner)
        for url in ('/new', '/new?preset=swhurl-web'):
            with self.subTest(url=url):
                page = c.get(url, headers=WHO).text
                self.assertIn('action="/new/repo"', page)
                self.assertIn('<h1>New app</h1>', page)
                self.assertNotIn('name="host"', page, 'v2 derives the address from the name and the exposure')
                self.assertNotIn('app-new', page)
                self.assertNotIn('swhurl-v2 migrate', page)
        self.assertIsNone(jobs.get(1), 'reading the form starts nothing')

    def test_without_github_tokens_the_form_is_refused_and_nothing_is_created(self):
        runner = tree_fake()
        c, jobs = self.client(runner, github=False)
        response = c.post('/new/repo', data=NEW, headers=WHO)
        self.assertEqual(response.status_code, 409)
        self.assertIn('Both GitHub tokens must be configured', response.text)
        self.assertIsNone(jobs.get(1))
        self.assertEqual(self.api.requests, [])

    def test_the_form_creates_the_repository_and_opens_staging_with_one_v2_command(self):
        runner = tree_fake()
        runner._rules.insert(0, (('swhurl-v2', 'app-repo'), Result((), 0, 'Rendering the typescript template\n'
                                                                    'Created https://github.com/samclement/weather-api\n'
                                                                    'Opened https://github.com/x/pull/9\n')))
        c, jobs = self.client(runner)
        response = c.post('/new/repo', data=NEW, headers=WHO, follow_redirects=False)
        self.assertEqual((response.status_code, response.headers['location']), (303, '/jobs/1'))
        job = jobs.get(1)
        self.assertEqual((job.state, job.link, job.lines[-1]), ('succeeded', 'https://github.com/x/pull/9',
                                                               'Opened https://github.com/x/pull/9'))
        ran = next(call for call in runner.calls if call[:2] == ('swhurl-v2', 'app-repo'))
        self.assertEqual(ran[2:], ('--app', 'weather-api', '--type', 'web-app', '--stack', 'typescript',
                                   '--requested-by', 'sam@swhurl.com', '--repository', 'samclement/swhurl-platform',
                                   '--owner', 'samclement', '--set', 'team=platform',
                                   '--set', 'exposure=authenticated-web', '--hook-host',
                                   'flux-webhook.homelab.swhurl.com'))
        self.assertEqual(runner.environments, [{'CONSOLE_TOKEN': TOKEN,
                                                'APP_REPOS_TOKEN': 'github_pat_app_repos_fixture_0123',
                                                'IMAGE_WEBHOOK_TOKEN': 'fixture-image-webhook-token'}],
                         'the tokens reach the child\'s environment, never its argv')

    def test_cross_site_post_is_refused_before_the_route(self):
        c, _ = self.client(tree_fake())
        self.assertEqual(c.post('/new/repo', data=NEW, headers={**WHO, 'Origin': 'https://evil.example'}).status_code, 403)


def staging_reads(runner):
    """A healthy, applied staging image for promotion; all reads remain offline."""
    ready = {'conditions': [{'type': 'Ready', 'status': 'True'}]}
    objects = {
        'kustomization': {'spec': {}, 'status': {**ready, 'lastAppliedRevision': REVISION}},
        'gitrepository': {'status': {'artifact': {'revision': REVISION}}},
        'helmrelease': {'status': ready, 'spec': {'values': {'controllers': {'main': {
            'containers': {'main': {'image': parse_image(IMAGE)}}}}}}},
        'pods': {'items': [{'metadata': {'name': 'hello-1'}, 'status': {'containerStatuses': [
            {'ready': True, 'imageID': IMAGE.split(':2.0')[0] + '@' + IMAGE.split('@')[1]}]}}]},
        'deploy,statefulset,daemonset,cronjob': {'items': [{'kind': 'Deployment', 'metadata': {'name': 'hello'},
                                                  'spec': {'replicas': 1}, 'status': {'readyReplicas': 1}}]},
        'ingress': {'items': []}, 'certificate': {'items': []},
    }
    for kind, obj in objects.items():
        ns = 'flux-system' if kind in ('kustomization', 'gitrepository') else 'hello-staging'
        runner.on('kubectl', '-n', ns, 'get', kind, stdout=json.dumps(obj))
    return runner




if __name__ == '__main__':
    unittest.main()


class TeamsPageTests(unittest.TestCase):
    """The Teams page: the registry on main beside the apps that run, and changes as pull requests."""

    def client(self, namespaces=None, **kwargs):
        staging = (staging_app('orders'), staging_app('billing'), staging_app('ghost'))
        self.runner = tree_fake(staging=staging)
        labels = namespaces or {'orders-staging': 'payments', 'billing-staging': 'payments', 'ghost-staging': 'retired'}
        # Each app's team comes with the block Go reports; the page reads no namespace list of its own.
        for name, team in labels.items():
            app, _, env = name.rpartition('-')
            self.runner.inventories[app, env] = {**self.runner.inventories[app, env], 'team': team}
        self.jobs = actions.Jobs(self.runner, audit=lambda line: None, inline=True)
        self.api = FakeGitHub()
        return TestClient(app_under_test(self.runner, jobs=self.jobs, github=self.api.github, **kwargs))

    def test_shows_each_team_with_its_apps_and_the_apps_left_without_one(self):
        page = self.client().get('/teams', headers=WHO).text
        self.assertIn('<h2>platform</h2>', page)
        self.assertLess(page.index('<h2>payments</h2>'), page.index('<h2>platform</h2>'), 'alphabetical, not the registry\'s order')
        self.assertNotIn('>default</span>', page, 'no team is special')
        self.assertIn('The platform itself', page)
        self.assertIn('<a href="/apps/billing/staging" title="billing/staging:', page, 'each app, with what runs where')
        self.assertIn('<a href="/apps?team=payments">2 apps on the Deployments list</a>', page)
        self.assertIn('Apps without a registered team', page)
        self.assertIn('<td>retired</td>', page)
        self.assertIn('<a href="/apps/ghost/staging">ghost</a>', page)
        self.assertIn('It owns billing, orders, so it is not deleted', page, 'a team with apps is deleted only after they name another')
        self.assertIn('<button class="primary" disabled>', page, 'and its delete button cannot open a refused pull request')
        self.assertEqual(page.count('action="/teams/remove"'), 2, 'every team can be deleted: none is special')
        self.assertEqual(page.count('name="rename"'), 2, 'and renamed')
        self.assertNotIn('default team', page)
        self.assertEqual(page.count('action="/teams/edit"'), 2, 'one Edit per team: its description and its name are one form')
        self.assertEqual(page.count('<details class="act" name="team-form"'), 7,
                         'new team, and per team edit, channel and delete: one open at a time')
        self.assertIn('href="/teams" class="here"', page)
        self.assertTrue(all((call[0] == 'kubectl' and ('get' in call or call[1:3] == ('config', 'view'))) or call[:2] == ('swhurl-v2', 'status') for call in self.runner.calls), 'the page only reads')

    def test_an_apps_language_comes_from_its_own_repository_and_is_asked_for_once(self):
        asked = []

        def opener(request):
            asked.append(request.full_url)
            if 'ghost' in request.full_url:
                raise OSError('GitHub is away')
            return b'version: 1\nkind: worker\nstack: kotlin\n'
        c = self.client(repo_opener=opener)
        page = c.get('/teams', headers=WHO).text
        self.assertEqual(asked[0], 'https://api.github.com/repos/samclement/billing/contents/swhurl.yaml')
        self.assertEqual(page.count('<span class="pill">Kotlin</span>'), 1, 'billing; orders is not asked for once GitHub fails')
        self.assertEqual(len(asked), 2, 'after one failure the rest are not asked for: the page must not wait once per app')
        c.get('/teams', headers=WHO)
        self.assertEqual(len(asked), 2, 'kept: an app does not change language')

    def test_each_team_change_is_one_v2_change_that_waits_for_review(self):
        cases = (
            ('add', {'name': 'billing', 'description': ' Invoices  and\nrefunds '},
             ('--description', 'Invoices and refunds')),
            ('edit', {'name': 'payments', 'description': 'Orders', 'rename': 'orders'},
             ('--description', 'Orders', '--rename', 'orders')),
            ('edit', {'name': 'payments', 'description': '', 'rename': ''}, ('--description', '')),
            ('remove', {'name': 'payments'}, ()),
        )
        for action, form, flags in cases:
            with self.subTest(action=action, form=form):
                c = self.client()
                response = c.post(f'/teams/{action}', data=form, headers=WHO, follow_redirects=False)
                self.assertEqual(response.status_code, 303, response.text)
                ran = [call for call in self.runner.calls if call[:2] == ('swhurl-v2', 'change')]
                self.assertEqual(ran, [('swhurl-v2', 'change', '--kind', f'team-{action}', '--team', form['name'],
                                        '--requested-by', 'sam@swhurl.com',
                                        '--repository', 'samclement/swhurl-platform', *flags)])
                job = self.jobs.get(1)
                self.assertEqual((job.state, job.link), ('succeeded', 'https://github.com/x/pull/7'))
                self.assertEqual(cluster.target(job.unit), (f"team {form['name']}", f"/teams#{form['name']}"))

    def test_the_new_app_form_states_its_connections_as_v2_settings(self):
        chosen = proposal.NewApp.from_form(
            {'name': 'weather-api', 'type': 'web-app', 'stack': 'typescript', 'description': 'Weather',
             'team': 'platform', 'exposure': 'authenticated-web', 'calls': 'orders', 'subscribe': 'orders.created'},
            {'platform': 'The platform itself'}, {'orders'}, {'orders.created'})
        self.assertEqual(chosen.flags, ('team=platform', 'exposure=authenticated-web', 'calls=orders',
                                        'subscriptions=orders.created'))
        self.assertEqual((chosen.name, chosen.stack, chosen.app_type), ('weather-api', 'typescript', 'web-app'))

    def test_each_team_card_says_whether_it_has_a_channel_and_offers_to_set_replace_or_remove_it(self):
        page = self.client().get('/teams', headers=WHO).text
        self.assertEqual(page.count('<span class="pill ok" title="Gets a copy of the deployment and health messages of this '
                                    'team\'s apps">notification channel · ntfy</span>'), 1, 'payments has one')
        self.assertEqual(page.count('No notification channel: messages about its apps go to the platform topic only.'), 1)
        self.assertEqual(page.count('action="/team-channel/set"'), 2, 'every team can be given a channel or a new address')
        self.assertEqual(page.count('Open new address PR'), 1)
        self.assertEqual(page.count('Open channel PR'), 1)
        self.assertEqual(page.count('action="/team-channel/remove"'), 1, 'only a team that has one can remove it')
        self.assertEqual(page.count('data-generate hidden>Generate</button>'), 2)
        self.assertNotIn('payments-phone', page, 'a channel has no name to manage: it is the team\'s')
        self.assertNotIn('name="notify"', page)
        self.assertNotIn('ntfy.sh/', page.replace('https://ntfy.sh/your-topic', ''), 'no address is ever on the page')

    def test_a_channels_address_reaches_only_the_commands_standard_input(self):
        address = 'https://ntfy.sh/swhurl-0123456789abcdef01234567'
        c = self.client()
        received = []
        self.runner._rules.insert(0, (('swhurl-v2', 'change'), lambda args, text: (
            received.append(text), Result(args, 0, 'Opened https://github.com/x/pull/7\n'))[1]))
        response = c.post('/team-channel/set', data={'name': 'payments', 'transport': 'ntfy', 'address': f' {address} '},
                          headers=WHO, follow_redirects=False)
        self.assertEqual(response.status_code, 303, response.text)
        self.assertEqual(received, [address + '\n'])
        tool = next(call for call in self.runner.calls if call[:2] == ('swhurl-v2', 'change'))
        self.assertEqual(tool[2:], ('--kind', 'team-channel-set', '--team', 'payments', '--requested-by',
                                    'sam@swhurl.com', '--repository', 'samclement/swhurl-platform',
                                    '--transport', 'ntfy'))
        job = self.jobs.get(1)
        self.assertEqual((job.state, cluster.target(job.unit)), ('succeeded', ('team payments', '/teams#payments')))
        everywhere = '\n'.join([*job.lines, *(' '.join(call) for call in self.runner.calls), *self.runner.echoed,
                                c.get('/jobs/1', headers=WHO).text, c.get('/teams', headers=WHO).text])
        self.assertNotIn('0123456789abcdef', everywhere)
        self.assertIn('<redacted>', self.runner.redact(f'posting to {address}'))
        c = self.client()
        self.assertEqual(c.post('/team-channel/remove', data={'name': 'payments'}, headers=WHO,
                                follow_redirects=False).status_code, 303)
        tool = next(call for call in self.runner.calls if call[:2] == ('swhurl-v2', 'change'))
        self.assertEqual(tool[2:], ('--kind', 'team-channel-remove', '--team', 'payments', '--requested-by',
                                    'sam@swhurl.com', '--repository', 'samclement/swhurl-platform'))

    def test_a_malformed_channel_opens_nothing_and_never_echoes_the_address(self):
        c = self.client()
        # A malformed address is v2's to refuse, in the job, and v2 never repeats it (v2/cmd/swhurl-v2/team_test.go).
        cases = (('set', {'name': 'Bad Name', 'address': 'https://ntfy.sh/private-topic'}),
                 ('remove', {'name': '--root=/'}), ('explode', {'name': 'x', 'address': 'https://ntfy.sh/private-topic'}))
        for action, form in cases:
            with self.subTest(action=action, form=form):
                response = c.post(f'/team-channel/{action}', data=form, headers=WHO)
                self.assertEqual(response.status_code, 400)
                self.assertNotIn('private-topic', response.text)
        self.assertFalse(any(call[:2] == ('swhurl-v2', 'change') for call in self.runner.calls))
        without_token = TestClient(app_under_test(tree_fake(), jobs=actions.Jobs(tree_fake(), audit=lambda line: None, inline=True)))
        self.assertEqual(without_token.post('/team-channel/set', data={'name': 'x', 'address': 'https://ntfy.sh/t'}, headers=WHO).status_code, 409)

    def test_malformed_input_opens_nothing_and_a_refused_command_shows_its_words(self):
        c = self.client()
        for action, form in (('add', {'name': 'Bad Name'}), ('add', {'name': ''}), ('edit', {'name': 'a', 'rename': '--root=/'}),
                             ('remove', {'name': ''}), ('explode', {'name': 'a'})):
            with self.subTest(action=action, form=form):
                self.assertEqual(c.post(f'/teams/{action}', data=form, headers=WHO).status_code, 400)
        self.assertFalse(any(call[:2] == ('swhurl-v2', 'change') for call in self.runner.calls))
        message = 'team_in_use: team: an app record names the team: move it first with edit --set team='
        c = self.client()
        self.runner._rules.insert(0, (('swhurl-v2', 'change'), Result((), 2, '', message + '\n')))
        c.post('/teams/remove', data={'name': 'payments'}, headers=WHO)
        job = self.jobs.get(1)
        self.assertEqual(job.state, 'failed')
        self.assertIn(message, job.lines)
        self.assertEqual(job.link, '')
        without_token = TestClient(app_under_test(tree_fake(), jobs=actions.Jobs(tree_fake(), audit=lambda line: None, inline=True)))
        self.assertEqual(without_token.post('/teams/add', data={'name': 'billing'}, headers=WHO).status_code, 409)
        self.assertIn('cannot open pull requests', without_token.get('/teams', headers=WHO).text)

    def test_an_unreadable_registry_is_said_so_and_raises_no_false_alarm(self):
        self.client()
        unreadable = FakeGitHub(fail={'GET /contents/v2-records/teams.json': (404, 'Not Found')})
        page = TestClient(app_under_test(self.runner, jobs=self.jobs, github=unreadable.github)).get('/teams', headers=WHO).text
        self.assertIn('no app can be called unregistered', page)
        self.assertNotIn('Apps without a registered team', page)
        self.assertIn('<section class="card" id="payments">', page, 'the teams apps name are still shown')
        self.assertIn('<section class="card" id="retired">', page)

"""Console changes through GitHub: form to tooling argv, download, commit, branch, PR; offline, with
FakeRunner for the tree's tooling and a fake GitHub API."""
import base64
import io
import json
import shutil
import sys
import tarfile
import tempfile
import unittest
import urllib.error
from dataclasses import replace
from pathlib import Path
from unittest import mock

import httpx
import yaml
from starlette.testclient import TestClient

from swhurl import ROOT
from swhurl.apps import calling, channels, egress, new
from swhurl.apps.instance import Instance
from swhurl.apps.model import write as model_sync
from swhurl.apps.state import State
from swhurl.console import actions, cluster, server, stages
from swhurl.console import github as gh
from swhurl.run import FakeRunner, Result

TOKEN = 'github_pat_fixture_0123456789abcdef'
HEAD = 'abc1234' + '0' * 33
WHO = {'X-Auth-Request-Email': 'sam@swhurl.com', 'Origin': 'http://testserver'}
NEW = {'name': 'weather-api', 'type': 'web-app', 'stack': 'typescript', 'team': 'platform', 'exposure': 'authenticated-web', 'host': ''}
IMAGE = 'ghcr.io/me/hello:2.0@sha256:' + 'a' * 64
REVISION = 'main@sha1:' + HEAD
SAMPLE = ROOT / 'tests/fixtures/instance'  # frozen sample instance; see its README
MAIN = {'README.md': b'# repo\n', calling.SIGNING_KEYS_FILE.as_posix(): (ROOT / calling.SIGNING_KEYS_FILE).read_bytes()}
for env in ('staging', 'prod'):
    for path in (SAMPLE / 'apps/hello' / env).glob('*.yaml'):
        MAIN[path.relative_to(SAMPLE).as_posix()] = path.read_bytes()
    rel = f'apps/hello/{env}/helmrelease.yaml'
    doc = yaml.safe_load(MAIN[rel])
    doc['spec']['values']['controllers']['main']['containers']['main']['image'] = new.parse_image(
        IMAGE if env == 'staging' else 'ghcr.io/me/hello:1.0@sha256:' + 'b' * 64)
    MAIN[rel] = yaml.safe_dump(doc, sort_keys=False).encode()
    record = f'apps/hello/{env}/release.yaml'  # the Release record says the same image
    doc = yaml.safe_load(MAIN[record])
    doc['image'] = new.parse_image(IMAGE if env == 'staging' else 'ghcr.io/me/hello:1.0@sha256:' + 'b' * 64)
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
PROMOTION = {'image': IMAGE}  # what the review page submits



ROUTES = channels.routes_configmap({'channels': {'payments-phone': 'ntfy', 'spare': 'ntfy'},
                                    'teams': {'payments': ['payments-phone']}})
"""The generated routes on main: two channels, one of them notified by team payments."""


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
            'GET /contents/apps/teams.yaml': {'content': base64.b64encode(
                b'teams:\n  platform: {description: The platform itself}\n  payments: {}\n').decode()},
            'GET /contents/platform/notify/routes.yaml': {'content': base64.b64encode(ROUTES.encode()).decode()},
            'POST /pulls': {'html_url': 'https://github.com/x/pull/7', 'number': 7},
            'POST /issues/7/labels': [{'name': 'auto-merge'}],
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


def rendered(hash, digest, settings, records):
    """An instance a tree has records for and has rendered (stages.Target)."""
    return stages.Target(records, settings, digest, hash, digest, hash)






class NewAppRenderTargetTests(unittest.TestCase):
    def test_manual_records_merge_then_port_allocation_is_creation_completion_not_replacement(self):
        records = {path: data for path, data in MAIN.items() if path in (
            'apps/hello/app.yaml', 'apps/hello/staging/release.yaml', 'apps/hello/staging/instance.yaml', calling.SIGNING_KEYS_FILE.as_posix())}
        settings = yaml.safe_load(records['apps/hello/staging/instance.yaml'])
        settings.get('allocations', {}).pop('egressPort', None)
        records['apps/hello/staging/instance.yaml'] = yaml.safe_dump(settings).encode()
        with mock.patch.dict(MAIN, records, clear=True):
            before = stages.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['hello/staging']
        self.assertTrue(before.pending)
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for path, data in records.items():
                (root / path).parent.mkdir(parents=True, exist_ok=True)
                (root / path).write_bytes(data)
            shutil.copytree(ROOT / egress.DIRECTORY, root / egress.DIRECTORY, dirs_exist_ok=True)
            model_sync.sync(root)
            rendered = {p.relative_to(root).as_posix(): p.read_bytes() for p in root.rglob('*') if p.is_file()}
        self.assertIn('egressPort', yaml.safe_load(rendered['apps/hello/staging/instance.yaml'])['allocations'])
        with mock.patch.dict(MAIN, rendered, clear=True):
            current = stages.render_targets(FakeRunner(), FakeGitHub().github, HEAD)['hello/staging']
        self.assertEqual(before.records, current.records, 'system port allocation is not a new request')
        self.assertEqual(before.settings, current.settings)
        def live(target):
            return cluster.AppRow(Instance('hello', 'staging'), State('healthy'), False, 'x', digest=target.digest,
                                  render_hash=target.hash, render_ready=True, render_state=State('healthy'), image_matches=True)
        for action in stages.NEW_APP_ACTIONS:
            with self.subTest(action=action):
                result = stages.progress(action, before, current, live(current))
                self.assertEqual(result.state, 'live')
                # Even if the first read after Render sees its own build rather than the seed,
                # the creation still follows it; a later operator setting does supersede it.
                built = replace(current, hash='built', expected='built', digest='own-build', rendered='own-build', records='new-release')
                result = stages.progress(action, before, built, live(built))
                self.assertEqual((result.state, result.newer), ('live', 'own-build'))
                changed = replace(current, hash='changed', settings='operator-change', records='operator-change')
                result = stages.progress(action, before, changed, live(changed))
                self.assertEqual(result.state, 'superseded')




class TreeRunner(FakeRunner):
    """A FakeRunner whose tooling commands edit the downloaded tree they run in, as app-new does."""

    def __init__(self, edit=None, **kwargs):
        super().__init__(**kwargs)
        self.edit = edit  # None: app-promote writes production's records, anything else a new file

    def _execute(self, argv, *, input, env, cwd):
        if cwd is not None and argv[:3] == (sys.executable, '-m', 'swhurl'):
            if self.edit is not None:
                self.edit(Path(cwd))
            elif argv[3:4] == ('app-promote',):
                promoted(Path(cwd))
            elif argv[3] in SETTINGS_COMMANDS:
                settings_edit(Path(cwd), argv[3:])
            elif argv[3:4] == ('app-new',):
                app = argv[4]
                for name in ('app.yaml', 'staging/release.yaml', 'staging/instance.yaml', 'staging/helmrelease.yaml'):
                    path = Path(cwd) / 'apps' / app / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_bytes(MAIN[f'apps/hello/{name}'])
                path = Path(cwd) / 'apps' / app / 'staging/instance.yaml'
                doc = yaml.safe_load(path.read_text())
                for flag in argv[5:]:
                    key, _, value = flag.removeprefix('--').partition('=')
                    if key in ('calls', 'subscribe'):
                        doc['calls' if key == 'calls' else 'subscriptions'] = value.split(',')
                path.write_text(yaml.safe_dump(doc))
            elif argv[3:4] == ('app-remove',):
                shutil.rmtree(Path(cwd) / 'apps' / argv[4] / argv[5])
            elif argv[3].startswith('team-'):
                path = Path(cwd) / 'apps/teams.yaml'
                path.write_text((path.read_text() if path.is_file() else 'teams: {}\n') + f'# {argv[3:]}\n')
            else:
                (Path(cwd) / 'apps/x').mkdir(parents=True, exist_ok=True)
                (Path(cwd) / 'apps/x/helmrelease.yaml').write_text('new\n')
        return super()._execute(argv, input=input, env=env, cwd=cwd)


SETTINGS_COMMANDS = {'app-scale': '', 'app-schedule': '', 'app-expose': '', 'app-calls': 'calls',
                     'app-events': 'subscriptions', 'app-egress': 'egress', 'app-allows': 'allows'}
"""The instance commands whose console PR carries only the instance's Settings, and the list each edits."""


def settings_edit(tree: Path, argv: tuple[str, ...]) -> None:
    """What an instance command writes that its PR carries: the instance's instance.yaml, changed by its flags."""
    command, app, env, *flags = argv
    path = tree / 'apps' / app / env / 'instance.yaml'
    doc = yaml.safe_load(path.read_text())
    for flag in flags:
        name, _, value = flag.removeprefix('--').partition('=')
        items = [item for item in value.split(',') if item]
        if name == 'replicas':
            doc['replicas'] = int(value)
        elif name in ('cpu', 'memory', 'memory-limit'):
            doc.setdefault('capacity', {})['memoryLimit' if name == 'memory-limit' else name] = value
        elif name in ('exposure', 'host', 'schedule'):
            doc[name] = value
        elif name.startswith('add'):
            doc[SETTINGS_COMMANDS[command]] = [*doc.get(SETTINGS_COMMANDS[command], []), *items]
        elif name.startswith('remove'):
            doc[SETTINGS_COMMANDS[command]] = [item for item in doc.get(SETTINGS_COMMANDS[command], []) if item not in items]
    path.write_text(yaml.safe_dump(doc, sort_keys=False))


def connected_main() -> dict[str, bytes]:
    """MAIN with hello/staging already calling orders and receiving orders.created: something to disconnect."""
    rel = 'apps/hello/staging/instance.yaml'
    doc = {**yaml.safe_load(MAIN[rel]), 'calls': ['orders'], 'subscriptions': ['orders.created']}
    return {rel: yaml.safe_dump(doc, sort_keys=False).encode()}


def promoted(tree: Path) -> None:
    """What app-promote writes that a promotion PR carries: production's Release with staging's image
    (and, for a first promotion, production's Settings)."""
    staging = yaml.safe_load((tree / 'apps/hello/staging/release.yaml').read_text())
    target = tree / 'apps/hello/prod/release.yaml'
    doc = yaml.safe_load(target.read_text()) if target.is_file() else dict(staging)
    doc['image'] = staging['image']
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(yaml.safe_dump(doc, sort_keys=False))
    settings = tree / 'apps/hello/prod/instance.yaml'
    if not settings.is_file():
        settings.write_text('version: 1\nexposure: authenticated-web\n')


APP_NEW_OK = Result((), 0, '[OK] wrote apps/weather-api/staging/helmrelease.yaml\n')


def staging_app(name, variables=None, port=8080):
    """A unit and release as the cluster lists them, for the apps the New app form offers."""
    values = {'controllers': {'main': {'containers': {'main': {'env': variables or {}}}}}}
    if port:
        values['service'] = {'main': {'ports': {'http': {'port': port}}}}
    ready = {'conditions': [{'type': 'Ready', 'status': 'True'}]}
    return ({'metadata': {'name': f'app-{name}-staging'}, 'spec': {}, 'status': ready},
            {'metadata': {'namespace': f'{name}-staging', 'name': name}, 'spec': {'values': values}})


def tree_fake(app_new=APP_NEW_OK, edit=None, staging=()):
    """``staging``: the staging_app()s that run (none by default)."""
    runner = TreeRunner(edit)
    # Only the three lists the New app form reads, and what the Teams page judges each app's state from;
    # any other kubectl call is still unexpected.
    for prefix, found in ((cluster.UNITS[:5], [unit for unit, _ in staging]),
                          (cluster.RELEASES[:3], [release for _, release in staging]),
                          (('kubectl', 'get', 'namespaces'), []),
                          (cluster.PODS[:4], []), (cluster.WORKLOADS[:4], []), (cluster.INGRESSES[:4], [])):
        runner.on(*prefix, stdout=json.dumps({'items': found}))
    runner.on(*cluster.SOURCE[:6], stdout=json.dumps({'status': {'artifact': {'revision': REVISION}}}))
    runner.on('kubectl', '-n', 'console', 'get', 'configmap', 'app-dashboards', stdout='')  # no dashboard recorded
    (runner.on(sys.executable, '-m', 'swhurl', 'app-new', handler=lambda args, _: Result(
        args, app_new.returncode, app_new.stdout, app_new.stderr))
        .on(sys.executable, '-m', 'swhurl', handler=lambda args, _: Result(args, 0, '[OK] edited\n')))
    return runner


def app_under_test(runner, **kwargs):
    """The console app, its template questions read from COPIER_YML: no test here reaches GitHub."""
    kwargs.setdefault('repo_opener', ghcr_opener)
    return server.create_app(runner, **kwargs)








class NewAppRouteTests(unittest.TestCase):
    def client(self, runner, github=True):
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        self.api = FakeGitHub()
        github = self.api.github if github else None
        return TestClient(app_under_test(runner, jobs=jobs, github=github)), jobs

    def test_the_page_says_a_new_app_is_made_outside_the_console(self):
        runner = tree_fake()
        c, jobs = self.client(runner)
        for url in ('/new', '/new?preset=swhurl-web'):
            with self.subTest(url=url):
                page = c.get(url, headers=WHO).text
                self.assertIn('The console does not create apps any more', page)
                self.assertIn('make app-repo NAME=&lt;app&gt; TYPE=&lt;type&gt; STACK=&lt;stack&gt;', page)
                self.assertIn('make app-new NAME=&lt;app&gt; ARGS=', page)
                self.assertIn('swhurl-v2 migrate', page)
                self.assertNotIn('action="/new/repo"', page, 'the disabled page has no form')
        self.assertIsNone(jobs.get(1))
        self.assertEqual(runner.calls, [], 'the disabled page reads no cluster')
        self.assertEqual(self.api.requests, [], 'and GitHub')

    def test_the_old_form_is_refused_and_creates_nothing(self):
        runner = tree_fake()
        c, jobs = self.client(runner)
        response = c.post('/new/repo', data=NEW, headers=WHO)
        self.assertEqual(response.status_code, 409)
        self.assertIn('The console no longer creates apps.', response.text)
        self.assertIn('swhurl-v2 migrate', response.text)
        self.assertIsNone(jobs.get(1))
        self.assertEqual(runner.calls, [])
        self.assertEqual(self.api.requests, [])

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
            'containers': {'main': {'image': new.parse_image(IMAGE)}}}}}}},
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


APP_COMMIT = '9f8e7d6' + '1' * 33
DIGEST = 'sha256:' + 'd' * 64


class FakeAppGitHub:
    """GitHub's API as the second token sees it: notes does not exist until created; its first run succeeds."""

    def __init__(self, existing=('hello-ts',), runs=None, webhook='fixture-image-webhook-token', hooks_status=201):
        self.hooks_status = hooks_status
        self.requests: list[httpx.Request] = []
        self.existing = set(existing)
        self.runs = list(runs if runs is not None else [None, {'status': 'in_progress'}, {'status': 'completed',
                                                                                        'conclusion': 'success'}])
        from swhurl.console import repos
        self.config = repos.AppReposToken('github_pat_app_repos_fixture_0123', image_webhook_token=webhook,
                                          transport=httpx.MockTransport(self.handle))

    def handle(self, request: httpx.Request) -> httpx.Response:
        self.requests.append(request)
        path, method = request.url.path, request.method
        if method == 'GET' and path.count('/') == 3 and path.startswith('/repos/samclement/'):
            return httpx.Response(200 if path.rsplit('/', 1)[1] in self.existing else 404, json={'message': 'x'})
        if (method, path) == ('POST', '/user/repos'):
            self.existing.add(json.loads(request.content)['name'])
            return httpx.Response(201, json={'html_url': 'https://github.com/samclement/notes'})
        answers = {('GET', '/repos/samclement/notes/git/ref/heads/main'): {'object': {'sha': 'init000'}},
                   ('POST', '/repos/samclement/notes/git/blobs'): {'sha': 'blob'},
                   ('POST', '/repos/samclement/notes/git/trees'): {'sha': 'tree'},
                   ('POST', '/repos/samclement/notes/git/commits'): {'sha': APP_COMMIT},
                   ('PATCH', '/repos/samclement/notes/git/refs/heads/main'): {'ref': 'refs/heads/main'}}
        if (method, path) == ('GET', '/repos/samclement/notes/actions/runs'):
            run = self.runs.pop(0) if len(self.runs) > 1 else self.runs[0]
            runs = [] if run is None else [{'name': 'Container', 'run_number': 1, 'html_url': 'https://run/1',
                                            'conclusion': None, **run}]
            return httpx.Response(200, json={'workflow_runs': runs})
        if (method, path) == ('POST', '/repos/samclement/notes/hooks'):
            return httpx.Response(self.hooks_status, json={'message': 'Resource not accessible by personal access token'})
        if (method, path) in answers:
            return httpx.Response(200, json=answers[(method, path)])
        return httpx.Response(404, json={'message': 'Not Found'})

    def writes(self):
        return [(r.method, r.url.path) for r in self.requests if r.method != 'GET']


COPIER_YML = """_subdirectory: template
app_name:
  type: str
description:
  type: str
kind:
  type: str
  help: web or worker
  choices: {web: web, worker: worker}
  default: web
database:
  type: str
  help: none or sqlite
  choices: [none, sqlite]
  default: none
api:
  type: str
  choices: [none, openapi]
  default: none
events:
  type: str
  choices: [none, asyncapi]
  default: none
flags:
  type: str
  choices: [none, example]
  default: none
frontend:
  type: str
  choices: [none, vite]
  default: none
"""


def ghcr_opener(request, seed=False):
    """GHCR serving every tag except a seed (0-<sha>), unless ``seed``."""
    if '/contents/copier.yml?ref=' in request.full_url:
        return {}, COPIER_YML.encode()
    if '/manifests/0-' in request.full_url and not seed:
        raise urllib.error.HTTPError(request.full_url, 404, 'not found', {}, None)
    if 'token' in request.full_url:
        return {}, json.dumps({'token': 'anon'}).encode()
    return {'Docker-Content-Digest': DIGEST}, b''


def render_into_dest(args, _input):
    dest = Path(args[-1])
    (dest / '.github/workflows').mkdir(parents=True)
    (dest / 'swhurl.yaml').write_text('version: 1\n')
    (dest / '.github/workflows/container.yml').write_text('name: Container\n')
    return Result(args, 0)


def repo_runner(runner=None):
    runner = runner or FakeRunner()
    return runner.on('copier', handler=render_into_dest).on('uvx', handler=render_into_dest)


class AppReposTests(unittest.TestCase):
    def job(self):
        return actions.Job(1, 'new app and repository', 'notes/staging', 'sam@swhurl.com', None)

    def test_creates_pushes_waits_and_returns_the_first_image(self):
        from swhurl.apps import repo
        from swhurl.console import repos
        api = FakeAppGitHub()
        client, job = repos.AppRepos(repo_runner(), api.config), self.job()
        image = repos.create_app_repo(client.runner, client, job, repo.Request('notes'), sleep=lambda _: None,
                                      opener=ghcr_opener)
        self.assertEqual(image, f'ghcr.io/samclement/notes:1-9f8e7d6@{DIGEST}')
        self.assertEqual(api.writes(), [('POST', '/user/repos')] + [('POST', '/repos/samclement/notes/git/blobs')] * 2
                         + [('POST', '/repos/samclement/notes/git/trees'), ('POST', '/repos/samclement/notes/git/commits'),
                            ('PATCH', '/repos/samclement/notes/git/refs/heads/main'),
                            ('POST', '/repos/samclement/notes/hooks')])
        hook = json.loads(next(r.content for r in api.requests if r.url.path.endswith('/hooks')))
        self.assertEqual((hook['events'], hook['config']['secret']), (['package', 'registry_package'],
                                                                      'fixture-image-webhook-token'))
        self.assertIn('/hook/', hook['config']['url'])
        self.assertIn('Added the image webhook: Flux scans as soon as an image is published', job.lines)
        self.assertNotIn('fixture-image-webhook-token', '\n'.join(job.lines))
        created = json.loads(api.requests[1].content)
        self.assertEqual((created['private'], created['auto_init']), (False, True))
        tree = json.loads(next(r.content for r in api.requests if r.url.path.endswith('/git/trees')))
        self.assertEqual(sorted(e['path'] for e in tree['tree']), ['.github/workflows/container.yml', 'swhurl.yaml'])
        self.assertNotIn('base_tree', tree, 'the rendered files replace the initial README')
        commit = json.loads(next(r.content for r in api.requests if r.url.path.endswith('/git/commits')))
        self.assertEqual(commit['parents'], ['init000'])
        self.assertIn('Requested-by: sam@swhurl.com', commit['message'])
        self.assertIn('A swhurl.yaml', job.lines)
        self.assertIn('run 1 in_progress', job.lines)
        self.assertNotIn('github_pat_app_repos_fixture_0123', '\n'.join(job.lines))

    def test_a_seed_published_before_the_build_finishes_is_the_first_image(self):
        from swhurl.apps import repo
        from swhurl.console import repos
        api = FakeAppGitHub(runs=[{'status': 'in_progress'}])
        client, job = repos.AppRepos(repo_runner(), api.config), self.job()
        seen = []

        def opener(request):
            seen.append(request.full_url)
            return ghcr_opener(request, seed=True)
        image = repos.create_app_repo(client.runner, client, job, repo.Request('notes'), sleep=lambda _: None,
                                      opener=opener)
        self.assertEqual(image, f'ghcr.io/samclement/notes:0-9f8e7d6@{DIGEST}')
        self.assertIn('https://ghcr.io/v2/samclement/notes/manifests/0-9f8e7d6', seen)
        self.assertTrue(job.lines[-1].startswith(f'Seeded {image}'), job.lines[-1])
        self.assertFalse([r for r in api.requests if r.url.path.endswith('/actions/runs')], 'no wait for the build')

    def test_a_missing_or_refused_image_webhook_is_a_warning_not_a_failure(self):
        from swhurl.apps import repo
        from swhurl.console import repos
        for name, api, expected in (('no token', FakeAppGitHub(webhook=''), 'IMAGE_WEBHOOK_TOKEN is not set'),
                                    ('refused', FakeAppGitHub(hooks_status=403), 'Webhooks: Read and write')):
            with self.subTest(name=name):
                client, job = repos.AppRepos(repo_runner(), api.config), self.job()
                image = repos.create_app_repo(client.runner, client, job, repo.Request('notes'), sleep=lambda _: None,
                                              opener=ghcr_opener)
                self.assertTrue(image.startswith('ghcr.io/samclement/notes:1-'))
                warning = next(line for line in job.lines if line.startswith('Warning: no image webhook'))
                self.assertIn(expected, warning)
                self.assertIn('make app-hooks', warning)

    def test_a_permission_refusal_names_the_permission_to_check(self):
        from swhurl.apps import repo
        from swhurl.console import repos
        api = FakeAppGitHub()
        handle = api.handle
        api.handle = None
        def refuse_blobs(request):
            if request.url.path.endswith('/git/blobs'):
                return httpx.Response(403, json={'message': 'Resource not accessible by personal access token'})
            return handle(request)
        config = repos.AppReposToken(api.config.token, transport=httpx.MockTransport(refuse_blobs))
        client = repos.AppRepos(repo_runner(), config)
        with self.assertRaisesRegex(actions.ActionError, 'Contents: Read and write and Workflows: Read and write'):
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None)

    def test_writes_only_to_a_repository_it_created(self):
        from swhurl.console import repos
        api = FakeAppGitHub()
        client = repos.AppRepos(FakeRunner(), api.config)
        with self.assertRaisesRegex(actions.ActionError, 'writes only to a repository it created'):
            client.push_first_commit('hello-ts', Path('.'), 'x', sleep=lambda _: None)
        self.assertEqual(api.requests, [])
        self.assertFalse([name for name in dir(client) if 'delete' in name.lower()], 'no deleting method')

    def test_an_existing_name_or_a_failed_first_run_stops_with_the_reason(self):
        from swhurl.apps import repo
        from swhurl.console import repos
        api = FakeAppGitHub(existing=('notes',))
        client = repos.AppRepos(repo_runner(), api.config)
        with self.assertRaisesRegex(
                actions.ActionError,
                r'Repository samclement/notes already exists on GitHub\. Setup stopped before creating a new '
                r'repository\. Choose another name and retry from New app\.'):
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None)
        self.assertEqual(api.writes(), [])
        self.assertEqual(client.runner.calls, [], 'nothing rendered')
        api = FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'failure'}])
        client = repos.AppRepos(repo_runner(), api.config)
        add = 'run make app-new NAME=notes ARGS="--from-repo samclement/notes --env staging --image <image the run prints>"'
        with self.assertRaises(actions.ActionError) as failed:
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None,
                                  opener=ghcr_opener)
        self.assertIn('ended failure: https://run/1', str(failed.exception))
        self.assertIn(add, str(failed.exception))
        ticks = iter(range(0, 10_000, 100))
        client = repos.AppRepos(repo_runner(), FakeAppGitHub(runs=[None]).config)
        with self.assertRaises(actions.ActionError) as late:
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None,
                                  clock=lambda: next(ticks), opener=ghcr_opener)
        self.assertIn('no finished Container run after 10 minutes', str(late.exception))
        self.assertIn(add, str(late.exception))


class TeamsPageTests(unittest.TestCase):
    """The Teams page: the registry on main beside the apps that run, and changes as pull requests."""

    def client(self, namespaces=None, **kwargs):
        staging = (staging_app('orders'), staging_app('billing'), staging_app('ghost'))
        self.runner = tree_fake(staging=staging)
        labels = namespaces or {'orders-staging': 'payments', 'billing-staging': 'payments', 'ghost-staging': 'retired'}
        self.runner._rules.insert(0, (('kubectl', 'get', 'namespaces'), Result((), 0, json.dumps({'items': [
            {'metadata': {'name': name, 'labels': {'platform.swhurl.com/team': team}}} for name, team in labels.items()]}))))
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
        self.assertIn('Move its 2 app(s) to', page, 'a team with apps is deleted only by moving them')
        self.assertEqual(page.count('action="/teams/remove"'), 2, 'every team can be deleted: none is special')
        self.assertEqual(page.count('name="rename"'), 2, 'and renamed')
        self.assertNotIn('default team', page)
        self.assertEqual(page.count('action="/teams/edit"'), 2, 'one Edit per team: its description and its name are one form')
        self.assertEqual(page.count('<details class="act" name="team-form"'), 7,
                         'new team, and per team edit, channel and delete: one open at a time')
        self.assertIn('href="/teams" class="here"', page)
        self.assertTrue(all(call[0] == 'kubectl' and 'get' in call for call in self.runner.calls), 'the page only reads')

    def test_an_apps_language_comes_from_its_own_repository_and_is_asked_for_once(self):
        asked = []

        def opener(request):
            asked.append(request.full_url)
            if 'ghost' in request.full_url:
                raise OSError('GitHub is away')
            return {}, b'version: 1\nkind: worker\nstack: kotlin\n'
        c = self.client(repo_opener=opener)
        page = c.get('/teams', headers=WHO).text
        self.assertEqual(asked[0], 'https://api.github.com/repos/samclement/billing/contents/swhurl.yaml')
        self.assertEqual(page.count('<span class="pill">Kotlin</span>'), 1, 'billing; orders is not asked for once GitHub fails')
        self.assertEqual(len(asked), 2, 'after one failure the rest are not asked for: the page must not wait once per app')
        c.get('/teams', headers=WHO)
        self.assertEqual(len(asked), 2, 'kept: an app does not change language')

    def test_changes_run_the_clones_team_commands_and_wait_for_review(self):
        cases = (
            ('add', {'name': 'billing', 'description': ' Invoices  and\nrefunds '},
             ('team-add', 'billing', '--description=Invoices and refunds'), '[console] teams: add billing'),
            ('edit', {'name': 'payments', 'description': 'Orders', 'rename': 'orders'},
             ('team-edit', 'payments', '--description=Orders', '--rename=orders'), '[console] teams: change payments'),
            ('edit', {'name': 'payments', 'description': '', 'rename': ''},
             ('team-edit', 'payments', '--description='), '[console] teams: change payments'),
            ('remove', {'name': 'payments', 'move_apps_to': 'platform'},
             ('team-remove', 'payments', '--move-apps-to=platform'), '[console] teams: remove payments'),
        )
        for action, form, command, title in cases:
            with self.subTest(action=action, form=form):
                c = self.client()
                response = c.post(f'/teams/{action}', data=form, headers=WHO, follow_redirects=False)
                self.assertEqual(response.status_code, 303, response.text)
                tool = next(call for call in self.runner.calls if call[1:3] == ('-m', 'swhurl'))
                self.assertEqual(tool[3:], command)
                job = self.jobs.get(1)
                self.assertEqual((job.state, job.link), ('succeeded', 'https://github.com/x/pull/7'))
                self.assertEqual(self.api.sent('POST /pulls')[0]['title'], title)
                self.assertEqual(self.api.sent('POST /issues/7/labels'), [], 'a team change waits for review')
                self.assertEqual(cluster.target(job.unit), (f"team {form['name']}", f"/teams#{form['name']}"))

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
        unreadable = FakeGitHub(fail={'GET /contents/platform/notify/routes.yaml': (404, 'Not Found')})
        page = TestClient(app_under_test(self.runner, jobs=self.jobs, github=unreadable.github)).get('/teams', headers=WHO).text
        self.assertEqual(page.count('No notification channel'), 2, 'unreadable routes: no channel is claimed')

    def test_a_channels_address_reaches_only_the_commands_standard_input(self):
        address = 'https://ntfy.sh/swhurl-0123456789abcdef01234567'
        c = self.client()
        received = []
        self.runner._rules.insert(0, ((sys.executable, '-m', 'swhurl', 'team-channel'), lambda args, text: (
            received.append(text), Result(args, 0, '[OK] channel payments (ntfy) added; its address is not shown again\n'))[1]))
        response = c.post('/team-channel/set', data={'name': 'payments', 'transport': 'ntfy', 'address': f' {address} '},
                          headers=WHO, follow_redirects=False)
        self.assertEqual(response.status_code, 303, response.text)
        self.assertEqual(received, [address + '\n'])
        tool = next(call for call in self.runner.calls if call[1:3] == ('-m', 'swhurl'))
        self.assertEqual(tool[3:], ('team-channel', 'payments', '--transport=ntfy'))
        job = self.jobs.get(1)
        self.assertEqual((job.state, cluster.target(job.unit)), ('succeeded', ('team payments', '/teams#payments')))
        self.assertEqual(self.api.sent('POST /pulls')[0]['title'], '[console] teams: set the notification channel of payments')
        self.assertEqual(self.api.sent('POST /issues/7/labels'), [], 'a channel change waits for review')
        everywhere = '\n'.join([*job.lines, *(' '.join(call) for call in self.runner.calls), *self.runner.echoed,
                                json.dumps(self.api.sent('POST /pulls')), json.dumps(self.api.sent('POST /git/commits')),
                                c.get('/jobs/1', headers=WHO).text, c.get('/teams', headers=WHO).text])
        self.assertNotIn('0123456789abcdef', everywhere)
        self.assertIn('<redacted>', self.runner.redact(f'posting to {address}'))
        c = self.client()
        self.assertEqual(c.post('/team-channel/remove', data={'name': 'payments'}, headers=WHO, follow_redirects=False).status_code, 303)
        tool = next(call for call in self.runner.calls if call[1:3] == ('-m', 'swhurl'))
        self.assertEqual(tool[3:], ('team-channel', 'payments', '--remove'))
        self.assertEqual(self.api.sent('POST /pulls')[0]['title'], '[console] teams: remove the notification channel of payments')

    def test_a_malformed_channel_opens_nothing_and_never_echoes_the_address(self):
        c = self.client()
        cases = (('set', {'name': 'Bad Name', 'address': 'https://ntfy.sh/private-topic'}),
                 ('set', {'name': 'x', 'address': 'http://ntfy.sh/private-topic'}),
                 ('set', {'name': 'x', 'address': 'https://ntfy.sh/private-topic?x=1'}),
                 ('set', {'name': 'x', 'address': ''}), ('set', {'name': 'x', 'transport': 'pigeon', 'address': 'https://ntfy.sh/t'}),
                 ('remove', {'name': '--root=/'}), ('explode', {'name': 'x', 'address': 'https://ntfy.sh/private-topic'}))
        for action, form in cases:
            with self.subTest(action=action, form=form):
                response = c.post(f'/team-channel/{action}', data=form, headers=WHO)
                self.assertEqual(response.status_code, 400)
                self.assertNotIn('private-topic', response.text)
        self.assertFalse(any(call[1:3] == ('-m', 'swhurl') for call in self.runner.calls))
        without_token = TestClient(app_under_test(tree_fake(), jobs=actions.Jobs(tree_fake(), audit=lambda line: None, inline=True)))
        self.assertEqual(without_token.post('/team-channel/set', data={'name': 'x', 'address': 'https://ntfy.sh/t'}, headers=WHO).status_code, 409)

    def test_malformed_input_opens_nothing_and_a_refused_command_shows_its_words(self):
        c = self.client()
        for action, form in (('add', {'name': 'Bad Name'}), ('add', {'name': ''}), ('edit', {'name': 'a', 'rename': '--root=/'}),
                             ('remove', {'name': 'a', 'move_apps_to': 'x y'}), ('add', {'name': 'a', 'description': 'd' * 201}),
                             ('explode', {'name': 'a'})):
            with self.subTest(action=action, form=form):
                self.assertEqual(c.post(f'/teams/{action}', data=form, headers=WHO).status_code, 400)
        self.assertFalse(any(call[1:3] == ('-m', 'swhurl') for call in self.runner.calls))
        message = '[ERROR] team payments still owns billing, orders: move them with --move-apps-to <team>'
        c = self.client()
        self.runner._rules.insert(0, ((sys.executable, '-m', 'swhurl'), Result((), 2, '', message + '\n')))
        c.post('/teams/remove', data={'name': 'payments'}, headers=WHO)
        job = self.jobs.get(1)
        self.assertEqual(job.state, 'failed')
        self.assertIn(message, job.lines)
        self.assertEqual(self.api.sent('POST /pulls'), [])
        without_token = TestClient(app_under_test(tree_fake(), jobs=actions.Jobs(tree_fake(), audit=lambda line: None, inline=True)))
        self.assertEqual(without_token.post('/teams/add', data={'name': 'billing'}, headers=WHO).status_code, 409)
        self.assertIn('cannot open pull requests', without_token.get('/teams', headers=WHO).text)

    def test_an_unreadable_registry_is_said_so_and_raises_no_false_alarm(self):
        self.client()
        unreadable = FakeGitHub(fail={'GET /contents/apps/teams.yaml': (404, 'Not Found')})
        page = TestClient(app_under_test(self.runner, jobs=self.jobs, github=unreadable.github)).get('/teams', headers=WHO).text
        self.assertIn('no app can be called unregistered', page)
        self.assertNotIn('Apps without a registered team', page)
        self.assertIn('<section class="card" id="payments">', page, 'the teams apps name are still shown')
        self.assertIn('<section class="card" id="retired">', page)

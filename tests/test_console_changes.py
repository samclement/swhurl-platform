"""Console changes through GitHub: form to app-new argv, download, commit, branch, PR; offline, with
FakeRunner for the tree's tooling and a fake GitHub API."""
import base64
import io
import json
import sys
import tarfile
import tempfile
import unittest
from dataclasses import replace
from pathlib import Path
from unittest import mock

import httpx
import yaml
from starlette.testclient import TestClient

from swhurl import ROOT
from swhurl.apps import new, ops, promotion
from swhurl.console import actions, changes, server
from swhurl.console import promotion as console_promotion
from swhurl.run import CommandError, FakeRunner, Result

TOKEN = 'github_pat_fixture_0123456789abcdef'
HEAD = 'abc1234' + '0' * 33
WHO = {'X-Auth-Request-Email': 'sam@swhurl.com', 'Origin': 'http://testserver'}
FORM = {'name': 'weather-api', 'env': 'staging', 'image': 'ghcr.io/me/weather:1.0', 'exposure': 'authenticated-web',
        'host': 'weather.homelab.swhurl.com', 'health_path': '/ready', 'kind': '', 'port': ''}
IMAGE = 'ghcr.io/me/hello:2.0@sha256:' + 'a' * 64
REVISION = 'main@sha1:' + HEAD
MAIN = {'README.md': b'# repo\n'}
for env in ('staging', 'prod'):
    for path in (ROOT / 'apps/hello' / env).glob('*.yaml'):
        MAIN[path.relative_to(ROOT).as_posix()] = path.read_bytes()
    rel = f'apps/hello/{env}/helmrelease.yaml'
    doc = yaml.safe_load(MAIN[rel])
    doc['spec']['values']['controllers']['main']['containers']['main']['image'] = new.parse_image(
        IMAGE if env == 'staging' else 'ghcr.io/me/hello:1.0@sha256:' + 'b' * 64)
    MAIN[rel] = yaml.safe_dump(doc, sort_keys=False).encode()
for rel in ('clusters/home/app-hello-staging.yaml', 'clusters/home/app-hello-prod.yaml'):
    MAIN[rel] = (ROOT / rel).read_bytes()
with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    for rel, data in MAIN.items():
        (root / rel).parent.mkdir(parents=True, exist_ok=True)
        (root / rel).write_bytes(data)
    PROMOTION = {'image': IMAGE, 'revision': REVISION,
                 'configuration': promotion.fingerprint(root, 'hello', 'staging'),
                 'target': promotion.fingerprint(root, 'hello', 'prod')}



class FakeGitHub:
    """GitHub's REST API for one repository whose main is ``MAIN``; records every request."""

    def __init__(self, fail: dict[str, tuple[int, str]] | None = None):
        self.requests: list[httpx.Request] = []
        self.fail = fail or {}
        self.github = changes.GitHub('samclement/swhurl-platform', TOKEN, transport=httpx.MockTransport(self.handle))

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
            f'GET /tarball/{HEAD}': self.tarball(),
            f'GET /git/commits/{HEAD}': {'tree': {'sha': 'tree0'}},
            'POST /git/blobs': {'sha': f'blob{len(self.sent("POST /git/blobs")) - 1}'},
            'POST /git/trees': {'sha': 'tree1'},
            'POST /git/commits': {'sha': 'def5678' + '0' * 33},
            'POST /git/refs': {'ref': 'x'},
            'POST /pulls': {'html_url': 'https://github.com/x/pull/7', 'number': 7},
            'POST /issues/7/labels': [{'name': 'auto-merge'}],
            'GET /pulls': [{'number': 7, 'title': '[console] apps: add weather-api staging', 'html_url': 'https://github.com/x/pull/7',
                            'head': {'ref': 'console/new-weather-api-staging-abc1234'}, 'created_at': '2026-10-01T07:00:00Z'},
                           {'number': 8, 'title': 'chore(deps): bump', 'html_url': 'https://github.com/x/pull/8',
                            'head': {'ref': 'renovate/x'}, 'created_at': '2026-10-01T07:00:00Z'}],
        }
        if key not in answers:
            return httpx.Response(404, json={'message': 'Not Found'})
        answer = answers[key]
        return httpx.Response(200, content=answer) if isinstance(answer, bytes) else httpx.Response(201, json=answer)

    def sent(self, key: str) -> list[dict]:
        return [json.loads(r.content) for r in self.requests if f'{r.method} {r.url.path}'.endswith(key.split(' ', 1)[1])
                and r.method == key.split(' ', 1)[0]]


class TreeRunner(FakeRunner):
    """A FakeRunner whose tooling commands edit the downloaded tree they run in, as app-new does."""

    def __init__(self, edit=None, **kwargs):
        super().__init__(**kwargs)
        self.edit = edit if edit is not None else (lambda tree: (tree / 'apps/x').mkdir(parents=True, exist_ok=True)
                                                   or (tree / 'apps/x/helmrelease.yaml').write_text('new\n'))

    def _execute(self, argv, *, input, env, cwd):
        if cwd is not None and argv[:3] == (sys.executable, '-m', 'swhurl'):
            self.edit(Path(cwd))
        return super()._execute(argv, input=input, env=env, cwd=cwd)


APP_NEW_OK = Result((), 0, '[OK] wrote apps/weather-api/staging/helmrelease.yaml\n')


def tree_fake(app_new=APP_NEW_OK, edit=None):
    runner = TreeRunner(edit)
    (runner.on(sys.executable, '-m', 'swhurl', 'app-new', handler=lambda args, _: Result(
        args, app_new.returncode, app_new.stdout, app_new.stderr))
        .on(sys.executable, '-m', 'swhurl', handler=lambda args, _: Result(args, 0, '[OK] edited\n')))
    return runner


def app_under_test(runner, **kwargs):
    """The console app, its template questions read from COPIER_YML: no test here reaches GitHub."""
    kwargs.setdefault('repo_opener', ghcr_opener)
    return server.create_app(runner, **kwargs)


class FormTests(unittest.TestCase):
    def test_form_becomes_flag_equals_value_arguments(self):
        name, env, argv = changes.new_app_args({**FORM, 'cpu': '--no-policy-check'})
        self.assertEqual((name, env), ('weather-api', 'staging'))
        self.assertEqual(argv, ['weather-api', '--env=staging', '--exposure=authenticated-web',
                                '--image=ghcr.io/me/weather:1.0', '--host=weather.homelab.swhurl.com',
                                '--health-path=/ready', '--cpu=--no-policy-check', '--no-otlp'])

    def test_otlp_checkbox_becomes_a_bare_flag(self):
        _, _, ticked = changes.new_app_args({**FORM, 'otlp': 'on'})
        _, _, unticked = changes.new_app_args(FORM)
        self.assertEqual(ticked[-1], '--otlp')
        self.assertEqual(unticked[-1], '--no-otlp', 'explicit, so unticking overrides a preset')
        with self.assertRaisesRegex(actions.ActionError, 'checkbox'):
            changes.new_app_args({**FORM, 'otlp': '--no-policy-check'})

    def test_otlp_hint_states_the_cluster_default(self):
        self.assertIn('OTEL_EXPORTER_OTLP_ENDPOINT=http://$(HOST_IP):4318', changes.OTLP_HINT)
        self.assertIn('OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf', changes.OTLP_HINT)
        self.assertNotIn('otlp', changes.new_app_defaults())

    def test_preset_is_passed_to_app_new(self):
        _, _, argv = changes.new_app_args({'name': 'weather-api', 'env': 'staging', 'preset': 'swhurl-web',
                                           'image': 'ghcr.io/me/weather:42-abc1234', 'otlp': 'on'})
        self.assertEqual(argv, ['weather-api', '--env=staging', '--preset=swhurl-web',
                                '--image=ghcr.io/me/weather:42-abc1234', '--otlp'])
        with self.assertRaisesRegex(actions.ActionError, 'preset must be'):
            changes.new_app_args({**FORM, 'preset': 'nope'})

    def test_preset_defaults_come_from_app_new(self):
        self.assertEqual(changes.new_app_defaults('swhurl-web')['health_path'], '/healthz')
        self.assertEqual(changes.new_app_defaults('swhurl-web')['exposure'], 'authenticated-web')
        self.assertEqual(changes.new_app_checked('swhurl-web'), {'otlp': True})
        self.assertEqual(changes.new_app_checked(''), {'otlp': False})

    def test_expose_form_becomes_app_expose_arguments(self):
        self.assertEqual(changes.expose_args({'exposure': 'public', 'host': 'weather.example.com'}),
                         ['--exposure=public', '--host=weather.example.com'])
        self.assertEqual(changes.expose_args({'exposure': 'private', 'host': ''}), ['--exposure=private'])
        for form, message in (({'exposure': 'open'}, 'exposure must'),
                              ({'exposure': 'public', 'host': '--root=/'}, 'not a DNS name')):
            with self.subTest(form=form), self.assertRaisesRegex(actions.ActionError, message):
                changes.expose_args(form)

    def test_bad_input_is_refused_before_anything_runs(self):
        for form, message in (({**FORM, 'name': 'Weather'}, 'DNS label'), ({**FORM, 'env': 'dev'}, 'env must'),
                              ({**FORM, 'kind': 'cron'}, 'Kind must'), ({**FORM, 'host': 'a\nb'}, 'one line')):
            with self.subTest(message=message), self.assertRaisesRegex(actions.ActionError, message):
                changes.new_app_args(form)

    def test_only_console_branches(self):
        self.assertEqual(changes.branch_name('new-weather-api-staging', 'abc1234'), 'console/new-weather-api-staging-abc1234')
        for slug, rev in (('x', 'main'), ('../main', 'abc1234'), ('X', 'abc1234'), ('x y', 'abc1234')):
            with self.subTest(slug=slug), self.assertRaises(actions.ActionError):
                changes.branch_name(slug, rev)

    def test_token_from_environment(self):
        runner = FakeRunner()
        for value in ('', 'REPLACE_ME', '  '):
            self.assertIsNone(changes.github_from_env(runner, {'GITHUB_TOKEN': value}))
        found = changes.github_from_env(runner, {'GITHUB_TOKEN': TOKEN, 'CONSOLE_REPO': 'me/repo'})
        self.assertEqual(found, changes.GitHub('me/repo', TOKEN))
        self.assertEqual(runner.redact(TOKEN), '<redacted>')


class DefaultsTests(unittest.TestCase):
    def test_defaults_come_from_app_new(self):
        parser = new.parser()
        defaults = changes.new_app_defaults()
        self.assertEqual(defaults['port'], '8080')
        self.assertEqual(defaults['uid'], '65532')
        for field, value in defaults.items():
            self.assertEqual(value, str(parser.get_default(field)), field)
        self.assertNotIn('image', defaults)
        self.assertNotIn('host', defaults)


class OpenPrTests(unittest.TestCase):
    def open(self, runner, api=None, change=None):
        api = api or FakeGitHub()
        job = actions.Job(1, 'new app', 'weather-api/staging', 'sam@swhurl.com', None)
        url = changes.open_pr(runner, api.github, job, slug='new-weather-api-staging', title='apps: add weather-api staging',
                              body='body', change=change or (lambda tree: changes.run_app_new(runner, job, tree, ['x'])))
        return url, job, api

    def test_auto_merge_labels_the_pr_unless_it_adds_a_secret(self):
        api = FakeGitHub()
        job = actions.Job(1, 'new app', 'weather-api/staging', 'sam@swhurl.com', None)
        runner = tree_fake()
        changes.open_pr(runner, api.github, job, slug='new-weather-api-staging', title='t', body='b', auto_merge=True,
                        change=lambda tree: changes.run_app_new(runner, job, tree, ['x']))
        self.assertEqual(api.sent('POST /issues/7/labels'), [{'labels': ['auto-merge']}])
        self.assertIn('Labelled auto-merge: it merges itself when Validate passes', job.lines)

        def with_secret(tree):
            (tree / 'apps/x').mkdir(parents=True)
            (tree / 'apps/x/secret.sops.yaml').write_text('sops: {}\n')
        api, job, runner = FakeGitHub(), actions.Job(1, 'new app', 'x/staging', 'sam@swhurl.com', None), tree_fake(edit=with_secret)
        changes.open_pr(runner, api.github, job, slug='new-x-staging', title='t', body='b', auto_merge=True,
                        change=lambda tree: changes.run_app_new(runner, job, tree, ['x']))
        self.assertEqual(api.sent('POST /issues/7/labels'), [])
        self.assertNotIn('Merges itself', api.sent('POST /pulls')[0]['body'])
        self.assertIn('Not merging automatically: set the Secret values on the branch first, then merge it yourself', job.lines)

        api = FakeGitHub(fail={'POST /issues/7/labels': (403, 'Resource not accessible')})
        job, runner = actions.Job(1, 'new app', 'x/staging', 'sam@swhurl.com', None), tree_fake()
        url = changes.open_pr(runner, api.github, job, slug='new-x-staging', title='t', body='b', auto_merge=True,
                              change=lambda tree: changes.run_app_new(runner, job, tree, ['x']))
        self.assertEqual(url, 'https://github.com/x/pull/7', 'a failed label leaves the PR for a person')
        self.assertTrue(any(line.startswith('Could not label it auto-merge') for line in job.lines))

    def test_commits_exactly_the_changed_files_on_a_console_branch(self):
        def edit(tree):
            (tree / 'apps/hello/staging/helmrelease.yaml').write_text('kind: HelmRelease\nchanged: true\n')
            (tree / 'README.md').unlink()
            (tree / 'apps/x').mkdir(parents=True)
            (tree / 'apps/x/run.sh').write_text('#!/bin/sh\n')
            (tree / 'apps/x/run.sh').chmod(0o755)
        url, job, api = self.open(tree_fake(edit=edit))
        self.assertEqual(url, 'https://github.com/x/pull/7')
        blobs = [base64.b64decode(b['content']) for b in api.sent('POST /git/blobs')]
        self.assertEqual(blobs, [b'kind: HelmRelease\nchanged: true\n', b'#!/bin/sh\n'])
        (tree,) = api.sent('POST /git/trees')
        self.assertEqual(tree, {'base_tree': 'tree0', 'tree': [
            {'path': 'README.md', 'mode': '100644', 'type': 'blob', 'sha': None},
            {'path': 'apps/hello/staging/helmrelease.yaml', 'mode': '100644', 'type': 'blob', 'sha': 'blob0'},
            {'path': 'apps/x/run.sh', 'mode': '100755', 'type': 'blob', 'sha': 'blob1'}]})
        (commit,) = api.sent('POST /git/commits')
        self.assertEqual((commit['parents'], commit['tree'], commit['author']['name']), ([HEAD], 'tree1', 'swhurl console'))
        self.assertTrue(commit['message'].startswith('[console] apps: add weather-api staging'))
        self.assertIn('Requested-by: sam@swhurl.com', commit['message'])
        self.assertEqual(api.sent('POST /git/refs'), [{'ref': 'refs/heads/console/new-weather-api-staging-abc1234',
                                                       'sha': 'def5678' + '0' * 33}])
        (pr,) = api.sent('POST /pulls')
        self.assertEqual((pr['head'], pr['base'], pr['title']),
                         ('console/new-weather-api-staging-abc1234', 'main', '[console] apps: add weather-api staging'))
        self.assertEqual(job.lines[2:6], ['D README.md', 'M apps/hello/staging/helmrelease.yaml', 'A apps/x/run.sh',
                                          'Committed def5678 [console] apps: add weather-api staging'])
        self.assertIn('Opened https://github.com/x/pull/7', job.lines)

    def test_token_only_in_the_authorization_header_and_never_sent_off_github(self):
        runner = tree_fake()
        _, _, api = self.open(runner)
        for request in api.requests:
            self.assertEqual(request.url.host, 'api.github.com')
            self.assertEqual(request.headers['authorization'], f'Bearer {TOKEN}')
            self.assertNotIn(TOKEN, str(request.url) + request.content.decode(errors='replace'))
        for call in runner.calls:
            self.assertFalse(any(TOKEN in arg for arg in call), call)

    def test_no_change_opens_nothing_and_the_tree_is_removed(self):
        seen = []
        api = FakeGitHub()
        with self.assertRaisesRegex(actions.ActionError, 'no files changed'):
            self.open(tree_fake(), api, change=seen.append)
        self.assertFalse(seen[0].parent.exists())
        self.assertEqual([r.method for r in api.requests], ['GET', 'GET'])

    def test_refused_or_unvalidated_app_new_writes_nothing_to_github(self):
        failures = [(2, '[ERROR] production instances must pin an image digest'),
                    (1, '[ERROR] could not validate the app policy (helm unavailable)')]
        for code, message in failures:
            with self.subTest(code=code):
                runner = tree_fake(app_new=Result((), code, '', message + '\n'))
                api = FakeGitHub()
                with self.assertRaisesRegex(actions.ActionError, 'refused'):
                    self.open(runner, api)
                self.assertEqual({r.method for r in api.requests}, {'GET'})

    def test_app_new_runs_the_trees_tooling(self):
        runner = tree_fake()
        self.open(runner)
        (call,) = [c for c in runner.calls if 'app-new' in c]
        self.assertEqual(call, (sys.executable, '-m', 'swhurl', 'app-new', 'x'))

    def test_dry_run_lists_the_change_and_writes_nothing(self):
        runner = tree_fake()
        runner.dry_run = True
        url, job, api = self.open(runner)
        self.assertEqual(url, '')
        self.assertEqual({r.method for r in api.requests}, {'GET'})
        self.assertIn('Dry run: would commit 1 file(s) to console/new-weather-api-staging-abc1234; no PR opened', job.lines)

    def test_github_error_is_a_redacted_action_error(self):
        api = FakeGitHub(fail={'POST /git/refs': (422, f'Reference already exists (token {TOKEN})')})
        with self.assertRaises(actions.ActionError) as caught:
            self.open(tree_fake(), api)
        self.assertIn('POST /git/refs returned 422: Reference already exists', str(caught.exception))
        self.assertNotIn(TOKEN, str(caught.exception))
        self.assertEqual(api.sent('POST /pulls'), [])


class NewAppRouteTests(unittest.TestCase):
    def client(self, runner, github=True):
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        self.api = FakeGitHub()
        github = self.api.github if github else None
        return TestClient(app_under_test(runner, jobs=jobs, github=github)), jobs

    def test_form_opens_a_pr_as_a_job(self):
        runner = tree_fake()
        c, jobs = self.client(runner)
        preset = c.get('/new?preset=swhurl-web', headers=WHO).text
        self.assertIn('<a href="/new?preset=swhurl-web" class="here" aria-current="page">\n    <strong>Deploy an existing image</strong>', preset)
        self.assertIn('<a href="/new?preset=swhurl-web" class="here" aria-current="page">Web app, platform conventions</a>', preset)
        self.assertIn('choose <strong>Custom</strong>', preset, 'says when the conventions do not fit')
        self.assertIn('<input type="hidden" name="preset" value="swhurl-web">', preset)
        self.assertIn('name="exposure" value="authenticated-web" checked>', preset)
        self.assertIn('<details class="advanced">', preset)
        self.assertIn('<input type="checkbox" id="otlp" name="otlp" checked>', preset)
        form = c.get('/new?preset=', headers=WHO).text
        self.assertIn('Open pull request', form)
        self.assertIn('<details class="advanced" open>', form, 'no preset fills Advanced, so it starts open')
        self.assertIn('id="port" name="port" value="" placeholder="8080"', form)
        self.assertIn('id="uid" name="uid" value="" placeholder="65532"', form)
        self.assertIn('<option value="">web (default)</option>', form)
        self.assertIn('name="exposure" value="private" checked>', form)
        self.assertNotIn('clones', form + preset, 'the intro describes the API-based PR')
        self.assertIn('nginx-unprivileged is 101', form)
        self.assertIn('<input type="checkbox" id="otlp" name="otlp" >', form)
        self.assertIn('OTEL_EXPORTER_OTLP_ENDPOINT=http://$(HOST_IP):4318', form)
        response = c.post('/new', data=FORM, headers=WHO, follow_redirects=False)
        self.assertEqual((response.status_code, response.headers['location']), (303, '/jobs/1'))
        job = jobs.get(1)
        self.assertEqual((job.state, job.link, job.unit), ('succeeded', 'https://github.com/x/pull/7', 'weather-api/staging'))
        self.assertEqual(self.api.sent('POST /issues/7/labels'), [{'labels': ['auto-merge']}], 'a new staging app')
        rejected = c.post('/new', data={**FORM, 'env': 'prod', 'image': 'ghcr.io/me/weather:1.0@sha256:' + 'a' * 64}, headers=WHO)
        self.assertEqual(rejected.status_code, 400)
        self.assertIsNone(jobs.get(2))
        self.assertEqual(len(self.api.sent('POST /pulls')), 1, 'forged production submissions open no PR')
        self.assertIn('https://github.com/x/pull/7', c.get('/jobs/1', headers=WHO).text)

    def test_every_field_is_on_the_form_once_and_a_bad_form_keeps_advanced_open(self):
        c, _ = self.client(tree_fake())
        for preset in ('swhurl-web', 'swhurl-worker', ''):
            page = c.get(f'/new?preset={preset}', headers=WHO).text
            for field, _, _ in changes.NEW_APP_FIELDS:
                with self.subTest(preset=preset, field=field):
                    self.assertEqual(page.count(f'name="{field}"'), 3 if field == 'exposure' else 1)
        page = c.post('/new', data={**FORM, 'name': 'Bad', 'port': '9090', 'preset': 'swhurl-web'}, headers=WHO).text
        self.assertIn('<details class="advanced" open>', page)
        self.assertIn('value="9090"', page)

    def test_pr_description_explains_setting_secret_values(self):
        body = changes.new_app_body('weather-api', 'staging', ['weather-api', '--env=staging', '--secret-keys=API_TOKEN,DB_URL'])
        self.assertIn('Before merging, set the secret values** (API_TOKEN, DB_URL)', body)
        self.assertIn('    sops apps/weather-api/staging/secret.sops.yaml', body)
        self.assertNotIn('sops', changes.new_app_body('weather-api', 'staging', ['weather-api', '--env=staging']))

    def test_open_console_prs_are_listed_and_others_ignored(self):
        c, _ = self.client(tree_fake())
        text = c.get('/activity', headers=WHO).text
        self.assertIn('#7 [console] apps: add weather-api staging', text)
        self.assertNotIn('pull/8', text)
        self.assertEqual(changes.console_prs(FakeRunner(), self.api.github)[0].opened, '2026-10-01')
        c, _ = self.client(tree_fake(), github=False)
        self.assertIn('No GitHub token is configured, so open pull requests are not listed', c.get('/activity', headers=WHO).text)
        api = FakeGitHub(fail={'GET /pulls': (401, 'Bad credentials')})
        page = TestClient(app_under_test(tree_fake(), jobs=actions.Jobs(tree_fake(), inline=True), github=api.github))
        self.assertIn('Could not list open pull requests', page.get('/activity', headers=WHO).text)

    def test_an_image_can_take_its_settings_from_the_repository(self):
        c, jobs = self.client(tree_fake())
        page = c.get('/new?preset=from-repo', headers=WHO).text
        self.assertIn('class="here" aria-current="page">From the repository&#39;s swhurl.yaml</a>', page)
        self.assertIn('<input type="hidden" name="preset" value="from-repo">', page)
        self.assertIn('id="repo" name="repo" required', page)
        self.assertIn('empty fields come from swhurl.yaml', page)
        self.assertIn('name="exposure" value="" checked>', page, "exposure defaults to the file's, not private")
        self.assertIn('id="port" name="port" value="" placeholder="from swhurl.yaml">', page, 'no preset default is shown')
        form = {**FORM, 'preset': 'from-repo', 'repo': 'samclement/hello-ts@v1', 'exposure': '', 'host': '', 'health_path': ''}
        _, _, argv = changes.new_app_args(form)
        self.assertEqual(argv[:3], ['weather-api', '--env=staging', '--from-repo=samclement/hello-ts@v1'])
        self.assertNotIn('--preset', ' '.join(argv))
        self.assertNotIn('--no-otlp', argv, "unticked keeps swhurl.yaml's telemetry")
        self.assertIn('--otlp', changes.new_app_args({**form, 'otlp': 'on'})[2])
        for bad in ('', 'hello-ts', '--all/x', 'a/b c'):
            with self.subTest(bad), self.assertRaisesRegex(actions.ActionError, 'repository must be OWNER/REPO'):
                changes.new_app_args({**form, 'repo': bad})
        response = c.post('/new', data={**form, 'repo': 'nope'}, headers=WHO)
        self.assertEqual(response.status_code, 400)
        self.assertIn('id="repo" name="repo" required pattern=', response.text, 'the returned form keeps its tab')

    def test_reserved_secret_keys_are_refused_on_the_form(self):
        with self.assertRaisesRegex(actions.ActionError, 'reserved for the platform: OTEL_SERVICE_NAME'):
            changes.new_app_args({**FORM, 'secret_keys': 'API_TOKEN, OTEL_SERVICE_NAME'})
        response = self.client(tree_fake())[0].post('/new', data={**FORM, 'secret_keys': 'DATABASE_PATH'}, headers=WHO)
        self.assertEqual(response.status_code, 400)
        self.assertIn('reserved for the platform: DATABASE_PATH', response.text)
        self.assertEqual(self.api.requests, [], 'nothing reaches GitHub')

    def test_a_new_app_not_yet_applied_waits_for_its_pr(self):
        runner = tree_fake().on('kubectl', '-n', 'flux-system', 'get', 'kustomization', stdout='')
        c, _ = self.client(runner)
        response = c.get('/apps/weather-api/staging', headers=WHO)
        self.assertEqual(response.status_code, 200, 'the open PR on its console/new-weather-api-staging-* branch')
        self.assertIn('Not created yet', response.text)
        self.assertIn('href="https://github.com/x/pull/7"', response.text)
        self.assertEqual(c.get('/apps/weather/staging', headers=WHO).status_code, 404, 'another app\'s PR does not count')
        c, _ = self.client(runner, github=False)
        self.assertEqual(c.get('/apps/weather-api/staging', headers=WHO).status_code, 404, 'neither a PR nor a job')
        pr = changes.PullRequest(1, 't', 'u', 'console/new-a-staging-staging-abc1234', '2026-10-02')
        self.assertTrue(changes.creates_instance(pr, 'a-staging', 'staging'))
        self.assertFalse(changes.creates_instance(pr, 'a', 'staging'))

    def test_a_new_app_job_without_a_token_still_shows_on_its_page(self):
        runner = tree_fake().on('kubectl', '-n', 'flux-system', 'get', 'kustomization', stdout='')
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        jobs.submit('new app', 'weather-api/staging', 'sam@swhurl.com', lambda job: None)
        text = TestClient(app_under_test(runner, jobs=jobs)).get('/apps/weather-api/staging', headers=WHO).text
        self.assertIn('Not created yet', text)
        self.assertIn('href="/jobs/1"', text)

    def test_invalid_form_or_missing_token_runs_nothing(self):
        runner = tree_fake()
        c, _ = self.client(runner)
        response = c.post('/new', data={**FORM, 'name': 'Bad'}, headers=WHO)
        self.assertEqual(response.status_code, 400)
        self.assertIn('value="Bad"', response.text)
        c, _ = self.client(runner, github=False)
        self.assertIn('No GitHub token', c.get('/new?preset=swhurl-web', headers=WHO).text)
        self.assertEqual(c.post('/new', data=FORM, headers=WHO).status_code, 409)
        self.assertEqual(runner.calls, [])

    def test_cross_site_post_is_refused(self):
        runner = tree_fake()
        c, _ = self.client(runner)
        self.assertEqual(c.post('/new', data=FORM, headers={**WHO, 'Origin': 'https://evil.example'}).status_code, 403)
        self.assertEqual(runner.calls, [])



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
        'deploy,statefulset,daemonset': {'items': [{'kind': 'Deployment', 'metadata': {'name': 'hello'},
                                                  'spec': {'replicas': 1}, 'status': {'readyReplicas': 1}}]},
        'ingress': {'items': []}, 'certificate': {'items': []},
    }
    for kind, obj in objects.items():
        ns = 'flux-system' if kind in ('kustomization', 'gitrepository') else 'hello-staging'
        runner.on('kubectl', '-n', ns, 'get', kind, stdout=json.dumps(obj))
    return runner


class ChangeAppRouteTests(unittest.TestCase):
    def client(self, runner, github=True):
        staging_reads(runner)
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        self.api = FakeGitHub()
        github = self.api.github if github else None
        return TestClient(app_under_test(runner, jobs=jobs, github=github)), jobs

    def tool_call(self, runner):
        return next(c for c in runner.calls if c[:3] == (sys.executable, '-m', 'swhurl'))

    def test_scale_promote_and_remove_run_the_clones_commands(self):
        cases = (
            ('/apps/hello/prod/scale', {'replicas': '2', 'memory_limit': '256Mi', 'cpu': ''},
             ('app-scale', 'hello', 'prod', '--replicas=2', '--memory-limit=256Mi'), 'hello/prod',
             'console/scale-hello-prod-abc1234', '[console] apps: scale hello/prod'),
            ('/apps/hello/staging/promote', PROMOTION, ('app-promote', 'hello', f'--expect-image={IMAGE}', f'--expect-config={PROMOTION["configuration"]}'), 'hello/prod',
             'console/promote-hello-staging-abc1234', '[console] apps: promote hello/staging to hello/prod'),
            ('/apps/hello/staging/remove', {}, ('app-remove', 'hello', 'staging'), 'hello/staging',
             'console/remove-hello-staging-abc1234', '[console] apps: remove hello/staging'),
        )
        for path, form, command, target, branch, title in cases:
            with self.subTest(path=path):
                runner = tree_fake()
                c, jobs = self.client(runner)
                response = c.post(path, data=form, headers=WHO, follow_redirects=False)
                self.assertEqual(response.status_code, 303, response.text)
                self.assertEqual(self.tool_call(runner)[3:], command)
                job = jobs.get(1)
                self.assertEqual((job.state, job.unit, job.link), ('succeeded', target, 'https://github.com/x/pull/7'))
                (payload,) = self.api.sent('POST /pulls')
                self.assertEqual((payload['head'], payload['title']), (branch, title))

    def test_scale_requires_review_and_promotion_auto_merge_is_default(self):
        cases = (('/apps/hello/prod/scale', {'replicas': '2'}, False),
                 ('/apps/hello/prod/scale', {'replicas': '0', 'auto_merge': 'on'}, False),
                 ('/apps/hello/staging/promote', PROMOTION, True),
                 ('/apps/hello/staging/promote', {**PROMOTION, 'hold': 'on'}, False),
                 ('/apps/hello/staging/expose', {'exposure': 'public', 'host': 'hello.example.com'}, False),
                 ('/apps/hello/staging/remove', {'auto_merge': 'on'}, False))
        for path, form, merges in cases:
            with self.subTest(path=path, form=form):
                c, jobs = self.client(tree_fake())
                c.post(path, data=form, headers=WHO)
                self.assertEqual(jobs.get(1).state, 'succeeded')
                self.assertEqual(self.api.sent('POST /issues/7/labels'), [{'labels': ['auto-merge']}] if merges else [])
                self.assertEqual('Merges itself when Validate passes' in self.api.sent('POST /pulls')[0]['body'], merges)

    def test_refusals_run_nothing(self):
        runner = tree_fake()
        c, _ = self.client(runner)
        for path, form, code in (('/apps/hello/prod/promote', {}, 400), ('/apps/hello/prod/scale', {}, 400),
                                 ('/apps/hello/prod/scale', {'replicas': '2; rm'}, 400), ('/apps/hello/prod/delete', {}, 404),
                                 ('/apps/Hello/prod/remove', {}, 404), ('/apps/hello/dev/remove', {}, 404)):
            with self.subTest(path=path, form=form):
                self.assertEqual(c.post(path, data=form, headers=WHO).status_code, code)
        c, _ = self.client(runner, github=False)
        self.assertEqual(c.post('/apps/hello/prod/remove', headers=WHO).status_code, 409)
        self.assertEqual(runner.calls, [])

    def test_promotion_refuses_missing_or_stale_review_and_unhealthy_staging(self):
        good = ops.gather_status(staging_reads(FakeRunner()), ops.Instance('hello', 'staging'))
        cases = (({}, good, 'review the image'),
                 ({**PROMOTION, 'image': IMAGE.replace('2.0', '1.0')}, good, 'changed'),
                 ({**PROMOTION, 'revision': 'old'}, good, 'changed'),
                 (PROMOTION, replace(good, release=('False', 'upgrade failed')), 'healthy'),
                 (PROMOTION, replace(good, running_images=['x@sha256:old']), 'healthy'),
                 (PROMOTION, replace(good, unit_suspended=True), 'healthy'),
                 (PROMOTION, replace(good, release_suspended=True), 'healthy'),
                 (PROMOTION, replace(good, release=None), 'healthy'))
        for form, status, message in cases:
            with self.subTest(form=form, status=status):
                runner = tree_fake()
                c, jobs = self.client(runner)
                with mock.patch.object(ops, 'gather_status', return_value=status):
                    response = c.post('/apps/hello/staging/promote', data=form, headers=WHO)
                self.assertEqual(response.status_code, 400)
                self.assertIn(message, response.text)
                self.assertEqual(jobs.recent(), [])
                self.assertEqual(self.api.sent('POST /pulls'), [])

    def test_promotion_rechecks_staging_when_the_background_job_starts(self):
        good = ops.gather_status(staging_reads(FakeRunner()), ops.Instance('hello', 'staging'))
        c, jobs = self.client(tree_fake())
        with mock.patch.object(ops, 'gather_status', side_effect=[good, replace(good, desired_image='new')]):
            response = c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO, follow_redirects=False)
        self.assertEqual(response.status_code, 303)
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertIn('changed', jobs.get(1).lines[-1])
        self.assertEqual(self.api.sent('POST /pulls'), [])

    def test_promotion_read_failure_is_a_502_and_opens_nothing(self):
        c, jobs = self.client(tree_fake())
        with mock.patch.object(ops, 'gather_status', side_effect=CommandError(['kubectl'], 'Forbidden')):
            response = c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
        self.assertEqual(response.status_code, 502)
        self.assertIn('Forbidden', response.text)
        self.assertEqual(jobs.recent(), [])
        self.assertEqual(self.api.sent('POST /pulls'), [])

    def test_review_page_submits_the_displayed_image_revision_and_configuration(self):
        good = ops.gather_status(staging_reads(FakeRunner()), ops.Instance('hello', 'staging'))
        c, _ = self.client(tree_fake())
        with mock.patch.object(ops, 'gather_status', return_value=good):
            response = c.get('/apps/hello/staging/promotion', headers=WHO)
        self.assertEqual(response.status_code, 200, response.text)
        for name, value in PROMOTION.items():
            self.assertIn(f'name="{name}" value="{value}"', response.text)
        self.assertIn('Hold for manual review', response.text)
        with mock.patch.object(ops, 'gather_status', return_value=replace(good, release=None)):
            response = c.get('/apps/hello/staging/promotion', headers=WHO)
        self.assertEqual(response.status_code, 409)
        self.assertNotIn('action="/apps/hello/staging/promote"', response.text)

    def test_duplicate_click_and_restart_recover_the_existing_pr(self):
        pr = changes.PullRequest(9, 'promote hello', 'https://github.com/x/pull/9',
                                 'console/promote-hello-staging-abcdef0', '2026-10-03',
                                 '<!-- swhurl-promotion ' + json.dumps({'image': IMAGE}) + ' -->')
        runner = tree_fake()
        c, jobs = self.client(runner)
        with mock.patch.object(changes, 'console_prs', return_value=[pr]):
            for _ in range(2):
                self.assertEqual(c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO).status_code, 200)
            self.assertEqual(jobs.get(2).link, pr.url)
            fresh, fresh_jobs = self.client(tree_fake())
            response = fresh.get('/apps/hello/staging/promotion', headers=WHO)
            self.assertIn(pr.url, response.text)
            self.assertIn('Hold PR', response.text)
            self.assertEqual(fresh_jobs.recent(), [])
        self.assertFalse(any(c[:3] == (sys.executable, '-m', 'swhurl') for c in runner.calls))
        self.assertEqual(self.api.sent('POST /pulls'), [])

    def test_existing_pr_requires_explicit_replacement_for_another_image(self):
        pr = changes.PullRequest(9, 'promote hello', 'https://github.com/x/pull/9',
                                 'console/promote-hello-staging-abcdef0', '2026-10-03',
                                 '<!-- swhurl-promotion ' + json.dumps({'image': 'older'}) + ' -->')
        c, jobs = self.client(tree_fake())
        with mock.patch.object(changes, 'console_prs', return_value=[pr]):
            response = c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
        self.assertEqual(response.status_code, 400)
        self.assertIn('Close it explicitly', response.text)
        self.assertEqual(jobs.recent(), [])

    def test_configuration_or_destination_movement_opens_nothing(self):
        for field in ('configuration', 'target'):
            c, jobs = self.client(tree_fake())
            response = c.post('/apps/hello/staging/promote', data={**PROMOTION, field: 'stale'}, headers=WHO)
            self.assertEqual(response.status_code, 200)  # redirect to the failed job page
            self.assertEqual(jobs.get(1).state, 'failed')
            self.assertIn('changed since review', jobs.get(1).lines[-1])
            self.assertEqual(self.api.sent('POST /pulls'), [])

    def test_staging_movement_after_generation_still_opens_nothing(self):
        good = ops.gather_status(staging_reads(FakeRunner()), ops.Instance('hello', 'staging'))
        c, jobs = self.client(tree_fake())
        with mock.patch.object(ops, 'gather_status', side_effect=[good, good, replace(good, desired_image='new')]):
            c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertIn('changed', jobs.get(1).lines[-1])
        self.assertEqual(self.api.sent('POST /git/refs'), [])


class PromotionControlTests(unittest.TestCase):
    def client(self, setup=False, labels=('auto-merge',)):
        requests = []
        pr = {'state': 'open', 'base': {'ref': 'main'},
              'head': {'ref': 'console/promote-hello-staging-abcdef0',
                       'repo': {'full_name': 'samclement/swhurl-platform'}},
              'labels': [{'name': label} for label in labels],
              'body': '<!-- swhurl-promotion ' + json.dumps({'app': 'hello', 'image': IMAGE, 'setup': setup}) + ' -->'}

        def reply(request):
            requests.append(request)
            return httpx.Response(200, json=pr if request.method == 'GET' else {})
        github = changes.GitHub('samclement/swhurl-platform', TOKEN, transport=httpx.MockTransport(reply))
        return FakeRunner(), github, requests

    def test_hold_removes_only_the_auto_merge_label_and_resume_requests_rechecks(self):
        runner, github, requests = self.client()
        console_promotion.control(runner, github, 7, 'hold')
        self.assertEqual([(r.method, r.url.path.split('/issues/')[-1]) for r in requests if r.method != 'GET'],
                         [('DELETE', '7/labels/auto-merge')])
        runner, github, requests = self.client(labels=())
        console_promotion.control(runner, github, 7, 'resume')
        self.assertEqual(json.loads(requests[-1].content), {'labels': ['auto-merge']})

    def test_setup_and_failed_merge_checks_cannot_be_resumed(self):
        for setup, labels in [(True, ()), (False, ('promotion-review-required',))]:
            runner, github, requests = self.client(setup=setup, labels=labels)
            with self.assertRaises(actions.ActionError):
                console_promotion.control(runner, github, 7, 'resume')
            self.assertTrue(all(r.method == 'GET' for r in requests))


if __name__ == '__main__':
    unittest.main()


APP_COMMIT = '9f8e7d6' + '1' * 33
DIGEST = 'sha256:' + 'd' * 64


class FakeAppGitHub:
    """GitHub's API as the second token sees it: notes does not exist until created; its first run succeeds."""

    def __init__(self, existing=('hello-ts',), runs=None):
        self.requests: list[httpx.Request] = []
        self.existing = set(existing)
        self.runs = list(runs if runs is not None else [None, {'status': 'in_progress'}, {'status': 'completed',
                                                                                        'conclusion': 'success'}])
        from swhurl.console import repos
        self.config = repos.AppReposToken('github_pat_app_repos_fixture_0123', transport=httpx.MockTransport(self.handle))

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
"""


def ghcr_opener(request):
    if request.full_url.endswith('/contents/copier.yml'):
        return {}, COPIER_YML.encode()
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
                            ('PATCH', '/repos/samclement/notes/git/refs/heads/main')])
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
        with self.assertRaisesRegex(actions.ActionError, 'already exists on GitHub'):
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None)
        self.assertEqual(api.writes(), [])
        self.assertEqual(client.runner.calls, [], 'nothing rendered')
        api = FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'failure'}])
        client = repos.AppRepos(repo_runner(), api.config)
        with self.assertRaisesRegex(actions.ActionError, 'ended failure: https://run/1'):
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None)
        ticks = iter(range(0, 10_000, 100))
        client = repos.AppRepos(repo_runner(), FakeAppGitHub(runs=[None]).config)
        with self.assertRaisesRegex(actions.ActionError, 'no finished Container run after 10 minutes'):
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None,
                                  clock=lambda: next(ticks))


class NewRepoRouteTests(unittest.TestCase):
    def test_the_form_creates_the_repository_then_opens_the_platform_pr(self):
        runner = repo_runner(tree_fake())
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        platform_api, app_api = FakeGitHub(), FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'success'}])
        c = TestClient(app_under_test(runner, jobs=jobs, github=platform_api.github, app_repos=app_api.config))
        page = c.get('/new', headers=WHO).text
        self.assertIn('<a href="/new" class="here" aria-current="page">\n    <strong>Start a new app</strong>', page)
        self.assertIn('<a href="/new?preset=swhurl-web">\n    <strong>Deploy an existing image</strong>', page)
        self.assertIn('action="/new/repo"', page)
        self.assertIn('name="stack" value="typescript" checked', page)
        self.assertIn('name="feature-kind" value="web" checked', page)
        self.assertIn('name="feature-database" value="sqlite" >', page)
        self.assertIn('none or sqlite', page, "the template's own help text")
        response = c.post('/new/repo', data={'name': 'notes', 'stack': 'typescript', 'description': 'Take notes',
                                             'exposure': 'authenticated-web', 'host': ''}, headers=WHO,
                          follow_redirects=False)
        self.assertEqual((response.status_code, response.headers['location']), (303, '/jobs/1'))
        job = jobs.get(1)
        self.assertEqual((job.state, job.link, job.unit), ('succeeded', 'https://github.com/x/pull/7', 'notes/staging'),
                         '\n'.join(job.lines))
        app_new = next(c for c in runner.calls if c[3:4] == ('app-new',))
        self.assertEqual(app_new[4:], ('notes', '--from-repo=samclement/notes', '--env=staging',
                                       f'--image=ghcr.io/samclement/notes:1-9f8e7d6@{DIGEST}',
                                       '--exposure=authenticated-web'))
        copy = next(c for c in runner.calls if 'copy' in c)
        self.assertIn('kind=web', copy)
        self.assertIn('database=none', copy)
        body = platform_api.sent('POST /pulls')[0]['body']
        self.assertIn('created https://github.com/samclement/notes from', body)

    def test_a_worker_with_sqlite_gets_no_route_and_its_answers(self):
        runner = repo_runner(tree_fake())
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        c = TestClient(app_under_test(runner, jobs=jobs, github=FakeGitHub().github, app_repos=FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'success'}]).config))
        c.post('/new/repo', data={'name': 'notes', 'stack': 'typescript', 'feature-kind': 'worker',
                                  'feature-database': 'sqlite', 'exposure': 'authenticated-web', 'host': ''}, headers=WHO)
        self.assertEqual(jobs.get(1).state, 'succeeded', '\n'.join(jobs.get(1).lines))
        app_new = next(c for c in runner.calls if c[3:4] == ('app-new',))
        self.assertFalse([a for a in app_new if a.startswith(('--exposure', '--host'))], 'swhurl.yaml makes a worker private')
        copy = next(c for c in runner.calls if 'copy' in c)
        self.assertIn('kind=worker', copy)
        self.assertIn('database=sqlite', copy)
        self.assertIn('Rendering samclement/swhurl-app-template-typescript (typescript) for notes with database=sqlite, kind=worker',
                      jobs.get(1).lines)
        for form, message in (({'feature-kind': 'cron'}, 'kind must be one of web, worker'),
                              ({'feature-kind': 'worker', 'host': 'notes.homelab.swhurl.com'}, 'a worker has no web address')):
            with self.subTest(message=message):
                response = c.post('/new/repo', data={'name': 'notes2', 'stack': 'typescript', **form}, headers=WHO)
                self.assertEqual(response.status_code, 400)
                self.assertIn(message, response.text)

    def test_kotlin_is_a_stack_with_its_own_template(self):
        runner = repo_runner(tree_fake())
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        c = TestClient(app_under_test(runner, jobs=jobs, github=FakeGitHub().github,
                                      app_repos=FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'success'}]).config))
        page = c.get('/new', headers=WHO).text
        self.assertIn('name="stack" value="kotlin"', page)
        self.assertIn('Kotlin on Micronaut', page)
        self.assertEqual(page.count('<fieldset class="features"'), 2, 'one Features set per stack')
        c.post('/new/repo', data={'name': 'notes', 'stack': 'kotlin', 'feature-kind': 'web', 'feature-database': 'sqlite'},
               headers=WHO)
        self.assertEqual(jobs.get(1).state, 'succeeded', '\n'.join(jobs.get(1).lines))
        copy = next(c for c in runner.calls if 'copy' in c)
        self.assertIn('https://github.com/samclement/swhurl-app-template-kotlin.git', copy)

    def test_template_questions_are_read_on_first_use_whatever_the_clock(self):
        from unittest import mock
        with mock.patch('swhurl.console.server.time.monotonic', return_value=5.0):  # a host booted 5 s ago
            page = TestClient(app_under_test(repo_runner(tree_fake()), github=FakeGitHub().github,
                                             app_repos=FakeAppGitHub().config)).get('/new', headers=WHO).text
        self.assertIn('name="feature-kind" value="web" checked', page)

    def test_bad_input_or_a_missing_token_creates_nothing(self):
        runner = repo_runner(tree_fake())
        app_api = FakeAppGitHub()
        c = TestClient(app_under_test(runner, jobs=actions.Jobs(runner, inline=True), github=FakeGitHub().github,
                                         app_repos=app_api.config))
        for form, message in (({'name': 'Notes', 'stack': 'typescript'}, 'DNS label'),
                              ({'name': 'notes', 'stack': 'cobol'}, 'stack must be one of'),
                              ({'name': 'notes', 'stack': 'typescript', 'host': 'bad host'}, 'not a DNS name')):
            with self.subTest(message=message):
                response = c.post('/new/repo', data=form, headers=WHO)
                self.assertEqual(response.status_code, 400)
                self.assertIn(message, response.text)
        c = TestClient(app_under_test(runner, jobs=actions.Jobs(runner, inline=True), github=FakeGitHub().github))
        self.assertIn('No token for creating repositories', c.get('/new', headers=WHO).text)
        self.assertEqual(c.post('/new/repo', data={'name': 'notes', 'stack': 'typescript'}, headers=WHO).status_code, 409)
        self.assertEqual((runner.calls, app_api.requests), ([], []))

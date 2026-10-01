"""Console changes through GitHub: form to app-new argv, download, commit, branch, PR; offline, with
FakeRunner for the tree's tooling and a fake GitHub API."""
import base64
import io
import json
import sys
import tarfile
import unittest
from pathlib import Path

import httpx
from starlette.testclient import TestClient

from swhurl.apps import new
from swhurl.console import actions, changes, server
from swhurl.run import FakeRunner, Result

TOKEN = 'github_pat_fixture_0123456789abcdef'
HEAD = 'abc1234' + '0' * 33
WHO = {'X-Auth-Request-Email': 'sam@swhurl.com', 'Origin': 'http://testserver'}
FORM = {'name': 'weather-api', 'env': 'staging', 'image': 'ghcr.io/me/weather:1.0', 'exposure': 'authenticated-web',
        'host': 'weather.homelab.swhurl.com', 'health_path': '/ready', 'kind': '', 'port': ''}
MAIN = {'README.md': b'# repo\n', 'apps/hello/staging/helmrelease.yaml': b'kind: HelmRelease\n'}


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
            'POST /pulls': {'html_url': 'https://github.com/x/pull/7'},
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

    def test_refused_app_new_writes_nothing_to_github(self):
        runner = tree_fake(app_new=Result((), 2, '', '[ERROR] production instances must pin an image digest\n'))
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
        return TestClient(server.create_app(runner, jobs=jobs, github=github)), jobs

    def test_form_opens_a_pr_as_a_job(self):
        runner = tree_fake()
        c, jobs = self.client(runner)
        preset = c.get('/new', headers=WHO).text
        self.assertIn('<a href="/new?preset=swhurl-web" class="here" aria-current="page">Web app from the swhurl template</a>', preset)
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
        page = TestClient(server.create_app(tree_fake(), jobs=actions.Jobs(tree_fake(), inline=True), github=api.github))
        self.assertIn('Could not list open pull requests', page.get('/activity', headers=WHO).text)

    def test_invalid_form_or_missing_token_runs_nothing(self):
        runner = tree_fake()
        c, _ = self.client(runner)
        response = c.post('/new', data={**FORM, 'name': 'Bad'}, headers=WHO)
        self.assertEqual(response.status_code, 400)
        self.assertIn('value="Bad"', response.text)
        c, _ = self.client(runner, github=False)
        self.assertIn('No GitHub token', c.get('/new', headers=WHO).text)
        self.assertEqual(c.post('/new', data=FORM, headers=WHO).status_code, 409)
        self.assertEqual(runner.calls, [])

    def test_cross_site_post_is_refused(self):
        runner = tree_fake()
        c, _ = self.client(runner)
        self.assertEqual(c.post('/new', data=FORM, headers={**WHO, 'Origin': 'https://evil.example'}).status_code, 403)
        self.assertEqual(runner.calls, [])



class ChangeAppRouteTests(unittest.TestCase):
    def client(self, runner, github=True):
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        self.api = FakeGitHub()
        github = self.api.github if github else None
        return TestClient(server.create_app(runner, jobs=jobs, github=github)), jobs

    def tool_call(self, runner):
        return next(c for c in runner.calls if c[:3] == (sys.executable, '-m', 'swhurl'))

    def test_scale_promote_and_remove_run_the_clones_commands(self):
        cases = (
            ('/apps/hello/prod/scale', {'replicas': '2', 'memory_limit': '256Mi', 'cpu': ''},
             ('app-scale', 'hello', 'prod', '--replicas=2', '--memory-limit=256Mi'), 'hello/prod',
             'console/scale-hello-prod-abc1234', '[console] apps: scale hello/prod'),
            ('/apps/hello/staging/promote', {}, ('app-promote', 'hello', '--from=staging', '--to=prod'), 'hello/prod',
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


if __name__ == '__main__':
    unittest.main()

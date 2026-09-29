"""Console changes through Git: form to app-new argv, clone, commit, push, PR; offline, with FakeRunner."""
import json
import sys
import unittest

from starlette.testclient import TestClient

from swhurl.apps import new
from swhurl.console import actions, changes, server
from swhurl.run import CommandError, FakeRunner, Result

TOKEN = 'github_pat_fixture_0123456789abcdef'
GITHUB = changes.GitHub('samclement/swhurl-platform', TOKEN)
WHO = {'X-Auth-Request-Email': 'sam@swhurl.com', 'Origin': 'http://testserver'}
FORM = {'name': 'weather-api', 'env': 'staging', 'image': 'ghcr.io/me/weather:1.0', 'exposure': 'authenticated-web',
        'host': 'weather.homelab.swhurl.com', 'health_path': '/ready', 'kind': '', 'port': ''}


APP_NEW_OK = Result((), 0, '[OK] wrote apps/weather-api/staging/helmrelease.yaml\n')


def git_fake(app_new=APP_NEW_OK, status=' A apps/x\n'):
    runner = FakeRunner()
    runner.stdin = []

    def remember(answer):
        def handler(args, stdin):
            runner.stdin.append((args, stdin))
            return Result(args, answer.returncode, answer.stdout, answer.stderr)
        return handler
    (runner.on('git', 'clone').on('git', '-C', handler=lambda args, stdin: remember(Result((), 0, {
        'rev-parse': 'abc1234\n', 'status': status, 'show': 'abc1234 [console] apps: add\n 2 files changed\n'}.get(
        next((a for a in args if a in ('rev-parse', 'status', 'show')), ''), '')))(args, stdin))
        .on(sys.executable, '-m', 'swhurl', 'app-new', handler=remember(app_new))
        .on(sys.executable, '-m', 'swhurl', handler=remember(Result((), 0, '[OK] edited\n')))
        .on('curl', handler=remember(Result((), 0, json.dumps({'html_url': 'https://github.com/x/pull/7'})))))
    return runner


class FormTests(unittest.TestCase):
    def test_form_becomes_flag_equals_value_arguments(self):
        name, env, argv = changes.new_app_args({**FORM, 'cpu': '--no-policy-check'})
        self.assertEqual((name, env), ('weather-api', 'staging'))
        self.assertEqual(argv, ['weather-api', '--env=staging', '--exposure=authenticated-web',
                                '--image=ghcr.io/me/weather:1.0', '--host=weather.homelab.swhurl.com',
                                '--health-path=/ready', '--cpu=--no-policy-check'])

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
    def open(self, runner, change=lambda clone: None):
        job = actions.Job(1, 'new app', 'weather-api/staging', 'sam@swhurl.com', None)
        url = changes.open_pr(runner, GITHUB, job, slug='new-weather-api-staging', title='apps: add weather-api staging',
                              body='body', change=change)
        return url, job

    def test_clone_commit_push_and_pr_without_the_token_in_any_command_line(self):
        runner = git_fake()
        url, job = self.open(runner)
        self.assertEqual(url, 'https://github.com/x/pull/7')
        for call in runner.calls:
            self.assertFalse(any(TOKEN in arg for arg in call), call)
        clone = next(c for c in runner.calls if c[:2] == ('git', 'clone'))
        self.assertEqual(clone[-2], 'https://github.com/samclement/swhurl-platform.git')
        push = next(c for c in runner.calls if 'push' in c)
        self.assertEqual(push[-2:], ('origin', 'HEAD:refs/heads/console/new-weather-api-staging-abc1234'))
        commit = next(stdin for args, stdin in runner.stdin if 'commit' in args)
        self.assertTrue(commit.startswith('[console] apps: add weather-api staging'))
        self.assertIn('Requested-by: sam@swhurl.com', commit)
        curl_args, curl_stdin = next((a, s) for a, s in runner.stdin if a[0] == 'curl')
        self.assertEqual(curl_stdin, f'header = "Authorization: Bearer {TOKEN}"\n')
        self.assertEqual(json.loads(curl_args[curl_args.index('--data-binary') + 1])['head'],
                         'console/new-weather-api-staging-abc1234')
        self.assertIn('Opened https://github.com/x/pull/7', job.lines)

    def test_clone_is_removed_even_on_failure(self):
        runner = git_fake(status='')
        seen = []
        with self.assertRaisesRegex(actions.ActionError, 'no files changed'):
            self.open(runner, change=seen.append)
        self.assertFalse(seen[0].parent.exists())
        self.assertFalse([c for c in runner.calls if 'push' in c or c[0] == 'curl'])

    def test_refused_app_new_pushes_nothing(self):
        runner = git_fake(app_new=Result((), 2, '', '[ERROR] production instances must pin an image digest\n'))
        job = actions.Job(1, 'new app', 'x/prod', 'sam@swhurl.com', None)
        with self.assertRaisesRegex(actions.ActionError, 'refused'):
            changes.open_pr(runner, GITHUB, job, slug='new-x-prod', title='t', body='b',
                            change=lambda clone: changes.run_app_new(runner, job, clone, ['x', '--env=prod']))
        self.assertIn('[ERROR] production instances must pin an image digest', job.lines)
        self.assertFalse([c for c in runner.calls if 'push' in c or c[0] == 'curl'])

    def test_app_new_runs_the_clones_tooling(self):
        runner = git_fake()
        self.open(runner, change=lambda clone: changes.run_app_new(runner, actions.Job(1, '', '', '', None), clone, ['x']))
        call = next(c for c in runner.calls if 'app-new' in c)
        self.assertEqual(call, (sys.executable, '-m', 'swhurl', 'app-new', 'x'))

    def test_dry_run_plans_push_and_pr(self):
        runner = git_fake()
        runner.dry_run = True
        url, job = self.open(runner)
        self.assertEqual(url, '')
        self.assertEqual({p[0] for p in runner.planned}, {'git', 'curl'})
        self.assertIn('Dry run: no PR opened', job.lines)

    def test_push_failure_is_a_redacted_error(self):
        runner = git_fake()
        runner.on('git', '-C', handler=lambda args, _: Result(args, 128 if 'push' in args else 0,
                                                              'abc1234\n', f'denied for {TOKEN}'))
        runner._rules.insert(0, runner._rules.pop())
        with self.assertRaises(CommandError) as caught:
            self.open(runner)
        self.assertNotIn(TOKEN, str(caught.exception))


class NewAppRouteTests(unittest.TestCase):
    def client(self, runner, github=GITHUB):
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        return TestClient(server.create_app(runner, jobs=jobs, github=github)), jobs

    def test_form_opens_a_pr_as_a_job(self):
        runner = git_fake()
        c, jobs = self.client(runner)
        form = c.get('/new', headers=WHO).text
        self.assertIn('Open pull request', form)
        self.assertIn('id="port" name="port" value="" placeholder="8080"', form)
        self.assertIn('id="uid" name="uid" value="" placeholder="65532"', form)
        self.assertIn('<option value="">web (default)</option>', form)
        self.assertIn('<option value="">private (default)</option>', form)
        self.assertIn('nginx-unprivileged is 101', form)
        response = c.post('/new', data=FORM, headers=WHO, follow_redirects=False)
        self.assertEqual((response.status_code, response.headers['location']), (303, '/jobs/1'))
        job = jobs.get(1)
        self.assertEqual((job.state, job.link, job.unit), ('succeeded', 'https://github.com/x/pull/7', 'weather-api/staging'))
        self.assertIn('https://github.com/x/pull/7', c.get('/jobs/1', headers=WHO).text)

    def test_invalid_form_or_missing_token_runs_nothing(self):
        runner = git_fake()
        c, _ = self.client(runner)
        response = c.post('/new', data={**FORM, 'name': 'Bad'}, headers=WHO)
        self.assertEqual(response.status_code, 400)
        self.assertIn('value="Bad"', response.text)
        c, _ = self.client(runner, github=None)
        self.assertIn('No GitHub token', c.get('/new', headers=WHO).text)
        self.assertEqual(c.post('/new', data=FORM, headers=WHO).status_code, 409)
        self.assertEqual(runner.calls, [])

    def test_cross_site_post_is_refused(self):
        runner = git_fake()
        c, _ = self.client(runner)
        self.assertEqual(c.post('/new', data=FORM, headers={**WHO, 'Origin': 'https://evil.example'}).status_code, 403)
        self.assertEqual(runner.calls, [])



class ChangeAppRouteTests(unittest.TestCase):
    def client(self, runner, github=GITHUB):
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        return TestClient(server.create_app(runner, jobs=jobs, github=github)), jobs

    def tool_call(self, runner):
        return next(c for c in runner.calls if c[:3] == (sys.executable, '-m', 'swhurl'))

    def test_scale_promote_and_remove_run_the_clones_commands(self):
        cases = (
            ('/apps/hello/prod/scale', {'replicas': '2', 'memory_limit': '256Mi', 'cpu': ''},
             ('app-scale', 'hello', 'prod', '--replicas=2', '--memory-limit=256Mi'), 'hello/prod',
             'console/scale-hello-prod-abc1234', '[console] apps: scale hello prod'),
            ('/apps/hello/staging/promote', {}, ('app-promote', 'hello', '--from=staging', '--to=prod'), 'hello/prod',
             'console/promote-hello-staging-abc1234', '[console] apps: promote hello staging to prod'),
            ('/apps/hello/staging/remove', {}, ('app-remove', 'hello', 'staging'), 'hello/staging',
             'console/remove-hello-staging-abc1234', '[console] apps: remove hello staging'),
        )
        for path, form, command, target, branch, title in cases:
            with self.subTest(path=path):
                runner = git_fake()
                c, jobs = self.client(runner)
                response = c.post(path, data=form, headers=WHO, follow_redirects=False)
                self.assertEqual(response.status_code, 303, response.text)
                self.assertEqual(self.tool_call(runner)[3:], command)
                job = jobs.get(1)
                self.assertEqual((job.state, job.unit, job.link), ('succeeded', target, 'https://github.com/x/pull/7'))
                curl_args = next(a for a, _ in runner.stdin if a[0] == 'curl')
                payload = json.loads(curl_args[curl_args.index('--data-binary') + 1])
                self.assertEqual((payload['head'], payload['title']), (branch, title))

    def test_refusals_run_nothing(self):
        runner = git_fake()
        c, _ = self.client(runner)
        for path, form, code in (('/apps/hello/prod/promote', {}, 400), ('/apps/hello/prod/scale', {}, 400),
                                 ('/apps/hello/prod/scale', {'replicas': '2; rm'}, 400), ('/apps/hello/prod/delete', {}, 404),
                                 ('/apps/Hello/prod/remove', {}, 404), ('/apps/hello/dev/remove', {}, 404)):
            with self.subTest(path=path, form=form):
                self.assertEqual(c.post(path, data=form, headers=WHO).status_code, code)
        c, _ = self.client(runner, github=None)
        self.assertEqual(c.post('/apps/hello/prod/remove', headers=WHO).status_code, 409)
        self.assertEqual(runner.calls, [])


if __name__ == '__main__':
    unittest.main()

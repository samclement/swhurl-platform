"""Console changes through GitHub: form to tooling argv, download, commit, branch, PR; offline, with
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
from swhurl.apps import calling, channels, helmrelease, new, ops, promotion, repo
from swhurl.apps.instance import Instance
from swhurl.apps.state import State
from swhurl.console import actions, changes, cluster, server
from swhurl.console import promotion as console_promotion
from swhurl.run import CommandError, FakeRunner, Result, Runner

TOKEN = 'github_pat_fixture_0123456789abcdef'
HEAD = 'abc1234' + '0' * 33
WHO = {'X-Auth-Request-Email': 'sam@swhurl.com', 'Origin': 'http://testserver'}
NEW = {'name': 'weather-api', 'stack': 'typescript', 'team': 'platform', 'exposure': 'authenticated-web', 'host': ''}
IMAGE = 'ghcr.io/me/hello:2.0@sha256:' + 'a' * 64
REVISION = 'main@sha1:' + HEAD
SAMPLE = ROOT / 'tests/fixtures/instance'  # frozen sample instance; see its README
MAIN = {'README.md': b'# repo\n'}
for env in ('staging', 'prod'):
    for path in (SAMPLE / 'apps/hello' / env).glob('*.yaml'):
        MAIN[path.relative_to(SAMPLE).as_posix()] = path.read_bytes()
    rel = f'apps/hello/{env}/helmrelease.yaml'
    doc = yaml.safe_load(MAIN[rel])
    doc['spec']['values']['controllers']['main']['containers']['main']['image'] = new.parse_image(
        IMAGE if env == 'staging' else 'ghcr.io/me/hello:1.0@sha256:' + 'b' * 64)
    MAIN[rel] = yaml.safe_dump(doc, sort_keys=False).encode()
for rel in ('clusters/home/app-hello-staging.yaml', 'clusters/home/app-hello-prod.yaml'):
    MAIN[rel] = (SAMPLE / rel).read_bytes()
with tempfile.TemporaryDirectory() as tmp:
    root = Path(tmp)
    for rel, data in MAIN.items():
        (root / rel).parent.mkdir(parents=True, exist_ok=True)
        (root / rel).write_bytes(data)
    PROMOTION = {'image': IMAGE,
                 'configuration': promotion.fingerprint(root, 'hello', 'staging'),
                 'target': promotion.fingerprint(root, 'hello', 'prod')}



ROUTES = channels.routes_configmap({'channels': {'payments-phone': 'ntfy', 'spare': 'ntfy'},
                                    'teams': {'payments': ['payments-phone']}})
"""The generated routes on main: two channels, one of them notified by team payments."""


class FakeGitHub:
    """GitHub's REST API for one repository whose main is ``MAIN``; records every request."""

    def __init__(self, fail: dict[str, tuple[int, str]] | None = None):
        self.requests: list[httpx.Request] = []
        self.fail = fail or {}
        self.also: dict[str, object] = {}  # further answers, by "METHOD /path"
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
        if key not in answers:
            return httpx.Response(404, json={'message': 'Not Found'})
        answer = answers[key]
        return httpx.Response(200, content=answer) if isinstance(answer, bytes) else httpx.Response(201, json=answer)

    def sent(self, key: str) -> list[dict]:
        return [json.loads(r.content) for r in self.requests if f'{r.method} {r.url.path}'.endswith(key.split(' ', 1)[1])
                and r.method == key.split(' ', 1)[0]]


class ConsolePrProgressTests(unittest.TestCase):
    def listed(self, *, labels=('auto-merge',), sha=HEAD, runs=(), mergeable='clean', fail=None, comments=()):
        requests = []
        pull = {'number': 7, 'title': 'apps: scale hello', 'html_url': 'https://github.com/x/pull/7',
                'head': {'ref': 'console/scale-hello-staging-abc1234', 'sha': sha},
                'created_at': '2026-10-01T07:00:00Z', 'labels': [{'name': label} for label in labels]}

        def handle(request):
            path = request.url.path.removeprefix('/repos/samclement/swhurl-platform')
            requests.append(path)
            if path == fail:
                return httpx.Response(503, json={'message': 'Unavailable'})
            answers = {'/pulls': [pull], '/actions/runs': {'workflow_runs': list(runs)},
                       '/pulls/7': {'mergeable_state': mergeable}, '/issues/7/comments': list(comments)}
            return httpx.Response(200, json=answers[path])

        github = changes.GitHub('samclement/swhurl-platform', TOKEN, transport=httpx.MockTransport(handle))
        return changes.console_prs(FakeRunner(), github)[0], requests

    def test_validation_statuses_and_checks_urls(self):
        cases = (
            ('success', 'completed', 'Checks passed; awaiting merge verification'),
            ('failure', 'completed', 'Failed validation; unmerged'),
            ('cancelled', 'completed', 'Failed validation; unmerged'),
            ('timed_out', 'completed', 'Failed validation; unmerged'),
            ('action_required', 'completed', 'Failed validation; unmerged'),
            (None, 'in_progress', 'Checks running'),
            (None, 'queued', 'Checks pending'),
            ('neutral', 'completed', 'Checks pending'),
            ('skipped', 'completed', 'Checks pending'),
        )
        for conclusion, status, expected in cases:
            with self.subTest(conclusion=conclusion, status=status):
                run = {'name': 'Validate', 'head_sha': HEAD, 'conclusion': conclusion,
                       'status': status, 'html_url': 'https://github.com/x/actions/runs/9'}
                pr, requests = self.listed(runs=(run,))
                self.assertEqual((pr.progress, pr.checks_url), (expected, run['html_url']))
                self.assertEqual(requests, ['/pulls', '/actions/runs', '/pulls/7'])

    def test_review_label_precedes_auto_merge_and_missing_sha_needs_no_checks(self):
        cases = (
            ((), HEAD, 'Awaiting setup or manual review'),
            (('auto-merge',), '', 'Awaiting setup or manual review'),
        )
        for labels, sha, expected in cases:
            with self.subTest(labels=labels, sha=sha):
                pr, requests = self.listed(labels=labels, sha=sha)
                self.assertEqual((pr.progress, pr.checks_url), (expected, pr.url + '/checks'))
                # Validate is read whoever merges it (the stages on Activity), unless there is no head to read it for.
                self.assertEqual(requests, ['/pulls', '/actions/runs', '/pulls/7'] if sha else ['/pulls'])
                self.assertEqual(pr.checks, 'pending' if sha else '')

    def test_a_refused_merge_shows_the_gates_newest_reason_and_nobody_elses(self):
        held = ('promotion-review-required', 'auto-merge')
        gate, person = {'login': 'github-actions[bot]'}, {'login': 'someone'}
        said = [{'user': gate, 'body': 'Auto-merge refused: production changed after review\n\nReview the changes.'},
                {'user': gate, 'body': 'Auto-merge refused: diff includes unrelated edits\n\nReview the changes.'},
                {'user': person, 'body': 'Auto-merge refused: <b>trust me</b>'}, {'user': gate, 'body': 'another note'}]
        cases = ((said, None, 'Auto-merge refused: diff includes unrelated edits'),
                 (said[2:], None, 'Auto-merge refused; reason in its checks'),
                 ((), None, 'Auto-merge refused; reason in its checks'),
                 ([{'user': gate, 'body': 'Auto-merge refused: ' + 'x' * 400}], None, 'Auto-merge refused: ' + 'x' * 300),
                 (said, '/issues/7/comments', 'Auto-merge refused; reason in its checks'))
        for comments, fail, expected in cases:
            with self.subTest(expected=expected, fail=fail):
                pr, requests = self.listed(labels=held, comments=comments, fail=fail)
                self.assertEqual((pr.progress, pr.checks_url), (expected, pr.url + '/checks'))
                self.assertEqual(requests, ['/pulls', '/actions/runs', '/pulls/7', '/issues/7/comments'])

    def test_only_validate_for_this_sha_counts_and_first_matching_run_wins(self):
        run = {'name': 'Validate', 'head_sha': HEAD, 'conclusion': 'failure', 'html_url': 'matching'}
        runs = ({**run, 'head_sha': 'old', 'conclusion': 'success', 'html_url': 'wrong-sha'},
                {**run, 'name': 'Other', 'conclusion': 'success', 'html_url': 'wrong-name'},
                run, {**run, 'conclusion': 'success', 'html_url': 'older-match'})
        pr, _requests = self.listed(runs=runs)
        self.assertEqual((pr.progress, pr.checks_url), ('Failed validation; unmerged', 'matching'))
        pr, _requests = self.listed(runs=runs[:2])
        self.assertEqual((pr.progress, pr.checks_url), ('Checks pending', pr.url + '/checks'))

    def test_conflict_overrides_validation_status(self):
        for conclusion in ('success', 'failure', None):
            with self.subTest(conclusion=conclusion):
                run = {'name': 'Validate', 'head_sha': HEAD, 'conclusion': conclusion, 'html_url': 'checks'}
                pr, _requests = self.listed(runs=(run,), mergeable='dirty')
                self.assertEqual((pr.progress, pr.checks_url), ('Merge conflict; review required', 'checks'))

    def test_read_failure_keeps_checks_url_available_at_that_point(self):
        run = {'name': 'Validate', 'head_sha': HEAD, 'conclusion': 'success', 'html_url': 'checks'}
        for fail, expected_url, expected_requests in (
            ('/actions/runs', 'https://github.com/x/pull/7/checks', ['/pulls', '/actions/runs']),
            ('/pulls/7', 'checks', ['/pulls', '/actions/runs', '/pulls/7']),
        ):
            with self.subTest(fail=fail):
                pr, requests = self.listed(runs=(run,), fail=fail)
                self.assertEqual((pr.progress, pr.checks_url),
                                 ('Merge requested; check progress on GitHub', expected_url))
                self.assertEqual(requests, expected_requests)


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
                          (cluster.PODS[:4], []), (cluster.WORKLOADS[:4], [])):
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


class FormTests(unittest.TestCase):
    def test_an_instance_change_is_a_value_with_its_commands_wording_and_job(self):
        hello = Instance('hello', 'staging')
        change = changes.Change.to_instance('scale', hello, {'replicas': '2', 'memory_limit': '256Mi'})
        self.assertEqual(change, changes.Change(
            kind='scale', target='hello/staging', slug='scale-hello-staging', title='apps: scale hello/staging',
            commands=(changes.Command('app-scale', ('hello', 'staging', '--replicas=2', '--memory-limit=256Mi')),),
            make='make app-scale APP=hello ENV=staging ARGS="--replicas=2 --memory-limit=256Mi"'))
        self.assertEqual(change.body, 'Generated by the console with:\n\n'
                         '    make app-scale APP=hello ENV=staging ARGS="--replicas=2 --memory-limit=256Mi"')
        self.assertFalse(change.auto_merge, 'a person merges it')
        removal = changes.Change.to_instance('remove', hello, {})
        self.assertEqual((removal.commands, removal.title, removal.make),
                         ((changes.Command('app-remove', ('hello', 'staging')),), 'apps: remove hello/staging',
                          'make app-remove APP=hello ENV=staging'))
        for kind, form, message in (('scale', {}, 'change at least one'), ('scale', {'cpu': '--root=/'}, 'not a number or quantity'),
                                    ('promote', {}, 'no such change'), ('rename', {}, 'no such change')):
            with self.subTest(kind=kind, form=form), self.assertRaisesRegex(actions.ActionError, message):
                changes.Change.to_instance(kind, hello, form)

    def test_expose_form_becomes_app_expose_arguments(self):
        hello = Instance('hello', 'staging')
        change = changes.Change.to_instance('expose', hello, {'exposure': 'public', 'host': 'weather.example.com'})
        self.assertEqual(change.commands, (changes.Command('app-expose', ('hello', 'staging', '--exposure=public',
                                                                          '--host=weather.example.com')),))
        self.assertEqual(change.title, 'apps: change access to hello/staging')
        self.assertEqual(changes.Change.to_instance('expose', hello, {'exposure': 'private', 'host': ''}).commands,
                         (changes.Command('app-expose', ('hello', 'staging', '--exposure=private')),))
        for form, message in (({'exposure': 'open'}, 'exposure must'),
                              ({'exposure': 'public', 'host': '--root=/'}, 'not a DNS name')):
            with self.subTest(form=form), self.assertRaisesRegex(actions.ActionError, message):
                changes.Change.to_instance('expose', hello, form)

    def test_connections_are_added_and_removed_by_the_commands_make_runs(self):
        hello, offered = Instance('hello', 'staging'), ({'orders', 'billing'}, {'orders.created'})
        change = changes.Change.to_instance('connect', hello, {'calls': 'orders, billing', 'subscribe': 'orders.created',
                                                               'publish': 'seen'})
        self.assertEqual(change.commands,
                         (changes.Command('app-calls', ('hello', 'staging', '--add=orders,billing')),
                          changes.Command('app-events', ('hello', 'staging', '--add-subscribe=orders.created', '--add-publish=seen'))))
        self.assertTrue(change.touches_others, 'a call or a subscription changes the other app too')
        self.assertEqual(change.manual_merge_reason, 'also changes existing app settings for billing, orders')
        publishing = changes.Change.to_instance('connect', hello, {'publish': 'seen, gone'})
        self.assertEqual(publishing.commands, (changes.Command('app-events', ('hello', 'staging', '--add-publish=seen,gone')),))
        self.assertFalse(publishing.touches_others)
        for form, message in (({}, 'choose an app to call'), ({'publish': '--root=/'}, 'a published event a lowercase name')):
            with self.subTest(form=form), self.assertRaisesRegex(actions.ActionError, message):
                changes.Change.to_instance('connect', hello, form)
        for form in ({'calls': 'reports'}, {'subscribe': 'orders.paid'}):
            with self.subTest(form=form), self.assertRaisesRegex(actions.ActionError, 'not a web app, or an event'):
                changes.Connections.from_form(form).check_offered(*offered)
        removed = {'calls': ({'role': 'calls', 'app': 'orders'}, changes.Command('app-calls', ('hello', 'staging', '--remove=orders'))),
                   'subscribes': ({'role': 'subscribes', 'app': 'orders', 'event': 'created'},
                                  changes.Command('app-events', ('hello', 'staging', '--remove-subscribe=orders.created'))),
                   'publishes': ({'role': 'publishes', 'event': 'seen'},
                                 changes.Command('app-events', ('hello', 'staging', '--remove-publish=seen'))),
                   'reaches': ({'role': 'reaches', 'event': 'api.stripe.com:443'},
                               changes.Command('app-egress', ('hello', 'staging', '--remove=api.stripe.com:443')))}
        for role, (form, command) in removed.items():
            with self.subTest(role=role):
                self.assertEqual(changes.Change.to_instance('disconnect', hello, form).commands, (command,))
        for form in ({}, {'role': 'called-by', 'app': 'orders'}, {'role': 'calls', 'app': '--root=/'},
                     {'role': 'subscribes', 'app': 'orders', 'event': 'a.b'}, {'role': 'publishes', 'event': 'Seen'},
                     {'role': 'reaches', 'event': '--root=/'}):
            with self.subTest(form=form), self.assertRaisesRegex(actions.ActionError, 'no such connection to remove'):
                changes.Change.to_instance('disconnect', hello, form)
        both = changes.Change.to_instance('connect', hello, {'calls': 'orders', 'publish': 'seen'})
        self.assertEqual(both.make, 'make app-calls APP=hello ENV=staging ARGS="--add=orders" && '
                         'make app-events APP=hello ENV=staging ARGS="--add-publish=seen"')
        self.assertEqual((both.title, both.slug), ('apps: change the connections of hello/staging', 'connect-hello-staging'))
        granting = changes.Change.to_instance('egress', hello, {'hosts': 'API.stripe.com, *.example.com:8443'})
        self.assertEqual((granting.commands, granting.touches_others), ((changes.Command(
            'app-egress', ('hello', 'staging', '--add=api.stripe.com:443,*.example.com:8443')),), False))
        for form, message in (({'hosts': ' '}, 'name a host'), ({'hosts': '10.0.0.1'}, 'a grant is a host name'),
                              ({'hosts': '--root=/'}, 'a grant is a host name')):
            with self.subTest(form=form), self.assertRaisesRegex(actions.ActionError, message):
                changes.Change.to_instance('egress', hello, form)

    def test_team_and_channel_changes_are_values_and_an_address_is_only_standard_input(self):
        add = changes.Change.to_team('add', {'name': 'payments', 'description': ' Takes  money '})
        self.assertEqual(add, changes.Change('team add', 'team:payments', 'team-add-payments', 'teams: add payments',
                                             (changes.Command('team-add', ('payments', '--description=Takes money')),),
                                             "make team-add NAME=payments DESCRIPTION='Takes money'"))
        edit = changes.Change.to_team('edit', {'name': 'payments', 'description': 'x', 'rename': 'billing'})
        self.assertEqual(edit.commands, (changes.Command('team-edit', ('payments', '--description=x', '--rename=billing')),))
        remove = changes.Change.to_team('remove', {'name': 'payments', 'move_apps_to': 'platform'})
        self.assertEqual((remove.commands, remove.make),
                         ((changes.Command('team-remove', ('payments', '--move-apps-to=platform')),),
                          'make team-remove NAME=payments ARGS="--move-apps-to platform"'))
        for action, form, message in (('add', {'name': 'Payments'}, 'team name'), ('rename', {'name': 'payments'}, 'no such team change'),
                                      ('add', {'name': 'payments', 'description': 'x' * 201}, 'at most 200')):
            with self.subTest(action=action, form=form), self.assertRaisesRegex(actions.ActionError, message):
                changes.Change.to_team(action, form)
        address = 'https://ntfy.sh/a-private-topic-0123456789'
        channel = changes.Change.to_channel('set', {'name': 'payments', 'transport': 'ntfy', 'address': address})
        (command,) = channel.commands
        self.assertEqual((command.name, command.argv, command.input), ('team-channel', ('payments', '--transport=ntfy'), address + '\n'))
        self.assertNotIn(address, repr(channel) + channel.body + channel.title + channel.slug)
        self.assertEqual((channel.kind, channel.target, channel.slug), ('channel set', 'team:payments', 'team-channel-set-payments'))
        self.assertEqual(changes.Change.to_channel('remove', {'name': 'payments'}).commands,
                         (changes.Command('team-channel', ('payments', '--remove')),))
        with self.assertRaisesRegex(actions.ActionError, 'no such channel change'):
            changes.Change.to_channel('rotate', {'name': 'payments'})

    def test_the_clone_s_tooling_leaves_only_the_files_it_means_to(self):
        """A real interpreter on a small clone: without the setting, its bytecode would join the commit."""
        with tempfile.TemporaryDirectory() as tmp:
            clone = Path(tmp)
            (clone / 'tools/swhurl').mkdir(parents=True)
            (clone / 'tools/swhurl/__init__.py').write_text('')
            (clone / 'tools/swhurl/helper.py').write_text('WORD = "edited"\n')
            (clone / 'tools/swhurl/__main__.py').write_text(
                'import sys\nfrom pathlib import Path\nfrom swhurl.helper import WORD\n'
                'Path("out.txt").write_text(WORD + " " + " ".join(sys.argv[1:]))\nprint("[OK]", WORD)\n')
            before = changes.snapshot(clone)
            job = actions.Job(1, 'scale', 'hello/staging', 'sam@swhurl.com', None)
            changes.Change.to_instance('scale', Instance('hello', 'staging'), {'replicas': '2'}).apply(Runner(), job, clone)
            self.assertEqual(job.lines, ['[OK] edited'])
            self.assertEqual((clone / 'out.txt').read_text(), 'edited app-scale hello staging --replicas=2')
            self.assertEqual(sorted(changes.snapshot(clone).keys() - before.keys()), ['out.txt'])

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


class OpenPrTests(unittest.TestCase):
    def open(self, runner, api=None, change=None):
        api = api or FakeGitHub()
        job = actions.Job(1, 'new app', 'weather-api/staging', 'sam@swhurl.com', None)
        url = changes.open_pr(runner, api.github, job, slug='new-weather-api-staging', title='apps: add weather-api staging',
                              body='body', change=change or (lambda tree: changes.run_tool(runner, job, tree, 'app-new', ['x'])))
        return url, job, api

    def test_auto_merge_labels_the_pr_unless_it_adds_a_secret(self):
        api = FakeGitHub()
        job = actions.Job(1, 'new app', 'weather-api/staging', 'sam@swhurl.com', None)
        runner = tree_fake()
        changes.open_pr(runner, api.github, job, slug='new-weather-api-staging', title='t', body='b', auto_merge=True,
                        change=lambda tree: changes.run_tool(runner, job, tree, 'app-new', ['x']))
        self.assertEqual(api.sent('POST /issues/7/labels'), [{'labels': ['auto-merge']}])
        self.assertIn('Labelled auto-merge: it merges itself when Validate passes', job.lines)

        def with_secret(tree):
            (tree / 'apps/x').mkdir(parents=True)
            (tree / 'apps/x/secret.sops.yaml').write_text('sops: {}\n')
        api, job, runner = FakeGitHub(), actions.Job(1, 'new app', 'x/staging', 'sam@swhurl.com', None), tree_fake(edit=with_secret)
        changes.open_pr(runner, api.github, job, slug='new-x-staging', title='t', body='b', auto_merge=True,
                        change=lambda tree: changes.run_tool(runner, job, tree, 'app-new', ['x']))
        self.assertEqual(api.sent('POST /issues/7/labels'), [])
        self.assertNotIn('Merges itself', api.sent('POST /pulls')[0]['body'])
        self.assertIn('Manual merge required: this pull request needs Secret values to be set on the pull request branch.',
                      api.sent('POST /pulls')[0]['body'])
        self.assertTrue(any(line.startswith('Merge plan: Manual merge required') for line in job.lines))

        def with_events_key(tree):  # an app that publishes or subscribes: its NATS key is generated whole
            (tree / 'apps/x/prod').mkdir(parents=True)
            (tree / 'apps/x/prod/events.sops.yaml').write_text('sops: {}\n')
        api, job, runner = FakeGitHub(), actions.Job(1, 'promote', 'x/prod', 'sam@swhurl.com', None), tree_fake(edit=with_events_key)
        changes.open_pr(runner, api.github, job, slug='promote-x-staging', title='t', body='b', auto_merge=True,
                        change=lambda tree: changes.run_tool(runner, job, tree, 'app-new', ['x']))
        self.assertEqual(api.sent('POST /issues/7/labels'), [], 'still reviewed by a person')
        self.assertIn("Manual merge required: this pull request adds the app's generated encrypted events key.",
                      api.sent('POST /pulls')[0]['body'])
        self.assertFalse(any('set the Secret values' in line for line in job.lines))

        api = FakeGitHub(fail={'POST /issues/7/labels': (403, 'Resource not accessible')})
        job, runner = actions.Job(1, 'new app', 'x/staging', 'sam@swhurl.com', None), tree_fake()
        url = changes.open_pr(runner, api.github, job, slug='new-x-staging', title='t', body='b', auto_merge=True,
                              change=lambda tree: changes.run_tool(runner, job, tree, 'app-new', ['x']))
        self.assertEqual(url, 'https://github.com/x/pull/7', 'a failed label leaves the PR for a person')
        self.assertTrue(any(line.startswith('Could not label it auto-merge') for line in job.lines))

    def test_manual_merge_plan_names_existing_apps_changed_by_new_app_connections(self):
        def connect_to_existing_app(tree):
            path = tree / 'apps/demo-orders/staging/review-for-test.yaml'
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('needs-review: true\n')

        api, job = FakeGitHub(), actions.Job(1, 'new app', 'test-5/staging', 'sam@swhurl.com', None)
        changes.open_pr(tree_fake(), api.github, job, slug='new-test-5-staging',
                        title='apps: add test-5/staging', body='generated app', auto_merge=False,
                        manual_merge_reason='also changes existing app settings for demo-orders',
                        change=connect_to_existing_app)
        body = api.sent('POST /pulls')[0]['body']
        self.assertIn('**Merge plan:** Manual merge required:', body)
        self.assertIn('also changes existing app settings for demo-orders', body)
        self.assertTrue(any('also changes existing app settings for demo-orders' in line for line in job.lines))

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
        self.assertEqual(job.lines[2:7], ['D README.md', 'M apps/hello/staging/helmrelease.yaml', 'A apps/x/run.sh',
                                          'Merge plan: Manual merge required: this pull request needs manual review. Review the changes, then merge it yourself.',
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

    def test_the_page_only_starts_a_new_app(self):
        runner = tree_fake()
        c, jobs = self.client(runner)
        for url in ('/new', '/new?preset=swhurl-web'):
            with self.subTest(url=url):
                page = c.get(url, headers=WHO).text
                self.assertIn('action="/new/repo"', page)
                self.assertIn('Create repository and open pull request', page)
                self.assertNotIn('Deploy an existing image', page)
                self.assertNotIn('name="image"', page)
        self.assertEqual(c.post('/new', data={'name': 'weather-api', 'image': 'ghcr.io/me/weather:1.0'},
                                headers=WHO).status_code, 405)
        self.assertIsNone(jobs.get(1))
        self.assertTrue(all(call[0] == 'kubectl' and 'get' in call for call in runner.calls),
                        'it reads the apps it could connect to and changes nothing')
        self.assertEqual({(r.method, r.url.path.rsplit('/', 1)[-1]) for r in self.api.requests}, {('GET', 'teams.yaml'), ('GET', 'routes.yaml')},
                         'the page reads the team registry and each team\'s channel, and nothing else')
        self.assertEqual(len(self.api.requests), 2, 'and keeps both for five minutes')

    def test_the_team_is_chosen_from_the_registry_on_main(self):
        c, _ = self.client(tree_fake())
        page = c.get('/new', headers=WHO).text
        self.assertIn('name="team" value="platform" required >', page)
        self.assertNotIn('checked>\n            <span><strong>platform', page)
        self.assertEqual(page.count('name="team" value="platform" required checked'), 0, 'no team is preselected: there is no default')
        self.assertIn('<span class="muted">The platform itself</span>', page)
        self.assertIn('name="team" value="payments" required >', page)
        self.assertLess(page.index('name="team" value="payments"'), page.index('name="team" value="platform"'), 'alphabetical')
        self.assertEqual((page.count('>notification channel</span>'), page.count('>no notification channel</span>')), (1, 1),
                         'each team is marked with whether it has a channel')
        teams = changes.platform_teams(FakeRunner(), self.api.github)
        self.assertEqual(teams, {'platform': 'The platform itself', 'payments': ''})
        web = [repo.Question('kind', '', ('web', 'worker'), 'web')]
        self.assertEqual(changes.NewApp.from_form({**NEW, 'team': 'payments'}, web, teams).flags,
                         ('--team=payments', '--exposure=authenticated-web'))
        self.assertEqual(changes.NewApp.from_form({**NEW, 'team': 'payments', 'feature-kind': 'worker'}, web, teams).flags,
                         ('--team=payments',), 'a worker has a team too')
        for form, registered, message in (({**NEW, 'team': ''}, teams, 'choose the team that owns the app: one of platform, payments'),
                                          ({**NEW, 'team': 'nobody'}, teams, 'choose the team that owns the app'),
                                          (NEW, {}, 'no team is registered')):
            with self.subTest(message=message), self.assertRaisesRegex(actions.ActionError, message):
                changes.NewApp.from_form(form, web, registered)
        unreadable = FakeGitHub(fail={'GET /contents/apps/teams.yaml': (404, 'Not Found')})
        page = TestClient(app_under_test(tree_fake(), github=unreadable.github)).get('/new', headers=WHO).text
        self.assertIn('No team is registered yet', page)
        self.assertNotIn('name="team" value=', page)

    def test_the_form_offers_what_staging_has_to_connect_to_and_says_what_a_team_is(self):
        staging = (staging_app('orders', {'SWHURL_EVENTS_PUBLISH': 'created,paid'}), staging_app('job', port=None),
                   staging_app('billing'))
        c, _ = self.client(tree_fake(staging=staging))
        page = c.get('/new', headers=WHO).text
        self.assertIn('name="calls" value="orders"', page)
        self.assertIn('name="calls" value="billing"', page)
        self.assertNotIn('name="calls" value="job"', page, 'a worker cannot be called')
        self.assertIn('name="subscribe" value="orders.created"', page)
        self.assertIn('name="subscribe" value="orders.paid"', page)
        self.assertIn('It does not grant or restrict anyone\'s access', page)
        empty = self.client(tree_fake())[0].get('/new', headers=WHO).text
        self.assertIn('No web app is in staging yet.', empty)
        self.assertIn('No app in staging publishes an event yet.', empty)

    def test_connections_become_app_new_arguments_and_only_offered_ones_are_accepted(self):
        offered = ({'orders', 'billing'}, {'orders.created'})
        teams = {'platform': ''}
        chosen = changes.NewApp.from_form({**NEW, 'calls': 'orders,billing', 'subscribe': 'orders.created',
                                           'publish': 'sent, failed'}, [], teams, *offered)
        self.assertEqual(chosen.flags, ('--team=platform', '--exposure=authenticated-web', '--calls=orders,billing',
                                        '--subscribe=orders.created', '--publish=sent,failed'))
        self.assertTrue(chosen.touches_others)
        alone = changes.NewApp.from_form({**NEW, 'publish': 'sent'}, [], teams, *offered)
        self.assertEqual((alone.flags[2:], alone.touches_others), (('--publish=sent',), False), 'publishing changes no other app')
        self.assertEqual(changes.NewApp.from_form(NEW, [], teams, *offered).flags[2:], (), 'nothing chosen adds nothing')
        cases = (({'calls': 'stranger'}, 'not a web app, or an event, that staging has now'),
                 ({'subscribe': 'orders.cancelled'}, 'not a web app, or an event, that staging has now'),
                 ({'calls': '--root=/'}, 'a call names an app'), ({'publish': 'Sent'}, 'a published event a lowercase name'),
                 ({'subscribe': 'created'}, 'a subscription <app>.<event>'))
        for form, message in cases:
            with self.subTest(form=form), self.assertRaisesRegex(actions.ActionError, message):
                changes.NewApp.from_form({**NEW, **form}, [], teams, *offered)

    def test_a_new_app_is_a_value_a_job_keeps_and_its_pull_request_merges_itself_unless_it_changes_another_app(self):
        teams, offered = {'platform': ''}, ({'orders'}, set())
        chosen = changes.NewApp.from_form({**NEW, 'description': ' Says  the weather '}, [], teams, *offered)
        self.assertEqual(changes.NewApp.from_record(json.loads(json.dumps(chosen.record))), chosen, 'what a job keeps reads back')
        self.assertEqual(chosen.record['kind'], changes.NEW_APP)
        change = chosen.change(IMAGE)
        self.assertEqual(change.commands, (changes.Command('app-new', (
            'weather-api', f'--from-repo={chosen.request.repo}', '--env=staging', f'--image={IMAGE}', '--team=platform',
            '--exposure=authenticated-web')),))
        self.assertEqual((change.kind, change.target, change.slug, change.title),
                         ('new app and repository', 'weather-api/staging', 'new-weather-api-staging', 'apps: add weather-api/staging'))
        self.assertTrue(change.auto_merge)
        self.assertTrue(change.body.startswith(f'The console created https://github.com/{chosen.request.repo} from ['))
        self.assertIn(f'\n\nGenerated by the console with:\n\n    make app-new NAME=weather-api ARGS="--from-repo={chosen.request.repo} ', change.body)
        calling = changes.NewApp.from_form({**NEW, 'calls': 'orders'}, [], teams, *offered).change(IMAGE)
        self.assertFalse(calling.auto_merge, "the other team's review is its consent")
        self.assertEqual(calling.manual_merge_reason, 'also changes existing app settings for orders')
        subscribing = changes.NewApp.from_form({**NEW, 'subscribe': 'orders.created'}, [], teams,
                                                {'orders'}, {'orders.created'}).change(IMAGE)
        self.assertEqual(subscribing.manual_merge_reason, 'also changes existing app settings for orders')
        for record in ({}, {**chosen.record, 'name': '../x'}, {**chosen.record, 'stack': 'cobol'},
                       {**chosen.record, 'extra': ['weather-api; rm']}, {**chosen.record, 'extra': None}):
            with self.subTest(record=record), self.assertRaises((KeyError, TypeError, ValueError)):
                changes.NewApp.from_record(record)

    def test_open_console_prs_are_listed_and_others_ignored(self):
        c, _ = self.client(tree_fake())
        text = c.get('/activity', headers=WHO).text
        self.assertIn('#7 apps: add weather-api staging</a>', text, 'every pull request here is a console one: the prefix is left out')
        self.assertIn('Manual merge required', text)
        self.assertIn('<span class="state suspended">Waiting for you</span>', text)
        self.assertIn('Waiting for you: merge #7 on GitHub', text, 'a pull request no remembered job opened still shows its path')
        self.assertIn('Live in staging', text)
        self.assertNotIn('pull/8', text)
        self.assertEqual(changes.console_prs(FakeRunner(), self.api.github)[0].opened, '2026-10-01')
        c, _ = self.client(tree_fake(), github=False)
        self.assertIn('No GitHub token is configured, so open pull requests are not listed', c.get('/activity', headers=WHO).text)
        api = FakeGitHub(fail={'GET /pulls': (401, 'Bad credentials')})
        page = TestClient(app_under_test(tree_fake(), jobs=actions.Jobs(tree_fake(), inline=True), github=api.github))
        self.assertIn('Could not list open pull requests', page.get('/activity', headers=WHO).text)

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
        app_api = FakeAppGitHub()
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        c = TestClient(app_under_test(runner, jobs=jobs, github=FakeGitHub().github, app_repos=app_api.config))
        response = c.post('/new/repo', data={**NEW, 'name': 'Bad'}, headers=WHO)
        self.assertEqual(response.status_code, 400)
        self.assertIn('value="Bad"', response.text)
        c = TestClient(app_under_test(runner, jobs=jobs, app_repos=app_api.config))
        self.assertIn('No GitHub token is configured', c.get('/new', headers=WHO).text)
        self.assertEqual(c.post('/new/repo', data=NEW, headers=WHO).status_code, 409)
        self.assertTrue(all(call[0] == 'kubectl' and 'get' in call for call in runner.calls), 'only the form\'s reads')
        self.assertEqual(app_api.requests, [])

    def test_cross_site_post_is_refused(self):
        runner = tree_fake()
        app_api = FakeAppGitHub()
        c = TestClient(app_under_test(runner, jobs=actions.Jobs(runner, audit=lambda line: None, inline=True),
                                      github=FakeGitHub().github, app_repos=app_api.config))
        self.assertEqual(c.post('/new/repo', data=NEW, headers={**WHO, 'Origin': 'https://evil.example'}).status_code, 403)
        self.assertEqual((runner.calls, app_api.requests), ([], []))


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

    def merged(self, jobs, count=2):
        """Finished changes to hello/staging whose pull requests merged, #2 after #1, and the calls made to read them."""
        for number in range(1, count + 1):
            jobs.submit('scale', 'hello/staging', 'sam@swhurl.com',
                        lambda job, number=number: setattr(job, 'link', f'https://github.com/x/pull/{number}'))
        return mock.Mock(side_effect=lambda _runner, _github, number: ('merged', '2026-10-13T10:00:00Z', f'merge{number}'))

    def row(self, state='healthy', applied='main@sha1:applied'):
        return cluster.AppRow(Instance('hello', 'staging'), State(state), False, 'x:1',
                              applied=applied)

    def test_a_merged_change_is_judged_against_its_own_commit_and_its_ending_kept(self):
        c, jobs = self.client(tree_fake())
        own = 'apps/hello/staging/helmrelease.yaml'
        since = {'merge1': (True, ['docs/x.md', own]), 'merge2': (True, ['docs/x.md'])}
        compare = mock.Mock(side_effect=lambda _runner, _github, base, _head: since[base])
        rows = [self.row()]
        with mock.patch.object(changes, 'console_prs', return_value=[]), mock.patch.object(changes, 'pull_state', self.merged(jobs)), \
                mock.patch.object(changes, 'changed_since', compare), mock.patch.object(cluster, 'apps', side_effect=lambda _runner: rows):
            older, newer = (c.get(f'/jobs/{n}', headers=WHO).text for n in (1, 2))
            self.assertIn('a later change to hello/staging has been applied since', older)
            self.assertIn('hello/staging runs it.', newer)
            self.assertEqual([jobs.get(n).ending for n in (1, 2)], [{'state': 'superseded'}, {'state': 'live'}])
            # Whatever happens to the instance later is not this change's doing, and nothing is asked again.
            rows[:] = [self.row('failing')]
            asked = compare.call_count
            activity = c.get('/activity', headers=WHO).text
        self.assertIn('hello/staging runs it.', activity)
        self.assertNotIn('is failing', activity)
        self.assertEqual(compare.call_count, asked)

    def test_a_merge_flux_has_not_applied_or_that_fails_is_not_over(self):
        c, jobs = self.client(tree_fake())
        since = {'merge1': (False, None)}
        compare = mock.Mock(side_effect=lambda _runner, _github, base, _head: since[base])
        rows = [self.row('healthy')]
        with mock.patch.object(changes, 'console_prs', return_value=[]), mock.patch.object(changes, 'pull_state', self.merged(jobs, 1)), \
                mock.patch.object(changes, 'changed_since', compare), mock.patch.object(cluster, 'apps', side_effect=lambda _runner: rows):
            self.assertIn('Flux is applying it to hello/staging', c.get('/jobs/1', headers=WHO).text, 'healthy on what ran before')
            c.get('/jobs/1', headers=WHO)
            self.assertEqual(compare.call_count, 1, 'two commits always compare the same')
            rows[:] = [self.row('failing', 'main@sha1:later')]
            since['merge1'] = (True, [])
            activity = c.get('/activity', headers=WHO).text
            self.assertIn('Merged, but hello/staging is failing', activity)
            self.assertLess(activity.index('Merged, but hello/staging is failing'), activity.index('<h2>Finished</h2>'), 'it needs someone')
            self.assertIsNone(jobs.get(1).ending, 'failing can recover: it is not kept')
            rows[:] = []  # removed since, by a change the console does not know: what Flux fetched says so
            since['merge1'] = (True, ['clusters/home/app-hello-staging.yaml'])
            self.assertIn('Merged to main', c.get('/jobs/1', headers=WHO).text)
            rows[:] = [self.row(applied='main@sha1:unseen')]
            compare.side_effect = actions.ActionError('rate limited')
            jobs.get(1).ending = None
            self.assertIn('GitHub could not say whether Flux has applied the merge', c.get('/jobs/1', headers=WHO).text)

    def test_how_a_pull_request_ended_is_read_once_and_outlives_a_github_failure(self):
        c, jobs = self.client(tree_fake())
        read = self.merged(jobs)
        with mock.patch.object(changes, 'pull_state', read), mock.patch.object(cluster, 'apps', return_value=[self.row()]), \
                mock.patch.object(changes, 'changed_since', return_value=(True, [])):
            with mock.patch.object(changes, 'console_prs', return_value=[]):
                for _ in range(2):
                    c.get('/activity', headers=WHO)
            self.assertEqual(read.call_count, 2, 'once per pull request')
            with mock.patch.object(changes, 'console_prs', side_effect=actions.ActionError('rate limited')):
                text = c.get('/activity', headers=WHO).text
        self.assertIn('Could not list open pull requests', text)
        self.assertNotIn('could not be read from GitHub', text, 'what was read before is still known')
        self.assertEqual(read.call_count, 2)

    def test_scale_promote_and_remove_run_the_clones_commands(self):
        cases = (
            ('/apps/hello/prod/scale', {'replicas': '2', 'memory_limit': '256Mi', 'cpu': ''},
             ('app-scale', 'hello', 'prod', '--replicas=2', '--memory-limit=256Mi'), 'hello/prod',
             'console/scale-hello-prod-abc1234', '[console] apps: scale hello/prod'),
            ('/apps/hello/staging/promote', PROMOTION, ('app-promote', 'hello', f'--expect-image={IMAGE}', f'--expect-config={PROMOTION["configuration"]}'), 'hello/prod',
             'console/promote-hello-staging-abc1234', '[console] apps: promote hello/staging to hello/prod'),
            ('/apps/hello/staging/remove', {}, ('app-remove', 'hello', 'staging'), 'hello/staging',
             'console/remove-hello-staging-abc1234', '[console] apps: remove hello/staging'),
            ('/apps/hello/staging/connect', {'publish': 'created, paid'},
             ('app-events', 'hello', 'staging', '--add-publish=created,paid'), 'hello/staging',
             'console/connect-hello-staging-abc1234', '[console] apps: change the connections of hello/staging'),
            ('/apps/hello/staging/disconnect', {'role': 'subscribes', 'app': 'orders', 'event': 'created'},
             ('app-events', 'hello', 'staging', '--remove-subscribe=orders.created'), 'hello/staging',
             'console/disconnect-hello-staging-abc1234', '[console] apps: change the connections of hello/staging'),
            ('/catalogue/staging/use', {'app': 'hello', 'publish': 'created'},  # the catalogue's choice of app: the same change
             ('app-events', 'hello', 'staging', '--add-publish=created'), 'hello/staging',
             'console/connect-hello-staging-abc1234', '[console] apps: change the connections of hello/staging'),
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
                 ('/apps/hello/staging/disconnect', {'role': 'calls', 'app': 'orders'}, False),
                 ('/apps/hello/staging/remove', {'auto_merge': 'on'}, False))
        for path, form, merges in cases:
            with self.subTest(path=path, form=form):
                c, jobs = self.client(tree_fake())
                c.post(path, data=form, headers=WHO)
                self.assertEqual(jobs.get(1).state, 'succeeded')
                self.assertEqual(self.api.sent('POST /issues/7/labels'), [{'labels': ['auto-merge']}] if merges else [])
                self.assertEqual('Merges itself when Validate and the trusted merge checks pass' in self.api.sent('POST /pulls')[0]['body'], merges)

    def test_a_first_promotion_that_changes_the_apps_it_calls_is_merged_by_a_person(self):
        """The merge gate holds a promotion to its own app: one that edits its callees must not be labelled for it."""
        release = yaml.safe_load(MAIN['apps/hello/staging/helmrelease.yaml'])
        helmrelease.container(release).setdefault('env', {})[calling.callee_variable('orders')] = calling.callee_address('orders', 8080)
        main = {rel: data for rel, data in MAIN.items() if 'hello/prod' not in rel and 'app-hello-prod' not in rel}
        main['apps/hello/staging/helmrelease.yaml'] = yaml.safe_dump(release, sort_keys=False).encode()
        main['apps/orders/prod/helmrelease.yaml'] = MAIN['apps/hello/prod/helmrelease.yaml']
        with tempfile.TemporaryDirectory() as tmp, mock.patch.dict(MAIN, main, clear=True):
            root = Path(tmp)
            for rel, data in main.items():
                (root / rel).parent.mkdir(parents=True, exist_ok=True)
                (root / rel).write_bytes(data)
            good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
            review = console_promotion.review(root, 'hello', good)
            self.assertEqual((review['admits'], review['setup']), (['orders'], True))
            (root / 'apps/orders/prod/helmrelease.yaml').unlink()
            (root / 'apps/orders/prod').rmdir()
            self.assertEqual(console_promotion.admitting(root, 'hello', release), [], 'a callee with no production is not edited')
            c, jobs = self.client(tree_fake())
            page = c.get('/apps/hello/staging/promotion', headers=WHO).text
            self.assertIn('This also lets hello call orders in production', page)
            self.assertNotIn('name="hold"', page, 'there is no automatic merge to hold')
            form = {'image': IMAGE, 'configuration': review['configuration'], 'target': review['target']}
            c.post('/apps/hello/staging/promote', data=form, headers=WHO)
        job = jobs.get(1)
        self.assertEqual(job.state, 'succeeded', job.lines)
        self.assertTrue(any(line.startswith('Merge plan: Manual merge required') and 'orders' in line for line in job.lines))
        self.assertEqual(self.api.sent('POST /issues/7/labels'), [])
        body = self.api.sent('POST /pulls')[0]['body']
        self.assertIn('This also lets `hello` call `orders/prod`', body)
        self.assertNotIn('Merges itself', body)
        self.assertTrue(console_promotion.body_metadata(body)['setup'], 'the promotion needs review and cannot be resumed')

    def test_a_change_the_tooling_refuses_shows_its_own_words_and_opens_nothing(self):
        """The console runs the same commands as make, so their refusals read the same in the job."""
        refusals = (
            ('/apps/hello/staging/disconnect', {'role': 'calls', 'app': 'nowhere'}, '[ERROR] hello/staging does not call nowhere'),
            ('/apps/hello/staging/remove', {}, '[ERROR] hello/staging is called by reports: remove those calls first'),
            ('/apps/hello/prod/scale', {'replicas': '9'}, '[BAD] quota: replicas and sizes exceed the namespace quota for pods'),
        )
        for path, form, message in refusals:
            with self.subTest(path=path):
                runner = TreeRunner()
                runner.on(sys.executable, '-m', 'swhurl', handler=lambda args, _, message=message: Result(args, 2, '', message + '\n'))
                c, jobs = self.client(runner)
                self.assertEqual(c.post(path, data=form, headers=WHO, follow_redirects=False).status_code, 303)
                job = jobs.get(1)
                self.assertEqual(job.state, 'failed')
                self.assertIn(message, job.lines)
                self.assertTrue(any('refused this change (see above); nothing was written to GitHub' in line for line in job.lines))
                self.assertEqual(self.api.sent('POST /pulls'), [])
                self.assertIn(message.split('] ')[1][:30], c.get('/jobs/1', headers=WHO).text.replace('&#39;', "'"))

    def test_refusals_run_nothing(self):
        runner = tree_fake()
        c, _ = self.client(runner)
        for path, form, code in (('/apps/hello/prod/promote', {}, 400), ('/apps/hello/prod/scale', {}, 400),
                                 ('/apps/hello/prod/scale', {'replicas': '2; rm'}, 400), ('/apps/hello/prod/delete', {}, 404),
                                 ('/apps/hello/prod/connect', {}, 400), ('/apps/hello/prod/connect', {'calls': '--root=/'}, 400),
                                 ('/apps/hello/prod/connect', {'publish': 'Created'}, 400),
                                 ('/apps/hello/prod/connect', {'subscribe': 'created'}, 400),
                                 ('/apps/hello/prod/disconnect', {'role': 'called-by', 'app': 'orders'}, 400),
                                 ('/catalogue/prod/use', {'calls': 'orders'}, 400), ('/catalogue/prod/use', {'app': '../hello', 'calls': 'orders'}, 404),
                                 ('/catalogue/dev/use', {'app': 'hello', 'calls': 'orders'}, 404),
                                 ('/apps/hello/prod/calls', {}, 404), ('/apps/hello/prod/events', {}, 404),
                                 ('/apps/Hello/prod/remove', {}, 404), ('/apps/hello/dev/remove', {}, 404)):
            with self.subTest(path=path, form=form):
                self.assertEqual(c.post(path, data=form, headers=WHO).status_code, code)
        c, _ = self.client(runner, github=False)
        self.assertEqual(c.post('/apps/hello/prod/remove', headers=WHO).status_code, 409)
        self.assertEqual(runner.calls, [])

    def test_promotion_refuses_missing_or_stale_review_and_unhealthy_staging(self):
        good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
        cases = (({}, good, 'review the image'),
                 ({**PROMOTION, 'image': IMAGE.replace('2.0', '1.0')}, good, 'changed'),
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
        good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
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

    def test_a_commit_elsewhere_does_not_hold_a_promotion_and_one_to_staging_does(self):
        """Staging's unit is one commit behind main: GitHub says which files that commit changed."""
        good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
        newer = 'fed9876' + '0' * 33
        behind = replace(good, desired_revision='main@sha1:' + newer, unit=('Unknown', 'Reconciliation in progress'),
                         unit_reason='Progressing')
        compare = f'GET /compare/{HEAD}...{newer}'
        elsewhere = {'status': 'ahead', 'files': [{'filename': 'docs/apps.md'}, {'filename': 'apps/orders/staging/helmrelease.yaml'}]}
        cases = ((elsewhere, ''),
                 ({'status': 'ahead', 'files': [{'filename': 'apps/hello/staging/helmrelease.yaml'}]}, 'a change to staging is in Git'),
                 ({'status': 'ahead', 'files': [{'filename': 'apps/hello/old.yaml', 'previous_filename': 'apps/hello/staging/x.yaml'}]},
                  'a change to staging is in Git'),
                 ({'status': 'diverged', 'files': []}, 'Flux is still applying'),
                 ({'status': 'ahead', 'files': [{'filename': f'docs/{n}.md'} for n in range(300)]}, 'Flux is still applying'),
                 (None, 'Flux is still applying'))
        for answer, problem in cases:
            with self.subTest(answer=str(answer)[:60]):
                runner = tree_fake().on('kubectl', 'get', 'namespace', stdout='{}')  # the app page reads its team
                c, jobs = self.client(runner)
                if answer is not None:
                    self.api.also[compare] = answer
                with mock.patch.object(ops, 'gather_status', return_value=behind):
                    review = c.get('/apps/hello/staging/promotion', headers=WHO)
                    page = c.get('/apps/hello/staging', headers=WHO)
                    response = c.post('/apps/hello/staging/promote', data=PROMOTION, headers=WHO)
                if problem:
                    self.assertEqual((review.status_code, response.status_code), (409, 400))
                    self.assertIn(problem, review.text)
                    self.assertIn(problem, page.text)
                    self.assertIn(problem, response.text)
                    self.assertEqual((jobs.recent(), self.api.sent('POST /pulls')), ([], []))
                else:
                    self.assertEqual(review.status_code, 200, review.text)
                    self.assertIn('See exactly what would go to production', page.text)
                    self.assertEqual(jobs.get(1).state, 'succeeded', jobs.get(1).lines)
                    self.assertIn(f'--source-unchanged={HEAD}..{newer}', self.tool_call(runner),
                                  "the clone's own check is told what was compared: it has no history")
                    self.assertEqual(len(self.api.sent('POST /pulls')), 1)
        c, _ = self.client(tree_fake())
        with mock.patch.object(ops, 'gather_status', return_value=good):
            c.get('/apps/hello/staging/promotion', headers=WHO)
        self.assertFalse([r for r in self.api.requests if '/compare/' in r.url.path], 'an applied staging asks GitHub nothing more')

    def test_review_page_submits_the_displayed_image_and_configuration(self):
        good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
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
            self.assertIn('Manual merge required: held by you', response.text)
            self.assertIn('action="/promotion-prs/9/resume"', response.text)
            self.assertNotIn('action="/promotion-prs/9/hold"', response.text, 'only the control that applies now')
            self.assertEqual(fresh_jobs.recent(), [])
        self.assertFalse(any(c[:3] == (sys.executable, '-m', 'swhurl') for c in runner.calls))
        self.assertEqual(self.api.sent('POST /pulls'), [])

    def test_an_open_pr_says_who_merges_it_and_offers_the_one_control_that_applies(self):
        def pr(branch='console/promote-hello-staging-abcdef0', labels=(), meta=None):
            body = '<!-- swhurl-promotion ' + json.dumps(meta) + ' -->' if meta else ''
            return changes.PullRequest(9, 'promote hello', 'https://github.com/x/pull/9', branch, '2026-10-03', body, labels)
        mode = server.promotion.merge_mode
        self.assertEqual(mode(pr(labels=('auto-merge',), meta={'image': IMAGE})), ('Auto-merge after checks', 'hold'))
        self.assertEqual(mode(pr(meta={'image': IMAGE})), ('Manual merge required: held by you', 'resume'))
        self.assertEqual(mode(pr(meta={'image': IMAGE, 'setup': True})),
                         ('Manual merge required: setup needs your review', ''), 'setup is never automatic')
        self.assertEqual(mode(pr(labels=('promotion-review-required',), meta={'image': IMAGE})),
                         ('Manual merge required: auto-merge was refused', ''))
        self.assertEqual(mode(pr('console/new-hello-staging-abcdef0', ('auto-merge',))), ('Auto-merge after checks', ''),
                         'only a promotion can be held')
        self.assertEqual(mode(pr('console/scale-hello-prod-abcdef0')), ('Manual merge required', ''))
        manual = changes.PullRequest(9, 'new hello', 'https://github.com/x/pull/9',
                                     'console/new-hello-staging-abcdef0', '2026-10-03',
                                     '**Merge plan:** Manual merge required: this pull request also changes existing app settings for orders.')
        self.assertEqual(mode(manual),
                         ('Manual merge required: this pull request also changes existing app settings for orders.', ''))

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
        good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))
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
"""


def ghcr_opener(request):
    if '/contents/copier.yml?ref=' in request.full_url:
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
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None)
        self.assertIn('ended failure: https://run/1', str(failed.exception))
        self.assertIn(add, str(failed.exception))
        ticks = iter(range(0, 10_000, 100))
        client = repos.AppRepos(repo_runner(), FakeAppGitHub(runs=[None]).config)
        with self.assertRaises(actions.ActionError) as late:
            repos.create_app_repo(client.runner, client, self.job(), repo.Request('notes'), sleep=lambda _: None,
                                  clock=lambda: next(ticks))
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
        page = self.client().get('/new', headers=WHO).text
        self.assertEqual(page.count('>notification channel</span>'), 1, 'payments, on New app')
        self.assertIn('>no notification channel</span>', page)

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


class NewRepoRouteTests(unittest.TestCase):
    def test_the_form_creates_the_repository_then_opens_the_platform_pr(self):
        runner = repo_runner(tree_fake())
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        platform_api, app_api = FakeGitHub(), FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'success'}])
        c = TestClient(app_under_test(runner, jobs=jobs, github=platform_api.github, app_repos=app_api.config))
        page = c.get('/new', headers=WHO).text
        self.assertIn('What happens', page)
        self.assertNotIn('Deploy an existing image', page)
        self.assertIn('action="/new/repo"', page)
        self.assertIn('name="stack" value="typescript" checked', page)
        self.assertIn('name="feature-kind" value="web" checked', page)
        self.assertIn('name="feature-database" value="sqlite" >', page)
        self.assertIn('none or sqlite', page, "the template's own help text")
        response = c.post('/new/repo', data={'name': 'notes', 'stack': 'typescript', 'team': 'platform', 'description': 'Take notes',
                                             'exposure': 'authenticated-web', 'host': ''}, headers=WHO,
                          follow_redirects=False)
        self.assertEqual((response.status_code, response.headers['location']), (303, '/jobs/1'))
        job = jobs.get(1)
        self.assertEqual((job.state, job.link, job.unit), ('succeeded', 'https://github.com/x/pull/7', 'notes/staging'),
                         '\n'.join(job.lines))
        app_new = next(c for c in runner.calls if c[3:4] == ('app-new',))
        self.assertEqual(app_new[4:], ('notes', '--from-repo=samclement/notes', '--env=staging',
                                       f'--image=ghcr.io/samclement/notes:1-9f8e7d6@{DIGEST}',
                                       '--team=platform', '--exposure=authenticated-web'))
        copy = next(c for c in runner.calls if 'copy' in c)
        self.assertIn('kind=web', copy)
        self.assertIn('database=none', copy)
        self.assertEqual(platform_api.sent('POST /issues/7/labels'), [{'labels': ['auto-merge']}], 'a new staging app')
        self.assertIn('https://github.com/x/pull/7', c.get('/jobs/1', headers=WHO).text)
        body = platform_api.sent('POST /pulls')[0]['body']
        self.assertIn('created https://github.com/samclement/notes from', body)

    def test_a_new_app_that_connects_passes_its_choices_and_waits_for_review(self):
        staging = (staging_app('orders', {'SWHURL_EVENTS_PUBLISH': 'created'}), staging_app('billing'))
        runner = repo_runner(tree_fake(staging=staging))
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        platform_api, app_api = FakeGitHub(), FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'success'}])
        c = TestClient(app_under_test(runner, jobs=jobs, github=platform_api.github, app_repos=app_api.config))
        form = 'name=notes&stack=typescript&team=payments&calls=orders&calls=billing&subscribe=orders.created&publish=sent'
        response = c.post('/new/repo', content=form, headers={**WHO, 'content-type': 'application/x-www-form-urlencoded'},
                          follow_redirects=False)
        self.assertEqual(response.status_code, 303, response.text)
        self.assertEqual(jobs.get(1).state, 'succeeded', '\n'.join(jobs.get(1).lines))
        app_new = next(c for c in runner.calls if c[3:4] == ('app-new',))
        self.assertEqual(app_new[7:], ('--image=ghcr.io/samclement/notes:1-9f8e7d6@' + DIGEST, '--team=payments',
                                       '--calls=orders,billing', '--subscribe=orders.created', '--publish=sent'))
        self.assertEqual(platform_api.sent('POST /issues/7/labels'), [],
                         'it changes other apps, so it is not labelled to merge itself')
        refused = c.post('/new/repo', content='name=notes2&stack=typescript&team=platform&calls=stranger',
                         headers={**WHO, 'content-type': 'application/x-www-form-urlencoded'})
        self.assertEqual(refused.status_code, 400)
        self.assertIn('stranger: not a web app, or an event, that staging has now', refused.text)
        self.assertEqual(len(app_api.writes()), len([w for w in app_api.writes() if 'notes2' not in w[1]]),
                         'no repository is created for a refused form')

    def test_a_worker_with_sqlite_gets_no_route_and_its_answers(self):
        runner = repo_runner(tree_fake())
        jobs = actions.Jobs(runner, audit=lambda line: None, inline=True)
        c = TestClient(app_under_test(runner, jobs=jobs, github=FakeGitHub().github, app_repos=FakeAppGitHub(runs=[{'status': 'completed', 'conclusion': 'success'}]).config))
        c.post('/new/repo', data={'name': 'notes', 'stack': 'typescript', 'team': 'platform', 'feature-kind': 'worker',
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
                response = c.post('/new/repo', data={'name': 'notes2', 'stack': 'typescript', 'team': 'platform', **form}, headers=WHO)
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
        self.assertEqual(page.count('<section class="card features"'), 2, 'one Features set per stack')
        c.post('/new/repo', data={'name': 'notes', 'stack': 'kotlin', 'team': 'platform', 'feature-kind': 'web', 'feature-database': 'sqlite'},
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
        for form, message in (({'name': 'Notes', 'stack': 'typescript', 'team': 'platform'}, 'DNS label'),
                              ({'name': 'notes', 'stack': 'cobol'}, 'stack must be one of'),
                              ({'name': 'notes', 'stack': 'typescript', 'team': 'platform', 'host': 'bad host'}, 'not a DNS name')):
            with self.subTest(message=message):
                response = c.post('/new/repo', data=form, headers=WHO)
                self.assertEqual(response.status_code, 400)
                self.assertIn(message, response.text)
        c = TestClient(app_under_test(runner, jobs=actions.Jobs(runner, inline=True), github=FakeGitHub().github))
        self.assertIn('No token for creating repositories', c.get('/new', headers=WHO).text)
        self.assertEqual(c.post('/new/repo', data={'name': 'notes', 'stack': 'typescript', 'team': 'platform'}, headers=WHO).status_code, 409)
        self.assertTrue(all(call[0] == 'kubectl' and 'get' in call for call in runner.calls), 'only the form\'s reads')
        self.assertEqual(app_api.requests, [])

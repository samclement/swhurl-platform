"""make app-repo: refusal, render and push order, waiting for the first run, the GHCR digest; offline."""
import json
import unittest
import urllib.error

from swhurl.apps import repo
from swhurl.apps.contract import (
    STACK_REVISIONS,
)
from swhurl.run import FakeRunner, Result

DIGEST = 'sha256:' + 'c' * 64
SHA = 'abcdef0123456789'


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
"""


def ghcr(status=None, seed=False):
    """A GHCR that serves every tag except a seed (0-<sha>), unless ``seed``."""
    seen = []

    def open_(request):
        seen.append(request)
        if '/contents/copier.yml?ref=' in request.full_url:
            return {}, COPIER_YML.encode()
        if '/manifests/0-' in request.full_url and not seed:
            raise urllib.error.HTTPError(request.full_url, 404, 'not found', {}, None)
        if status:
            raise urllib.error.HTTPError(request.full_url, status, 'denied', {}, None)
        if 'token' in request.full_url:
            return {}, json.dumps({'token': 'anon'}).encode()
        return {'Docker-Content-Digest': DIGEST}, b''
    return open_, seen


class Cluster:
    def __init__(self, exists=False, runs=None, token=True):
        self.hooks = []
        self.runs = list(runs or [[], [{'number': 1, 'headSha': SHA, 'status': 'in_progress', 'conclusion': '',
                                        'url': 'u'}],
                                  [{'number': 1, 'headSha': SHA, 'status': 'completed', 'conclusion': 'success',
                                    'url': 'u'}]])
        r = self.runner = FakeRunner()
        r.on('gh', 'repo', 'view', returncode=0 if exists else 1)
        r.on('uvx', stdout='')
        r.on('copier', stdout='')
        r.on('git', handler=lambda args, _: Result(args, 0, SHA + '\n' if 'rev-parse' in args else ''))
        r.on('gh', 'repo', 'create', stdout='')
        r.on('sops', 'decrypt', stdout='stringData: {token: fixture-webhook-token}\n', returncode=0 if token else 1)
        r.on('gh', 'api', 'repos/samclement/notes/hooks', stdout='[]')
        r.on('gh', 'api', '--method', 'POST', 'repos/samclement/notes/hooks', handler=self.add_hook)
        r.on('gh', 'run', 'list', handler=self.run_list)

    def add_hook(self, args, body):
        self.hooks.append(json.loads(body))
        return Result(args, 0, '{}')

    def run_list(self, args, _):
        return Result(args, 0, json.dumps(self.runs.pop(0) if len(self.runs) > 1 else self.runs[0]))


class AppRepoTests(unittest.TestCase):
    def create(self, cluster, req=None, opener=None):
        lines = []
        image = repo.create(cluster.runner, req or repo.Request('notes'), out=lines.append, sleep=lambda _: None,
                            opener=opener or ghcr()[0])
        return image, lines

    def test_renders_creates_pushes_and_prints_the_first_image(self):
        cluster = Cluster()
        opener, seen = ghcr()
        image, lines = self.create(cluster, repo.Request('notes', description='Take notes'), opener)
        self.assertEqual(image, f'ghcr.io/samclement/notes:1-abcdef0@{DIGEST}')
        verbs = [c[1] if c[0] == 'git' and c[1] != '-C' else (c[3] if c[0] == 'git' else ' '.join(c[:3]))
                 for c in cluster.runner.calls if c[:3] != ('gh', 'run', 'list')]
        self.assertEqual(verbs[2:], ['init', 'add', 'commit', 'rev-parse', 'gh repo create', 'push', 'sops decrypt ' + verbs[-3].split()[-1],
                                     'gh api repos/samclement/notes/hooks', 'gh api --method'])
        self.assertEqual([h['events'] for h in cluster.hooks], [['package', 'registry_package']])
        self.assertIn('[OK] Added the image webhook: Flux scans as soon as an image is published', lines)
        self.assertNotIn('fixture-webhook-token', ' '.join(' '.join(c) for c in cluster.runner.calls))
        copy = next(c for c in cluster.runner.calls if 'copy' in c)
        self.assertIn('app_name=notes', copy)
        self.assertEqual(copy[copy.index('--vcs-ref') + 1], STACK_REVISIONS['typescript'])
        self.assertTrue(any(r.full_url.endswith('?ref=' + STACK_REVISIONS['typescript']) for r in seen))
        self.assertIn('description=Take notes', copy)
        self.assertIn('https://github.com/samclement/swhurl-app-template-typescript.git', copy)
        push = next(c for c in cluster.runner.calls if 'push' in c)
        self.assertIn('ssh://git@github.com/samclement/notes.git', push)
        self.assertIn('--public', next(c for c in cluster.runner.calls if c[:3] == ('gh', 'repo', 'create')))
        manifest = seen[-1]
        self.assertEqual((manifest.get_method(), manifest.full_url),
                         ('HEAD', 'https://ghcr.io/v2/samclement/notes/manifests/1-abcdef0'))
        self.assertIn('application/vnd.oci.image.index.v1+json', manifest.get_header('Accept'))
        self.assertIn(f'make app-new NAME=notes ARGS="--from-repo samclement/notes --env staging --team <team> --image {image}"',
                      lines[-1])

    def test_a_seed_published_first_is_the_first_image(self):
        cluster = Cluster(runs=[[{'number': 1, 'headSha': SHA, 'status': 'in_progress', 'conclusion': '', 'url': 'u'}]])
        opener, seen = ghcr(seed=True)
        image, lines = self.create(cluster, opener=opener)
        self.assertEqual(image, f'ghcr.io/samclement/notes:0-abcdef0@{DIGEST}')
        self.assertEqual(seen[-1].full_url, 'https://ghcr.io/v2/samclement/notes/manifests/0-abcdef0')
        self.assertIn('seed', lines[-3])
        self.assertIn(f'--image {image}"', lines[-1])
        self.assertNotIn(('gh', 'run', 'list'), [c[:3] for c in cluster.runner.calls], 'no need to wait for the build')

    def test_an_unreadable_webhook_token_is_a_warning_not_a_failure(self):
        cluster = Cluster(token=False)
        image, lines = self.create(cluster)
        self.assertTrue(image.startswith('ghcr.io/samclement/notes:1-'))
        self.assertEqual(cluster.hooks, [])
        self.assertTrue(any(line.startswith('[WARN] no image webhook') and 'make app-hooks' in line for line in lines), lines)

    def test_refuses_an_existing_repository_or_a_bad_name_before_any_change(self):
        for req, cluster, message in ((repo.Request('notes'), Cluster(exists=True), 'already exists'),
                                      (repo.Request('Notes'), Cluster(), 'DNS label'),
                                      (repo.Request('notes', stack='cobol'), Cluster(), 'STACK must be')):
            with self.subTest(message=message), self.assertRaisesRegex(repo.RepoError, message):
                self.create(cluster, req)
            self.assertFalse([c for c in cluster.runner.calls if c[0] in ('uvx', 'copier', 'git') or 'create' in c])

    def test_a_failed_first_run_or_a_private_package_is_explained(self):
        failed = Cluster(runs=[[{'number': 1, 'headSha': SHA, 'status': 'completed', 'conclusion': 'failure',
                                 'url': 'https://github.com/samclement/notes/actions/runs/1'}]])
        with self.assertRaisesRegex(repo.RepoError, 'ended failure: https://github.com/samclement/notes/actions/runs/1'):
            self.create(failed)
        with self.assertRaisesRegex(repo.RepoError, 'is the package private'):
            self.create(Cluster(), opener=ghcr(status=401)[0])

    def test_answers_are_the_templates_own_questions(self):
        questions = repo.parse_questions(COPIER_YML)
        self.assertEqual([(q.name, q.choices, q.default) for q in questions],
                         [('kind', ('web', 'worker'), 'web'), ('database', ('none', 'sqlite'), 'none'),
                      ('api', ('none', 'openapi'), 'none'), ('events', ('none', 'asyncapi'), 'none'),
                      ('flags', ('none', 'example'), 'none')])
        self.assertEqual(repo.parse_answers('kind=worker database=sqlite'), {'kind': 'worker', 'database': 'sqlite'})
        cluster = Cluster()
        self.create(cluster, repo.Request('notes', answers={'kind': 'worker', 'database': 'sqlite'}))
        copy = next(c for c in cluster.runner.calls if 'copy' in c)
        self.assertEqual([copy[i + 1] for i, a in enumerate(copy) if a == '--data'],
                         ['app_name=notes', 'database=sqlite', 'kind=worker'])
        for answers, message in (({'kind': 'cron'}, 'kind must be one of web, worker'),
                                 ({'colour': 'red'}, "unknown question 'colour'")):
            cluster = Cluster()
            with self.subTest(message=message), self.assertRaisesRegex(repo.RepoError, message):
                self.create(cluster, repo.Request('notes', answers=answers))
            self.assertEqual(cluster.runner.calls, [], 'refused before anything ran')
        with self.assertRaisesRegex(repo.RepoError, 'question=choice'):
            repo.parse_answers('kind')

    def test_a_type_sets_the_answers_the_stack_and_where_staging_starts(self):
        warnings = []
        api = repo.from_environment('weather', {'TYPE': 'api', 'STACK': 'kotlin', 'DESCRIPTION': 'Weather'}, warnings.append)
        self.assertEqual((api.stack, api.exposure, api.description), ('kotlin', 'private', 'Weather'))
        self.assertEqual(api.answers, {'kind': 'web', 'database': 'sqlite', 'api': 'openapi', 'events': 'asyncapi', 'flags': 'example'})
        self.assertEqual(repo.from_environment('site', {'TYPE': 'web-app'}, warnings.append).stack, 'typescript',
                         "the type's first stack")
        self.assertEqual(warnings, [])
        tweaked = repo.from_environment('weather', {'TYPE': 'api', 'ANSWERS': 'database=none'}, warnings.append)
        self.assertEqual(tweaked.answers['database'], 'none')
        self.assertEqual(len(warnings), 1)
        self.assertIn("database=none: a combination the template's CI may not build", warnings[0])
        self.assertEqual(repo.from_environment('w', {'ANSWERS': 'kind=worker'}, warnings.append).answers, {'kind': 'worker'})
        for environ, message in (({}, 'TYPE is required: one of web-app, api, worker'),
                                 ({'TYPE': 'cron'}, 'TYPE must be one of'),
                                 ({'TYPE': 'web-app', 'STACK': 'kotlin'}, 'a Web app is built with typescript, not kotlin')):
            with self.subTest(environ=environ), self.assertRaisesRegex(repo.RepoError, message):
                repo.from_environment('w', environ, warnings.append)

    def test_dry_run_creates_nothing(self):
        cluster = Cluster()
        cluster.runner.dry_run = True
        _, lines = self.create(cluster)
        self.assertEqual(cluster.runner.calls, [('gh', 'repo', 'view', 'samclement/notes', '--json', 'name')])
        self.assertEqual(lines[0], 'Plan (app-repo samclement/notes):')


if __name__ == '__main__':
    unittest.main()

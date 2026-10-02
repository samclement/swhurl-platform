"""make app-repo: refusal, render and push order, waiting for the first run, the GHCR digest; offline."""
import json
import unittest
import urllib.error

from swhurl.apps import repo
from swhurl.run import FakeRunner, Result

DIGEST = 'sha256:' + 'c' * 64
SHA = 'abcdef0123456789'


def ghcr(status=None):
    seen = []

    def open_(request):
        seen.append(request)
        if status:
            raise urllib.error.HTTPError(request.full_url, status, 'denied', {}, None)
        if 'token' in request.full_url:
            return {}, json.dumps({'token': 'anon'}).encode()
        return {'Docker-Content-Digest': DIGEST}, b''
    return open_, seen


class Cluster:
    def __init__(self, exists=False, runs=None):
        self.runs = list(runs or [[], [{'number': 1, 'headSha': SHA, 'status': 'in_progress', 'conclusion': '',
                                        'url': 'u'}],
                                  [{'number': 1, 'headSha': SHA, 'status': 'completed', 'conclusion': 'success',
                                    'url': 'u'}]])
        r = self.runner = FakeRunner()
        r.on('gh', 'repo', 'view', returncode=0 if exists else 1)
        r.on('uvx', stdout='')
        r.on('copier', stdout='')
        r.on('git', stdout='')
        r.on('gh', 'repo', 'create', stdout='')
        r.on('gh', 'run', 'list', handler=self.run_list)

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
        self.assertEqual(verbs[2:], ['init', 'add', 'commit', 'gh repo create', 'push'])
        copy = next(c for c in cluster.runner.calls if 'copy' in c)
        self.assertIn('app_name=notes', copy)
        self.assertIn('description=Take notes', copy)
        self.assertIn('https://github.com/samclement/swhurl-app-template-typescript.git', copy)
        push = next(c for c in cluster.runner.calls if 'push' in c)
        self.assertIn('ssh://git@github.com/samclement/notes.git', push)
        self.assertIn('--public', next(c for c in cluster.runner.calls if c[:3] == ('gh', 'repo', 'create')))
        manifest = seen[-1]
        self.assertEqual((manifest.get_method(), manifest.full_url),
                         ('HEAD', 'https://ghcr.io/v2/samclement/notes/manifests/1-abcdef0'))
        self.assertIn('application/vnd.oci.image.index.v1+json', manifest.get_header('Accept'))
        self.assertIn(f'make app-new NAME=notes ARGS="--from-repo samclement/notes --env staging --image {image}"',
                      lines[-1])

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

    def test_dry_run_creates_nothing(self):
        cluster = Cluster()
        cluster.runner.dry_run = True
        _, lines = self.create(cluster)
        self.assertEqual(cluster.runner.calls, [('gh', 'repo', 'view', 'samclement/notes', '--json', 'name')])
        self.assertEqual(lines[0], 'Plan (app-repo samclement/notes):')


if __name__ == '__main__':
    unittest.main()

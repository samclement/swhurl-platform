"""make app-hooks: the image webhook is created, repointed or left alone; the token stays off command lines."""
import hashlib
import json
import unittest

from swhurl.apps import hooks
from swhurl.run import FakeRunner, Result

TOKEN = 'fixture-image-webhook-token'
HOST = 'flux-webhook.example.test'


class Repo:
    def __init__(self, existing=None):
        self.sent = []
        r = self.runner = FakeRunner()
        r.on('gh', 'api', 'repos/samclement/notes/hooks', stdout=json.dumps(existing or []))
        r.on('gh', 'api', '--method', handler=self.write)

    def write(self, args, body):
        self.sent.append((args[3], args[4], json.loads(body)))
        return Result(args, 0, '{}')

    def ensure(self):
        return hooks.ensure(self.runner, 'samclement/notes', TOKEN, HOST)


class AppHooksTests(unittest.TestCase):
    def test_the_url_is_the_path_flux_derives_for_the_receiver(self):
        digest = hashlib.sha256(f'{TOKEN}app-imagesflux-system'.encode()).hexdigest()
        self.assertEqual(hooks.hook_url(TOKEN, HOST), f'https://{HOST}/hook/{digest}')

    def test_creates_a_missing_webhook_with_the_token_on_stdin_only(self):
        repo = Repo(existing=[{'id': 1, 'active': True, 'events': ['push'], 'config': {'url': 'https://elsewhere/x'}}])
        self.assertEqual(repo.ensure(), 'created')
        (method, path, body), = repo.sent
        self.assertEqual((method, path), ('POST', 'repos/samclement/notes/hooks'))
        self.assertEqual(body['events'], ['package', 'registry_package'])
        self.assertEqual(body['config'], {'url': hooks.hook_url(TOKEN, HOST), 'content_type': 'json', 'secret': TOKEN,
                                          'insecure_ssl': '0'})
        self.assertFalse([c for c in repo.runner.calls if any(TOKEN in a for a in c)])
        self.assertEqual(repo.runner.redact(f'x {TOKEN}'), 'x <redacted>')

    def test_leaves_a_correct_webhook_and_repoints_a_stale_or_disabled_one(self):
        good = {'id': 7, 'active': True, 'events': ['registry_package', 'package'],
                'config': {'url': hooks.hook_url(TOKEN, HOST)}}
        repo = Repo(existing=[good])
        self.assertEqual(repo.ensure(), 'ok')
        self.assertEqual(repo.sent, [])
        for name, stale in {'rotated token': {'config': {'url': hooks.hook_url('old', HOST)}},
                            'disabled': {'active': False}, 'events': {'events': ['push']}}.items():
            with self.subTest(name=name):
                repo = Repo(existing=[{**good, **stale}])
                self.assertEqual(repo.ensure(), 'updated')
                (method, path, body), = repo.sent
                self.assertEqual((method, path), ('PATCH', 'repos/samclement/notes/hooks/7'))
                self.assertEqual((body['active'], body['config']['url']), (True, hooks.hook_url(TOKEN, HOST)))

    def test_a_dry_run_changes_nothing(self):
        repo = Repo()
        repo.runner.dry_run = True
        self.assertEqual(repo.ensure(), 'created')
        self.assertEqual(repo.sent, [])
        self.assertNotIn(TOKEN, '\n'.join(repo.runner.echoed))

    def test_repositories_come_from_the_scanned_images(self):
        found = hooks.auto_deploy_repositories()
        self.assertEqual(found.get('hello-ts'), 'samclement/hello-ts')

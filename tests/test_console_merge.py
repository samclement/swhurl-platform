"""Auto-merge gates preserve reviewed settings and never push on failed or stale checks."""
import json
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from swhurl.apps import new, policy, promotion
from swhurl.console import merge
from swhurl.console import promotion as console
from swhurl.run import CommandError, FakeRunner, Result

BASE = 'a' * 40
HEAD = 'b' * 40
REPO = 'samclement/swhurl-platform'
IMAGE = 'ghcr.io/example/app:2-abcdef0@sha256:' + 'a' * 64


class MergeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / 'clusters/home').mkdir(parents=True)
        (self.root / 'clusters/home/kustomization.yaml').write_text('resources: []\n')
        for env in ('staging', 'prod'):
            image = IMAGE if env == 'staging' else IMAGE.replace('2-', '1-').replace('a' * 64, 'c' * 64)
            args = new.parse_args(['example', '--preset', 'swhurl-web', '--env', env, '--no-auto-deploy', '--image', image])
            new.generate(args, self.root)
        self.meta = console.metadata(self.root, 'example', IMAGE, False)
        self.pr = {'state': 'open', 'head': {'ref': 'console/promote-example-staging-aaaaaaa', 'sha': HEAD,
                                           'repo': {'full_name': REPO}}, 'base': {'ref': 'main'},
                   'labels': [{'name': 'auto-merge'}],
                   'body': '<!-- swhurl-promotion ' + json.dumps(self.meta) + ' -->'}
        self.files = ['apps/example/prod/helmrelease.yaml']

    def runner(self, *, validation=0, latest=None, current=BASE, passed=True):
        runner = FakeRunner()
        reads = 0

        def pr(args, _):
            nonlocal reads
            reads += 1
            return Result(args, stdout=json.dumps(latest if reads > 1 and latest is not None else self.pr))

        def worktree(args, _):
            dst = Path(args[-2])
            shutil.copytree(self.root, dst)
            if dst.name == 'candidate':
                source, target = promotion.read(dst, 'example')
                if target is None:
                    with mock.patch.object(policy, 'evaluate', return_value=[]):
                        source['spec']['values']['controllers']['main']['containers']['main']['image'] = new.parse_image(IMAGE)
                        promotion.create(dst, 'example', source, runner=runner)
                else:
                    document = promotion.YamlFile(dst / 'apps/example/prod/helmrelease.yaml')
                    promotion.image_of(document.data).update(new.parse_image(IMAGE))
                    document.save()
            return Result(args)

        (runner.on('gh', 'api', f'repos/{REPO}/pulls/7', handler=pr)
         .on('gh', 'run', 'list', stdout='[{"databaseId": 1, "status": "completed", "conclusion": "success"}]' if passed else '[]')
         .on('gh', 'pr', 'diff', stdout='\n'.join(self.files))
         .on('git', 'rev-parse', 'origin/main', stdout=BASE)
         .on('git', 'rev-parse', 'FETCH_HEAD', stdout=HEAD)
         .on('git', 'worktree', 'add', handler=worktree)
         .on('git', 'ls-remote', stdout=current + '\trefs/heads/main\n')
         .on('git', stdout='')
         .on('make', 'check', returncode=validation, stderr='validation failed' if validation else ''))
        return runner

    def check(self, runner):
        with mock.patch.object(policy, 'evaluate', return_value=[]):
            merge.merge(runner, self.root, 7, HEAD, REPO)

    def pushes(self, runner):
        return [c for c in runner.calls if c[:3] == ('git', 'push', 'origin')]

    def test_validated_image_update_pushes_the_checked_merge(self):
        runner = self.runner()
        self.check(runner)
        self.assertEqual([c for c in runner.calls if c[:2] == ('git', 'fetch')],
                         [('git', 'fetch', 'origin', 'main'), ('git', 'fetch', 'origin', 'pull/7/head')])
        self.assertEqual(self.pushes(runner)[0], ('git', 'push', 'origin', 'HEAD:refs/heads/main'))
        self.assertLess(runner.calls.index(('make', 'check')), runner.calls.index(self.pushes(runner)[0]))

    def test_head_validation_failure_merge_failure_hold_and_base_race_never_push(self):
        held = {**self.pr, 'labels': []}
        for runner, error in [(self.runner(passed=False), merge.Hold), (self.runner(validation=1), CommandError),
                              (self.runner(latest=held), merge.Hold), (self.runner(current='d' * 40), merge.Hold)]:
            with self.subTest(error=error), self.assertRaises(error):
                self.check(runner)
            self.assertEqual(self.pushes(runner), [])

    def test_destination_or_tooling_change_requires_fresh_review(self):
        path = self.root / 'apps/example/prod/helmrelease.yaml'
        original = path.read_text()
        path.write_text(original.replace('cpu: 10m', 'cpu: 75m'))
        runner = self.runner()
        with self.assertRaisesRegex(merge.Hold, 'production changed'):
            self.check(runner)
        self.assertEqual(self.pushes(runner), [])
        path.write_text(original)
        (self.root / 'tools/swhurl/apps').mkdir(parents=True)
        (self.root / 'tools/swhurl/apps/new.py').write_text('# changed generator\n')
        runner = self.runner()
        with self.assertRaisesRegex(merge.Hold, 'tooling, policy'):
            self.check(runner)
        self.assertEqual(self.pushes(runner), [])

    def test_unrelated_changes_and_newer_staging_build_keep_reviewed_image(self):
        (self.root / 'README.md').write_text('unrelated documentation update\n')
        source = self.root / 'apps/example/staging/helmrelease.yaml'
        source.write_text(source.read_text().replace('2-abcdef0', '3-abcdef0').replace('a' * 64, 'd' * 64))
        runner = self.runner()
        self.check(runner)
        self.assertTrue(self.pushes(runner))

    def test_first_production_reproduces_conversion_and_rejects_source_config_change(self):
        shutil.rmtree(self.root / 'apps/example/prod')
        (self.root / 'clusters/home/app-example-prod.yaml').unlink()
        (self.root / 'clusters/home/kustomization.yaml').write_text('resources:\n- app-example-staging.yaml\n')
        self.meta = console.metadata(self.root, 'example', IMAGE, False)
        self.pr['body'] = '<!-- swhurl-promotion ' + json.dumps(self.meta) + ' -->'
        self.files = sorted(['apps/example/prod/' + name for name in ('namespace.yaml', 'helmrelease.yaml', 'kustomization.yaml')]
                            + ['clusters/home/app-example-prod.yaml', 'clusters/home/kustomization.yaml'])
        runner = self.runner()
        self.check(runner)
        self.assertTrue(self.pushes(runner))
        source = self.root / 'apps/example/staging/helmrelease.yaml'
        source.write_text(source.read_text().replace('cpu: 10m', 'cpu: 75m'))
        runner = self.runner()
        with self.assertRaisesRegex(merge.Hold, 'staging configuration changed'):
            self.check(runner)
        self.assertEqual(self.pushes(runner), [])

    def test_setup_infrastructure_and_other_app_files_cannot_merge(self):
        for filename in ('platform/reloader/helmrelease.yaml', 'apps/other/prod/helmrelease.yaml',
                         'apps/example/prod/secret.sops.yaml'):
            self.files = [filename]
            runner = self.runner()
            with self.subTest(filename=filename), self.assertRaises(merge.Hold):
                self.check(runner)
            self.assertEqual(self.pushes(runner), [])
        self.files = ['apps/example/prod/helmrelease.yaml']
        self.meta['setup'] = True
        self.pr['body'] = '<!-- swhurl-promotion ' + json.dumps(self.meta) + ' -->'
        runner = self.runner()
        with self.assertRaisesRegex(merge.Hold, 'setup'):
            self.check(runner)
        self.assertEqual(self.pushes(runner), [])

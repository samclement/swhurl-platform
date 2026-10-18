"""Auto-merge gates preserve reviewed settings and never push on failed or stale checks."""
import contextlib
import io
import json
import shutil
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from swhurl import ROOT
from swhurl.apps import edit, egress, new, policy, promotion
from swhurl.console import merge
from swhurl.console import promotion as console
from swhurl.run import CommandError, FakeRunner, Result

BASE = 'a' * 40
HEAD = 'b' * 40
REPO = 'samclement/swhurl-platform'
IMAGE = 'ghcr.io/example/app:2-abcdef0@sha256:' + 'a' * 64
MANIFESTS = ROOT / 'tests/fixtures/manifests'


class MergeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        (self.root / 'clusters/home').mkdir(parents=True)
        (self.root / 'clusters/home/kustomization.yaml').write_text('resources: []\n')
        for env in ('staging', 'prod'):
            image = IMAGE if env == 'staging' else IMAGE.replace('2-', '1-').replace('a' * 64, 'c' * 64)
            args = new.parse_args(['example', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', env, '--no-auto-deploy', '--image', image])
            new.generate(args, self.root)
        self.meta = console.metadata(self.root, 'example', IMAGE, False)
        self.pr = {'state': 'open', 'head': {'ref': 'console/promote-example-staging-aaaaaaa', 'sha': HEAD,
                                           'repo': {'full_name': REPO}}, 'base': {'ref': 'main'},
                   'labels': [{'name': 'auto-merge'}],
                   'body': '<!-- swhurl-promotion ' + json.dumps(self.meta) + ' -->'}
        self.files = ['apps/example/prod/helmrelease.yaml']
        self.candidate_files: dict[str, str] = {}  # written into the merge result only (what the PR adds)
        self.sync_changes = ''  # what `git status --porcelain` shows after make app-sync on the merge result
        self.unmerged = ''  # what `git diff --diff-filter=U` lists after a new app's merge onto main
        self.after_candidate = None  # edits the merge result after the PR's files are in it
        self.candidate_dir: Path | None = None

    def runner(self, *, validation=0, latest=None, current=BASE, passed=True, validate=None):
        """``validate``: what each successive read of the PR's Validate run returns (the last one repeats)."""
        runner = FakeRunner()
        reads = 0
        done = '[{"databaseId": 1, "status": "completed", "conclusion": "success"}]'
        answers = list(validate if validate is not None else [done if passed else
                                                              done.replace('success', 'failure')])

        def validate_run(args, _):
            return Result(args, stdout=answers.pop(0) if len(answers) > 1 else answers[0])

        def pr(args, _):
            nonlocal reads
            reads += 1
            return Result(args, stdout=json.dumps(latest if reads > 1 and latest is not None else self.pr))

        def worktree(args, _):
            dst = Path(args[-2])
            shutil.copytree(self.root, dst)
            if dst.name == 'candidate':
                if self.pr['head']['ref'].startswith('console/new-fresh-'):
                    fresh = new.parse_args(['fresh', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', 'staging',
                                            '--no-auto-deploy', '--image', IMAGE])
                    new.generate(fresh, dst)
                    for rel, text in self.candidate_files.items():
                        (dst / rel).parent.mkdir(parents=True, exist_ok=True)
                        (dst / rel).write_text(text)
                    if self.after_candidate:
                        self.after_candidate(dst)
                    self.candidate_dir = dst
                    return Result(args)
                staging = promotion.YamlFile(dst / 'apps/example/staging/helmrelease.yaml')
                promotion.image_of(staging.data).update(new.parse_image(IMAGE))  # the reviewed image, whatever staging runs now
                staging.save()
                with contextlib.redirect_stdout(io.StringIO()):
                    edit.promote(dst, 'example', runner=runner)
                shutil.copyfile(self.root / 'apps/example/staging/helmrelease.yaml', dst / 'apps/example/staging/helmrelease.yaml')
                if self.after_candidate:
                    self.after_candidate(dst)
            return Result(args)

        (runner.on('gh', 'api', f'repos/{REPO}/pulls/7', handler=pr)
         .on('gh', 'run', 'list', handler=validate_run)
         .on('gh', 'pr', 'diff', stdout='\n'.join(self.files))
         .on('git', 'rev-parse', 'origin/main', stdout=BASE)
         .on('git', 'rev-parse', 'FETCH_HEAD', stdout=HEAD)
         .on('git', 'worktree', 'add', handler=worktree)
         .on('git', 'ls-remote', stdout=current + '\trefs/heads/main\n')
         .on('git', 'status', '--porcelain', stdout=self.sync_changes)
         .on('git', 'diff', '--name-only', '--diff-filter=U', stdout=self.unmerged)
         .on('git', stdout='')
         .on('make', returncode=validation, stderr='validation failed' if validation else ''))
        return runner

    def check(self, runner):
        self.slept = []
        with mock.patch.object(policy, 'evaluate', return_value=[]):
            merge.merge(runner, self.root, 7, HEAD, REPO, sleep=self.slept.append)

    def pushes(self, runner):
        return [c for c in runner.calls if c[:3] == ('git', 'push', 'origin')]

    def test_validated_image_update_pushes_the_checked_merge(self):
        runner = self.runner()
        self.check(runner)
        self.assertEqual([c for c in runner.calls if c[:2] == ('git', 'fetch')],
                         [('git', 'fetch', 'origin', 'main'), ('git', 'fetch', 'origin', 'pull/7/head')])
        self.assertEqual(self.pushes(runner)[0], ('git', 'push', 'origin', 'HEAD:refs/heads/main'))
        checks = ('make', '-j2', '--output-sync=target', 'check-repo', 'check-apps')
        self.assertEqual([c for c in runner.calls if c[0] == 'make'], [checks])
        self.assertLess(runner.calls.index(checks), runner.calls.index(self.pushes(runner)[0]))

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

    def test_a_promotion_may_carry_what_the_image_offers_and_nothing_else(self):
        """The documents read at the reviewed image's commit merge with it; a change to what it publishes waits."""
        from swhurl.apps import edit
        api = 'openapi: 3.0.1\npaths: {}\n'
        with contextlib.redirect_stdout(io.StringIO()):
            edit.write_offers(self.root, 'example', 'staging', 'example/app', 'abcdef0', {'openapi.yaml': api}, None,
                              api_path='/openapi.yaml')  # production gets where the image serves it, too
        self.meta = console.metadata(self.root, 'example', IMAGE, False)
        self.pr['body'] = '<!-- swhurl-promotion ' + json.dumps(self.meta) + ' -->'
        self.files = ['apps/example/prod/helmrelease.yaml', 'apps/example/prod/offers/openapi.yaml', 'apps/example/prod/offers/source.yaml']
        carried = self.runner()
        self.check(carried)
        self.assertTrue(self.pushes(carried))

        def another_commit(dst):
            path = dst / 'apps/example/prod/offers/source.yaml'
            path.write_text(path.read_text().replace('abcdef0', '1234567'))

        def another_document(dst):
            (dst / 'apps/example/prod/offers/openapi.yaml').write_text(api + '# edited\n')

        def publishes_more(dst):
            path = dst / 'apps/example/prod/helmrelease.yaml'
            path.write_text(path.read_text().replace('SWHURL_APP: example', 'SWHURL_APP: example\n              SWHURL_EVENTS_PUBLISH: created'))
        for change, rel in ((another_commit, 'offers/source.yaml'), (another_document, 'offers/openapi.yaml'),
                            (publishes_more, 'helmrelease.yaml')):
            self.after_candidate = change
            with self.subTest(change=change.__name__), self.assertRaisesRegex(
                    merge.Hold, f'differs from the promotion replayed on main: apps/example/prod/{rel}'):
                held = self.runner()
                self.check(held)
            self.assertEqual(self.pushes(held), [])

    def test_a_first_production_is_merged_by_a_person(self):
        shutil.rmtree(self.root / 'apps/example/prod')
        (self.root / 'clusters/home/app-example-prod.yaml').unlink()
        (self.root / 'clusters/home/kustomization.yaml').write_text('resources:\n- app-example-staging.yaml\n')
        self.meta = console.metadata(self.root, 'example', IMAGE, False)
        self.pr['body'] = '<!-- swhurl-promotion ' + json.dumps(self.meta) + ' -->'
        self.files = sorted(['apps/example/prod/' + name for name in ('namespace.yaml', 'helmrelease.yaml',
                                                                       'kustomization.yaml', 'networkpolicy.yaml',
                                                                       'resourcequota.yaml')]
                            + ['clusters/home/app-example-prod.yaml', 'clusters/home/kustomization.yaml'])
        runner = self.runner()
        with self.assertRaisesRegex(merge.Hold, 'a person reviews and merges it'):
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

    def test_new_app_checks_the_merge_result_before_the_push(self):
        self.pr['head']['ref'] = 'console/new-fresh-staging-aaaaaaa'
        self.files = ['apps/fresh/staging/helmrelease.yaml', 'clusters/home/app-fresh-staging.yaml',
                      'clusters/home/kustomization.yaml']
        runner = self.runner()
        self.check(runner)
        checks = ('make', '-j2', '--output-sync=target', 'check-repo', 'check-apps')
        self.assertEqual([c for c in runner.calls if c[0] == 'make'], [('make', 'app-sync'), checks],
                         'the shared files are made on the newest main, then the result is checked')
        self.assertTrue(self.pushes(runner))
        self.assertLess(runner.calls.index(checks), runner.calls.index(self.pushes(runner)[0]))

    def test_new_app_validation_failure_and_base_race_never_push(self):
        self.pr['head']['ref'] = 'console/new-fresh-staging-aaaaaaa'
        self.files = ['apps/fresh/staging/helmrelease.yaml']
        for runner, error in ((self.runner(validation=1), CommandError),
                              (self.runner(current='d' * 40), merge.Hold)):
            with self.subTest(error=error), self.assertRaises(error):
                self.check(runner)
            self.assertEqual(self.pushes(runner), [])

    def test_new_app_scope_still_refuses_other_apps_and_secrets(self):
        self.pr['head']['ref'] = 'console/new-fresh-staging-aaaaaaa'
        for filename in ('apps/example/staging/helmrelease.yaml', 'apps/fresh/staging/secret.sops.yaml',
                         'platform/oauth2-proxy/secret.sops.yaml', 'platform/console/helmrelease.yaml'):
            self.files = ['apps/fresh/staging/helmrelease.yaml', filename]
            runner = self.runner()
            with self.subTest(filename=filename), self.assertRaises(merge.Hold):
                self.check(runner)
            self.assertEqual([call for call in runner.calls if call[0] == 'make'], [])
            self.assertEqual(self.pushes(runner), [])

    KEY = 'apps/fresh/staging/events.sops.yaml'
    RECIPIENT = 'age1' + 'q' * 58

    def events_key(self, values=None, recipient=None, name='fresh-events'):
        doc = {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': name, 'namespace': 'fresh-staging'},
               'type': 'Opaque', 'stringData': values or {'SWHURL_EVENTS_SEED': 'ENC[AES256_GCM,data:x,type:str]'},
               'sops': {'age': [{'recipient': recipient or self.RECIPIENT, 'enc': 'x'}], 'mac': 'ENC[x]'}}
        return json.dumps(doc)

    def new_app_with_generated_files(self):
        self.pr['head']['ref'] = 'console/new-fresh-staging-aaaaaaa'
        (self.root / '.sops.yaml').write_text(f"creation_rules:\n  - path_regex: apps/.*\\.sops\\.ya?ml$\n    age: {self.RECIPIENT}\n")
        for rel in merge.DERIVED:
            (self.root / rel).parent.mkdir(parents=True, exist_ok=True)
            (self.root / rel).write_text('before: true\n')
        self.candidate_files = {self.KEY: self.events_key(), **{rel: 'after: true\n' for rel in merge.DERIVED}}
        self.files = ['apps/fresh/staging/helmrelease.yaml', self.KEY, 'clusters/home/app-fresh-staging.yaml',
                      'clusters/home/kustomization.yaml', *merge.DERIVED]

    def test_new_app_merges_its_generated_key_and_the_shared_files_app_sync_derives(self):
        self.new_app_with_generated_files()
        runner = self.runner()
        self.check(runner)
        sync = ('make', 'app-sync')
        self.assertIn(sync, runner.calls)
        self.assertTrue(self.pushes(runner))
        self.assertLess(runner.calls.index(sync), runner.calls.index(self.pushes(runner)[0]))

    def proxy_port(self, root, app, port):
        document = promotion.YamlFile(root / f'apps/{app}/staging/helmrelease.yaml')
        document.data['spec']['values']['controllers']['main']['containers']['main']['env'].update(egress.variables(port))
        document.save()

    def test_two_new_apps_from_the_same_main_both_merge(self):
        """The second app's PR conflicts with the first in the unit registry and the shared files, and was given
        the egress port the first now holds: the gate makes those again on main and merges it."""
        self.new_app_with_generated_files()
        self.unmerged = 'clusters/home/kustomization.yaml\nplatform/nats/users.yaml\n'
        self.proxy_port(self.root, 'example', egress.FIRST_PORT)  # the first app, merged
        self.after_candidate = lambda root: self.proxy_port(root, 'fresh', egress.FIRST_PORT)
        seen = {}

        def app_sync(args, _):
            env = promotion.YamlFile(self.candidate_dir / 'apps/fresh/staging/helmrelease.yaml').data[
                'spec']['values']['controllers']['main']['containers']['main']['env']
            seen['proxy'] = env.get('HTTPS_PROXY')
            return Result(args)
        runner = self.runner().on('make', 'app-sync', handler=app_sync)
        runner._rules.insert(0, runner._rules.pop())  # before the catch-all make rule
        self.check(runner)
        self.assertIsNone(seen['proxy'], "the taken port is given up before make app-sync gives it a free one")
        calls = runner.calls
        restore = ('git', 'checkout', 'HEAD', '--', 'clusters/home/kustomization.yaml', *merge.DERIVED)
        self.assertIn(restore, calls, "the shared files are main's before they are made again")
        self.assertLess(calls.index(restore), calls.index(('make', 'app-sync')))
        self.assertIn(('git', '-c', 'user.name=swhurl auto-merge', '-c', 'user.email=console@users.noreply.github.com',
                       'commit', '--no-edit'), calls)
        self.assertTrue(self.pushes(runner))

    def test_a_new_app_conflicting_outside_the_shared_files_is_held(self):
        self.new_app_with_generated_files()
        self.unmerged = 'clusters/home/kustomization.yaml\napps/fresh/staging/helmrelease.yaml\n'
        runner = self.runner()
        with self.assertRaisesRegex(merge.Hold, 'conflicts with main outside the files every new app changes: '
                                                'apps/fresh/staging/helmrelease.yaml$'):
            self.check(runner)
        self.assertEqual(self.pushes(runner), [])

    def test_new_app_holds_shared_files_not_derived_or_a_key_that_is_more_than_its_seed(self):
        seed_only = "the events key is not the app's generated, encrypted NATS seed alone"
        cases = {
            'hand-edited shared file': {'sync': ' M platform/nats/users.yaml\n',
                                        'reason': r'make app-sync\): platform/nats/users\.yaml$'},
            'another value': {'reason': seed_only, 'key': self.events_key(values={'SWHURL_EVENTS_SEED': 'ENC[x]', 'OTHER': 'ENC[y]'})},
            'plaintext seed': {'reason': seed_only, 'key': self.events_key(values={'SWHURL_EVENTS_SEED': 'SUAPLAINTEXT'})},
            'another recipient': {'reason': seed_only, 'key': self.events_key(recipient='age1' + 'z' * 58)},
            "another app's name": {'reason': seed_only, 'key': self.events_key(name='example-events')},
        }
        for name, case in cases.items():
            with self.subTest(name):
                self.new_app_with_generated_files()
                if 'key' in case:
                    self.candidate_files[self.KEY] = case['key']
                self.sync_changes = case.get('sync', '')
                runner = self.runner()
                with self.assertRaisesRegex(merge.Hold, case['reason']):
                    self.check(runner)
                self.assertEqual(self.pushes(runner), [])
                self.sync_changes = ''

    def test_waits_for_a_running_validate_then_merges(self):
        running = '[{"databaseId": 1, "status": "in_progress", "conclusion": ""}]'
        done = '[{"databaseId": 1, "status": "completed", "conclusion": "success"}]'
        runner = self.runner(validate=['[]', running, done])
        with contextlib.redirect_stdout(io.StringIO()) as out:
            self.check(runner)
        self.assertEqual(out.getvalue().splitlines(), ['Waiting up to 120 s for Validate on bbbbbbb',
                                                       'Validate passed after a 10 s wait'])
        self.assertEqual(self.slept, [merge.VALIDATE_POLL] * 2)
        self.assertTrue(self.pushes(runner))

    def test_a_failed_validate_is_not_waited_for_and_a_stuck_one_is_given_up(self):
        runner = self.runner(passed=False)
        with self.assertRaisesRegex(merge.Hold, 'Validate has not passed'):
            self.check(runner)
        self.assertEqual((self.slept, self.pushes(runner)), ([], []))
        runner = self.runner(validate=['[{"databaseId": 1, "status": "queued", "conclusion": ""}]'])
        with self.assertRaisesRegex(merge.Hold, 'Validate has not passed'), contextlib.redirect_stdout(io.StringIO()):
            self.check(runner)
        self.assertEqual(sum(self.slept), merge.VALIDATE_WAIT)
        self.assertEqual(self.pushes(runner), [])


class RefusalTests(unittest.TestCase):
    """A refused merge leaves its reason on the pull request, where the console's Activity reads it."""

    def test_a_refusal_swaps_the_label_and_comments_the_reason_on_one_line(self):
        runner = FakeRunner().on('gh', 'api')
        sent = []
        real = runner.run
        runner.run = lambda args, **kwargs: (sent.append((tuple(args), kwargs.get('input'))), real(args, **kwargs))[1]
        with mock.patch.object(merge, 'merge', side_effect=merge.Hold('diff includes\nunrelated   edits ' + 'x' * 400)), \
                mock.patch.dict('os.environ', {'GITHUB_REPOSITORY': REPO}), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(merge.main(['59', HEAD], runner), 1)
        paths = [next(arg for arg in args if arg.startswith('repos/')) for args, _ in sent]
        self.assertEqual(paths, [f'repos/{REPO}/issues/59/labels/auto-merge', f'repos/{REPO}/issues/59/labels',
                                 f'repos/{REPO}/issues/59/comments'])
        body = json.loads(sent[-1][1])['body']
        first = body.split('\n', 1)[0]
        self.assertTrue(first.startswith(merge.REFUSAL + 'diff includes unrelated edits x'))
        self.assertEqual(len(first), len(merge.REFUSAL) + merge.REFUSAL_LIMIT)

    def test_a_race_with_main_is_retried_and_says_nothing(self):
        runner = FakeRunner()
        with mock.patch.object(merge, 'merge', side_effect=merge.Hold('main moved during validation; retry against the new base')), \
                mock.patch.dict('os.environ', {'GITHUB_REPOSITORY': REPO}), contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(merge.main(['59', HEAD], runner), 1)
        self.assertEqual(runner.calls, [])

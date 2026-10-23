"""The merge gate merges what its rule allows onto the newest main, and never pushes a failed or held change."""
import atexit
import base64
import contextlib
import functools
import io
import json
import shutil
import sys
import tempfile
import unittest
from dataclasses import fields, replace
from pathlib import Path
from unittest import mock

from ci_changes import records_only
from full_settings import DEFAULT, FULL, NAMES, PARTS, value, varied

from swhurl import ROOT, bots, yamlio
from swhurl.apps import cli, edit, egress, events, new, policy, promotion
from swhurl.apps import release as intake
from swhurl.apps.instance import Instance
from swhurl.apps.model import write as model_sync
from swhurl.apps.model.records import Settings
from swhurl.console import merge
from swhurl.run import CommandError, FakeRunner, Result

BASE = 'a' * 40
HEAD = 'b' * 40
REPO = 'samclement/swhurl-platform'
IMAGE = 'ghcr.io/example/app:2-abcdef0@sha256:' + 'a' * 64
MANIFESTS = ROOT / 'tests/fixtures/manifests'


NEW_IMAGE = 'ghcr.io/samclement/fresh:1-abcdef0@sha256:' + 'e' * 64
NEW_ARGV = ['fresh', '--from-repo=samclement/fresh', '--env=staging', f'--image={NEW_IMAGE}', '--team=demo']
CHECKS = ('make', '-j2', '--output-sync=target', 'check-repo', 'check-apps')
COMMIT = 'c' * 40


def github(request):
    """--from-repo's swhurl.yaml, without GitHub: the web fixture's, at the commit the image's tag names."""
    assert request.full_url.endswith('/contents/swhurl.yaml?ref=abcdef0'), request.full_url
    return (MANIFESTS / 'web.yaml').read_bytes()


@functools.cache
def baseline() -> Path:
    """main with example in staging and production, generated once per process; each test copies it.

    Not setUpClass: the test runner keeps such a class in one job, and these tests are better spread out."""
    root = Path(tempfile.mkdtemp())
    atexit.register(shutil.rmtree, root, True)
    (root / 'clusters/home').mkdir(parents=True)
    (root / 'clusters/home/kustomization.yaml').write_text('resources: []\n')
    for env in ('staging', 'prod'):
        image = IMAGE if env == 'staging' else IMAGE.replace('2-', '1-').replace('a' * 64, 'c' * 64)
        args = new.parse_args(['example', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', env, '--no-auto-deploy', '--image', image])
        new.generate(args, root)
    return root


def changed_files(base: Path, work: Path) -> list[str]:
    def files(root):
        return {p.relative_to(root).as_posix(): p.read_bytes() for p in root.rglob('*') if p.is_file()}
    before, after = files(base), files(work)
    return [path for path in before.keys() | after.keys() if before.get(path) != after.get(path)]


STAGED_IMAGE = IMAGE  # staging runs build 2
PROD_TAG = '1-abcdef0'


class GateCase(unittest.TestCase):
    def runner(self, *, checks=0, push_failures=0, latest=None, history_failure=0, merge_failure=0, sync_failure=0, conflicts_on_failure='', validate=None, on_push_race=None):
        runner = FakeRunner()
        reads, self.attempts = 0, 0
        self.conflicts = ''

        def pr(args, _):
            nonlocal reads
            reads += 1
            return Result(args, stdout=json.dumps(latest if reads > 1 and latest is not None else self.pr))

        def worktree(args, _):
            shutil.copytree(self.root, Path(args[-2]))
            return Result(args)

        def three_way(args, cwd):
            """git merge --no-commit of the PR head into the worktree (main), by whole files."""
            work = Path(cwd)
            paths = set(changed_files(self.base, self.head))
            ours = set(changed_files(self.base, work))
            conflicts = [path for path in paths & ours if (self.head / path).read_bytes() != (work / path).read_bytes()]
            if merge_failure:
                self.conflicts = conflicts_on_failure
                return Result(args, merge_failure, '', 'fatal: could not read object')
            if conflicts:
                self.conflicts = '\n'.join(conflicts)
                return Result(args, 1, '', f'CONFLICT (content): Merge conflict in {conflicts[0]}')
            for path in paths:
                if (self.head / path).is_file():
                    (work / path).parent.mkdir(parents=True, exist_ok=True)
                    shutil.copyfile(self.head / path, work / path)
                else:
                    (work / path).unlink(missing_ok=True)
            return Result(args)

        def removed(args, _):
            if Path(args[-1]).name == 'work' and Path(args[-1]).is_dir():
                self.result = Path(self.tmp.name) / 'result'
                shutil.rmtree(self.result, ignore_errors=True)
                shutil.copytree(args[-1], self.result)
            return Result(args)

        def logged(args, _):
            if history_failure:
                return Result(args, history_failure, '', 'fatal: could not read history')
            return Result(args, stdout='\n'.join(f'{i + 1:040x}' for i in range(len(self.staged))))

        def shown(args, _):
            revision = args[2].split(':', 1)[0]
            return Result(args, stdout=self.staged[int(revision, 16) - 1])

        def push(args, _):
            self.attempts += 1
            if self.attempts <= push_failures:
                (self.root / 'NEWS.md').write_text(f'another writer {self.attempts}\n')  # main moved
                if on_push_race:
                    on_push_race()
                return Result(args, 1, '', '! [rejected] (fetch first)')
            return Result(args)

        answers = list(validate or ['[{"databaseId": 1, "status": "completed", "conclusion": "success"}]'])
        def validated(args, _):
            return Result(args, stdout=answers.pop(0) if len(answers) > 1 else answers[0])

        real = runner._execute

        def execute(argv, *, input, env, cwd):
            """git merge and app-sync act on the worktree they run in (``cwd``)."""
            if argv[:len(merge.GIT) + 1] == (*merge.GIT, 'merge'):
                runner.calls.append(argv)
                return three_way(argv, cwd)
            if argv[:4] == (sys.executable, '-m', 'swhurl', 'app-sync'):
                runner.calls.append(argv)
                if sync_failure:
                    return Result(argv, sync_failure, '', 'temporary render tool failure')
                with contextlib.redirect_stdout(io.StringIO()):
                    return Result(argv, cli.main_sync(['--root', str(cwd)]))
            return real(argv, input=input, env=env, cwd=cwd)
        runner._execute = execute
        (runner.on('gh', 'api', f'repos/{REPO}/pulls/7', handler=pr)
         .on('gh', 'run', 'list', handler=validated)
         .on('gh', 'pr', 'diff', stdout='\n'.join(self.files))
         .on('git', 'rev-parse', 'origin/main', stdout=BASE)
         .on('git', 'rev-parse', 'FETCH_HEAD', stdout=HEAD)
         .on('git', 'worktree', 'add', handler=worktree)
         .on('git', 'worktree', 'remove', handler=removed)
         .on('git', 'diff', '--name-status', handler=lambda args, _: Result(args, stdout='\n'.join(
             f'{"D" if not (self.head / p).exists() else "A" if not (self.base / p).exists() else "M"}\t{p}'
             for p in self.files)))
         .on('git', 'log', handler=logged)
         .on('git', 'show', handler=shown)
         .on('git', 'diff', '--name-only', '--diff-filter=U', handler=lambda args, _: Result(args, stdout=self.conflicts))
         .on('git', 'push', 'origin', '--delete')
         .on('git', 'push', handler=push)
         .on(*merge.GIT, 'commit-tree', stdout=COMMIT)
         .on('git', stdout='')
         .on('make', returncode=checks, stderr='[BAD] policy: broken' if checks else ''))
        return runner

    def check(self, runner):
        merge.merge(runner, self.root, 7, HEAD, REPO, sleep=lambda _: None)

    def pushes(self, runner):
        return [c for c in runner.calls if c[:2] == ('git', 'push') and '--delete' not in c]

    def held(self, message, runner=None):
        runner = runner or self.runner()
        with self.assertRaisesRegex(merge.Hold, message):
            self.check(runner)
        self.assertEqual(self.pushes(runner), [])
        return runner



class PromotionGateTests(GateCase):
    """A promotion PR carries production's records; the gate merges them onto the newest main, applies
    promotion.held, renders, checks and pushes (docs/console.md#auto-merge)."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name) / 'main'
        shutil.copytree(baseline(), self.root)
        self.staged = []  # complete staging Release snapshots selected on main, not a set of digest strings
        self.pr = {'state': 'open', 'head': {'ref': 'console/promote-example-staging-aaaaaaa', 'sha': HEAD,
                                           'repo': {'full_name': REPO}}, 'base': {'ref': 'main'},
                   'labels': [{'name': 'auto-merge'}], 'title': 'apps: promote example/staging to example/prod',
                   'body': f'Promote the reviewed staging image `{IMAGE}` to `example/prod`.'}

    def open(self, edit_head=None):
        """The PR as the console opens it from main as it is now: app-promote's records, nothing rendered."""
        self.base = Path(self.tmp.name) / 'pr-base'
        self.head = Path(self.tmp.name) / 'pr-head'
        for path in (self.base, self.head):
            shutil.rmtree(path, ignore_errors=True)
            shutil.copytree(self.root, path)
        with contextlib.redirect_stdout(io.StringIO()):
            edit.promote(self.head, 'example', runner=FakeRunner())
        for path in changed_files(self.base, self.head):
            if not promotion.records('example', path):  # the console leaves rendered files out
                if (self.base / path).is_file():
                    shutil.copyfile(self.base / path, self.head / path)
                else:
                    (self.head / path).unlink()
        self.staged.append((self.base / 'apps/example/staging/release.yaml').read_text())
        if edit_head:
            edit_head(self.head)
        self.files = sorted(changed_files(self.base, self.head))

    def promoted_tag(self, root):
        return 'tag: 2-abcdef0' in (root / 'apps/example/prod/helmrelease.yaml').read_text()

    def sync(self):
        with contextlib.redirect_stdout(io.StringIO()):
            edit.sync(self.root)

    # Merged ----------------------------------------------------------------------------------------

    def test_a_records_only_promotion_is_merged_rendered_checked_and_pushed_as_one_merge(self):
        self.open()
        self.assertEqual(self.files, ['apps/example/prod/release.yaml'], 'the PR carries the record alone')
        runner = self.runner()
        self.check(runner)
        self.assertEqual([c for c in runner.calls if c[:2] == ('git', 'fetch')],
                         [('git', 'fetch', 'origin', 'main'), ('git', 'fetch', 'origin', 'pull/7/head')])
        made = (*merge.GIT, 'commit-tree', '', '-p', BASE, '-p', HEAD, '-m',
                'Merge pull request #7: apps: promote example/staging to example/prod')
        self.assertIn(made, runner.calls)
        self.assertEqual(self.pushes(runner), [('git', 'push', 'origin', f'{COMMIT}:refs/heads/main')])
        order = [c[0] if c[0] != sys.executable else 'app-sync' for c in runner.calls if c[0] in ('make', sys.executable)]
        self.assertEqual(order, ['app-sync', 'make'])
        self.assertLess(runner.calls.index(CHECKS), runner.calls.index(self.pushes(runner)[0]))
        self.assertTrue(self.promoted_tag(self.result), "main renders production's new release")
        self.assertIn(('git', 'push', 'origin', '--delete', 'console/promote-example-staging-aaaaaaa'), runner.calls)

    def test_a_newer_staging_deploy_another_apps_call_and_a_tooling_commit_on_main_do_not_hold_it(self):
        self.open()
        record = self.root / 'apps/example/staging/release.yaml'
        record.write_text(record.read_text().replace('2-abcdef0', '3-abcdef0').replace('a' * 64, 'd' * 64))
        with contextlib.redirect_stdout(io.StringIO()):
            args = new.parse_args(['caller', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'demo', '--env', 'prod',
                                   '--no-auto-deploy', '--image', IMAGE.replace('example/app', 'example/caller'),
                                   '--calls', 'example'])
            new.generate(args, self.root)
            edit.set_allows(self.root, 'example', 'prod', add=['caller'])
        self.sync()
        (self.root / 'tools/swhurl/apps').mkdir(parents=True)
        (self.root / 'tools/swhurl/apps/new.py').write_text('# changed generator\n')
        runner = self.runner()
        self.check(runner)
        self.assertTrue(self.pushes(runner))
        self.assertTrue(self.promoted_tag(self.result))
        self.assertIn('tag: 3-abcdef0', (self.result / 'apps/example/staging/helmrelease.yaml').read_text(),
                      "staging's newer build stays staging's")
        self.assertIn('caller', (self.result / 'apps/example/prod/helmrelease.yaml').read_text(),
                      "production is rendered with main's callers")

    def test_a_push_that_loses_to_another_writer_is_merged_again_on_the_newer_main(self):
        self.open()
        runner = self.runner(push_failures=1)
        with contextlib.redirect_stdout(io.StringIO()) as out:
            self.check(runner)
        self.assertEqual(len(self.pushes(runner)), 2)
        self.assertEqual(runner.calls.count(('git', 'fetch', 'origin', 'main')), 2)
        self.assertIn('attempt 1 of 3', out.getvalue())
        self.assertTrue((self.result / 'NEWS.md').is_file(), 'the second merge is made on the newer main')
        runner = self.runner(push_failures=merge.ATTEMPTS)
        with self.assertRaisesRegex(RuntimeError, 'main kept moving'), contextlib.redirect_stdout(io.StringIO()):
            self.check(runner)
        self.assertEqual(len(self.pushes(runner)), merge.ATTEMPTS)

    def test_history_and_encrypted_rules_are_rejudged_after_a_push_race(self):
        self.open()
        runner = self.runner(push_failures=1, on_push_race=self.staged.clear)
        with mock.patch.object(merge, 'encrypted_reason', wraps=merge.encrypted_reason) as encrypted, \
                self.assertRaisesRegex(merge.Hold, 'does not match staging.*history'), contextlib.redirect_stdout(io.StringIO()):
            self.check(runner)
        self.assertEqual(encrypted.call_count, 2)
        self.assertEqual(sum(call[:2] == ('git', 'log') for call in runner.calls), 2)
        self.assertEqual(len(self.pushes(runner)), 1, 'the newest-main hold prevents a second push')

    # Held ------------------------------------------------------------------------------------------

    def test_a_rollback_is_held(self):
        record = self.root / 'apps/example/prod/release.yaml'
        record.write_text(record.read_text().replace(PROD_TAG, '3-abcdef0'))
        self.sync()
        self.open()
        self.held('older image than production runs')

    def test_a_change_to_port_secret_names_or_publishes_is_held(self):
        record = self.root / 'apps/example/staging/release.yaml'
        original = record.read_text()
        for change, needs, words in (('port: 8080', 'port: 9090', 'its port'),
                                     ('telemetry: true', 'telemetry: true\nsecrets: [API_TOKEN]', 'its secret names')):
            with self.subTest(needs=needs):
                record.write_text(original.replace(change, needs))
                self.sync()
                self.open()
                self.held(words)
        record.write_text(original)
        self.sync()

        def publishes(tree):
            path = tree / 'apps/example/prod/release.yaml'
            path.write_text(path.read_text() + 'publishes: [created]\n')
        self.open(publishes)
        self.held('what it publishes')

    def test_a_first_promotion_is_held(self):
        shutil.rmtree(self.root / 'apps/example/prod')
        (self.root / 'clusters/home/app-example-prod.yaml').unlink(missing_ok=True)
        registry = self.root / 'clusters/home/kustomization.yaml'
        registry.write_text(registry.read_text().replace('- app-example-prod.yaml\n', ''))
        self.open()
        self.assertIn('apps/example/prod/instance.yaml', self.files)
        self.assertFalse([path for path in self.files if not promotion.records('example', path)], 'records only')
        self.held('creates production')

    def test_a_pr_touching_production_settings_or_a_rendered_file_is_held(self):
        def replicas(tree):
            path = tree / 'apps/example/prod/instance.yaml'
            path.write_text(path.read_text().replace('version: 1\n', 'version: 1\nreplicas: 2\n'))
        self.open(replicas)
        self.held('changes apps/example/prod/instance.yaml')

        def rendered(tree):
            path = tree / 'apps/example/prod/helmrelease.yaml'
            path.write_text(path.read_text() + '# by hand\n')
        self.open(rendered)
        runner = self.held("not example's promotion records")
        self.assertFalse([c for c in runner.calls if c[:3] == ('git', 'worktree', 'add')], 'nothing is merged')

    def test_a_merge_conflict_and_a_release_not_selected_in_staging_are_held(self):
        self.open()
        record = self.root / 'apps/example/prod/release.yaml'
        record.write_text(record.read_text().replace(PROD_TAG, '4-abcdef0'))
        self.held('conflicts with main: close it and promote again')
        shutil.copyfile(self.base / 'apps/example/prod/release.yaml', record)
        self.staged.clear()
        self.held('does not match staging.*history')

    def test_history_merge_render_and_check_errors_keep_the_label_and_never_push(self):
        self.open()
        for failure in ({'history_failure': 128}, {'merge_failure': 128}, {'sync_failure': 1}, {'checks': 1}):
            with self.subTest(failure=failure):
                runner = self.runner(**failure)
                with mock.patch.object(merge, 'ROOT', self.root), \
                        mock.patch.dict('os.environ', {'GITHUB_REPOSITORY': REPO}), \
                        contextlib.redirect_stdout(io.StringIO()):
                    self.assertEqual(merge.main(['7', HEAD], runner), 1)
                self.assertEqual(self.pushes(runner), [])
                self.assertFalse([call for call in runner.calls if call[:4] == ('gh', 'api', '--method', 'DELETE')],
                                 'an operational error must not remove auto-merge')
                self.assertFalse([call for call in runner.calls if call[:4] == ('gh', 'api', '--method', 'POST')],
                                 'an operational error must not leave a permanent refusal')

    def test_a_retagged_rollback_or_tampered_release_cannot_use_a_staged_digest(self):
        prod = self.root / 'apps/example/prod/release.yaml'
        prod.write_text(prod.read_text().replace(PROD_TAG, '3-abcdef0'))
        self.sync()
        def retag(tree):
            record = tree / 'apps/example/prod/release.yaml'
            record.write_text(record.read_text().replace('2-abcdef0', '999-abcdef0'))
        self.open(retag)
        self.held('does not match staging.*history')
        prod.write_text(prod.read_text().replace('3-abcdef0', PROD_TAG))
        self.sync()
        for before, after in (('healthPath: /healthz', 'healthPath: /tampered'), ('uid: 65532', 'uid: 10000')):
            def tamper(tree, before=before, after=after):
                record = tree / 'apps/example/prod/release.yaml'
                text = record.read_text()
                self.assertIn(before, text)
                record.write_text(text.replace(before, after))
            self.open(tamper)
            self.held('does not match staging.*history')

    def test_a_digest_in_a_comment_is_not_a_selected_image(self):
        self.open()
        text = self.staged[-1]
        self.staged = [text.replace('a' * 64, 'd' * 64) + '\n# observed digest ' + IMAGE.split('@')[1] + '\n']
        self.held('does not match staging.*history')
        runner = self.runner()
        self.held('does not match staging.*history', runner)
        history = next(call for call in runner.calls if call[:2] == ('git', 'log'))
        self.assertIn('--first-parent', history, 'a side branch is not evidence that main selected a release')
        self.assertIn('--diff-filter=AM', history, 'only snapshots with the record present are read')

    def test_a_label_removed_during_the_checks_is_not_pushed(self):
        self.open()
        runner = self.runner(latest={**self.pr, 'labels': []})
        with self.assertRaises(merge.Skip):
            self.check(runner)
        self.assertEqual(self.pushes(runner), [])


class NewAppGateTests(GateCase):
    """Records made by app-new, filtered as the console does, really merge and render on latest main."""

    KEY = 'apps/fresh/staging/events.sops.yaml'
    RECIPIENT = 'age1' + 'q' * 58
    PUBLIC = 'U' + 'A' * 55

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name) / 'main'
        shutil.copytree(baseline(), self.root)
        (self.root / egress.PROXY_FILE).parent.mkdir(parents=True)
        shutil.copy(ROOT / egress.PROXY_FILE, self.root / egress.PROXY_FILE)
        self.staged = []
        self.pr = {'state': 'open', 'head': {'ref': 'console/new-fresh-staging-aaaaaaa', 'sha': HEAD,
                                           'repo': {'full_name': REPO}}, 'base': {'ref': 'main'},
                   'labels': [{'name': 'auto-merge'}], 'title': 'apps: add fresh/staging', 'body': 'No CLI request'}
        self.sync()

    def sync(self):
        with contextlib.redirect_stdout(io.StringIO()):
            edit.sync(self.root)

    def events_key(self):
        return {'apiVersion': 'v1', 'kind': 'Secret',
                'metadata': {'name': 'fresh-events', 'namespace': 'fresh-staging'}, 'type': 'Opaque',
                'stringData': {'SWHURL_EVENTS_SEED': 'ENC[AES256_GCM,data:x,type:str]'},
                'sops': {'age': [{'recipient': self.RECIPIENT, 'enc': 'x'}], 'mac': 'ENC[x]'}}

    def nats(self):
        (self.root / '.sops.yaml').write_text(f"creation_rules:\n  - path_regex: apps/.*\\.sops\\.ya?ml$\n    age: {self.RECIPIENT}\n")
        path = self.root / events.CONTROLLER_KEY_FILE
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text('UCONTROLLER\n')
        # Existing instances already have keys; the gate must never create one for the new app.
        for folder in (self.root / 'apps/example').iterdir():
            if folder.is_dir():
                record = folder / 'instance.yaml'
                doc = yamlio.load(record.read_text())
                doc.setdefault('allocations', {})['eventsKey'] = self.PUBLIC
                record.write_text(yamlio.dump(doc))
                (folder / events.SECRET_FILE).write_text('encrypted fixture\n')
        self.sync()

    def open(self, edit_head=None, opener=github):
        self.base = Path(self.tmp.name) / 'pr-base'
        self.head = Path(self.tmp.name) / 'pr-head'
        for path in (self.base, self.head):
            shutil.rmtree(path, ignore_errors=True)
            shutil.copytree(self.root, path)
        with contextlib.redirect_stdout(io.StringIO()), mock.patch.object(model_sync.nkeys, 'generate', return_value=('fixture-secret', self.PUBLIC)), \
                mock.patch.object(model_sync, 'Runner', return_value=FakeRunner().on('sops', stdout=yamlio.dump(self.events_key()))):
            self.assertEqual(new.main([*NEW_ARGV, '--root', str(self.head), '--no-policy-check'], opener), 0)
        record = self.head / 'apps/fresh/staging/instance.yaml'
        doc = yamlio.load(record.read_text())
        doc.get('allocations', {}).pop('egressPort', None)
        record.write_text(yamlio.dump(doc))
        for path in changed_files(self.base, self.head):
            if not merge.new_app_records('fresh', path):
                if (self.base / path).is_file():
                    shutil.copyfile(self.base / path, self.head / path)
                else:
                    (self.head / path).unlink()
        if edit_head:
            edit_head(self.head)
        self.files = sorted(changed_files(self.base, self.head))

    def change_record(self, name, update):
        path = self.head / 'apps/fresh/staging' / name
        doc = yamlio.load(path.read_text())
        update(doc)
        path.write_text(yamlio.dump(doc))

    def test_records_are_merged_rendered_and_checked_before_push(self):
        self.open()
        self.assertEqual(self.files, ['apps/fresh/app.yaml', 'apps/fresh/staging/instance.yaml', 'apps/fresh/staging/release.yaml'])
        runner = self.runner()
        self.check(runner)
        self.assertTrue(self.pushes(runner))
        self.assertTrue((self.result / 'apps/fresh/staging/image-automation.yaml').is_file())
        self.assertIn(Instance('fresh', 'staging'), {each: entry.settings.allocations.egress_port for each, entry in model_sync.read(self.result)[0].items()})
        self.assertLess(runner.calls.index(CHECKS), runner.calls.index(self.pushes(runner)[0]))
        self.assertFalse([c for c in runner.calls if 'app-new' in c], 'no CLI replay or source fetch')

    def test_offers_and_existing_key_reach_the_rendered_merge_without_source_fetches(self):
        self.nats()
        documents = {
            'swhurl.yaml': (MANIFESTS / 'web.yaml').read_text() + (
                'provides:\n  api: /openapi.json\n  apiFile: openapi.yaml\n  eventsFile: asyncapi.yaml\n'
                'flags:\n  example: {type: boolean, default: false, description: Example}\n'),
            'openapi.yaml': 'openapi: 3.0.3\npaths: {}\n',
            'asyncapi.yaml': 'asyncapi: 3.0.0\nchannels:\n  created: {address: created}\n'
                             'operations:\n  created: {action: send, channel: {$ref: "#/channels/created"}}\n'}
        def opener(request):
            path = request.full_url.split('/contents/', 1)[1].split('?', 1)[0]
            return documents[path].encode()
        self.open(opener=opener)
        offers = [path for path in self.files if '/offers/' in path]
        self.assertEqual(len(offers), 4, 'API, events, flags and their source are all input documents')
        runner = self.runner()
        with mock.patch.object(intake, 'read_offers', side_effect=AssertionError('no source read at merge time')):
            self.check(runner)
        for path in [*offers, self.KEY]:
            self.assertEqual((self.result / path).read_bytes(), (self.head / path).read_bytes())
        release = yamlio.load((self.result / 'apps/fresh/staging/helmrelease.yaml').read_text())
        from swhurl.apps import helmrelease
        self.assertEqual(helmrelease.variables(release)['SWHURL_EVENTS_PUBLISH'], 'created')
        self.assertTrue((self.result / 'apps/fresh/staging/events.yaml').is_file())

    def test_two_creations_from_the_same_base_merge_with_unique_ports_and_all_units(self):
        self.open()
        # The other PR has the same base, but no shared generated files to conflict with.
        first = Path(self.tmp.name) / 'first'
        shutil.copytree(self.base, first)
        for path in self.files:
            target = first / path.replace('fresh', 'first')
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text((self.head / path).read_text().replace('fresh', 'first'))
        original_head = self.head
        self.head = first
        self.files = sorted(changed_files(self.base, first))
        self.pr['head']['ref'] = 'console/new-first-staging-aaaaaaa'
        self.check(self.runner())
        shutil.rmtree(self.root)
        shutil.copytree(self.result, self.root)
        self.head = original_head
        self.files = sorted(changed_files(self.base, self.head))
        self.pr['head']['ref'] = 'console/new-fresh-staging-aaaaaaa'
        self.check(self.runner())
        ports = {each: entry.settings.allocations.egress_port for each, entry in model_sync.read(self.result)[0].items()}
        self.assertEqual(len(ports), len(set(ports.values())))
        self.assertIn(Instance('first', 'staging'), ports)
        self.assertIn(Instance('fresh', 'staging'), ports)
        registry = (self.result / 'clusters/home/kustomization.yaml').read_text()
        for app in ('example', 'first', 'fresh'):
            self.assertIn(f'app-{app}-staging.yaml', registry)

    def test_push_race_retries_on_latest_main_in_the_same_run(self):
        self.open()
        def another_creation():
            argv = [arg.replace('fresh', 'raced') for arg in NEW_ARGV]
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(new.main([*argv, '--root', str(self.root), '--no-policy-check'], github), 0)
        runner = self.runner(push_failures=1, on_push_race=another_creation)
        with contextlib.redirect_stdout(io.StringIO()):
            self.check(runner)
        self.assertEqual(len(self.pushes(runner)), 2)
        self.assertTrue((self.result / 'NEWS.md').is_file())
        ports = {each: entry.settings.allocations.egress_port for each, entry in model_sync.read(self.result)[0].items()}
        self.assertIn(Instance('fresh', 'staging'), ports)
        self.assertIn(Instance('raced', 'staging'), ports)
        self.assertEqual(len(ports), len(set(ports.values())))

    def test_cross_app_rendered_or_other_secret_paths_are_held_before_merging(self):
        for path in ('apps/example/staging/instance.yaml', 'apps/fresh/staging/helmrelease.yaml',
                     'apps/fresh/staging/secret.sops.yaml', 'platform/nats/users.yaml'):
            def extra(tree, path=path):
                target = tree / path
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text('manual: true\n')
            self.open(extra)
            runner = self.held("not fresh's new-app records")
            self.assertFalse([c for c in runner.calls if c[:3] == ('git', 'worktree', 'add')])

    def test_existing_app_or_unit_and_merge_conflicts_are_held(self):
        self.open()
        (self.root / 'apps/fresh/prod').mkdir(parents=True)
        self.held('overwrites an existing app or instance')
        shutil.rmtree(self.root / 'apps/fresh')
        unit = self.root / 'clusters/home/app-fresh-staging.yaml'
        unit.write_text('existing: unit\n')
        self.held('overwrites an existing app or instance')
        unit.unlink()
        # Exercise Git's conflict refusal separately (not an app-folder collision).
        self.conflicts = ''
        runner = self.runner(merge_failure=1, conflicts_on_failure='apps/fresh/app.yaml')
        self.held('conflicts with main', runner)

    def test_missing_malformed_records_preallocated_port_and_connections_are_held(self):
        for name in ('../app.yaml', 'release.yaml', 'instance.yaml'):
            self.open()
            (self.head / 'apps/fresh/staging' / name).unlink()
            self.files = sorted(changed_files(self.base, self.head))
            self.held('records cannot be read')
            self.open()
            (self.head / 'apps/fresh/staging' / name).write_text('version: 999\n')
            self.held('records cannot be read')
        for change, reason in (({'allocations': {'egressPort': 18080}}, 'preallocate an egress port'),
                               ({'calls': ['example']}, 'calls or subscribes'),
                               ({'subscriptions': ['example.created']}, 'calls or subscribes')):
            self.open()
            self.change_record('instance.yaml', lambda doc, change=change: doc.update(change))
            self.held(reason)

    def test_policy_exceptions_cannot_make_an_unsafe_public_exposure_automatic(self):
        self.open()
        self.change_record('instance.yaml', lambda doc: doc.update(
            exposure='public', exceptions='exposure=not actually reviewed'))
        # No explicit host: rendering would use the default shared-cookie-domain host.
        runner = self.held('policy exceptions.*person reviews')
        self.assertFalse([call for call in runner.calls if 'app-sync' in call], 'hold before rendering or policy waivers')

    def test_arbitrary_invalid_schedule_records_fail_real_sync_and_never_push(self):
        for kind, schedule in (('job', '99 99 * * *'), ('job', '*/0 * * * *'), ('worker', '17 3 * * *')):
            with self.subTest(kind=kind, schedule=schedule):
                self.open()
                def job(doc, kind=kind):
                    doc['kind'] = kind
                    doc.pop('port', None)
                    doc.pop('healthPath', None)
                self.change_record('release.yaml', job)
                self.change_record('instance.yaml', lambda doc, schedule=schedule: doc.update(schedule=schedule))
                runner = self.runner()
                with self.assertRaises(CommandError), contextlib.redirect_stderr(io.StringIO()):
                    self.check(runner)
                self.assertEqual(self.pushes(runner), [])
                self.assertFalse([call for call in runner.calls if call[0] == 'make'], 'sync stops before checks or commit')

    def test_reserved_notify_name_is_held_without_pushing(self):
        self.open()
        (self.head / 'apps/fresh').rename(self.head / 'apps/notify')
        record = self.head / 'apps/notify/staging/release.yaml'
        record.write_text(record.read_text().replace('samclement/fresh', 'samclement/notify'))
        self.pr['head']['ref'] = 'console/new-notify-staging-aaaaaaa'
        self.files = sorted(changed_files(self.base, self.head))
        runner = self.held('notify.*reserved')
        self.assertFalse([call for call in runner.calls if 'app-sync' in call])

    def test_arbitrary_secret_and_database_records_fail_real_sync_without_pushing(self):
        for kind, secrets, database in (('web', ['HOST_IP'], ''), ('web', ['TOKEN', 'TOKEN'], ''),
                                        ('web', ['bad-name'], ''), ('worker', [], 'sqlite'), ('job', [], 'sqlite')):
            with self.subTest(kind=kind, secrets=secrets, database=database):
                self.open()
                def change(doc, kind=kind, secrets=secrets, database=database):
                    doc.update(kind=kind, secrets=secrets, database=database)
                    if kind != 'web':
                        doc.pop('port', None)
                        doc.pop('healthPath', None)
                self.change_record('release.yaml', change)
                if database:
                    self.change_record('instance.yaml', lambda doc: doc.setdefault('allocations', {}).update(
                        database={'size': '1Gi', 'storageClass': 'local-path-retain'}))
                runner = self.runner()
                with self.assertRaises(CommandError), contextlib.redirect_stderr(io.StringIO()):
                    self.check(runner)
                self.assertEqual(self.pushes(runner), [])
                self.assertFalse([call for call in runner.calls if call[0] == 'make'])

    def test_encrypted_key_and_public_allocation_are_required_and_strict(self):
        self.nats()
        for mutate in (lambda doc: doc.update(stringData={'SWHURL_EVENTS_SEED': 'plaintext'}),
                       lambda doc: doc['stringData'].update(OTHER='ENC[x]'),
                       lambda doc: doc['sops'].update(age=[{'recipient': 'another'}]),
                       lambda doc: doc['sops'].update(pgp=[{'fp': 'another'}]),
                       lambda doc: doc['metadata'].update(name='another'),
                       lambda doc: doc.update(data={'x': 'encoded'})):
            self.open()
            doc = self.events_key()
            mutate(doc)
            (self.head / self.KEY).write_text(yamlio.dump(doc))
            self.held('encrypted NATS seed alone')
        for content in (None, '[]', 'sops: [', 'sops: {age: [invalid]}'):
            self.open()
            key = self.head / self.KEY
            key.unlink() if content is None else key.write_text(content)
            self.held('encrypted NATS seed alone')
        self.open()
        self.change_record('instance.yaml', lambda doc: doc['allocations'].update(eventsKey='invalid'))
        self.held('no NATS public key')
        self.open()
        self.change_record('instance.yaml', lambda doc: doc['allocations'].pop('eventsKey'))
        self.held('no NATS public key')
        self.open()
        runner = self.runner()
        with mock.patch.object(model_sync, 'Runner', return_value=FakeRunner()):
            self.check(runner)
        self.assertEqual((self.result / self.KEY).read_bytes(), (self.head / self.KEY).read_bytes())
        self.assertIn(self.PUBLIC, (self.result / events.USERS_FILE).read_text())

    def test_platform_owned_image_is_checked_on_the_rendered_merge(self):
        self.open()
        self.change_record('release.yaml', lambda doc: doc['image'].update(repository='ghcr.io/other/fresh'))
        runner = self.runner()
        def checked(_, work):
            problems = policy.platform_image(work / 'apps/fresh/staging')
            self.assertTrue(any('not ghcr.io/samclement/fresh' in p for p in problems))
            raise CommandError(['make', *bots.CHECKS], 'platform-image: refused')
        with mock.patch.object(bots, 'checked', side_effect=checked), self.assertRaises(CommandError):
            self.check(runner)
        self.assertEqual(self.pushes(runner), [])

    def test_validate_waits_for_success_and_stops_on_failure_or_timeout_without_pushing(self):
        self.open()
        running = '[{"status": "in_progress", "conclusion": ""}]'
        done = '[{"status": "completed", "conclusion": "success"}]'
        slept = []
        runner = self.runner(validate=['[]', running, done])
        with contextlib.redirect_stdout(io.StringIO()):
            merge.merge(runner, self.root, 7, HEAD, REPO, sleep=slept.append)
        self.assertEqual(slept, [merge.VALIDATE_POLL] * 2)
        self.assertTrue(self.pushes(runner))
        for answer in (running, done.replace('success', 'failure')):
            runner = self.runner(validate=[answer])
            with self.assertRaises(merge.Skip), contextlib.redirect_stdout(io.StringIO()):
                self.check(runner)
            self.assertEqual(self.pushes(runner), [])

    def test_check_render_merge_failures_and_label_removal_never_push(self):
        self.open()
        for failure in ({'checks': 1}, {'sync_failure': 1}, {'merge_failure': 128}):
            runner = self.runner(**failure)
            with self.assertRaises(CommandError):
                self.check(runner)
            self.assertEqual(self.pushes(runner), [])
        runner = self.runner(latest={**self.pr, 'labels': []})
        with self.assertRaises(merge.Skip):
            self.check(runner)
        self.assertEqual(self.pushes(runner), [])


class InstanceGateTests(GateCase):
    """An instance change's PR carries the instance's Settings alone; the gate merges it onto the newest
    main and applies settings_held: automatic for how staging runs, its calls and taking access away."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name) / 'main'
        shutil.copytree(baseline(), self.root)
        with contextlib.redirect_stdout(io.StringIO()):
            args = new.parse_args(['caller', '--manifest', str(MANIFESTS / 'web.yaml'), '--team', 'payments', '--env', 'staging',
                                   '--no-auto-deploy', '--image', IMAGE.replace('example/app', 'example/caller')])
            new.generate(args, self.root)
        self.staged = []
        self.branch('scale-example-staging')

    def branch(self, name):
        self.pr = {'state': 'open', 'head': {'ref': f'console/{name}-aaaaaaa', 'sha': HEAD, 'repo': {'full_name': REPO}},
                   'base': {'ref': 'main'}, 'labels': [{'name': 'auto-merge'}], 'title': f'apps: {name}', 'body': ''}

    def open(self, command, edit_head=None):
        """The PR as the console opens it: the command run on main, only the instance's Settings kept."""
        self.base = Path(self.tmp.name) / 'pr-base'
        self.head = Path(self.tmp.name) / 'pr-head'
        for path in (self.base, self.head):
            shutil.rmtree(path, ignore_errors=True)
            shutil.copytree(self.root, path)
        with contextlib.redirect_stdout(io.StringIO()):
            command(self.head)
        for path in changed_files(self.base, self.head):
            if not path.endswith('/instance.yaml'):  # the console leaves rendered files out
                if (self.base / path).is_file():
                    shutil.copyfile(self.base / path, self.head / path)
                else:
                    (self.head / path).unlink()
        if edit_head:
            edit_head(self.head)
        self.files = sorted(changed_files(self.base, self.head))

    def env(self, root, app):
        release = yamlio.load((root / 'apps' / app / 'staging/helmrelease.yaml').read_text())
        return release['spec']['values']['controllers']['main']['containers']['main'].get('env', {})

    def test_a_staging_scale_merges_itself_and_main_renders_it(self):
        self.open(lambda head: edit.scale(head, 'example', 'staging', replicas=2))
        self.assertEqual(self.files, ['apps/example/staging/instance.yaml'], 'the PR carries the record alone')
        runner = self.runner()
        self.check(runner)
        self.assertEqual(self.pushes(runner), [('git', 'push', 'origin', f'{COMMIT}:refs/heads/main')])
        release = yamlio.load((self.result / 'apps/example/staging/helmrelease.yaml').read_text())
        self.assertEqual(release['spec']['values']['controllers']['main']['replicas'], 2)

    def test_the_same_scale_with_an_egress_host_and_a_production_scale_are_held(self):
        def egress_too(head):
            path = head / 'apps/example/staging/instance.yaml'
            doc = yamlio.load(path.read_text())
            doc['egress'] = ['api.stripe.com:443']
            path.write_text(yamlio.dump(doc))
        self.open(lambda head: edit.scale(head, 'example', 'staging', replicas=2), egress_too)
        self.held('grants hosts outside the cluster: api.stripe.com:443')
        self.branch('scale-example-prod')
        self.open(lambda head: edit.scale(head, 'example', 'prod', replicas=2))
        self.held('changes production settings')

    def test_a_call_merges_itself_and_connects_only_once_the_callee_allows_it(self):
        self.branch('connect-caller-staging')
        self.open(lambda head: edit.set_calls(head, 'caller', 'staging', add=['example']))
        runner = self.runner()
        self.check(runner)
        self.assertTrue(self.pushes(runner), 'a call uses only access already granted')
        self.assertNotIn('SWHURL_SERVICE_EXAMPLE_URL', self.env(self.result, 'caller'), 'not granted: it waits')
        self.assertNotIn('SWHURL_ALLOWED_CALLERS', self.env(self.result, 'example'))

        self.branch('allow-example-staging')
        self.open(lambda head: edit.set_allows(head, 'example', 'staging', add=['caller']))
        self.assertEqual(self.files, ['apps/example/staging/instance.yaml'])
        self.held('allows caller to call it')

    def test_taking_access_away_merges_itself(self):
        with contextlib.redirect_stdout(io.StringIO()):
            edit.set_allows(self.root, 'example', 'staging', add=['caller', 'team:demo'])
        self.branch('allow-example-staging')
        self.open(lambda head: edit.set_allows(head, 'example', 'staging', remove=['caller']))
        runner = self.runner()
        self.check(runner)
        self.assertTrue(self.pushes(runner))

    def test_an_events_key_or_another_instances_files_are_held_and_new_or_promote_never_name_production(self):
        self.open(lambda head: edit.scale(head, 'example', 'staging', replicas=2),
                  lambda head: (head / 'apps/example/staging/events.sops.yaml').write_text('encrypted\n'))
        self.held('which are not example\'s instance records')
        self.branch('connect-example-staging')
        self.held('generated encrypted events key')
        self.branch('new-example-prod')
        with self.assertRaises(merge.Skip):
            self.check(self.runner())


class ActionParityTests(GateCase):
    """Identical input trees through console preparation/publication and the gate's real merge loop."""

    def test_console_and_gate_reach_the_same_verdict_for_every_gated_action(self):
        from test_console_changes import HEAD as CONSOLE_HEAD
        from test_console_changes import IMAGE as CONSOLE_IMAGE
        from test_console_changes import MAIN, FakeGitHub, settings_edit, staging_reads, tree_fake

        from swhurl.apps import ops
        from swhurl.apps.model import store
        from swhurl.console import actions, proposal
        from swhurl.console import promotion as review

        # env, form, prior Settings, proposed Settings, automatic, optional encrypted-key status.
        # For expose and egress, the automatic case normalizes an already satisfied choice: no new access is granted.
        cases = {
            'new': [('staging', {'repo': 'samclement/fresh', 'image': NEW_IMAGE, 'flags': ('--team=demo',)}, {}, {}, True),
                    ('staging', {'repo': 'samclement/fresh', 'image': NEW_IMAGE, 'flags': ('--team=demo', '--calls=hello')},
                     {}, {}, False)],
            'promote': [('staging', {'image': CONSOLE_IMAGE}, {}, {}, True),
                        ('staging', {'image': CONSOLE_IMAGE}, {}, {'healthPath': '/unselected'}, False)],
            'scale': [('staging', {'replicas': '2'}, {}, {'replicas': 2}, True),
                      ('prod', {'replicas': '2'}, {}, {'replicas': 2}, False)],
            'schedule': [('staging', {'schedule': '*/5 * * * *'}, {}, {'schedule': '*/5 * * * *'}, True),
                         ('prod', {'schedule': '*/5 * * * *'}, {}, {'schedule': '*/5 * * * *'}, False)],
            'expose': [('staging', {'exposure': 'private'}, {'exposure': 'private'}, {'exposure': 'private'}, True),
                       ('staging', {'exposure': 'private'}, {}, {'exposure': 'private'}, False)],
            'connect': [('staging', {'calls': 'orders'}, {}, {'calls': ['orders']}, True),
                        ('prod', {'calls': 'orders'}, {}, {'calls': ['orders']}, False),
                        ('staging', {'calls': 'orders'}, {}, {'calls': ['orders']}, False, 'A'),
                        ('staging', {'calls': 'orders'}, {}, {'calls': ['orders']}, False, 'M'),
                        ('staging', {'calls': 'orders'}, {}, {'calls': ['orders']}, True, 'D')],
            'disconnect': [('staging', {'role': 'calls', 'app': 'orders'}, {'calls': ['orders']}, {'calls': []}, True),
                           ('prod', {'role': 'calls', 'app': 'orders'}, {'calls': ['orders']}, {'calls': []}, False)],
            'egress': [('staging', {'hosts': 'api.stripe.com'}, {'egress': ['api.stripe.com:443']},
                        {'egress': ['api.stripe.com:443']}, True),
                       ('staging', {'hosts': 'api.stripe.com'}, {}, {'egress': ['api.stripe.com:443']}, False)],
            'allow': [('staging', {'remove': 'orders'}, {'allows': ['orders']}, {'allows': []}, True),
                      ('staging', {'add': 'orders'}, {}, {'allows': ['orders']}, False)],
        }
        self.assertEqual(set(cases), {action for action, entry in merge.ACTIONS.items() if entry.gated})
        good = ops.gather_status(staging_reads(FakeRunner()), Instance('hello', 'staging'))

        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        tmp = self.tmp.name
        for action, rows in cases.items():
            for index, case in enumerate(rows):
                env, form, prior, proposed, automatic = case[:5]
                key_status = case[5] if len(case) > 5 else ''
                with self.subTest(action=action, case=index, automatic=automatic):
                    self.root = Path(tmp) / 'main'
                    shutil.rmtree(self.root, ignore_errors=True)
                    for path, content in MAIN.items():
                        target = self.root / path
                        target.parent.mkdir(parents=True, exist_ok=True)
                        target.write_bytes(content)
                    name = 'fresh' if action == 'new' else 'hello'
                    entry = merge.ACTIONS[action]
                    commands = entry.commands(name, env, form)
                    record = f'apps/{name}/{env}/instance.yaml'
                    if action in merge.INSTANCE_KINDS:
                        path = self.root / record
                        doc = yamlio.load(path.read_text())
                        doc.update(prior)
                        path.write_text(yamlio.dump(doc) + '# normalize this existing input\n')
                    if action == 'schedule':
                        for each in ('staging', 'prod'):
                            path = self.root / f'apps/hello/{each}/release.yaml'
                            doc = yamlio.load(path.read_text())
                            doc.update(kind='job')
                            doc.pop('port', None)
                            doc.pop('healthPath', None)
                            path.write_text(yamlio.dump(doc))
                    key = f'apps/{name}/{env}/events.sops.yaml'
                    if key_status in ('M', 'D'):
                        (self.root / key).write_text('encrypted original fixture\n')
                    inputs = {p.relative_to(self.root).as_posix(): p.read_bytes()
                              for p in self.root.rglob('*') if p.is_file()}
                    self.base, self.head = Path(tmp) / 'pr-base', Path(tmp) / 'pr-head'
                    for tree in (self.base, self.head):
                        shutil.rmtree(tree, ignore_errors=True)
                        shutil.copytree(self.root, tree)
                    self.staged = [(self.root / 'apps/hello/staging/release.yaml').read_text()]

                    def prepare(tree, action=action, commands=commands, name=name, proposed=proposed,
                                record=record, key=key, key_status=key_status):
                        if action == 'new':
                            self.assertEqual(new.main([*commands[0].argv, '--root', str(tree), '--no-policy-check'], github), 0)
                        elif action == 'promote':
                            edit.promote(tree, name, runner=FakeRunner())
                            path = tree / 'apps/hello/prod/release.yaml'
                            doc = yamlio.load(path.read_text())
                            doc.update(proposed)
                            path.write_text(yamlio.dump(doc))
                        else:
                            # Existing fake command implementation; adding an existing egress
                            # grant is idempotent, not a second copy of that grant.
                            for command in commands:
                                settings_edit(tree, (command.name, *command.argv))
                            path = tree / record
                            doc = yamlio.load(path.read_text())
                            doc.update(proposed)
                            path.write_text(yamlio.dump(doc))
                        if key_status in ('A', 'M'):
                            (tree / key).write_text('encrypted updated fixture\n')
                        elif key_status == 'D':
                            (tree / key).unlink()

                    with contextlib.redirect_stdout(io.StringIO()):
                        prepare(self.head)
                    # The console keeps only owned inputs; new-app port normalization is part
                    # of preparation, not a different semantic input for the gate.
                    if action == 'new':
                        path = self.head / 'apps/fresh/staging/instance.yaml'
                        doc = yamlio.load(path.read_text())
                        doc.get('allocations', {}).pop('egressPort', None)
                        path.write_text(store.text(Settings.parse(doc).document()))
                    for path in changed_files(self.base, self.head):
                        if not entry.owns(name, env, path):
                            if (self.base / path).is_file():
                                shutil.copyfile(self.base / path, self.head / path)
                            else:
                                (self.head / path).unlink()
                    self.files = sorted(changed_files(self.base, self.head))
                    self.pr = {'state': 'open', 'head': {'ref': merge.branch(action, name, env, BASE[:7]),
                               'sha': HEAD, 'repo': {'full_name': REPO}}, 'base': {'ref': 'main'},
                               'labels': [{'name': 'auto-merge'}], 'title': entry.title.format(name=name, env=env)}
                    api = FakeGitHub()
                    # No earlier staging selections: both history readers see exactly the
                    # current main Release, and exhaust successfully for the held promotion.
                    api.also[f'GET /git/commits/{CONSOLE_HEAD}'] = {'tree': {'sha': 'tree0'}, 'parents': []}
                    job = actions.Job(1, action, merge.target(action, name, env), 'sam@swhurl.com', None)
                    with mock.patch.dict(MAIN, inputs, clear=True), \
                            mock.patch.object(review, 'staging_status', return_value=good), \
                            mock.patch.object(merge, 'encrypted_reason', wraps=merge.encrypted_reason) as encrypted, \
                            contextlib.redirect_stdout(io.StringIO()):
                        proposal.open_action(tree_fake(edit=prepare), api.github, job, action, name, env, commands)
                        runner = self.runner()
                        reason = ''
                        try:
                            merge.merge_records(runner, self.root, 7, HEAD, REPO, self.pr, action, name, self.files, env)
                        except merge.Hold as held:
                            reason = str(held)
                    statuses = [call.args[3] for call in encrypted.call_args_list]
                    self.assertEqual(len(statuses), 2, 'each path checks encrypted statuses once')
                    self.assertEqual(statuses[0], statuses[1])
                    if key_status:
                        self.assertEqual(statuses[0][key], key_status)
                    entries = api.sent('POST /git/trees')[0]['tree']
                    self.assertEqual([e['path'] for e in entries], self.files)
                    blobs = {f'blob{i}': base64.b64decode(blob['content'])
                             for i, blob in enumerate(api.sent('POST /git/blobs'))}
                    for item in entries:
                        if item['sha'] is None:
                            self.assertFalse((self.head / item['path']).exists())
                        else:
                            self.assertEqual(blobs[item['sha']], (self.head / item['path']).read_bytes())
                    labelled = bool(api.sent('POST /issues/7/labels'))
                    self.assertEqual(labelled, automatic)
                    self.assertEqual(labelled, bool(self.pushes(runner)))
                    self.assertEqual(bool(reason), not automatic)
                    if reason:
                        manual = next(line.removeprefix('Merge plan: Manual merge required: this pull request ')
                                      for line in job.lines if line.startswith('Merge plan:'))
                        manual = manual.removesuffix('. Review the changes, then merge it yourself.')
                        self.assertEqual(reason, f'it {manual}: a person reviews it')
                        self.assertIn(manual, api.sent('POST /pulls')[0]['body'])

class EncryptedInputTests(unittest.TestCase):
    def test_one_exception_and_other_actions_additions_modifications_and_deletions(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            key = 'apps/fresh/staging/events.sops.yaml'
            folder = root / 'apps/fresh/staging'
            folder.mkdir(parents=True)
            (folder / 'instance.yaml').write_text(yamlio.dump({'version': 1, 'exposure': 'private',
                'allocations': {'eventsKey': NewAppGateTests.PUBLIC}}))
            (root / '.sops.yaml').write_text(f'creation_rules:\n  - path_regex: apps/.*\n    age: {NewAppGateTests.RECIPIENT}\n')
            doc = {'apiVersion': 'v1', 'kind': 'Secret', 'metadata': {'name': 'fresh-events', 'namespace': 'fresh-staging'},
                   'stringData': {'SWHURL_EVENTS_SEED': 'ENC[x]'}, 'sops': {'age': [{'recipient': NewAppGateTests.RECIPIENT}]}}
            (root / key).write_text(yamlio.dump(doc))
            for action in merge.ACTIONS:
                for status in 'AMD':
                    with self.subTest(action=action, status=status):
                        reason = merge.encrypted_reason(action, 'fresh', 'staging', {key: status}, root)
                        self.assertEqual(bool(reason), status != 'D' and (action != 'new' or status != 'A'))
            for extra in ('apps/fresh/staging/secret.sops.yaml', 'apps/other/staging/events.sops.yaml'):
                self.assertTrue(merge.encrypted_reason('new', 'fresh', 'staging', {key: 'A', extra: 'A'}, root))
            doc['stringData']['EXTRA'] = 'ENC[x]'
            (root / key).write_text(yamlio.dump(doc))
            self.assertIn('seed alone', merge.encrypted_reason('new', 'fresh', 'staging', {key: 'A'}, root))


class ActionTableTests(unittest.TestCase):
    """Every console PR is one merge.ACTIONS entry: the console and the gate read its branch the same way."""

    NAMES = {'new': ('fresh', 'staging'), 'promote': ('fresh', 'staging'), 'remove': ('fresh', 'prod'),
             'team-add': ('payments', ''), 'team-edit': ('payments', ''), 'team-remove': ('payments', ''),
             'team-channel-set': ('payments', ''), 'team-channel-remove': ('payments', ''),
             **{kind: ('fresh', 'prod') for kind in merge.INSTANCE_KINDS}}
    TARGETS = {'new': 'fresh/staging', 'promote': 'fresh/prod', 'remove': 'fresh/prod',
               **{kind: 'fresh/prod' for kind in merge.INSTANCE_KINDS}}

    def test_every_action_branch_round_trips_and_names_its_target(self):
        self.assertEqual(set(self.NAMES), set(merge.ACTIONS))
        for action, (name, env) in self.NAMES.items():
            with self.subTest(action=action):
                made = merge.branch(action, name, env, 'abc1234')
                self.assertEqual(merge.parse(made), (action, name, env))
                self.assertEqual(merge.target(action, name, env), self.TARGETS.get(action, f'team:{name}'))
        # Byte-for-byte the branches the console has always opened, a promotion naming production as its target.
        self.assertEqual(merge.branch('promote', 'hello', 'staging', 'abc1234'), 'console/promote-hello-staging-abc1234')
        self.assertEqual(merge.branch('team-channel-set', 'payments', '', 'abc1234'), 'console/team-channel-set-payments-abc1234')
        self.assertEqual(merge.branch('team-add', 'payments', '', 'abc1234'), 'console/team-add-payments-abc1234')
        self.assertEqual(merge.parse('console/new-a-staging-staging-abc1234'), ('new', 'a-staging', 'staging'))
        self.assertEqual(merge.parse('console/team-remove-core-staging-abc1234'), ('team-remove', 'core-staging', ''))

    def test_invalid_environments_names_and_branches_are_refused(self):
        for action, name, env in (('new', 'fresh', 'prod'), ('promote', 'fresh', 'prod'), ('scale', 'fresh', 'dev'),
                                  ('scale', 'Fresh', 'staging'), ('scale', '../main', 'staging'), ('team-add', 'payments', 'staging'),
                                  ('scale', 'x' * 41, 'staging')):
            with self.subTest(action=action, name=name, env=env), self.assertRaises(ValueError):
                merge.branch(action, name, env, 'abc1234')
        for ref in ('console/new-fresh-prod-abc1234', 'console/rename-fresh-staging-abc1234', 'console/scale-fresh-staging-main',
                    'console/scale-fresh-staging', 'renovate/x', 'console/scale--staging-abc1234', 'console/team-add-payments-staging'):
            with self.subTest(ref=ref):
                self.assertIsNone(merge.parse(ref))
                self.assertFalse(merge.gated(ref))

    def test_each_action_owns_only_its_inputs(self):
        owns = {('new', 'apps/fresh/app.yaml'): True, ('new', 'apps/fresh/staging/events.sops.yaml'): True,
                ('new', 'apps/fresh/staging/secret.sops.yaml'): False, ('new', 'apps/fresh/staging/helmrelease.yaml'): False,
                ('promote', 'apps/fresh/prod/release.yaml'): True, ('promote', 'apps/fresh/prod/secret.sops.yaml'): True,
                ('promote', 'apps/fresh/prod/helmrelease.yaml'): False, ('scale', 'apps/fresh/prod/instance.yaml'): True,
                ('scale', 'apps/fresh/prod/events.sops.yaml'): False, ('connect', 'apps/fresh/prod/events.sops.yaml'): True,
                ('scale', 'apps/other/prod/instance.yaml'): False, ('remove', 'apps/fresh/prod/offers/source.yaml'): True,
                ('remove', 'apps/fresh/prod/helmrelease.yaml'): False, ('team-edit', 'apps/teams.yaml'): True,
                ('team-edit', 'apps/fresh/staging/instance.yaml'): True, ('team-edit', 'platform/notify/routes.yaml'): False,
                ('team-channel-set', 'platform/notify/channel-payments.sops.yaml'): True,
                ('team-channel-set', 'platform/notify/routes.yaml'): False}
        for (action, path), expected in owns.items():
            name, env = self.NAMES[action]
            with self.subTest(action=action, path=path):
                self.assertEqual(merge.ACTIONS[action].owns(name, env, path), expected)
                if expected:
                    self.assertTrue(records_only([path]), 'Validate renders what the console carries')
        for action in merge.INSTANCE_KINDS:
            entry = merge.ACTIONS[action]
            for env in entry.envs:
                with self.subTest(action=action, env=env):
                    self.assertTrue(entry.owns('fresh', env, f'apps/fresh/{env}/instance.yaml'))
                    self.assertFalse(entry.owns('fresh', env, f'apps/fresh/{env}/helmrelease.yaml'))
                    self.assertFalse(entry.owns('fresh', env, f'apps/other/{env}/instance.yaml'))
        self.assertEqual(merge.ACTIONS['remove'].keeps, 'D', 'a removal carries deletions alone')

    def test_promotion_and_creation_commands_come_from_the_table(self):
        self.assertEqual(merge.ACTIONS['new'].commands('fresh', 'staging', {
            'repo': 'example/fresh', 'image': IMAGE, 'flags': ('--team=demo',)}),
            (merge.Command('app-new', ('fresh', '--from-repo=example/fresh', '--env=staging', f'--image={IMAGE}', '--team=demo')),))
        self.assertEqual(merge.ACTIONS['promote'].commands('fresh', 'staging', {
            'image': IMAGE, 'host': 'prod.example.com'}),
            (merge.Command('app-promote', ('fresh', f'--expect-image={IMAGE}', '--prepare-only', '--host=prod.example.com')),))

    def test_held_operations_are_never_merged_by_the_gate(self):
        for action in ('remove', 'team-add', 'team-edit', 'team-remove', 'team-channel-set', 'team-channel-remove'):
            name, env = self.NAMES[action]
            ref = merge.branch(action, name, env, 'abc1234')
            pr = {'state': 'open', 'head': {'ref': ref, 'sha': HEAD, 'repo': {'full_name': REPO}}, 'base': {'ref': 'main'},
                  'labels': [{'name': 'auto-merge'}]}
            with self.subTest(action=action):
                self.assertFalse(merge.gated(ref))
                with self.assertRaises(merge.Skip):
                    merge.eligible(pr, HEAD, REPO)
        for action in ('new', 'promote', *merge.INSTANCE_KINDS):
            self.assertTrue(merge.gated(merge.branch(action, 'fresh', 'staging', 'abc1234')))


class RefusalTests(unittest.TestCase):
    """A held PR loses its label and gets the reason, where Activity reads it; a failure keeps the label."""

    def run_main(self, outcomes):
        runner = FakeRunner().on('gh', 'pr', 'list', stdout=json.dumps(
            [{'number': n, 'headRefOid': HEAD, 'headRefName': f'console/promote-x-staging-abc{n}'} for n in range(1, len(outcomes) + 1)]
        )).on('gh', 'api')
        sent = []
        real = runner.run
        runner.run = lambda args, **kwargs: (sent.append((tuple(args), kwargs.get('input'))), real(args, **kwargs))[1]
        with mock.patch.object(merge, 'merge', side_effect=outcomes) as merged, \
                mock.patch.dict('os.environ', {'GITHUB_REPOSITORY': REPO}), contextlib.redirect_stdout(io.StringIO()) as out:
            code = merge.main([], runner)
        return code, merged, [(args, data) for args, data in sent if args[:2] == ('gh', 'api')], out.getvalue()

    def test_a_hold_removes_the_label_and_comments_the_reason_on_one_line(self):
        code, _, sent, _ = self.run_main([merge.Hold('diff includes\nunrelated   edits ' + 'x' * 400)])
        self.assertEqual(code, 1)
        paths = [next(arg for arg in args if arg.startswith('repos/')) for args, _ in sent]
        self.assertEqual(paths, [f'repos/{REPO}/issues/1/labels/auto-merge', f'repos/{REPO}/issues/1/comments'])
        first = json.loads(sent[-1][1])['body'].split('\n', 1)[0]
        self.assertTrue(first.startswith(merge.REFUSAL + 'diff includes unrelated edits x'))
        self.assertEqual(len(first), len(merge.REFUSAL) + merge.REFUSAL_LIMIT)

    def test_a_transient_failure_keeps_the_label_and_the_queue_goes_on(self):
        failure = CommandError(['gh', 'api'], 'gh api exited 1: HTTP 502 Bad Gateway')
        code, merged, sent, out = self.run_main([failure, None, merge.Hold('a rollback')])
        self.assertEqual(code, 1, 'the run fails, so the failure is seen')
        self.assertEqual(merged.call_count, 3, 'one failure does not stop the queue')
        self.assertEqual([next(arg for arg in args if arg.startswith('repos/')) for args, _ in sent],
                         [f'repos/{REPO}/issues/3/labels/auto-merge', f'repos/{REPO}/issues/3/comments'],
                         'only the held PR loses its label')
        self.assertIn('Could not merge #1 (CommandError), the next run tries again', out)

    def test_a_skipped_pr_says_nothing(self):
        code, _, sent, _ = self.run_main([merge.Skip('Validate has not passed on the current PR head')])
        self.assertEqual((code, sent), (0, []))


class SettingsReviewTests(unittest.TestCase):
    def test_every_grants_and_choices_field_has_a_deliberate_staging_review(self):
        self.assertEqual(set(merge.STAGING_REVIEW), {name for name, part in NAMES.items() if part != 'allocations'},
                         'a new Grants or Choices field needs a row in merge.STAGING_REVIEW')
        self.assertEqual(len(NAMES), sum(len(fields(kind)) for kind in PARTS.values()), 'field names are unique across the parts')
        for name in NAMES:
            self.assertNotEqual(value(FULL, name), value(DEFAULT, name), f'FULL sets {name}')
        self.assertTrue(all(isinstance(value(FULL, name), tuple) for name, (_, review) in merge.STAGING_REVIEW.items()
                            if review == 'added'), 'an added review lists names')
        both = replace(DEFAULT, grants=replace(DEFAULT.grants, allows=('shop',), egress=('x.com:443',), exceptions='a=b'))
        self.assertEqual(merge.settings_held('staging', DEFAULT, both), 'changes its app-policy exceptions', 'first row wins')
        for name, after in varied():
            held = merge.settings_held('staging', Settings(), after)
            with self.subTest(field=name):
                if NAMES[name] == 'allocations':
                    self.assertIn('allocated', held, 'allocations are compared whole')
                    continue
                reason, review = merge.STAGING_REVIEW[name]
                self.assertIn(review, ('changed', 'added', 'free'))
                self.assertEqual(held, reason.format(added=', '.join(value(after, name))) if review == 'added' else reason)
                if review == 'added':
                    self.assertEqual(merge.settings_held('staging', after, Settings()), '', 'taking access away merges itself')
                self.assertEqual(merge.settings_held('prod', Settings(), after), 'changes production settings')


if __name__ == '__main__':
    unittest.main()

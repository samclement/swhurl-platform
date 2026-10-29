"""bots.push_main: how Render, Deploy and the merge gate push to main (docs/architecture.md#how-changes-reach-the-cluster)."""
import contextlib
import io
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from swhurl import bots
from swhurl.run import CommandError, FakeRunner, Result, Runner

BASE = 'b' * 40


class PushMainTests(unittest.TestCase):
    def runner(self, *, checks=0, push_failures=0, sync=None):
        runner = FakeRunner()
        self.pushes, self.worktrees = 0, []

        def add(args, _):
            Path(args[-2]).mkdir(parents=True)
            self.worktrees.append(Path(args[-2]))
            return Result(args)

        def push(args, _):
            self.pushes += 1
            return Result(args, 1 if self.pushes <= push_failures else 0, '', '! [rejected] (fetch first)')

        if sync:
            runner.on(sys.executable, handler=sync)
        (runner.on('git', 'fetch')
         .on('git', 'rev-parse', 'origin/main', stdout=BASE)
         .on('git', 'worktree', 'add', handler=add)
         .on('git', 'worktree', 'remove')
         .on('git', 'add')
         .on('git', 'diff', '--cached', '--name-only', stdout='apps/a/staging/helmrelease.yaml\nplatform/auth/kustomization.yaml')
         .on('git', 'write-tree', stdout='tree0')
         .on('git', 'commit-tree', stdout='c' * 40)
         .on('git', 'push', handler=push)
         .on('make', returncode=checks, stdout='[BAD] policy: broken' if checks else ''))
        return runner

    def commits(self, runner):
        return [c for c in runner.calls if c[:2] == ('git', 'commit-tree')]

    def removed(self, runner):
        return [c for c in runner.calls if c[:3] == ('git', 'worktree', 'remove')]

    def test_a_lost_push_starts_again_from_the_newer_main(self):
        runner, seen = self.runner(push_failures=2), []
        with contextlib.redirect_stdout(io.StringIO()) as out:
            made = bots.push_main(runner, Path('/repo'), lambda work, base: seen.append(base) or bots.Commit('m', ('p' * 40,)))
        self.assertEqual((made, self.pushes, len(seen)), ('c' * 40, 3, 3))
        self.assertEqual(runner.calls.count(('git', 'fetch', 'origin', 'main')), 3)
        self.assertIn('attempt 2 of 5', out.getvalue())
        self.assertEqual(self.commits(runner)[0], ('git', 'commit-tree', 'tree0', '-p', BASE, '-p', 'p' * 40, '-m', 'm'))
        self.assertEqual(len(self.removed(runner)), 3, 'every worktree is removed')
        self.assertNotIn('--force', [arg for c in runner.calls if c[:2] == ('git', 'push') for arg in c])

    def test_every_lost_push_is_a_failure(self):
        runner = self.runner(push_failures=5)
        with contextlib.redirect_stdout(io.StringIO()), self.assertRaisesRegex(RuntimeError, 'main kept moving: 5 pushes lost'):
            bots.push_main(runner, Path('/repo'), lambda work, base: bots.Commit('m'))
        self.assertEqual(self.pushes, 5)

    def test_nothing_to_change_pushes_nothing(self):
        runner = self.runner()
        self.assertIsNone(bots.push_main(runner, Path('/repo'), lambda work, base: None))
        self.assertEqual((self.pushes, self.commits(runner), len(self.removed(runner))), (0, [], 1))
        self.assertFalse([c for c in runner.calls if c[0] == 'make'], 'nothing changed: nothing to check')

    def test_a_failed_check_pushes_nothing_shows_its_output_and_is_not_retried(self):
        runner = self.runner(checks=2)
        with contextlib.redirect_stderr(io.StringIO()) as err, self.assertRaises(CommandError):
            bots.push_main(runner, Path('/repo'), lambda work, base: bots.Commit('m'))
        self.assertIn('[BAD] policy: broken', err.getvalue())
        self.assertEqual((self.pushes, self.commits(runner), len(self.removed(runner))), (0, [], 1))

    def test_a_refusal_from_change_or_ready_ends_it_without_a_push(self):
        class Refused(Exception):
            pass

        def refuse(*_):
            raise Refused('a person reviews it')
        for change, ready in ((refuse, lambda: None), (lambda work, base: bots.Commit('m'), refuse)):
            runner = self.runner()
            with self.subTest(ready=ready), self.assertRaises(Refused):
                bots.push_main(runner, Path('/repo'), change, ready=ready)
            self.assertEqual((self.pushes, len(self.removed(runner))), (0, 1))
            self.assertEqual(runner.calls.count(('git', 'fetch', 'origin', 'main')), 1, 'not retried')

    def test_deploy_pushes_what_succeeded_and_fails_the_run(self):
        log = '[OK] a/staging: image ghcr.io/x/a:2\n[OK] a/staging needs: port 8080\n[ERROR] b/staging: held\n'
        runner = self.runner(sync=lambda args, _: Result(args, 1, log, ''))
        with contextlib.redirect_stdout(io.StringIO()) as out:
            code = bots.main_deploy(['--root', '/repo'], runner=runner)
        self.assertEqual((code, self.pushes), (1, 1))
        self.assertIn('[ERROR] b/staging: held', out.getvalue())
        message = self.commits(runner)[0][-1]
        self.assertTrue(message.startswith('deploy: a (deploy workflow)\n\n[OK] a/staging: image'), message)
        self.assertIn('needs: port 8080', message)
        self.assertNotIn('[ERROR]', message)

    def test_render_names_the_apps_it_rendered(self):
        runner = self.runner(sync=lambda args, _: Result(args))
        with contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(bots.main_render(['--root', '/repo'], runner=runner), 0)
        self.assertIn('pushed cccccccccccc to main', out.getvalue())
        self.assertTrue(self.commits(runner)[0][-1].startswith('render: a (render workflow)\n\napps/a/staging/'))
        self.assertEqual(bots.apps_in(['apps/teams.yaml', 'platform/x.yaml']), '', 'no app folder: shared files')


@unittest.skipUnless(shutil.which('git') and shutil.which('make'), 'needs git and make')
class RealGitTests(unittest.TestCase):
    """A non-forced push is refused once main moved, and the change is made again on top."""

    def git(self, cwd, *args):
        return subprocess.run(['git', '-c', 'user.name=t', '-c', 'user.email=t@t', *args], cwd=cwd, check=True,
                              capture_output=True, text=True).stdout.strip()

    def test_a_lost_race_lands_on_top_of_the_other_writer(self):
        tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, tmp)
        self.git(tmp, 'init', '--bare', '-b', 'main', 'origin.git')
        seed = tmp / 'seed'
        self.git(tmp, 'clone', 'origin.git', 'seed')
        (seed / 'Makefile').write_text('check-repo check-apps:\n\t@true\n.PHONY: check-repo check-apps\n')
        self.git(seed, 'add', '-A')
        self.git(seed, 'commit', '-m', 'seed')
        self.git(seed, 'push', 'origin', 'HEAD:main')
        self.git(tmp, 'clone', 'origin.git', 'bot')
        bot = tmp / 'bot'
        attempts = []

        def change(work, base):
            attempts.append(base)
            if len(attempts) == 1:  # another writer pushes while this one works
                (seed / 'other.txt').write_text('other\n')
                self.git(seed, 'add', '-A')
                self.git(seed, 'commit', '-m', 'other writer')
                self.git(seed, 'push', 'origin', 'HEAD:main')
            (work / 'rendered.txt').write_text(f'on {base}\n')
            return bots.Commit('render: test')
        git = ('git', '-c', 'user.name=bot', '-c', 'user.email=bot@t')
        with contextlib.redirect_stdout(io.StringIO()) as out:
            made = bots.push_main(Runner(), bot, change, git=git)
        self.assertIn('attempt 1 of 5', out.getvalue())
        self.assertEqual(len(attempts), 2)
        log = self.git(tmp / 'origin.git', 'log', '--format=%s', 'main').splitlines()
        self.assertEqual(log, ['render: test', 'other writer', 'seed'])
        self.assertEqual(self.git(tmp / 'origin.git', 'rev-parse', 'main'), made)
        self.assertEqual(self.git(tmp / 'origin.git', 'show', 'main:rendered.txt'), f'on {attempts[1]}')
        self.assertEqual(self.git(bot, 'worktree', 'list').count('\n'), 0, 'only the main worktree is left')


if __name__ == '__main__':
    unittest.main()

"""bots.push_main: how Render, Deploy and the merge gate push to main (docs/architecture.md#how-changes-reach-the-cluster)."""
import contextlib
import io
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

from swhurl import bots
from swhurl.run import CommandError, FakeRunner, Result, Runner

BASE = 'b' * 40


class PushMainTests(unittest.TestCase):
    def runner(self, *, checks=0, push_failures=0, deploy=None,
               paths='apps/a/staging/helmrelease.yaml\nplatform/auth/kustomization.yaml'):
        runner = FakeRunner()
        self.pushes, self.worktrees = 0, []

        def add(args, _):
            Path(args[-2]).mkdir(parents=True)
            self.worktrees.append(Path(args[-2]))
            return Result(args)

        def push(args, _):
            self.pushes += 1
            return Result(args, 1 if self.pushes <= push_failures else 0, '', '! [rejected] (fetch first)')

        if deploy is not None:  # before the generic make rule: the first match wins
            runner.on('make', 'v2-deploy', handler=deploy)
        (runner.on('git', 'fetch')
         .on('git', 'rev-parse', 'origin/main', stdout=BASE)
         .on('git', 'worktree', 'add', handler=add)
         .on('git', 'worktree', 'remove')
         .on('git', 'add')
         .on('git', 'diff', '--cached', '--name-only', stdout=paths)
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

    def test_deploy_v2_pushes_what_moved_and_fails_the_run(self):
        report = ('{"kind":"v2-deploy-records","version":1,"records":"v2-records","instances":['
                  '{"application":"a","environment":"staging","outcome":"changed","tag":"5-abc"},'
                  '{"application":"b","environment":"staging","outcome":"refused","reason":"refused: declaration: absent"}]}\n')
        lines = 'a/staging: changed 5-abc\nb/staging: refused declaration: absent\n1 changed, 0 unchanged, 1 refused, 0 failed\n'
        runner = self.runner(deploy=lambda args, _: Result(args, 2, report, lines),
                             paths='v2-records/a/staging/release.json\nv2-records/a/staging/instance.json\n'
                                   'v2-apps/instances/a/staging/helmrelease.json')
        with contextlib.redirect_stdout(io.StringIO()) as out:
            code = bots.main_deploy_v2(['--root', '/repo'], runner=runner)
        self.assertEqual((code, self.pushes), (1, 1), 'the refusal keeps its image and the run still fails')
        self.assertIn('b/staging: refused declaration: absent', out.getvalue())
        message = self.commits(runner)[0][-1]
        self.assertTrue(message.startswith('deploy: a (v2 deploy workflow)\n\nv2-records/a/staging/release.json'), message)
        self.assertNotIn('refused', message)
        self.assertIn(('make', 'check-v2'), runner.calls)
        self.assertFalse([c for c in runner.calls if c[:2] == ('make', '-j2')], "v1's checks are not v2's")
        self.assertIn(('make', '-s', 'v2-users'), runner.calls, 'a release that publishes needs the broker user list')
        self.assertIn(('make', '-s', 'v2-reloader'), runner.calls)

    def test_deploy_v2_pushes_nothing_when_no_instance_moved(self):
        report = ('{"kind":"v2-deploy-records","version":1,"records":"v2-records","instances":['
                  '{"application":"a","environment":"staging","outcome":"unchanged","tag":"4-abc"}]}\n')
        runner = self.runner(deploy=lambda args, _: Result(args, 0, report, ''), paths='')
        with contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(bots.main_deploy_v2(['--root', '/repo'], runner=runner), 0)
        self.assertEqual((self.pushes, self.commits(runner)), (0, []))
        self.assertIn('Nothing changed.', out.getvalue())
        self.assertFalse([c for c in runner.calls if c[0] == 'make' and c[1] not in ('v2-deploy', '-s')],
                         'nothing changed: nothing to render or check')

    def test_render_names_the_apps_it_rendered(self):
        runner = self.runner()
        with contextlib.redirect_stdout(io.StringIO()) as out:
            self.assertEqual(bots.main_render(['--root', '/repo'], runner=runner), 0)
        self.assertIn(('make', '-s', 'v2-render'), runner.calls, "v2's one render writes the platform documents too")
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

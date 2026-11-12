"""host/push-main.sh and images/pin-console.sh: how a bot pushes to main, against real Git.

Each test has a bare ``origin``, a checkout the script runs from, and a second clone that plays another writer. A
fake ``make`` on PATH renders, deploys and checks as the test asks (and can push from the other clone while the checks
run, so the bot's push is really refused); a fake ``curl`` plays GHCR. Nothing leaves the temporary directory.
"""
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CONSOLE = 'platform/console/helmrelease.yaml'
NOTIFY = 'platform/notify/helmrelease.yaml'
INPUTS = ('images/console/Dockerfile', '.dockerignore', 'pyproject.toml', 'uv.lock', 'tools/x', 'v2/x',
          'clusters/home/flux-system/sources/configmap-platform-settings.yaml')
DIGEST = 'sha256:' + 'b' * 64
IDENTITY = {'GIT_AUTHOR_NAME': 'github-actions[bot]', 'GIT_AUTHOR_EMAIL': 'bot@example.invalid',
            'GIT_COMMITTER_NAME': 'github-actions[bot]', 'GIT_COMMITTER_EMAIL': 'bot@example.invalid'}

MAKE = r"""#!/usr/bin/env bash
# The fake make: logs its arguments, then renders, deploys or checks as the test's environment says.
set -Eeuo pipefail
echo "$*" >>"$CALLS"
write() { [[ -z "$1" ]] || { mkdir -p "$(dirname "$1")"; echo "$2" >>"$1"; }; }
case "$*" in
  '-s v2-render') write "${RENDER_PATH:-}" rendered ;;
  v2-deploy) echo 'deploy: notes refused (fixture)'; write "${DEPLOY_PATH:-}" deployed; exit "${DEPLOY_CODE:-0}" ;;
  *)
    raced=$(cat "$RACES" 2>/dev/null || echo 0)
    if (( raced < ${RACE:-0} )); then  # another writer pushes while the bot checks
      echo $((raced + 1)) >"$RACES"
      git -C "$OTHER" pull --quiet --ff-only origin main
      echo "other $raced" >>"$OTHER/${RACE_FILE:-other.txt}"
      git -C "$OTHER" add -A && git -C "$OTHER" commit --quiet -m "other writer $raced"
      git -C "$OTHER" push --quiet origin HEAD:main
    fi
    if [[ "${CHECK_CODE:-0}" != 0 ]]; then echo '[BAD] policy: broken'; exit "$CHECK_CODE"; fi ;;
esac
"""

CURL = r"""#!/usr/bin/env bash
# The fake GHCR: a token for the anonymous token request, and the HEAD reply the test asks for.
set -Eeuo pipefail
printf '%s\n' "$*" >>"$CURLS"
case "$*" in
  *ghcr.io/token*) echo '{"token":"anon-token"}' ;;
  *--head*)
    grep -q 'Authorization: Bearer anon-token' && echo stdin-token >>"$CURLS"
    if [[ "${PUBLISHED:-true}" == true ]]; then printf 'HTTP/2 200\r\ndocker-content-digest: %s\r\n\r\n' "$DIGEST"
    else printf 'HTTP/2 404\r\n\r\n'; fi ;;
  *) exit 22 ;;
esac
"""


class Fixture(unittest.TestCase):
    """origin, the checkout, the other writer's clone and the fakes."""

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        self.bin = self.tmp / 'bin'
        self.bin.mkdir()
        for name, text in (('make', MAKE), ('curl', CURL)):
            (self.bin / name).write_text(text)
            (self.bin / name).chmod(0o700)
        (self.tmp / 'gitconfig').write_text('[init]\n\tdefaultBranch = main\n')
        self.env = {k: v for k, v in os.environ.items() if not k.startswith('GIT_')}
        self.env.update(IDENTITY, PATH=f'{self.bin}:{os.environ["PATH"]}', GIT_CONFIG_GLOBAL=str(self.tmp / 'gitconfig'),
                        GIT_CONFIG_NOSYSTEM='1', CALLS=str(self.tmp / 'calls'), CURLS=str(self.tmp / 'curls'),
                        RACES=str(self.tmp / 'races'), OTHER=str(self.tmp / 'other'), DIGEST=DIGEST,
                        TMPDIR=str(self.tmp))
        self.origin, self.checkout = self.tmp / 'origin.git', self.tmp / 'checkout'
        self.git('init', '--quiet', '--bare', str(self.origin))
        seed = self.tmp / 'seed'
        self.git('init', '--quiet', str(seed))
        for path in ('host/push-main.sh', 'images/content-tag.sh', 'images/pin-console.sh', CONSOLE, NOTIFY):
            (seed / path).parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(ROOT / path, seed / path)
        for path in INPUTS:
            (seed / path).parent.mkdir(parents=True, exist_ok=True)
            (seed / path).write_text(f'{path}\n')
        self.git('-C', str(seed), 'add', '-A')
        self.git('-C', str(seed), 'commit', '--quiet', '-m', 'seed')
        self.git('-C', str(seed), 'push', '--quiet', str(self.origin), 'HEAD:refs/heads/main')
        for clone in (self.checkout, self.tmp / 'other'):
            self.git('clone', '--quiet', str(self.origin), str(clone))
        self.seed_main = self.main()

    def git(self, *args):
        return subprocess.run(['git', *args], env=self.env, check=True, capture_output=True, text=True).stdout.strip()

    def main(self):
        return self.git('--git-dir', str(self.origin), 'rev-parse', 'main')

    def show(self, revision, path):
        return self.git('--git-dir', str(self.origin), 'show', f'{revision}:{path}')

    def run_bot(self, *args, **env):
        result = subprocess.run(['bash', str(self.checkout / 'host/push-main.sh'), *args], env={**self.env, **env},
                                capture_output=True, text=True)
        return result.returncode, result.stdout + result.stderr

    def calls(self):
        path = self.tmp / 'calls'
        return path.read_text().splitlines() if path.exists() else []

    def pushed(self):
        """The commits the bot pushed on top of the seed, newest first: (author, message)."""
        log = self.git('--git-dir', str(self.origin), 'log', '--format=%an%x00%B%x01', f'{self.seed_main}..main')
        return [tuple(entry.strip('\n').split('\0', 1)) for entry in log.split('\1') if entry.strip()]

    def tag(self):
        return subprocess.run(['bash', str(self.checkout / 'images/content-tag.sh'), 'console'], env=self.env,
                              check=True, capture_output=True, text=True).stdout.strip()

    def assertNoWorktreeLeft(self):
        self.assertEqual(len(self.git('-C', str(self.checkout), 'worktree', 'list').splitlines()), 1)
        self.assertEqual(sorted(p.name for p in self.tmp.glob('bot-*')), [])



class PushMainTests(Fixture):
    def test_render_pushes_the_rendered_files_as_one_commit_on_main(self):
        code, out = self.run_bot('render', RENDER_PATH='apps/notes/staging/x.yaml')
        self.assertEqual(code, 0, out)
        self.assertEqual(self.pushed(), [('github-actions[bot]', 'render: notes (render workflow)\n\napps/notes/staging/x.yaml')])
        self.assertEqual(self.git('--git-dir', str(self.origin), 'rev-parse', 'main^'), self.seed_main)
        self.assertIn(f'pushed {self.main()[:12]} to main', out)
        self.assertEqual(self.calls(), ['-s v2-render', '-j2 --output-sync=target check-repo check-apps'])
        self.assertNoWorktreeLeft()

    def test_a_render_of_shared_files_names_them_so(self):
        code, out = self.run_bot('render', RENDER_PATH='platform/nats/users.yaml')
        self.assertEqual(code, 0, out)
        self.assertEqual(self.pushed()[0][1], 'render: shared files (render workflow)\n\nplatform/nats/users.yaml')

    def test_nothing_to_render_pushes_nothing_and_checks_nothing(self):
        code, out = self.run_bot('render')
        self.assertEqual((code, self.main(), self.calls()), (0, self.seed_main, ['-s v2-render']), out)
        self.assertIn('nothing to do', out)
        self.assertNoWorktreeLeft()

    def test_a_lost_push_lands_on_top_of_the_other_writer(self):
        code, out = self.run_bot('render', RENDER_PATH='apps/notes/staging/x.yaml', RACE='2')
        self.assertEqual(code, 0, out)
        self.assertIn('attempt 1 of 5', out)
        self.assertIn('attempt 2 of 5', out)
        self.assertEqual([message.split('\n')[0] for _, message in self.pushed()],
                         ['render: notes (render workflow)', 'other writer 1', 'other writer 0'])
        self.assertEqual(self.calls().count('-s v2-render'), 3, 'each attempt renders the newer main again')
        self.assertNoWorktreeLeft()

    def test_every_lost_push_fails_the_run(self):
        code, out = self.run_bot('render', RENDER_PATH='apps/notes/staging/x.yaml', RACE='5')
        self.assertEqual(code, 1, out)
        self.assertIn('main kept moving: 5 pushes lost', out)
        self.assertFalse([m for _, m in self.pushed() if m.startswith('render')])
        self.assertNoWorktreeLeft()

    def test_a_failed_check_shows_its_output_pushes_nothing_and_is_not_retried(self):
        code, out = self.run_bot('render', RENDER_PATH='apps/notes/staging/x.yaml', CHECK_CODE='2')
        self.assertEqual((code, self.main()), (1, self.seed_main), out)
        self.assertIn('[BAD] policy: broken', out)
        self.assertEqual(self.calls().count('-s v2-render'), 1)
        self.assertNoWorktreeLeft()

    def test_deploy_pushes_what_moved_checks_v2_and_fails_the_run_for_a_held_instance(self):
        code, out = self.run_bot('deploy', DEPLOY_PATH='v2-records/notes/staging/release.json', DEPLOY_CODE='1')
        self.assertEqual(code, 1, out)
        self.assertEqual(self.pushed()[0][1], 'deploy: notes (v2 deploy workflow)\n\nv2-records/notes/staging/release.json')
        self.assertEqual(self.calls(), ['v2-deploy', '-s v2-render', 'check-v2'])
        self.assertIn('deploy: notes refused (fixture)', out, "the deploy's own output is shown")

    def test_deploy_with_nothing_moved_pushes_nothing(self):
        for deploy_code, code in (('0', 0), ('1', 1)):
            with self.subTest(deploy_code=deploy_code):
                got, out = self.run_bot('deploy', DEPLOY_CODE=deploy_code)
                self.assertEqual((got, self.main()), (code, self.seed_main), out)
                self.assertIn('Nothing changed.', out)

    def test_the_console_pin_on_the_newest_main(self):
        tag = self.tag()
        before = (self.checkout / CONSOLE).read_text()
        code, out = self.run_bot('console-image', '--expect', tag, RENDER_PATH='apps/notes/staging/x.yaml')
        self.assertEqual(code, 0, out)
        self.assertEqual(self.pushed(), [('github-actions[bot]', f'deploy: console image {tag} (Validate)')])
        for path in (CONSOLE, NOTIFY):
            pinned = self.show('main', path)
            self.assertIn(f'tag: {tag}', pinned, path)
            self.assertIn(f'digest: {DIGEST}', pinned, path)
        self.assertEqual(self.show('main', 'apps/notes/staging/x.yaml'), 'rendered', 'the pin carries the render')
        changed = [line for line in self.git('--git-dir', str(self.origin), 'diff', '-U0', f'{self.seed_main}', 'main', '--', CONSOLE).splitlines()
                   if line[:1] in '+-' and not line.startswith(('+++', '---'))]
        self.assertEqual(len(changed), 4, 'only the tag and digest lines change; comments stay')
        self.assertEqual((self.checkout / CONSOLE).read_text(), before, 'the checkout is left alone')
        curls = (self.tmp / 'curls').read_text()
        self.assertIn('stdin-token', curls)
        self.assertNotIn('anon-token', curls.replace('stdin-token', ''), 'the token goes on stdin only')
        self.assertNotIn('anon-token', out)

    def test_the_console_pin_does_nothing_when_main_builds_other_inputs(self):
        code, out = self.run_bot('console-image', '--expect', 'src-0000000000000000')
        self.assertEqual((code, self.main(), self.calls()), (0, self.seed_main, []), out)
        self.assertIn('nothing to do: main now builds', out)

    def test_main_moving_to_other_inputs_after_a_lost_push_pins_nothing(self):
        code, out = self.run_bot('console-image', '--expect', self.tag(), RACE='1', RACE_FILE='tools/x')
        self.assertEqual(code, 0, out)
        self.assertIn('nothing to do: main now builds', out)
        self.assertEqual([m for _, m in self.pushed()], ['other writer 0'])

    def test_an_already_pinned_console_pushes_nothing(self):
        self.assertEqual(self.run_bot('console-image', '--expect', self.tag())[0], 0)
        pinned = self.main()
        code, out = self.run_bot('console-image', '--expect', self.tag())
        self.assertEqual((code, self.main()), (0, pinned), out)
        self.assertIn('already pinned', out)

    def test_an_unpublished_console_image_fails_with_nothing_pushed(self):
        code, out = self.run_bot('console-image', '--expect', self.tag(), PUBLISHED='false')
        self.assertEqual((code, self.main()), (1, self.seed_main), out)
        self.assertIn('is not published yet', out)
        self.assertNoWorktreeLeft()

    def test_usage(self):
        for args in ((), ('nothing',), ('render', 'extra'), ('console-image',), ('console-image', '--expect', 'x')):
            with self.subTest(args=args):
                code, out = self.run_bot(*args)
                self.assertEqual((code, self.calls()), (2, []), out)
                self.assertIn('usage:', out)


class PinConsoleTests(Fixture):
    """images/pin-console.sh in its own checkout, as make console-image runs it."""

    def pin(self, **env):
        result = subprocess.run(['bash', str(self.checkout / 'images/pin-console.sh')], env={**self.env, **env},
                                capture_output=True, text=True)
        return result.returncode, result.stdout + result.stderr

    def test_uncommitted_inputs_are_refused(self):
        before = (self.checkout / CONSOLE).read_text()
        (self.checkout / 'tools/x').write_text('changed\n')
        code, out = self.pin()
        self.assertEqual(code, 1, out)
        self.assertIn('uncommitted changes', out)
        self.assertEqual((self.checkout / CONSOLE).read_text(), before)

    def test_a_release_without_exactly_one_tag_line_writes_nothing(self):
        console, notify = self.checkout / CONSOLE, self.checkout / NOTIFY
        console.write_text(console.read_text() + '              tag: again\n')
        self.git('-C', str(self.checkout), 'commit', '--quiet', '-am', 'two tags')
        before = (console.read_text(), notify.read_text())
        code, out = self.pin()
        self.assertEqual(code, 1, out)
        self.assertIn(f'{CONSOLE} must have exactly one image tag: line (found 2)', out)
        self.assertEqual((console.read_text(), notify.read_text()), before)

    def test_a_missing_notify_release_is_skipped_and_an_unmarked_one_refused(self):
        notify = self.checkout / NOTIFY
        text = notify.read_text()
        notify.unlink()
        self.git('-C', str(self.checkout), 'commit', '--quiet', '-am', 'no notify')
        code, out = self.pin()
        self.assertEqual(code, 0, out)
        self.assertIn(f'[OK] pinned {CONSOLE} to', out)
        notify.write_text(text.replace(' # image: console', ''))
        code, out = self.pin()
        self.assertEqual(code, 1, out)
        self.assertIn('has no tag: line marked "# image: console"', out)

    def test_make_console_image_has_no_dry_run(self):
        make = shutil.which('make', path=os.environ['PATH'])  # the real one, not the fixture's
        result = subprocess.run([make, '--no-print-directory', 'console-image', 'DRY_RUN=true'], cwd=ROOT,
                                env=self.env, capture_output=True, text=True)
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn('make console-image has no dry run', result.stderr)
        self.assertEqual(self.calls(), [])


if __name__ == '__main__':
    unittest.main()

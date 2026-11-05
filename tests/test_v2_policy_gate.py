"""The zero-argument entrypoint runs whole-tree Go checks and preserves their diagnostics."""
import contextlib
import io
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from swhurl import ROOT, platform
from swhurl.apps import validation
from swhurl.run import FakeRunner


class V2PolicyGateTests(unittest.TestCase):
    def root(self, temporary):
        root = Path(temporary)
        profile = root / platform.PROFILE
        profile.parent.mkdir(parents=True)
        profile.write_text(json.dumps({'chart': {'name': 'app-template', 'version': '5.2.1', 'repository': 'snapshot'}}))
        repositories = root / platform.HELM_REPOSITORIES
        repositories.parent.mkdir(parents=True)
        repositories.write_text('kind: HelmRepository\nmetadata: {name: snapshot}\nspec: {url: https://snapshot.example.test}\n')
        return root

    def test_local_chart_is_explicit_and_output_and_exit_status_are_preserved(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = self.root(temporary)
            for code in (0, 1, 2, 9):
                with self.subTest(code=code):
                    report = '{"schema":"v2-policy","instances":[]}\n'
                    diagnostic = 'source render difference or Go finding\n' if code else ''
                    runner = FakeRunner().on('bash', str(root / validation.RENDERED),
                                             returncode=code, stdout=report, stderr=diagnostic)
                    out, err = io.StringIO(), io.StringIO()
                    with mock.patch.object(validation.charts, 'chart_dir', return_value=Path('/local/pinned')) as chart, \
                         contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                        result = validation.main([], runner, root=root)
                    chart.assert_called_once_with('app-template', '5.2.1', 'https://snapshot.example.test', runner)
                    self.assertEqual(runner.calls, [('bash', str(root / validation.RENDERED), '--chart', '/local/pinned')])
                    self.assertEqual(result, code)
                    self.assertIn(report, out.getvalue())
                    self.assertEqual(err.getvalue(), diagnostic)
                    self.assertIn('[BAD]' if code else '[OK]', out.getvalue())

    def test_unexpected_arguments_are_refused_before_any_reads_or_calls(self):
        for argv in (['tests/fixtures/apps/smoke-web/staging'], ['--chart', '/local/chart'], ['--help']):
            with self.subTest(argv=argv):
                runner = FakeRunner()
                with mock.patch.object(Path, 'read_text', side_effect=AssertionError('file read')), \
                     mock.patch.object(validation.charts, 'chart_dir') as chart, \
                     mock.patch.object(validation.charts, 'helm_repositories') as repositories, \
                     mock.patch.object(validation.Runner, 'from_environment') as create_runner, \
                     contextlib.redirect_stderr(io.StringIO()) as err:
                    self.assertEqual(validation.main(argv, runner), 2)
                    self.assertEqual(validation.main(argv), 2)
                self.assertIn('usage: check-apps', err.getvalue())
                self.assertEqual(runner.calls, [])
                chart.assert_not_called()
                repositories.assert_not_called()
                create_runner.assert_not_called()

    def test_invalid_profile_or_repositories_do_not_acquire_a_chart(self):
        for location, contents in ((platform.PROFILE, None), (platform.PROFILE, '[]'),
                                   (platform.PROFILE, '{'), (platform.HELM_REPOSITORIES, 'bad: ['),
                                   (platform.HELM_REPOSITORIES, 'kind: ConfigMap'),
                                   (platform.HELM_REPOSITORIES, 'not-a-document')):
            with self.subTest(location=location, contents=contents), tempfile.TemporaryDirectory() as temporary:
                root = self.root(temporary)
                path = root / location
                path.unlink() if contents is None else path.write_text(contents)
                runner = FakeRunner()
                with mock.patch.object(validation.charts, 'chart_dir') as chart, \
                     contextlib.redirect_stderr(io.StringIO()) as err:
                    self.assertEqual(validation.main([], runner, root=root), 1)
                self.assertIn('invalid or unreadable pinned chart metadata', err.getvalue())
                chart.assert_not_called()
                self.assertEqual(runner.calls, [])

    def test_failed_chart_pull_never_runs_the_gate(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = self.root(temporary)
            runner = FakeRunner().on('helm', 'pull', returncode=7, stderr='chart pull refused')
            with mock.patch.object(validation.charts, 'CACHE', root / 'cache'), \
                 contextlib.redirect_stderr(io.StringIO()) as err:
                self.assertNotEqual(validation.main([], runner, root=root), 0)
            self.assertIn('chart pull refused', err.getvalue())
            self.assertEqual([call[:2] for call in runner.calls], [('helm', 'pull')])

    def test_silent_gate_failure_is_still_reported(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = self.root(temporary)
            runner = FakeRunner().on('bash', returncode=9)
            with mock.patch.object(validation.charts, 'chart_dir', return_value=Path('/local/chart')), \
                 contextlib.redirect_stdout(io.StringIO()) as out:
                self.assertEqual(validation.main([], runner, root=root), 9)
            self.assertIn('[BAD]', out.getvalue())
            self.assertIn('9', out.getvalue())

    def test_shell_stops_on_render_failure_and_propagates_policy_failure(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            go = root / 'go'
            go.write_text('''#!/bin/bash
set -eu
while [ "$1" != -o ]; do shift; done
cp "$POLICY_TEST_CLI" "$2"
chmod +x "$2"
''')
            go.chmod(0o700)
            cli = root / 'cli'
            cli.write_text('''#!/bin/bash
set -eu
case "$1" in
  render) exit "$POLICY_TEST_RENDER_STATUS" ;;
  policy) touch "$POLICY_TEST_MARKER"; exit 9 ;;
  *) exit 99 ;;
esac
''')
            marker = root / 'policy-invoked'
            for render_status in (7, 0):
                environment = dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}', POLICY_TEST_CLI=str(cli),
                                   POLICY_TEST_MARKER=str(marker), POLICY_TEST_RENDER_STATUS=str(render_status))
                result = subprocess.run(['bash', 'v2/check-rendered.sh', '--chart', '/local/chart'], cwd=ROOT,
                                        env=environment, capture_output=True, text=True)
                self.assertEqual(result.returncode, 1 if render_status else 9, result.stderr)
                self.assertEqual(marker.exists(), render_status == 0)

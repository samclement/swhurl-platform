"""The Python-free whole-tree gate preserves ordering, diagnostics and failures."""
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from swhurl import ROOT, commands, yamlio


class V2PolicyGateTests(unittest.TestCase):
    def executable(self, path, content):
        path.write_text('#!/bin/bash\nset -eu\n' + content)
        path.chmod(0o700)

    def test_unexpected_arguments_are_refused_before_reads_or_commands(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.executable(root / 'git', 'touch "$UNEXPECTED_CALL"; exit 99\n')
            marker = root / 'called'
            env = dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}', UNEXPECTED_CALL=str(marker))
            for script in ('check-apps.sh', 'chart.sh'):
                for argv in (['v2-apps/instances/demo-shop/prod'], ['--chart', '/local/chart'], ['--help']):
                    result = subprocess.run(['bash', str(ROOT / 'v2' / script), *argv], cwd=root,
                                            env=env, capture_output=True, text=True)
                    self.assertEqual(result.returncode, 2)
                    self.assertIn('usage:', result.stderr)
                    self.assertFalse(marker.exists())

    def test_make_gate_preserves_reports_and_stops_on_each_failure_without_python(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.executable(root / 'go', '''while [ "$1" != -o ]; do shift; done
cp "$POLICY_TEST_CLI" "$2"
chmod +x "$2"
''')
            self.executable(root / 'cli', '''printf '%s\\n' "$1" >> "$POLICY_TEST_CALLS"
case "$1" in
  chart)
    test "$#" = 7
    test "$2" = --profile && test "$3" = v2/testdata/profile.json
    test "$4" = --repositories && test "$5" = clusters/home/flux-system/sources/helmrepositories.yaml
    test "$6" = --cache && test "$7" = "$CHART_EXPECTED_CACHE"
    if [ "$CHART_STATUS" != 0 ]; then echo chart-failed >&2; exit "$CHART_STATUS"; fi
    printf '%s\\n' '/local/chart with spaces' ;;
  render) exit "$RENDER_STATUS" ;;
  policy)
    test "${@: -2:1}" = --chart
    test "${@: -1}" = '/local/chart with spaces'
    echo '{"schema":"v2-policy","instances":[]}'
    echo policy-diagnostic >&2
    exit "$POLICY_STATUS" ;;
  *) exit 99 ;;
esac
''')
            self.executable(root / 'python3', 'echo unexpected-python >&2; exit 88\n')
            calls = root / 'calls'
            for chart, render, policy, expected, sequence in (
                    (7, 0, 0, 7, ['chart']), (0, 7, 0, 1, ['chart', 'render']),
                    (0, 0, 9, 9, ['chart', 'render', 'render', 'policy']),
                    (0, 0, 0, 0, ['chart', 'render', 'render', 'policy'])):
                with self.subTest(chart=chart, render=render, policy=policy):
                    calls.unlink(missing_ok=True)
                    env = dict(os.environ, PATH=f'{root}:{os.environ["PATH"]}', POLICY_TEST_CLI=str(root / 'cli'),
                               POLICY_TEST_CALLS=str(calls), CHART_STATUS=str(chart), RENDER_STATUS=str(render),
                               XDG_CACHE_HOME=str(root / 'cache with spaces'),
                               CHART_EXPECTED_CACHE=str(root / 'cache with spaces/swhurl-platform/charts'),
                               POLICY_STATUS=str(policy))
                    result = subprocess.run(['bash', 'v2/check-apps.sh'], cwd=ROOT, env=env,
                                            capture_output=True, text=True)
                    self.assertEqual(result.returncode, expected, result.stderr)
                    self.assertEqual(calls.read_text().splitlines(), sequence)
                    self.assertNotIn('unexpected-python', result.stderr)
                    if sequence[-1] == 'policy':
                        self.assertIn('{"schema":"v2-policy","instances":[]}', result.stdout)
                        self.assertIn('policy-diagnostic', result.stderr)
                        self.assertIn('[BAD]' if policy else '[OK]', result.stderr if policy else result.stdout)
                    if expected == 0:
                        result = subprocess.run(['make', '--no-print-directory', 'check-apps'], cwd=ROOT, env=env,
                                                capture_output=True, text=True)
                        self.assertEqual(result.returncode, 0, result.stderr)
                        env.pop('XDG_CACHE_HOME')
                        env['HOME'] = str(root / 'home')
                        env['CHART_EXPECTED_CACHE'] = str(root / 'home/.cache/swhurl-platform/charts')
                        result = subprocess.run(['bash', 'v2/chart.sh'], cwd=ROOT, env=env,
                                                capture_output=True, text=True)
                        self.assertEqual(result.returncode, 0, result.stderr)

    def test_entrypoint_and_ci_use_one_acquisition_path(self):
        self.assertFalse(commands.TARGETS['check-apps'].run)
        self.assertFalse((ROOT / 'tools/swhurl/apps/validation.py').exists())
        for name in ('validate.yml', 'deploy-v2.yml'):
            workflow = yamlio.load((ROOT / '.github/workflows' / name).read_text())
            fetches = [step for job in workflow['jobs'].values() for step in job.get('steps', [])
                       if step.get('name') == 'Fetch the chart v2 compiles for']
            self.assertEqual(len(fetches), 1)
            self.assertEqual(fetches[0]['run'], 'bash v2/chart.sh')

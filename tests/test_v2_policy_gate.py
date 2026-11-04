"""The old entrypoint must run Go policy for all records and propagate either failed check."""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from swhurl import ROOT, platform
from swhurl.apps import policy
from swhurl.run import FakeRunner


class V2PolicyGateTests(unittest.TestCase):
    def test_local_chart_is_explicit_and_failure_is_not_success(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            profile = root / platform.PROFILE
            profile.parent.mkdir(parents=True)
            profile.write_text(json.dumps({'chart': {'name': 'app-template', 'version': '5.2.1', 'repository': 'bjw-s'}}))
            for code in (0, 1, 2):
                runner = FakeRunner().on('bash', str(root / policy.RENDERED), returncode=code, stderr='failed check' if code else '')
                with mock.patch.object(policy, 'chart_dir', return_value=Path('/local/pinned')) as chart, \
                     mock.patch.object(policy, 'helm_repositories', return_value={'bjw-s': 'https://charts.example.test'}):
                    result = policy.records_rendered(runner, root)
                chart.assert_called_once_with('app-template', '5.2.1', 'https://charts.example.test', runner)
                self.assertEqual(runner.calls, [('bash', str(root / policy.RENDERED), '--chart', '/local/pinned')])
                self.assertEqual(result, 'failed check' if code else None)

    def test_invalid_profile_does_not_acquire_a_chart(self):
        with tempfile.TemporaryDirectory() as temporary, mock.patch.object(policy, 'chart_dir') as chart:
            self.assertIn('invalid or unreadable', policy.records_rendered(FakeRunner(), Path(temporary)))
            chart.assert_not_called()

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

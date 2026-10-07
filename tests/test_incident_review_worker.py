"""The analysis worker's wrapper script, driven with a stub ``codex`` on PATH."""
from __future__ import annotations

import json
import os
import stat
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / 'images/incident-review-worker/analyse.sh'
KEY = 'sk-fixture-not-a-real-key-000000000000'
STUB = r'''#!/usr/bin/env bash
# Records how it was called; behaviour comes from STUB_MODE.
log=$STUB_LOG
if [[ $1 == --version ]]; then echo "codex-cli 9.9.9"; exit 0; fi
if [[ $1 == login ]]; then
  printf 'login argv: %s\n' "$*" >>"$log"; printf 'login stdin: %s\n' "$(cat)" >>"$log"
  printf 'login home: %s\n' "$CODEX_HOME" >>"$log"
  [[ $STUB_MODE == login-fail ]] && exit 1
  exit 0
fi
printf 'exec argv: %s\n' "$*" >>"$log"
printf 'exec stdin bytes: %s\n' "$(wc -c)" >>"$log"
printf 'exec env key: %s\n' "${OPENAI_API_KEY:-unset}" >>"$log"
target=
while (($#)); do [[ $1 == --output-last-message ]] && target=$2; shift; done
case $STUB_MODE in
  ok) echo '{"summary":"s"}' >"$target"; echo "noisy model text" ;;
  empty) : ;;
  fail) echo "provider said no" >&2; exit 1 ;;
  hang) sleep 30 ;;
esac
'''


class WorkerScriptTests(unittest.TestCase):
    def run_script(self, mode='ok', *, bundle='{"version":1}', key=KEY, env=None):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for name in ('work/bundle', 'work/diagnosis', 'scratch', 'bin', 'home', 'secret'):
                (root / name).mkdir(parents=True)
            if bundle is not None:
                (root / 'work/bundle/bundle.json').write_text(bundle)
            (root / 'work/diagnosis/diagnosis.json').write_text('stale')
            (root / 'home/prompt.md').write_text('prompt\n')
            (root / 'home/diagnosis.schema.json').write_text('{}')
            if key is not None:
                (root / 'secret/key').write_text(key)
            stub = root / 'bin/codex'
            stub.write_text(STUB)
            stub.chmod(stub.stat().st_mode | stat.S_IEXEC)
            environment = {'PATH': f"{root / 'bin'}:{os.environ['PATH']}", 'WORK': str(root / 'work'),
                           'REVIEW_HOME': str(root / 'home'), 'SCRATCH': str(root / 'scratch'),
                           'OPENAI_KEY_FILE': str(root / 'secret/key'), 'CODEX_MODEL': 'fixture-model',
                           'STUB_MODE': mode, 'STUB_LOG': str(root / 'stub.log'), **(env or {})}
            done = subprocess.run(['bash', str(SCRIPT)], env=environment, capture_output=True, text=True, timeout=60)
            status = json.loads((root / 'work/diagnosis/status.json').read_text())
            diagnosis = root / 'work/diagnosis/diagnosis.json'
            log = (root / 'stub.log').read_text() if (root / 'stub.log').exists() else ''
            return {'code': done.returncode, 'output': done.stdout + done.stderr, 'status': status, 'log': log,
                    'diagnosis': diagnosis.read_text() if diagnosis.exists() else None,
                    'scratch': sorted(p.name for p in (root / 'scratch').iterdir())}

    def test_no_bundle_is_skipped_and_codex_is_never_started(self):
        for bundle in (None, ''):
            result = self.run_script(bundle=bundle)
            self.assertEqual((result['code'], result['status']['status'], result['diagnosis']), (0, 'skipped', None))
            self.assertNotIn('login', result['log'])
            self.assertNotIn('exec', result['log'])

    def test_collect_only_mode_is_disabled_without_login(self):
        result = self.run_script(env={'INCIDENT_REVIEW_MODE': 'collect-only'})
        self.assertEqual((result['code'], result['status']['status'], result['log']), (0, 'disabled', ''))

    def test_ok_run_records_versions_and_keeps_the_key_out_of_argv_env_and_logs(self):
        result = self.run_script()
        self.assertEqual(result['code'], 0)
        self.assertEqual(result['status'], {'version': 1, 'status': 'ok', 'cli': 'codex-cli 9.9.9',
                                            'model': 'fixture-model', 'seconds': result['status']['seconds'],
                                            'usage': None})
        self.assertEqual(result['diagnosis'].strip(), '{"summary":"s"}')
        self.assertIn(f'login stdin: {KEY}', result['log'])
        self.assertIn('login argv: login --with-api-key\n', result['log'])
        self.assertIn('exec env key: unset', result['log'])
        argv = next(line for line in result['log'].splitlines() if line.startswith('exec argv'))
        for expected in ('--sandbox read-only', '--ephemeral', '--ignore-user-config', '--model fixture-model',
                         '--skip-git-repo-check', '--output-schema', '--output-last-message'):
            self.assertIn(expected, argv)
        self.assertTrue(argv.endswith(' -'))
        self.assertNotIn(KEY, argv)
        self.assertNotIn(KEY, result['output'])
        self.assertNotIn('noisy model text', result['output'])
        self.assertIn('exec stdin bytes: 20', result['log'])  # prompt.md plus the bundle
        self.assertEqual(result['scratch'], [])  # CODEX_HOME and the workspace are removed

    def test_failures_exit_zero_with_a_status_and_no_diagnosis(self):
        cases = [('fail', {}, 'error'), ('empty', {}, 'error'), ('login-fail', {}, 'error'),
                 ('hang', {'ANALYSE_SECONDS': '1'}, 'timeout'), ('ok', {'CODEX_MODEL': ''}, 'error'),
                 ('ok', {'CODEX_MODEL': 'bad model; rm -rf /'}, 'error'), ('ok', {'ANALYSE_SECONDS': 'soon'}, 'error')]
        for mode, env, expected in cases:
            with self.subTest(mode=mode, env=env):
                result = self.run_script(mode, env=env)
                self.assertEqual((result['code'], result['status']['status'], result['diagnosis']),
                                 (0, expected, None))
                self.assertNotIn('provider said no', result['output'])
                self.assertNotIn('rm -rf', json.dumps(result['status']))
        missing = self.run_script(key=None)
        self.assertEqual((missing['status']['status'], missing['log']), ('error', ''))


if __name__ == '__main__':
    unittest.main()

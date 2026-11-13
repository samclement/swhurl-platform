"""make app-hooks: the recipe stops without either token and hands both to swhurl-v2 in its environment only.

The decisions are v2's (``swhurl-v2 app-hooks``, tested in Go); this is the glue: fake ``gh``, ``sops`` and ``go``
executables on PATH, where the fake ``go build`` writes a fake ``swhurl-v2`` that records its arguments and whether
it was given the tokens, so nothing is built, decrypted or sent.
"""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
GH_TOKEN = 'fixture-gh-token'
HOOK_TOKEN = 'fixture-image-webhook-token'
HOST = 'flux-webhook.' + json.loads((ROOT / 'v2/testdata/profile.json').read_text())['routing']['baseDomain']

FAKE = """#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
tool = Path(sys.argv[0]).name
with open(os.environ['CALLS'], 'a') as f:
    f.write(json.dumps({'tool': tool, 'argv': sys.argv[1:], 'key': os.environ.get('SOPS_AGE_KEY_FILE'),
                        'gh': os.environ.get('GH_TOKEN') == os.environ['WANT_GH'],
                        'hook': os.environ.get('IMAGE_WEBHOOK_TOKEN') == os.environ['WANT_HOOK']}) + '\\n')
mode = os.environ.get(tool.upper().replace('-', '_') + '_MODE', 'ok')
if mode == 'fail':
    print(tool + ': refused by the fixture', file=sys.stderr)
    sys.exit(1)
if tool == 'gh':
    print('' if mode == 'empty' else os.environ['WANT_GH'])
elif tool == 'sops':
    print('' if mode == 'empty' else os.environ['WANT_HOOK'])
elif tool == 'go':  # go build -o PATH ./cmd/swhurl-v2: the built binary is this script
    out = Path(sys.argv[sys.argv.index('-o') + 1])
    out.write_text(Path(sys.argv[0]).read_text())
    out.chmod(0o700)
else:
    print('[OK] notes: image webhook on samclement/notes is in place')
"""


class AppHooksRecipeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.bin = Path(self.tmp.name)
        self.calls = self.bin / 'calls'
        for tool in ('gh', 'sops', 'go'):
            (self.bin / tool).write_text(FAKE)
            (self.bin / tool).chmod(0o700)
        self.env = {name: value for name, value in os.environ.items()
                    if name not in ('GH_TOKEN', 'SOPS_AGE_KEY_FILE', 'IMAGE_WEBHOOK_TOKEN', 'DRY_RUN')}
        self.env.update(PATH=f'{self.bin}:{os.environ["PATH"]}', CALLS=str(self.calls), WANT_GH=GH_TOKEN,
                        WANT_HOOK=HOOK_TOKEN)

    def run_make(self, *args, **env):
        result = subprocess.run(['make', '--no-print-directory', 'app-hooks', *args], cwd=ROOT,
                                env={**self.env, **env}, capture_output=True, text=True)
        calls = [json.loads(line) for line in self.calls.read_text().splitlines()] if self.calls.exists() else []
        for secret in (GH_TOKEN, HOOK_TOKEN):
            self.assertNotIn(secret, result.stdout + result.stderr)
            self.assertFalse([call for call in calls if any(secret in arg for arg in call['argv'])],
                             'a token on a command line')
        return result, calls

    def test_stops_without_a_github_token(self):
        for mode in ('fail', 'empty'):
            with self.subTest(gh=mode):
                self.calls.unlink(missing_ok=True)
                result, calls = self.run_make(GH_MODE=mode)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('no GitHub token', result.stderr)
                self.assertEqual([call['tool'] for call in calls], ['gh'], 'nothing decrypted or built')

    def test_stops_when_the_webhook_token_cannot_be_read(self):
        for mode in ('fail', 'empty'):
            with self.subTest(sops=mode):
                self.calls.unlink(missing_ok=True)
                result, calls = self.run_make(GH_TOKEN=GH_TOKEN, SOPS_MODE=mode)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn('cannot read the image webhook token from platform/flux-webhook/image-secret.sops.yaml '
                              '(age key at ./age.agekey?)', result.stderr)
                self.assertEqual([call['tool'] for call in calls], ['sops'], 'gh not asked, nothing built')

    def test_hands_both_tokens_to_v2_in_its_environment(self):
        result, calls = self.run_make()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, '[OK] notes: image webhook on samclement/notes is in place\n')
        gh, sops, go, v2 = calls
        self.assertEqual((gh['tool'], gh['argv']), ('gh', ['auth', 'token']))
        self.assertEqual(sops['argv'], ['decrypt', '--extract', '["stringData"]["token"]',
                                        'platform/flux-webhook/image-secret.sops.yaml'])
        self.assertEqual(sops['key'], './age.agekey')
        self.assertEqual(go['tool'], 'go')
        self.assertFalse(go['gh'] or go['hook'], 'the build must not receive either token')
        self.assertEqual(v2['argv'], ['app-hooks', '--records', 'v2-records', '--owner', 'samclement', '--hook-host', HOST])
        self.assertTrue(v2['gh'] and v2['hook'], 'both tokens in the environment of swhurl-v2')

    def test_a_given_token_and_key_are_used_and_a_dry_run_is_passed_on(self):
        result, calls = self.run_make('DRY_RUN=true', GH_TOKEN=GH_TOKEN, SOPS_AGE_KEY_FILE='/keys/age.agekey')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([call['tool'] for call in calls], ['sops', 'go', 'swhurl-v2'])
        self.assertEqual(calls[0]['key'], '/keys/age.agekey')
        self.assertEqual(calls[2]['argv'][-1], '--dry-run')
        self.assertFalse(calls[1]['gh'] or calls[1]['hook'], 'inherited tokens must not reach the build')
        self.assertTrue(calls[2]['gh'] and calls[2]['hook'])

    def test_a_failing_v2_fails_the_target(self):
        result, _ = self.run_make(GH_TOKEN=GH_TOKEN, SWHURL_V2_MODE='fail')
        self.assertNotEqual(result.returncode, 0)


if __name__ == '__main__':
    unittest.main()

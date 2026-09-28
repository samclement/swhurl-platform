"""platform-certs (Git settings edit) and wait-secret-key (polling), offline."""
import base64
import io
import json
import shutil
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'tools'))

from swhurl import runtime_inputs, settings  # noqa: E402
from swhurl.run import FakeRunner, Result  # noqa: E402

SETTINGS_TEXT = """# Cluster settings (fixture comment must survive)
apiVersion: v1
kind: ConfigMap
metadata:
  name: platform-settings
  namespace: flux-system
data:
  CERT_ISSUER: letsencrypt-prod   # trailing note is replaced with the value line
  OAUTH_HOST: oauth.homelab.swhurl.com
"""


class SettingsTests(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.root)
        shutil.copytree(ROOT / settings.ISSUERS, self.root / settings.ISSUERS)
        self.path = self.root / settings.SETTINGS
        self.path.parent.mkdir(parents=True)
        self.path.write_text(SETTINGS_TEXT)

    def test_allowed_issuers_come_from_git(self):
        self.assertEqual(settings.platform_issuers(), {'letsencrypt-prod', 'letsencrypt-staging'})

    def test_changes_only_the_one_line(self):
        messages = settings.set_cert_issuer('letsencrypt-staging', root=self.root)
        self.assertEqual(messages[0], f'[INFO] CERT_ISSUER updated to letsencrypt-staging in {settings.SETTINGS}')
        before, after = SETTINGS_TEXT.splitlines(), self.path.read_text().splitlines()
        changed = [(a, b) for a, b in zip(before, after, strict=True) if a != b]
        self.assertEqual(changed, [(before[7], '  CERT_ISSUER: letsencrypt-staging')])
        self.assertTrue(self.path.read_text().startswith('# Cluster settings'))
        self.assertFalse(list(self.path.parent.glob('*.tmp')), 'temporary file left behind')

    def test_already_set_and_dry_run_do_not_write(self):
        self.assertIn('already set', settings.set_cert_issuer('letsencrypt-prod', root=self.root)[0])
        self.assertIn('would update', settings.set_cert_issuer('letsencrypt-staging', root=self.root, dry_run=True)[0])
        self.assertEqual(self.path.read_text(), SETTINGS_TEXT)

    def test_every_run_ends_with_the_git_reminder(self):
        for issuer in ('letsencrypt-prod', 'letsencrypt-staging'):
            self.assertEqual(settings.set_cert_issuer(issuer, root=self.root)[-1],
                             '[INFO] Local Git edits only. Commit + push, then run: make flux-reconcile')

    def test_refusals_leave_the_file_untouched(self):
        cases = {
            'untrusted issuer': ('selfsigned', SETTINGS_TEXT, 'unknown issuer'),
            'typo': ('letsencrypt-prd', SETTINGS_TEXT, 'unknown issuer'),
            'missing key': ('letsencrypt-staging', SETTINGS_TEXT.replace('  CERT_ISSUER: letsencrypt-prod   '
                            '# trailing note is replaced with the value line\n', ''), "Missing key 'CERT_ISSUER'"),
            'duplicate line': ('letsencrypt-staging', SETTINGS_TEXT + '#  CERT_ISSUER: x\n  CERT_ISSUER: y\n', 'exactly one'),
            'not a ConfigMap': ('letsencrypt-staging', 'kind: Secret\ndata:\n  CERT_ISSUER: a\n', 'ConfigMap'),
        }
        for label, (issuer, text, message) in cases.items():
            with self.subTest(label):
                self.path.write_text(text)
                with self.assertRaisesRegex(settings.SettingsError, message):
                    settings.set_cert_issuer(issuer, root=self.root)
                self.assertEqual(self.path.read_text(), text)

    def test_set_value_refuses_collateral_changes(self):
        tricky = SETTINGS_TEXT.replace('OAUTH_HOST: oauth.homelab.swhurl.com', 'OAUTH_HOST: |\n    CERT_ISSUER: nested')
        with self.assertRaises(settings.SettingsError):
            settings.set_value(tricky, 'CERT_ISSUER', 'letsencrypt-staging')

    def test_cli_usage_and_errors(self):
        err = io.StringIO()
        with redirect_stderr(err):
            self.assertEqual(settings.platform_certs([]), 2)
            self.assertEqual(settings.platform_certs(['selfsigned']), 1)
        self.assertIn('unknown issuer', err.getvalue())


def secret_runner(values):
    """A FakeRunner whose Secret reads return successive values (None = not found)."""
    values = list(values)

    def handler(args, _input):
        value = values.pop(0) if len(values) > 1 else values[0]
        if value is None:
            return Result(args, 1, '', 'NotFound')
        return Result(args, 0, json.dumps({'data': {'KEY': value} if value else {}}))

    return FakeRunner().on('kubectl', '-n', 'ns', 'get', 'secret', 'app', handler=handler)


class Clock:
    def __init__(self):
        self.now = 0.0
        self.sleeps = []

    def __call__(self):
        return self.now

    def sleep(self, seconds):
        self.sleeps.append(seconds)
        self.now += seconds


class WaitSecretKeyTests(unittest.TestCase):
    def wait(self, runner, clock, *extra):
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = runtime_inputs.wait_secret_key(['ns', 'app', 'KEY', *extra], runner, clock=clock, sleep=clock.sleep)
        return code, out.getvalue() + err.getvalue()

    def test_present_immediately(self):
        clock = Clock()
        code, text = self.wait(secret_runner([base64.b64encode(b'v').decode()]), clock)
        self.assertEqual(code, 0)
        self.assertEqual(clock.sleeps, [])
        self.assertIn('[INFO] ns/app is present', text)

    def test_appears_after_polling(self):
        clock = Clock()
        value = base64.b64encode(b'fixture-value').decode()
        code, text = self.wait(secret_runner([None, '', value]), clock, '--interval', '5')
        self.assertEqual(code, 0)
        self.assertEqual(clock.sleeps, [5, 5])
        self.assertNotIn(value, text)
        self.assertNotIn('fixture-value', text)

    def test_times_out(self):
        clock = Clock()
        code, text = self.wait(secret_runner([None]), clock, '--timeout', '12', '--interval', '5')
        self.assertEqual(code, 1)
        self.assertEqual(clock.sleeps, [5, 5, 5])
        self.assertIn('[ERROR] Timed out waiting for app propagation (12s)', text)

    def test_zero_timeout_still_checks_once(self):
        clock = Clock()
        runner = secret_runner([base64.b64encode(b'v').decode()])
        self.assertEqual(self.wait(runner, clock, '--timeout', '0')[0], 0)
        self.assertEqual(len(runner.calls), 1)


if __name__ == '__main__':
    unittest.main()

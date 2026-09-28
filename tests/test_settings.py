"""platform-certs (Git settings edit), offline."""
import io
import shutil
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path

from swhurl import ROOT, settings

SETTINGS_TEXT = """# Cluster settings (fixture comment must survive)
apiVersion: v1
kind: ConfigMap
metadata:
  name: platform-settings
  namespace: flux-system
data:
  CERT_ISSUER: letsencrypt-prod   # trailing note is replaced with the value line
  BASE_DOMAIN: homelab.swhurl.com
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
        tricky = SETTINGS_TEXT.replace('BASE_DOMAIN: homelab.swhurl.com', 'BASE_DOMAIN: |\n    CERT_ISSUER: nested')
        with self.assertRaises(settings.SettingsError):
            settings.set_value(tricky, 'CERT_ISSUER', 'letsencrypt-staging')

    def test_cli_usage_and_errors(self):
        err = io.StringIO()
        with redirect_stderr(err):
            self.assertEqual(settings.platform_certs([]), 2)
            self.assertEqual(settings.platform_certs(['selfsigned']), 1)
        self.assertIn('unknown issuer', err.getvalue())


if __name__ == '__main__':
    unittest.main()

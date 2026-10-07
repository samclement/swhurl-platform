"""The incident review allowlist loader refuses anything it does not know."""
from __future__ import annotations

import copy
import unittest

import yaml

from swhurl.incident_review import PolicyError, allowlist, validate_patch


class AllowlistTests(unittest.TestCase):
    def setUp(self):
        self.doc = yaml.safe_load(allowlist.PATH.read_text())

    def refuse(self, change, pattern):
        doc = copy.deepcopy(self.doc)
        change(doc)
        with self.assertRaisesRegex(PolicyError, pattern):
            allowlist.parse(doc)

    def test_checked_in_file_loads(self):
        loaded = allowlist.load()
        app = loaded.app('hello-ts')
        self.assertEqual(app.repository, 'samclement/hello-ts')
        self.assertEqual(app.namespace('staging'), 'hello-ts-staging')
        self.assertEqual(loaded.repository('samclement/hello-ts'), app)
        self.assertEqual(loaded.defaults['monthly_refuse_cents'], 1600)
        self.assertFalse(loaded.covered('hello-ts', 'error-logs'))
        for lookup, value in ((loaded.app, 'other'), (loaded.repository, 'attacker/repo')):
            with self.assertRaisesRegex(PolicyError, 'not allowlisted'):
                lookup(value)

    def test_unknown_keys_versions_apps_and_signals_are_refused(self):
        app = lambda doc: doc['apps'][0]  # noqa: E731
        cases = [
            (lambda d: d.update(extra=1), 'exactly the keys'),
            (lambda d: d.update(version=2), 'version'),
            (lambda d: d.update(model=None), 'model'),
            (lambda d: d['defaults'].update(surprise=1), 'exactly the keys'),
            (lambda d: d['defaults'].pop('cooldown_hours'), 'exactly the keys'),
            (lambda d: d['defaults'].update(confidence_min=0), 'confidence_min'),
            (lambda d: d['defaults'].update(confidence_min=True), 'confidence_min'),
            (lambda d: d['defaults'].update(rate_ratio=0), 'positive integer'),
            (lambda d: d['defaults'].update(run_cost_cents='50'), 'positive integer'),
            (lambda d: d.update(apps=[]), 'non-empty'),
            (lambda d: app(d).update(namespace='kube-system'), 'exactly the keys'),
            (lambda d: app(d).update(app='Hello_TS'), 'DNS label'),
            (lambda d: app(d).update(repository='https://github.com/a/b'), 'owner/name'),
            (lambda d: app(d).update(environments=['dev']), 'not one of'),
            (lambda d: app(d).update(environments=[]), 'distinct non-empty'),
            (lambda d: app(d).update(signals=['error-logs', 'all-logs']), 'not one of'),
            (lambda d: app(d)['severity'].update({'error-logs': 'urgent'}), 'severity'),
            (lambda d: app(d)['severity'].pop('error-logs'), 'severity'),
            (lambda d: app(d).update(patch_paths=['../other']), 'safe relative'),
            (lambda d: app(d).update(patch_paths=['/etc']), 'safe relative'),
            (lambda d: app(d).update(patch_paths=['.github/workflows/']), 'safe relative'),
            (lambda d: app(d).update(checks=[1]), 'distinct non-empty'),
            (lambda d: d['apps'].append(copy.deepcopy(app(d))), 'twice'),
            (lambda d: d.update(alert_rules=[{'app': 'other', 'signal': 'error-logs', 'rule': 'r'}]), 'alert rule'),
            (lambda d: d.update(alert_rules=[{'app': 'hello-ts', 'signal': 'error-logs'}]), 'exactly the keys'),
        ]
        for change, pattern in cases:
            with self.subTest(pattern=pattern):
                self.refuse(change, pattern)

    def test_alert_rules_mark_coverage(self):
        doc = copy.deepcopy(self.doc)
        doc['alert_rules'] = [{'app': 'hello-ts', 'signal': 'error-logs', 'rule': 'hello-ts 5xx'}]
        loaded = allowlist.parse(doc)
        self.assertTrue(loaded.covered('hello-ts', 'error-logs'))
        self.assertFalse(loaded.covered('hello-ts', 'error-spans'))

    def test_patch_paths_come_from_the_allowlist(self):
        base = 'a' * 40

        def patch(path):
            return {'repository': 'samclement/hello-ts', 'base_revision': base,
                    'files': [{'path': path, 'diff': f'--- a/{path}\n+++ b/{path}\n@@ -1 +1 @@\n-a\n+b\n'}]}

        for path in ('src/server.ts', 'test/repeat.test.mjs', 'README.md'):
            with self.subTest(path=path):
                validate_patch(patch(path), repository='samclement/hello-ts', base_revision=base)
        for path in ('tests/old.test.mjs', 'Dockerfile', 'README.md.bak', 'srcx/a.ts'):
            with self.subTest(path=path), self.assertRaisesRegex(PolicyError, 'not allowlisted'):
                validate_patch(patch(path), repository='samclement/hello-ts', base_revision=base)


if __name__ == '__main__':
    unittest.main()

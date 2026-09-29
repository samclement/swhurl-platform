"""check-repo reports every problem in one run, offline, with FakeRunner."""
import io
import shutil
import tempfile
import unittest
from pathlib import Path

from swhurl import platform, validate
from swhurl.report import Report
from swhurl.run import FakeRunner

SETTINGS = 'apiVersion: v1\nkind: ConfigMap\nmetadata: {name: platform-settings}\ndata: {CERT_ISSUER: x}\n'
KUSTOMIZATION = 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n'


class ValidateTests(unittest.TestCase):
    def setUp(self):
        self.root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.root)
        for path in platform.BOOTSTRAP_PATHS:
            (self.root / path).mkdir(parents=True, exist_ok=True)
            (self.root / path / 'kustomization.yaml').write_text(KUSTOMIZATION)
        (self.root / platform.ROOT_UNITS).write_text('apiVersion: v1\nkind: ConfigMap\nmetadata: {name: placeholder}\n')
        (self.root / platform.SETTINGS).write_text(SETTINGS)
        (self.root / 'README.md').write_text('[ok](README.md) [gone](missing.md)\n')
        (self.root / 'infra').mkdir()
        (self.root / 'infra/secret.yaml').write_text(
            'apiVersion: v1\nkind: Secret\nmetadata: {name: s}\nstringData: {a: plain}\n')

    def runner(self):
        rendered = 'apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\n'
        return (FakeRunner()
                .on('git', 'ls-files', '*.md', stdout='README.md\n')
                .on('git', 'ls-files', '--others', '--exclude-standard', '*.md')
                .on('git', 'ls-files', '--cached', stdout='')
                .on('kubectl', 'kustomize', stdout=rendered)
                .on('flux-schema'))

    def test_every_failure_is_reported(self):
        out = io.StringIO()
        report = Report(out)
        code = validate.validate(self.runner(), report, self.root)
        self.assertEqual(code, 1)
        bad = [line for line in report.lines if line.startswith('[BAD]')]
        self.assertEqual(len(bad), 2, out.getvalue())
        self.assertIn('README.md: missing.md (missing file)', bad[0])
        self.assertIn('Git-managed Secret must be a .sops.yaml', bad[1])
        self.assertIn('rendered and schema-validated clusters/home/flux-system', out.getvalue(),
                      'renders still run after earlier failures')
        self.assertIn('Validation failed: 2 problem(s).', out.getvalue())

    def test_clean_repo_passes(self):
        (self.root / 'README.md').write_text('[ok](README.md)\n')
        (self.root / 'infra/secret.yaml').unlink()
        out = io.StringIO()
        self.assertEqual(validate.validate(self.runner(), Report(out), self.root), 0, out.getvalue())
        self.assertIn('Validation passed for 2 active render entrypoints.', out.getvalue())


class SubstitutionTests(unittest.TestCase):
    """Only units with postBuild.substituteFrom substitute, as Flux does."""
    RENDERED = 'apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\ndata: {auth: "${env:KEY}", host: "a.${BASE_DOMAIN}"}\n'
    SUBSTITUTES = {'postBuild': {'substituteFrom': [{'kind': 'ConfigMap', 'name': 'platform-settings', 'optional': False}]}}

    def render(self, spec, rendered=RENDERED):
        runner = FakeRunner().on('kubectl', 'kustomize', stdout=rendered).on('flux-schema')
        validate.validate_render(runner, Report(io.StringIO()), Path.cwd() / 'unit', spec, {'BASE_DOMAIN': 'example.test'},
                                 root=Path.cwd())
        return runner

    def test_unit_without_substitution_keeps_dollar_references(self):
        runner = self.render({}, 'apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\ndata: {auth: "${env:KEY}"}\n')
        schema_input = [c for c in runner.calls if c[0] == 'flux-schema']
        self.assertTrue(schema_input)

    def test_substituting_unit_rejects_an_unescaped_env_reference(self):
        with self.assertRaisesRegex(validate.ValidationError, r'unresolved Flux substitution \$\{env:KEY\}.*escape'):
            self.render(self.SUBSTITUTES)

    def test_substituting_unit_resolves_settings(self):
        self.render(self.SUBSTITUTES, 'apiVersion: v1\nkind: ConfigMap\nmetadata: {name: x}\ndata: {host: "a.${BASE_DOMAIN}"}\n')


if __name__ == '__main__':
    unittest.main()

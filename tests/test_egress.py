"""The egress proxy: a port per instance kept in its own variables, and the proxy's generated file."""
import io
import json
import shutil
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps import checks, derive, egress, helmrelease, per_environment
from swhurl.report import Report
from swhurl.run import FakeRunner

SAMPLE = ROOT / 'tests/fixtures/instance'  # frozen sample instance (hello, staging and prod)


class EgressTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        for rel in ('apps', 'clusters'):
            shutil.copytree(SAMPLE / rel, self.root / rel)
        (self.root / egress.PROXY_FILE).parent.mkdir(parents=True)

    def sync(self):
        with redirect_stdout(io.StringIO()):
            derive.sync(self.root)

    def env(self, env):
        return helmrelease.variables(yaml.safe_load((self.root / f'apps/hello/{env}/helmrelease.yaml').read_text()))

    def proxy(self):
        configmap, policy = yaml.safe_load_all((self.root / egress.PROXY_FILE).read_text())
        return yaml.safe_load(configmap['data']['values.yaml']), policy

    def test_each_instance_gets_a_port_of_its_own_that_never_moves(self):
        self.sync()
        self.assertEqual((egress.port_of(self.env('staging')), egress.port_of(self.env('prod'))), (30101, 30102))
        self.assertEqual(self.env('staging')['NO_PROXY'], 'localhost,127.0.0.1,.svc,.cluster.local,$(HOST_IP)')
        self.assertIn('-Dhttps.proxyPort=30101', self.env('staging')['JAVA_TOOL_OPTIONS'])
        self.assertIn('HOST_IP', self.env('staging'), 'NO_PROXY uses it, with telemetry or without')
        values, policy = self.proxy()
        conf = values['configMaps']['config']['data']['squid.conf']
        self.assertIn('http_port 30101 name=hello-staging\n', conf)
        self.assertIn('note instance hello-prod hello-prod\n', conf)
        self.assertEqual(values['service']['main']['ports'], {'p30101': {'port': 30101}, 'p30102': {'port': 30102}})
        self.assertEqual([(rule['from'][0]['namespaceSelector']['matchLabels']['kubernetes.io/metadata.name'],
                           rule['ports'][0]['port']) for rule in policy['spec']['ingress']],
                         [('hello-staging', 30101), ('hello-prod', 30102)])
        self.assertEqual(egress.problems(self.root), [])

        # A port once given stays, whatever else is free.
        release = self.root / 'apps/hello/staging/helmrelease.yaml'
        release.write_text(release.read_text().replace(':30101', ':30107').replace('Port=30101', 'Port=30107'))
        self.assertIn('egress-ports: platform/egress/proxy.yaml is not what the instances declare; make app-sync rewrites it',
                      egress.problems(self.root))
        self.sync()
        self.assertEqual(egress.port_of(self.env('staging')), 30107)
        self.assertEqual(self.proxy()[0]['service']['main']['ports'], {'p30102': {'port': 30102}, 'p30107': {'port': 30107}})
        before = {path: path.read_text() for path in self.root.rglob('*.yaml')}
        self.sync()
        self.assertEqual(before, {path: path.read_text() for path in self.root.rglob('*.yaml')})

    def test_duplicate_or_missing_ports_are_named(self):
        self.sync()
        release = self.root / 'apps/hello/prod/helmrelease.yaml'
        release.write_text(release.read_text().replace(':30102', ':30101'))
        self.assertIn('egress-ports: port 30101 is in hello/staging, hello/prod; remove HTTPS_PROXY from all but one '
                      'and run make app-sync', egress.problems(self.root))
        release.write_text('\n'.join(line for line in release.read_text().splitlines() if 'HTTPS_PROXY' not in line) + '\n')
        self.assertIn('egress-ports: hello/prod has no proxy port; make app-sync gives it one', egress.problems(self.root))

    def test_a_first_production_gets_its_own_port_not_stagings(self):
        self.sync()
        staging = yaml.safe_load((self.root / 'apps/hello/staging/helmrelease.yaml').read_text())
        variables = helmrelease.variables(per_environment.for_production(staging, 'hello', None))
        self.assertFalse({'HTTPS_PROXY', 'HTTP_PROXY', 'JAVA_TOOL_OPTIONS'} & set(variables))
        self.assertEqual(variables['NO_PROXY'], self.env('staging')['NO_PROXY'])

    def test_a_root_without_the_proxy_gets_no_proxy_settings(self):
        shutil.rmtree(self.root / egress.PROXY_FILE.parent)
        self.sync()
        self.assertNotIn('HTTPS_PROXY', self.env('staging'))
        self.assertEqual(egress.problems(self.root), [])

    def test_verify_names_a_proxy_that_is_down_or_short_of_replicas(self):
        self.sync()
        for ready, level in ((2, '[OK] the egress proxy runs 2 replicas'), (1, '[WARN] the egress proxy runs 1 of 2'),
                             (0, '[BAD] the egress proxy is not running')):
            with self.subTest(ready=ready):
                runner = FakeRunner().on('kubectl', '-n', 'egress', 'get', 'deployment', 'egress', stdout=json.dumps(
                    {'spec': {'replicas': 2}, 'status': {'readyReplicas': ready}}))
                report = Report()
                with redirect_stdout(io.StringIO()):
                    checks.check_egress(runner, report, self.root)
                self.assertTrue(any(line.startswith(level) for line in report.lines), report.lines)
                self.assertIn('[OK] 2 instance(s) each have a port of their own', report.lines)


if __name__ == '__main__':
    unittest.main()

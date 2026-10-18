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
from swhurl.apps import checks, derive, edit, egress, helmrelease, per_environment, promotion
from swhurl.apps.instance import Instance
from swhurl.apps.model import sync as model_sync
from swhurl.apps.yaml_file import EditError
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
        settings = self.root / 'apps/hello/staging/instance.yaml'
        settings.write_text(settings.read_text().replace('egressPort: 30101', 'egressPort: 30107'))
        self.sync()
        self.assertEqual(egress.port_of(self.env('staging')), 30107)
        self.assertEqual(self.proxy()[0]['service']['main']['ports'], {'p30102': {'port': 30102}, 'p30107': {'port': 30107}})
        before = {path: path.read_text() for path in self.root.rglob('*.yaml')}
        self.sync()
        self.assertEqual(before, {path: path.read_text() for path in self.root.rglob('*.yaml')})

    def grant(self, env='staging', **change):
        with redirect_stdout(io.StringIO()) as out:
            edit.set_egress(self.root, 'hello', env, **change)
        return out.getvalue()

    def release(self, env):
        return yaml.safe_load((self.root / f'apps/hello/{env}/helmrelease.yaml').read_text())

    def test_a_grant_lists_the_host_on_the_instances_port_and_logs_whether_it_covers_a_request(self):
        self.sync()
        self.assertIn('[OK] hello/staging may reach: api.stripe.com:443, *.example.com:8443',
                      self.grant(add=['API.Stripe.com', '*.example.com:8443']))
        self.assertEqual(self.release('staging')['metadata']['annotations'][egress.ANNOTATION],
                         'api.stripe.com:443,*.example.com:8443')
        conf = self.proxy()[0]['configMaps']['config']['data']['squid.conf']
        self.assertIn('granted=%{granted}note', conf)
        self.assertIn('acl port-443 port 443\nacl port-8443 port 8443\n', conf)
        self.assertIn('acl hello-staging-443 dstdomain -n api.stripe.com\n'
                      'note granted yes hello-staging hello-staging-443 port-443\n'
                      'acl hello-staging-8443 dstdomain -n .example.com\n'
                      'note granted yes hello-staging hello-staging-8443 port-8443\n', conf)
        self.assertNotIn('hello-prod-', conf, 'production is granted nothing by staging')
        self.assertTrue(conf.endswith('http_access allow all\n'), 'E2 still allows every host')
        self.assertEqual(egress.problems(self.root), [])

        for change, message in (({'add': ['api.stripe.com']}, 'already there: api.stripe.com:443'),
                                ({'remove': ['example.org']}, 'not there to remove: example.org:443'),
                                ({'add': ['a.example.com:8443']}, 'a wildcard grant already covers a.example.com:8443'),
                                ({'add': ['10.0.0.1']}, 'a grant is a host name'), ({}, 'give --add')):
            with self.subTest(change=change), self.assertRaisesRegex(EditError, message):
                self.grant(**change)

        self.grant(remove=['api.stripe.com', '*.example.com:8443'])
        self.assertNotIn('annotations', self.release('staging')['metadata'])
        self.assertNotIn('granted yes', self.proxy()[0]['configMaps']['config']['data']['squid.conf'])

    def test_malformed_or_overlapping_grants_are_named(self):
        self.sync()
        release = self.root / 'apps/hello/staging/helmrelease.yaml'
        text = release.read_text().replace('metadata:\n', 'metadata:\n  annotations:\n    platform.swhurl.com/egress: '
                                           '"example.com:443,*.example.com:443,x:1,example.com:443"\n', 1)
        release.write_text(text)
        found = egress.problems(self.root)
        for problem in ('egress-grants: hello/staging: x:1: a grant is a host name',
                        'egress-grants: hello/staging grants example.com:443 twice',
                        'egress-grants: hello/staging grants example.com:443, which a wildcard grant already covers'):
            self.assertEqual(sum(line.startswith(problem) for line in found), 1, found)

    def test_a_promotion_never_widens_production(self):
        self.sync()
        self.grant(add=['api.stripe.com'])
        self.assertTrue(per_environment.may_differ(egress.ANNOTATION))
        world, _ = model_sync.read(self.root)
        first = promotion.first_production(self.root, world, 'hello')
        self.assertEqual(first.entry.settings.egress, ())
        with redirect_stdout(io.StringIO()) as out:
            edit.egress_difference('hello', world[Instance('hello', 'staging')], world[Instance('hello', 'prod')])
        self.assertIn('make app-egress APP=hello ENV=prod ARGS="--add api.stripe.com:443"', out.getvalue())

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
        world, _ = model_sync.read(self.root)
        self.assertIsNotNone(world[Instance('hello', 'staging')].settings.allocations.egress_port)
        first = promotion.first_production(self.root, world, 'hello')
        self.assertIsNone(first.entry.settings.allocations.egress_port, 'make app-sync gives production a free one')

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

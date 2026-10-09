"""Rules about what is in Git: sign-in, Flux unit deletion and dependencies, Reloader scope, Traefik."""
import base64
import os
import re
import shutil
import tempfile
import unittest

import yaml

from swhurl import ROOT, platform
from swhurl.apps import policy as app_policy
from swhurl.run import Runner


class ManifestPolicyTests(unittest.TestCase):
    def test_shared_flux_units_orphan_on_deletion_and_apps_prune(self):
        units = {}
        for path in [ROOT / 'clusters/home/flux-system/kustomizations.yaml', *sorted((ROOT / 'clusters/home').glob('*.yaml'))]:
            for doc in yaml.safe_load_all(path.read_text()):
                if doc and doc.get('kind') == 'Kustomization' and 'spec' in doc:
                    units[doc['metadata']['name']] = doc['spec'].get('deletionPolicy', 'MirrorPrune')
        for name, policy in units.items():
            with self.subTest(unit=name):
                expected = 'MirrorPrune' if name.startswith('app-') else 'Orphan'
                self.assertEqual(policy, expected)
        for name in ('infra-cert-manager', 'infra-issuers', 'platform-clickstack', 'platform-otel'):
            self.assertIn(name, units)

    def test_flux_units_decouple_apps_and_order_issuers(self):
        deps, specs = {}, {}
        for path in sorted((ROOT / 'clusters/home').glob('*.yaml')):
            for doc in yaml.safe_load_all(path.read_text()):
                if doc and doc.get('kind') == 'Kustomization' and 'spec' in doc:
                    name = doc['metadata']['name']
                    deps[name] = {d['name'] for d in doc['spec'].get('dependsOn', [])}
                    specs[name] = doc['spec']
        for name, required in deps.items():
            self.assertLessEqual(required, set(deps), f'{name} depends on an unknown unit')

        def closure(name, seen=()):
            self.assertNotIn(name, seen, f'dependency cycle through {name}')
            result = set()
            for dep in deps.get(name, ()):
                result |= {dep} | closure(dep, (*seen, name))
            return result
        self.assertIn('infra-cert-manager', closure('infra-issuers'))
        for app in (n for n in deps if n.startswith('app-')):
            self.assertFalse(closure(app) & {'platform-clickstack', 'platform-otel'},
                             f'{app} must not wait for observability')
        for name, spec in specs.items():
            encrypted = any((ROOT / spec['path']).rglob('*.sops.yaml'))
            with self.subTest(unit=name):
                self.assertEqual('decryption' in spec, encrypted, 'decryption must match encrypted Secrets in the path')

    def test_platform_hosts_come_from_base_domain(self):
        """Platform manifests name the domain only through ${BASE_DOMAIN}; units using a setting substitute it."""
        domain = platform.base_domain()
        settings = set(yaml.safe_load((ROOT / platform.SETTINGS).read_text())['data'])
        host = re.compile(rf'(?<![@\w.-])(?:[\w-]+\.)*{re.escape(domain)}\b')  # a mailbox (ops@domain) is not a host
        for _, unit in platform.flux_unit_documents():
            spec = unit['spec']
            if spec['path'].startswith(('./apps/', './clusters/')):
                continue  # apps write hosts literally (the app policy checks them); roots hold only units and sources
            used = set()
            for path in sorted((ROOT / spec['path']).rglob('*.yaml')):
                text = path.read_text()
                with self.subTest(file=str(path.relative_to(ROOT))):
                    self.assertFalse(host.search(text), f'use ${{BASE_DOMAIN}} instead of the literal {domain}')
                used |= set(re.findall(r'(?<!\$)\$\{(\w+)\}', text))
            with self.subTest(unit=unit['metadata']['name']):
                self.assertLessEqual(used, settings, 'unknown setting')
                substitutes = [s['name'] for s in (spec.get('postBuild') or {}).get('substituteFrom', [])]
                self.assertEqual('platform-settings' in substitutes, bool(used))

    def test_reloader_is_scoped_and_opt_in(self):
        release = yaml.safe_load((ROOT / 'platform/reloader/helmrelease.yaml').read_text())
        values = release['spec']['values']['reloader']
        self.assertIs(values['watchGlobally'], False, 'Reloader must not get cluster-wide Secret access')
        self.assertFalse(values.get('autoReloadAll', False), 'Reloader must stay opt-in')
        watched = set(values['namespaces'])
        opt_ins = {
            'platform/oauth2-proxy/helmrelease.yaml': 'deploymentAnnotations',
            'platform/otel/helmrelease-cluster.yaml': 'annotations',
            'platform/otel/helmrelease-daemonset.yaml': 'annotations',
        }
        for path, key in opt_ins.items():
            with self.subTest(path=path):
                hr = yaml.safe_load((ROOT / path).read_text())
                self.assertIn('secret.reloader.stakater.com/reload', hr['spec']['values'][key])
                self.assertIn(hr['metadata']['namespace'], watched, 'opt-in outside a watched namespace never reloads')
        console = yaml.safe_load((ROOT / 'platform/console/helmrelease.yaml').read_text())
        self.assertEqual(console['spec']['values']['controllers']['main']['annotations']
                         ['secret.reloader.stakater.com/reload'], 'console-github')
        self.assertIn('console', watched)

    def test_sign_in_passes_the_email_the_console_reads(self):
        args = yaml.safe_load((ROOT / 'platform/oauth2-proxy/helmrelease.yaml').read_text())['spec']['values']['extraArgs']
        self.assertIs(args.get('set-xauthrequest'), True, 'without it oauth2-proxy sends no X-Auth-Request-Email')
        middleware = yaml.safe_load((ROOT / 'platform/oauth2-proxy/middleware.yaml').read_text())
        self.assertIn('X-Auth-Request-Email', middleware['spec']['forwardAuth']['authResponseHeaders'])

    def test_console_may_write_only_flux_units_and_sources_and_never_read_secrets(self):
        docs = [d for d in yaml.safe_load_all((ROOT / 'platform/console/rbac.yaml').read_text()) if d]
        roles = {d['metadata']['name']: d for d in docs if d['kind'] in ('Role', 'ClusterRole')}
        for name, role in roles.items():
            for rule in role['rules']:
                with self.subTest(role=name, resources=rule['resources']):
                    self.assertFalse({'secrets', 'pods/exec', 'pods/attach', '*'} & set(rule['resources']))
                    self.assertNotIn('*', rule['verbs'])
                    writes = set(rule['verbs']) - {'get', 'list', 'watch'}
                    if writes:
                        self.assertEqual((role['kind'], role['metadata'].get('namespace')), ('Role', 'flux-system'))
                        self.assertEqual(writes, {'patch'})
                        self.assertLessEqual(set(rule['resources']), {'kustomizations', 'gitrepositories'})
        subjects = [s for d in docs if d['kind'].endswith('Binding') for s in d['subjects']]
        self.assertEqual({(s['namespace'], s['name']) for s in subjects}, {('console', 'console')})

    def test_console_policy_admits_only_traefik_and_spares_acme_solvers(self):
        netpol = yaml.safe_load((ROOT / 'platform/console/networkpolicy.yaml').read_text())['spec']
        self.assertEqual(netpol['podSelector']['matchLabels'].get('app.kubernetes.io/name'), 'console',
                         'a namespace-wide policy blocks cert-manager HTTP-01 solver pods (port 8089)')
        (rule,) = netpol['ingress']
        (source,) = rule['from']
        self.assertEqual(source['podSelector']['matchLabels'], {'app.kubernetes.io/name': 'traefik'})
        self.assertEqual(source['namespaceSelector']['matchLabels'], {'kubernetes.io/metadata.name': 'kube-system'})

    def test_traefik_redirects_http_to_https(self):
        config = yaml.safe_load((ROOT / 'infra/traefik/helmchartconfig.yaml').read_text())
        web = yaml.safe_load(config['spec']['valuesContent'])['ports']['web']
        self.assertNotIn('redirectTo', web, 'redirectTo is ignored by Traefik chart 38; use redirections.entryPoint')
        self.assertEqual(web['redirections']['entryPoint'], {'to': 'websecure', 'scheme': 'https', 'permanent': True})

    def test_shared_sign_in_is_restricted_to_approved_emails(self):
        path = ROOT / 'platform/oauth2-proxy/helmrelease.yaml'
        values = yaml.safe_load(path.read_text())['spec']['values']
        args = values.get('extraArgs', {})
        self.assertNotIn('email-domain', args)
        self.assertNotIn('trusted-ip', args, '--trusted-ip lets those IPs skip sign-in')
        self.assertEqual(args.get('trusted-proxy-ip'), '10.42.0.0/16', 'only pods (Traefik) may set X-Forwarded-*')
        self.assertEqual(args.get('code-challenge-method'), 'S256')
        self.assertEqual(values['config'].get('configFile', '').strip(), 'email_domains = []',
                         'Chart default email_domains = ["*"] must stay overridden')
        emails = values.get('authenticatedEmailsFile', {})
        self.assertTrue(emails.get('enabled'))
        approved = [line.strip() for line in emails.get('restricted_access', '').splitlines() if line.strip()]
        self.assertTrue(approved, 'Approved email list is empty')
        for email in approved:
            self.assertRegex(email, r'^[^@\s*]+@[^@\s*]+\.[^@\s*]+$')


@unittest.skipUnless(shutil.which('helm') or os.environ.get('REQUIRE_HELM'), 'helm not installed')
class ClickStackRenderTests(unittest.TestCase):
    """Render the ClickStack HelmRelease as Flux would (settings substituted, valuesFrom stubbed)."""

    @classmethod
    def setUpClass(cls):
        cls.release = yaml.safe_load((ROOT / 'platform/clickstack/helmrelease.yaml').read_text())
        settings = yaml.safe_load((ROOT / platform.SETTINGS).read_text())['data']
        cls.domain = settings['BASE_DOMAIN']
        values_text = yaml.safe_dump(cls.release['spec']['values'])
        for key, value in settings.items():
            values_text = values_text.replace(f'${{{key}}}', str(value))
        spec = cls.release['spec']['chart']['spec']
        runner = Runner()
        chart = app_policy.chart_dir(spec['chart'], spec['version'],
                                     app_policy.helm_repositories()[spec['sourceRef']['name']], runner)
        cls.chart_defaults = yaml.safe_load((chart / 'values.yaml').read_text())
        stubs = [f'{v["targetPath"]}=stub-{v["valuesKey"].lower()}' for v in cls.release['spec']['valuesFrom']]
        with tempfile.NamedTemporaryFile('w', suffix='.yaml') as values:
            values.write(values_text)
            values.flush()
            out = runner.output(['helm', 'template', 'clickstack', str(chart), '-n', 'observability',
                                 '-f', values.name, '--set', ','.join(stubs)])
        cls.docs = [d for d in yaml.safe_load_all(out) if d]

    def find(self, kind, name=None):
        return next(d for d in self.docs if d['kind'] == kind and (name is None or d['metadata']['name'] == name))

    def test_every_chart_secret_comes_from_the_sops_secret(self):
        sources = {v['targetPath']: v['valuesKey'] for v in self.release['spec']['valuesFrom']}
        secret = self.find('Secret', 'clickstack-secret')
        rendered = {k: base64.b64decode(v).decode() for k, v in (secret.get('data') or {}).items()}
        rendered |= secret.get('stringData') or {}
        for key in self.chart_defaults['hyperdx']['secrets']:
            with self.subTest(key=key):
                self.assertIn(f'hyperdx.secrets.{key}', sources, 'a chart default secret would reach the cluster')
                self.assertEqual(rendered.get(key), f'stub-{sources[f"hyperdx.secrets.{key}"].lower()}')

    def test_ingress_requires_sign_in_on_the_platform_host(self):
        ingress = self.find('Ingress')
        self.assertEqual(ingress['metadata']['annotations']['traefik.ingress.kubernetes.io/router.middlewares'],
                         'ingress-oauth-auth-shared@kubernetescrd')
        self.assertEqual([r['host'] for r in ingress['spec']['rules']], [f'clickstack.{self.domain}'])
        config = self.find('ConfigMap', 'clickstack-config')['data']
        self.assertEqual(config['FRONTEND_URL'], f'https://clickstack.{self.domain}')

    def test_only_the_flux_webhook_route_skips_sign_in(self):
        """Raw platform Ingresses need sign-in, except GitHub's webhook to Flux's receiver (chart Ingresses: other tests)."""
        unauthenticated = []
        for _, unit in platform.flux_unit_documents():
            if not unit['spec']['path'].startswith('./platform/'):
                continue
            for path in sorted((ROOT / unit['spec']['path']).rglob('*.yaml')):
                for doc in yaml.safe_load_all(path.read_text()):
                    if doc and doc.get('kind') == 'Ingress' and 'router.middlewares' not in str(doc['metadata']):
                        unauthenticated.append(doc)
        self.assertEqual([d['metadata']['name'] for d in unauthenticated], ['flux-webhook'])
        paths = [p for rule in unauthenticated[0]['spec']['rules'] for p in rule['http']['paths']]
        self.assertEqual([(p['path'], p['backend']['service']['name']) for p in paths], [('/hook/', 'webhook-receiver')])
        receiver = yaml.safe_load((ROOT / 'platform/flux-webhook/receiver.yaml').read_text())
        self.assertEqual(receiver['spec']['secretRef']['name'], 'github-webhook-token')
        images = yaml.safe_load((ROOT / 'platform/flux-webhook/image-receiver.yaml').read_text())
        self.assertEqual(images['spec']['secretRef']['name'], 'image-webhook-token')
        self.assertEqual([(r['kind'], r['matchLabels']) for r in images['spec']['resources']],
                         [('ImageRepository', {'platform.swhurl.com/managed': 'true'})],
                         'an app repository webhook may only start image scans')
        self.assertIn("req.ref == 'refs/heads/main'", receiver['spec']['resourceFilter'])

    def test_mongodb_data_survives_claim_deletion(self):
        templates = self.find('MongoDBCommunity')['spec']['statefulSet']['spec']['volumeClaimTemplates']
        data = next(t for t in templates if t['metadata']['name'] == 'data-volume')
        self.assertEqual(data['spec']['storageClassName'], 'local-path-retain')


if __name__ == '__main__':
    unittest.main()

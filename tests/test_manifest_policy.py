"""Rules about what is in Git: sign-in, Flux unit deletion and dependencies, Reloader scope, Traefik."""
import re
import unittest

import yaml

from swhurl import ROOT, platform


class ManifestPolicyTests(unittest.TestCase):
    def test_shared_flux_units_orphan_on_deletion_and_apps_prune(self):
        units = {}
        for path in [ROOT / 'clusters/home/flux-system/kustomizations.yaml', *sorted((ROOT / 'clusters/home').glob('*.yaml'))]:
            for doc in yaml.safe_load_all(path.read_text()):
                if doc and doc.get('kind') == 'Kustomization' and 'spec' in doc:
                    units[doc['metadata']['name']] = doc['spec'].get('deletionPolicy', 'MirrorPrune')
        for name, policy in units.items():
            with self.subTest(unit=name):
                expected = 'MirrorPrune' if name.startswith('homelab-app-') else 'Orphan'
                self.assertEqual(policy, expected)
        for name in ('homelab-cert-manager', 'homelab-issuers', 'homelab-clickstack', 'homelab-otel'):
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
        self.assertIn('homelab-cert-manager', closure('homelab-issuers'))
        for app in (n for n in deps if n.startswith('homelab-app-')):
            self.assertFalse(closure(app) & {'homelab-clickstack', 'homelab-otel', 'homelab-minio'},
                             f'{app} must not wait for observability or MinIO')
        for name, spec in specs.items():
            encrypted = any((ROOT / spec['path']).rglob('*.sops.yaml'))
            with self.subTest(unit=name):
                self.assertEqual('decryption' in spec, encrypted, 'decryption must match encrypted Secrets in the path')
        self.assertIn('postBuild', specs['homelab-otel'], 'OTel needs substitution to unescape $${env:...}')

    def test_platform_hosts_come_from_base_domain(self):
        """Platform manifests name the domain only through ${BASE_DOMAIN}; units using a setting substitute it."""
        domain = platform.base_domain()
        settings = set(yaml.safe_load((ROOT / platform.SETTINGS).read_text())['data'])
        host = re.compile(rf'(?<![@\w.-])(?:[\w-]+\.)*{re.escape(domain)}\b')  # a mailbox (ops@domain) is not a host
        for _, unit in platform.flux_unit_documents():
            spec = unit['spec']
            if spec['path'].startswith(('./tenants/', './clusters/')):
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
                self.assertEqual('platform-settings' in substitutes, bool(used) or unit['metadata']['name'] == 'homelab-otel')

    def test_reloader_is_scoped_and_opt_in(self):
        release = yaml.safe_load((ROOT / 'platform-services/reloader/base/helmrelease-reloader.yaml').read_text())
        values = release['spec']['values']['reloader']
        self.assertIs(values['watchGlobally'], False, 'Reloader must not get cluster-wide Secret access')
        self.assertFalse(values.get('autoReloadAll', False), 'Reloader must stay opt-in')
        watched = set(values['namespaces'])
        opt_ins = {
            'platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml': 'deploymentAnnotations',
            'platform-services/otel/base/helmrelease-otel-k8s-cluster.yaml': 'annotations',
            'platform-services/otel/base/helmrelease-otel-k8s-daemonset.yaml': 'annotations',
        }
        for path, key in opt_ins.items():
            with self.subTest(path=path):
                hr = yaml.safe_load((ROOT / path).read_text())
                self.assertIn('secret.reloader.stakater.com/reload', hr['spec']['values'][key])
                self.assertIn(hr['metadata']['namespace'], watched, 'opt-in outside a watched namespace never reloads')

    def test_traefik_redirects_http_to_https(self):
        config = yaml.safe_load((ROOT / 'infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml').read_text())
        web = yaml.safe_load(config['spec']['valuesContent'])['ports']['web']
        self.assertNotIn('redirectTo', web, 'redirectTo is ignored by Traefik chart 38; use redirections.entryPoint')
        self.assertEqual(web['redirections']['entryPoint'], {'to': 'websecure', 'scheme': 'https', 'permanent': True})

    def test_shared_sign_in_is_restricted_to_approved_emails(self):
        path = ROOT / 'platform-services/oauth2-proxy/base/helmrelease-oauth2-proxy-shared.yaml'
        values = yaml.safe_load(path.read_text())['spec']['values']
        self.assertNotIn('email-domain', values.get('extraArgs', {}))
        self.assertEqual(values['config'].get('configFile', '').strip(), 'email_domains = []',
                         'Chart default email_domains = ["*"] must stay overridden')
        emails = values.get('authenticatedEmailsFile', {})
        self.assertTrue(emails.get('enabled'))
        approved = [line.strip() for line in emails.get('restricted_access', '').splitlines() if line.strip()]
        self.assertTrue(approved, 'Approved email list is empty')
        for email in approved:
            self.assertRegex(email, r'^[^@\s*]+@[^@\s*]+\.[^@\s*]+$')


if __name__ == '__main__':
    unittest.main()

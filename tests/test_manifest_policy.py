"""Rules about what is in Git: sign-in, Flux unit deletion and dependencies, Reloader scope, Traefik."""
import base64
import json
import os
import re
import shutil
import subprocess
import tempfile
import unittest
from collections.abc import Iterator
from dataclasses import dataclass
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
CLUSTER = Path('clusters/home')
ROOT_UNITS = CLUSTER / 'flux-system/kustomizations.yaml'
SOURCES = CLUSTER / 'flux-system/sources'
SETTINGS = SOURCES / 'configmap-platform-settings.yaml'
HELM_REPOSITORIES = SOURCES / 'helmrepositories.yaml'


def flux_unit_documents(root: Path = ROOT) -> Iterator[tuple[Path, dict]]:
    """Every Flux Kustomization defined in Git, with the file that defines it.

    A unit file may be YAML or JSON: JSON is a YAML subset, both are read the same way, and the v2
    compiler writes its units as JSON. Either must be found, or a unit Flux applies goes unchecked.
    """
    files = [root / ROOT_UNITS, *sorted(p for p in (root / CLUSTER).iterdir() if p.suffix in ('.yaml', '.yml', '.json'))]
    for path in files:
        for doc in yaml.safe_load_all(path.read_text()):
            if (isinstance(doc, dict) and doc.get('apiVersion') == 'kustomize.toolkit.fluxcd.io/v1'
                    and doc.get('kind') == 'Kustomization'):
                yield path, doc


@dataclass(frozen=True)
class Unit:
    """One Flux unit (GLOSSARY.md) as Git defines it."""
    name: str
    path: Path  # the directory it applies, from the repository root
    depends_on: tuple[str, ...]
    decrypts: bool  # it decrypts SOPS Secrets
    spec: dict

    @property
    def is_app(self) -> bool:
        """An app instance's unit (``app-<app>-<env>``) rather than a shared one."""
        return self.name.startswith('app-')


def units(root: Path = ROOT) -> list[Unit]:
    """Every Flux unit defined in Git: the two root units, the shared ones, then one per app instance."""
    found = []
    for _, doc in flux_unit_documents(root):
        spec = doc.get('spec') or {}
        found.append(Unit(doc['metadata']['name'], Path(spec.get('path', '')),
                          tuple(d['name'] for d in spec.get('dependsOn') or []), 'decryption' in spec, spec))
    return found


def settings() -> dict:
    """The platform-settings ConfigMap's data, which Flux substitutes into the shared units."""
    return yaml.safe_load((ROOT / SETTINGS).read_text())['data']


class UnitDiscoveryTests(unittest.TestCase):
    """A unit is found whatever holds it: a JSON unit Flux applies must not go unchecked."""

    def test_yaml_and_json_units_are_discovered(self):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root)
        (root / ROOT_UNITS).parent.mkdir(parents=True)
        (root / ROOT_UNITS).write_text('')
        unit = {'apiVersion': 'kustomize.toolkit.fluxcd.io/v1', 'kind': 'Kustomization',
                'metadata': {'name': 'app-json-staging', 'namespace': 'flux-system'},
                'spec': {'path': './v2-apps/instances/x/staging', 'prune': True, 'dependsOn': [{'name': 'infra-base'}]}}
        (root / CLUSTER / 'app-json-staging.json').write_text(json.dumps(unit))
        shared = dict(unit, metadata={'name': 'platform-shared', 'namespace': 'flux-system'})
        (root / CLUSTER / 'platform-shared.yaml').write_text(yaml.safe_dump(shared))
        found = {unit.name: unit for unit in units(root)}
        self.assertEqual(set(found), {'app-json-staging', 'platform-shared'})
        self.assertTrue(found['app-json-staging'].is_app)
        self.assertFalse(found['platform-shared'].is_app)
        self.assertEqual(found['app-json-staging'].path, Path('v2-apps/instances/x/staging'))
        self.assertEqual(found['app-json-staging'].depends_on, ('infra-base',))


class ManifestPolicyTests(unittest.TestCase):
    def test_shared_flux_units_orphan_on_deletion_and_apps_prune(self):
        found = units()
        for unit in found:
            with self.subTest(unit=unit.name):
                self.assertEqual(unit.spec.get('deletionPolicy', 'MirrorPrune'), 'MirrorPrune' if unit.is_app else 'Orphan')
        for name in ('infra-cert-manager', 'infra-issuers', 'platform-clickstack', 'platform-otel'):
            self.assertIn(name, [unit.name for unit in found])

    def test_flux_units_decouple_apps_and_order_issuers(self):
        found = units()
        deps = {unit.name: set(unit.depends_on) for unit in found}
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
        for unit in found:
            with self.subTest(unit=unit.name):
                self.assertEqual(unit.decrypts, any((ROOT / unit.path).rglob('*.sops.yaml')),
                                 'decryption must match encrypted Secrets in the path')

    def test_platform_hosts_come_from_base_domain(self):
        """Platform manifests name the domain only through ${BASE_DOMAIN}; units using a setting substitute it."""
        known = settings()
        domain = known['BASE_DOMAIN']
        host = re.compile(rf'(?<![@\w.-])(?:[\w-]+\.)*{re.escape(domain)}\b')  # a mailbox (ops@domain) is not a host
        for _, unit in flux_unit_documents():
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
                self.assertLessEqual(used, set(known), 'unknown setting')
                substitutes = [s['name'] for s in (spec.get('postBuild') or {}).get('substituteFrom', [])]
                self.assertEqual('platform-settings' in substitutes, bool(used))

    def test_reloader_is_scoped_and_opt_in(self):
        release = yaml.safe_load((ROOT / 'platform/reloader/helmrelease.yaml').read_text())
        values = release['spec']['values']['reloader']
        self.assertIs(values['watchGlobally'], False, 'Reloader must not get cluster-wide Secret access')
        self.assertFalse(values.get('autoReloadAll', False), 'Reloader must stay opt-in')
        configmap = json.loads((ROOT / 'platform/reloader/values.json').read_text())
        watched = set(json.loads(configmap['data']['values.yaml'])['reloader']['namespaces'])
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
                         ['secret.reloader.stakater.com/reload'], 'console-github,console-nats')
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

    def test_only_the_flux_webhook_route_skips_sign_in(self):
        """Raw platform Ingresses need sign-in, except GitHub's webhook to Flux's receiver (chart Ingresses: other tests)."""
        unauthenticated = []
        for unit in units():
            if unit.path.parts[0] != 'platform':
                continue
            for path in sorted((ROOT / unit.path).rglob('*.yaml')):
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


@unittest.skipUnless(shutil.which('helm') or os.environ.get('REQUIRE_HELM'), 'helm not installed')
class ClickStackRenderTests(unittest.TestCase):
    """Render the ClickStack HelmRelease as Flux would (settings substituted, valuesFrom stubbed)."""

    @classmethod
    def setUpClass(cls):
        cls.release = yaml.safe_load((ROOT / 'platform/clickstack/helmrelease.yaml').read_text())
        known = settings()
        cls.domain = known['BASE_DOMAIN']
        values_text = yaml.safe_dump(cls.release['spec']['values'])
        for key, value in known.items():
            values_text = values_text.replace(f'${{{key}}}', str(value))
        spec = cls.release['spec']['chart']['spec']
        repositories = {d['metadata']['name']: d['spec']['url'] for d in
                        yaml.safe_load_all((ROOT / HELM_REPOSITORIES).read_text()) if d and d['kind'] == 'HelmRepository'}
        stubs = [f'{v["targetPath"]}=stub-{v["valuesKey"].lower()}' for v in cls.release['spec']['valuesFrom']]
        with tempfile.TemporaryDirectory() as tmp:
            subprocess.run(['helm', 'pull', spec['chart'], '--repo', repositories[spec['sourceRef']['name']],
                            '--version', spec['version'], '--untar', '--untardir', tmp], check=True, capture_output=True)
            chart = Path(tmp) / spec['chart']
            cls.chart_defaults = yaml.safe_load((chart / 'values.yaml').read_text())
            values = Path(tmp) / 'values.yaml'
            values.write_text(values_text)
            out = subprocess.run(['helm', 'template', 'clickstack', str(chart), '-n', 'observability', '-f', str(values),
                                  '--set', ','.join(stubs)], check=True, capture_output=True, text=True).stdout
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

    def test_mongodb_data_survives_claim_deletion(self):
        templates = self.find('MongoDBCommunity')['spec']['statefulSet']['spec']['volumeClaimTemplates']
        data = next(t for t in templates if t['metadata']['name'] == 'data-volume')
        self.assertEqual(data['spec']['storageClassName'], 'local-path-retain')


if __name__ == '__main__':
    unittest.main()

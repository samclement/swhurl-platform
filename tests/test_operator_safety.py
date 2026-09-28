"""Exercise destructive-command guards, verifier credential boundaries and sign-in policy offline."""
import base64
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'tools'))
KEY = 'fixture-private-ingestion-token'
OTHER_KEY = 'fixture-other-private-token'


class OperatorSafetyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.bin = Path(self.tmp.name)
        self.calls = self.bin / 'calls'
        self.env = dict(os.environ, PATH=f'{self.bin}:{os.environ["PATH"]}',
                        CALLS=str(self.calls), KEY=KEY, OTHER_KEY=OTHER_KEY)
        for name in ('kubectl', 'flux'):
            path = self.bin / name
            path.write_text('''#!/usr/bin/env python3
import base64, json, os, sys
from pathlib import Path
with open(os.environ['CALLS'], 'a') as f:
    f.write(Path(sys.argv[0]).name + '\\n')
scenario = os.environ.get('SCENARIO', 'match')
argv = sys.argv[1:]
def emit(obj):
    print(json.dumps(obj))
if Path(sys.argv[0]).name == 'flux':
    pass
elif any(a.startswith('kustomizations') for a in argv):
    ready = 'False' if scenario == 'unready' else 'True'
    emit({'items': [{'metadata': {'name': 'homelab-clickstack'},
                     'status': {'conditions': [{'type': 'Ready', 'status': ready, 'message': 'fixture'}]}}]})
elif 'traefik' in argv:
    emit({'spec': {'template': {'spec': {'containers': [{'args': [
        '--entryPoints.web.http.redirections.entryPoint.to=:443',
        '--entryPoints.web.http.redirections.entryPoint.scheme=https']}]}}}})
elif 'clickhouse-client' in argv:
    print('9\\t9' if 'toIntervalDay(30)' in argv[-1] else '0')
elif 'pvc' in argv:
    emit({'metadata': {'annotations': {'helm.sh/resource-policy': 'keep'}}, 'spec': {'volumeName': 'pv-mongodb'}})
elif 'pv' in argv:
    emit({'spec': {'persistentVolumeReclaimPolicy': 'Retain'}})
elif 'exec' in argv:
    print(os.environ['KEY'])
    if scenario == 'mongo-failure':
        print('failed near ' + os.environ['KEY'], file=sys.stderr)
        sys.exit(1)
elif 'secret' in argv:
    value = os.environ['KEY'].encode()
    if scenario == 'mismatch': value = os.environ['OTHER_KEY'].encode()
    if scenario == 'newline': value += b'\\n'
    if scenario == 'double': value = base64.b64encode(value)
    encoded = base64.b64encode(value).decode()
    if scenario == 'invalid': encoded += '!'
    emit({'data': {} if scenario == 'missing' else {'HYPERDX_API_KEY': encoded}})
''')
            path.chmod(0o700)

    def test_destructive_targets_never_call_cluster_tools(self):
        for target in ('teardown', 'reinstall'):
            for dry in ('false', 'true'):
                with self.subTest(target=target, dry=dry):
                    result = subprocess.run(['make', target, f'DRY_RUN={dry}'], cwd=ROOT,
                                            env=self.env, capture_output=True, text=True)
                    self.assertEqual(result.returncode == 0, dry == 'true', result.stdout + result.stderr)
                    self.assertIn('disabled', result.stdout + result.stderr)
                    self.assertFalse(self.calls.exists(), 'A disabled target called a cluster tool')

    def test_recovery_dry_runs_never_call_cluster_tools(self):
        for target in ('backup-clickstack-mongodb', 'restore-test-clickstack-mongodb'):
            with self.subTest(target=target):
                result = subprocess.run(['make', target, 'DRY_RUN=true'], cwd=ROOT,
                                        env=dict(self.env, BACKUP_DIR=str(self.bin / 'backups')),
                                        capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertIn('Plan (', result.stdout)
                self.assertFalse(self.calls.exists(), 'A recovery dry run called a cluster tool')
                self.assertFalse((self.bin / 'backups').exists(), 'A recovery dry run wrote files')

    def test_destroy_data_refuses_without_exact_confirmation(self):
        cases = (('pvc/ns/data', ''), ('pvc/ns/data', 'pvc/ns/other'), ('pv/name', 'pv/other'),
                 ('pvc/ns', 'pvc/ns'), ('secret/ns/x', 'secret/ns/x'), ('', ''))
        for target, confirm in cases:
            with self.subTest(target=target, confirm=confirm):
                result = subprocess.run(['make', 'destroy-data', f'TARGET={target}', f'CONFIRM={confirm}'],
                                        cwd=ROOT, env=self.env, capture_output=True, text=True)
                self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                self.assertFalse(self.calls.exists(), 'A refused destroy-data called a cluster tool')

    def test_lifecycle_test_dry_run_never_calls_cluster_tools(self):
        result = subprocess.run(['make', 'lifecycle-test', 'DRY_RUN=true'], cwd=ROOT, env=self.env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(self.calls.exists())

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

    def test_reloader_test_dry_run_never_calls_cluster_tools(self):
        result = subprocess.run(['make', 'reloader-test', 'DRY_RUN=true'], cwd=ROOT, env=self.env,
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertFalse(self.calls.exists())

    def test_traefik_redirects_http_to_https(self):
        config = yaml.safe_load((ROOT / 'infrastructure/ingress-traefik/base/helmchartconfig-traefik.yaml').read_text())
        web = yaml.safe_load(config['spec']['valuesContent'])['ports']['web']
        self.assertNotIn('redirectTo', web, 'redirectTo is ignored by Traefik chart 38; use redirections.entryPoint')
        self.assertEqual(web['redirections']['entryPoint'], {'to': 'websecure', 'scheme': 'https', 'permanent': True})

    def test_secrets_check_flags_double_encoding(self):
        from swhurl import secrets_check as module
        uuid = b'0f8fad5b-d9cb-469f-a165-70867728950e'
        self.assertFalse(module.looks_double_encoded(uuid), 'a plain token is not double-encoded')
        self.assertTrue(module.looks_double_encoded(base64.b64encode(uuid)), 'base64 of a token is')
        self.assertFalse(module.looks_double_encoded(bytes(range(40))), 'binary bytes are not base64 text')
        self.assertNotIn('.sops.yaml', [p.name for p in module.secret_files()])

    def test_verifier_checks_actual_bytes_and_never_prints_credentials(self):
        """End to end through real subprocesses and the fake kubectl (contract test)."""
        for scenario in ('match', 'double', 'mismatch', 'missing', 'invalid',
                         'newline', 'mongo-failure', 'unready'):
            with self.subTest(scenario=scenario):
                result = subprocess.run([sys.executable, '-m', 'swhurl', 'verify-platform'], cwd=ROOT,
                                        env=dict(self.env, SCENARIO=scenario, PYTHONPATH=str(ROOT / 'tools')),
                                        capture_output=True, text=True)
                output = result.stdout + result.stderr
                self.assertEqual(result.returncode == 0, scenario == 'match', output)
                for value in (KEY, OTHER_KEY):
                    self.assertNotIn(value, output)
                    self.assertNotIn(base64.b64encode(value.encode()).decode(), output)

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

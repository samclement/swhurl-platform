"""Characterize retained Kubernetes rules before moving their owner to Go.

Synthetic objects only; the existing mutable fixture checks remain the end-to-end oracle.
"""
import copy
import unittest

from swhurl.apps import policy


def container():
    return {'name': 'main', 'image': 'ghcr.io/example/app@sha256:' + 'a' * 64,
            'securityContext': {'runAsNonRoot': True, 'allowPrivilegeEscalation': False,
                                'capabilities': {'drop': ['ALL']}},
            'resources': {'requests': {'cpu': '10m', 'memory': '32Mi'}, 'limits': {'memory': '32Mi'}}}


def workload():
    return {'kind': 'Deployment', 'metadata': {'name': 'app'},
            'spec': {'replicas': 1, 'strategy': {'type': 'Recreate'}, 'template': {'spec': {
                'automountServiceAccountToken': False, 'containers': [container()],
                'volumes': [{'persistentVolumeClaim': {'claimName': 'data'}}]}}}}


class RetainedPolicyContractTests(unittest.TestCase):
    def test_container_mutations_for_regular_and_init_containers(self):
        mutations = [
            ('image-pinned', lambda c: c.update(image='ghcr.io/example/app:latest')),
            ('prod-digest', lambda c: c.update(image='ghcr.io/example/app:123')),
            ('non-root', lambda c: c['securityContext'].update(runAsNonRoot=False)),
            ('no-escalation', lambda c: c['securityContext'].update(privileged=True)),
            ('no-escalation', lambda c: c['securityContext'].update(allowPrivilegeEscalation=True)),
            ('no-escalation', lambda c: c['securityContext']['capabilities'].update(add=['NET_ADMIN'])),
            ('no-escalation', lambda c: c['securityContext']['capabilities'].update(drop=[])),
            ('resources', lambda c: c['resources']['requests'].pop('cpu')),
            ('resources', lambda c: c['resources']['requests'].pop('memory')),
            ('resources', lambda c: c['resources']['limits'].pop('memory')),
        ]
        self.assertEqual(policy.check_pod(workload(), 'prod'), [])
        for field in ('containers', 'initContainers'):
            for rule, mutate in mutations:
                with self.subTest(field=field, rule=rule, mutate=mutate):
                    doc = workload()
                    pod = doc['spec']['template']['spec']
                    pod[field] = [container()]
                    mutate(pod[field][0])
                    self.assertIn(rule, [r for r, _ in policy.check_pod(doc, 'prod')])

    def test_pod_security_and_context_inheritance(self):
        for field in ('hostNetwork', 'hostPID', 'hostIPC', 'automountServiceAccountToken', 'hostPath'):
            doc = workload()
            pod = doc['spec']['template']['spec']
            if field == 'hostPath':
                pod['volumes'].append({'hostPath': {'path': '/tmp'}})
            else:
                pod[field] = True
            expected = 'no-sa-token' if field == 'automountServiceAccountToken' else 'no-host-access'
            self.assertEqual([r for r, _ in policy.check_pod(doc, 'prod')], [expected])
        doc = workload()
        pod = doc['spec']['template']['spec']
        pod['securityContext'] = {'runAsNonRoot': True}
        del pod['containers'][0]['securityContext']['runAsNonRoot']
        self.assertEqual(policy.check_pod(doc, 'prod'), [])
        pod['containers'][0]['securityContext']['runAsNonRoot'] = False
        self.assertEqual([r for r, _ in policy.check_pod(doc, 'prod')], ['non-root'])

    def test_host_ip_must_precede_its_use_and_have_the_right_source(self):
        host = {'name': 'HOST_IP', 'valueFrom': {'fieldRef': {'fieldPath': 'status.hostIP'}}}
        endpoint = {'name': 'OTEL_EXPORTER_OTLP_ENDPOINT', 'value': 'http://$(HOST_IP):4318'}
        for variables, expected in (([host, endpoint], []), ([endpoint, host], ['otlp-host-ip']),
                                    ([dict(host, valueFrom={}), endpoint], ['otlp-host-ip'])):
            c = container()
            c['env'] = variables
            self.assertEqual([r for r, _ in policy.check_container(c, {}, 'Deployment/app', 'prod')], expected)

    def test_single_writer_replicas_rollout_and_unmounted_claims(self):
        doc = workload()
        self.assertEqual(policy.check_single_writer(doc, {'data'}), [])
        doc['spec']['replicas'] = 2
        doc['spec']['strategy']['type'] = 'RollingUpdate'
        self.assertEqual([r for r, _ in policy.check_single_writer(doc, {'data'})], ['single-writer'] * 2)
        self.assertEqual(policy.check_single_writer(doc, {'another-claim'}), [])
        job = copy.deepcopy(doc)
        job['kind'] = 'Job'
        self.assertEqual(policy.check_single_writer(job, {'data'}), [])


class RetainedRelationshipPolicyTests(unittest.TestCase):
    def test_routes_require_sign_in_domain_and_tls(self):
        from swhurl.apps.contract import AUTH_MIDDLEWARE, COOKIE_DOMAIN
        host = f'app.{COOKIE_DOMAIN}'
        route = {'kind': 'Ingress', 'metadata': {'name': 'app', 'annotations': {
            'traefik.ingress.kubernetes.io/router.middlewares': AUTH_MIDDLEWARE}},
            'spec': {'rules': [{'host': host}], 'tls': [{'hosts': [host]}]}}
        self.assertEqual(policy.check_ingress(route, 'authenticated-web'), [])
        self.assertEqual([r for r, _ in policy.check_ingress(route, 'private')], ['exposure'])
        self.assertIn('exposure', [r for r, _ in policy.check_ingress(route, 'public')])
        route['metadata']['annotations'].clear()
        route['spec']['tls'] = []
        self.assertEqual([r for r, _ in policy.check_ingress(route, 'authenticated-web')],
                         ['exposure', 'ingress-tls'])

    def identity_release(self):
        from swhurl.apps.calling import identity_variables
        return {'kind': 'HelmRelease', 'metadata': {'name': 'app', 'namespace': 'app-staging'},
                'spec': {'values': {'controllers': {'main': {'containers': {'main': {
                    'env': identity_variables('app', 'staging', False, '')}}}}}}}

    def test_exact_audience_tokens_ordering_and_backup_exemption(self):
        from swhurl.apps.calling import TOKEN_SECONDS
        from swhurl.apps.contract import BACKUP_LABEL
        release, doc = self.identity_release(), workload()
        pod = doc['spec']['template']['spec']
        token = {'audience': 'notify', 'expirationSeconds': TOKEN_SECONDS, 'path': 'notify/token'}
        pod['volumes'] = [{'projected': {'sources': [{'serviceAccountToken': token}]}}]
        self.assertEqual(policy.check_identity([release, doc], {'platform.swhurl.com/environment': 'staging'}, ''), [])
        token['audience'] = 'api-server'
        self.assertIn('call-tokens', [r for r, _ in policy.check_identity([release, doc], {}, '')])
        doc['metadata']['labels'] = {BACKUP_LABEL: 'true'}
        pod['volumes'] = []
        self.assertNotIn('call-tokens', [r for r, _ in policy.check_identity([release, doc], {}, '')])
        pod['volumes'] = [{'projected': {'sources': [{'serviceAccountToken': token}]}}]
        self.assertIn('call-tokens', [r for r, _ in policy.check_identity([release, doc], {}, '')])
        doc['metadata']['labels'] = {}
        pod['containers'][0]['env'] = [{'name': 'SERVICE', 'value': 'http://app-$(SWHURL_ENV)'},
                                     {'name': 'SWHURL_ENV', 'value': 'staging'}]
        self.assertIn('identity', [r for r, _ in policy.check_identity([release, doc], {}, '')])

    def test_events_are_whole_exact_and_absent_when_not_declared(self):
        from swhurl.apps import events, helmrelease
        release = self.identity_release()
        labels = {'platform.swhurl.com/environment': 'staging'}
        env = helmrelease.variables(release)
        self.assertEqual(policy.check_events([], release, labels), [])
        env.update(SWHURL_EVENTS_NKEY='U' + 'A' * 55, SWHURL_EVENTS_URL=events.URL,
                   SWHURL_EVENTS_PUBLISH='created', SWHURL_EVENTS_SUBSCRIBE='orders.paid')
        main = helmrelease.container(release)
        main['envFrom'] = [{'secretRef': {'name': 'app-events'}}]
        docs = events.manifests('app', 'staging', ['created'], [('orders', 'paid')])
        self.assertEqual(policy.check_events(docs, release, labels), [])
        docs[-1]['spec']['ackWait'] = '1s'
        self.assertIn('events', [r for r, _ in policy.check_events(docs, release, labels)])
        main['envFrom'] = []
        self.assertIn('events', [r for r, _ in policy.check_events([], release, labels)])

    def test_storage_and_quota_on_mutable_rendered_fixture(self):
        from swhurl import ROOT
        from swhurl.apps.calling import signing_keys
        docs = policy.render(ROOT / 'tests/fixtures/apps/apps/smoke-data/prod')
        self.assertEqual(policy.check(docs, {'demo': ''}, signing_keys()), [])
        claim = next(d for d in docs if d['kind'] == 'PersistentVolumeClaim')
        claim['spec']['storageClassName'] = 'wrong'
        quota = next(d for d in docs if d['kind'] == 'ResourceQuota')
        quota['spec']['hard']['pods'] = '0'
        deployment = next(d for d in docs if d['kind'] == 'Deployment')
        deployment['spec']['replicas'] = 1000
        rules = [r for r, _ in policy.check(docs, {'demo': ''}, signing_keys())]
        self.assertIn('storage-class', rules)
        self.assertIn('quota', rules)
        self.assertIn('single-writer', rules)

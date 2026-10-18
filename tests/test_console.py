"""The read-only console: identity, pages and what it reads, offline, with FakeRunner."""
import datetime as dt
import io
import json
import re
import threading
import unittest
from contextlib import redirect_stderr
from dataclasses import replace
from unittest import mock

from starlette.testclient import TestClient

from swhurl import kube
from swhurl.apps import state
from swhurl.apps.instance import Instance
from swhurl.console import actions, changes, cluster, server
from swhurl.run import FakeRunner, Result, SharedReads, is_read

REV = 'main@sha1:abc1234def'
WHO = {'X-Auth-Request-Email': 'sam@swhurl.com'}


def unit(name, depends=(), ready='True', message='Applied', suspend=False):
    return {'metadata': {'name': name},
            'spec': {'dependsOn': [{'name': d} for d in depends], **({'suspend': True} if suspend else {})},
            'status': {'lastAppliedRevision': REV, 'conditions': [{'type': 'Ready', 'status': ready, 'message': message}]}}


def release(ns, name, tag='1.0', sqlite=False):
    container = {'image': {'repository': 'docker.io/x/web', 'tag': tag}}
    if sqlite:
        container['env'] = {'DATABASE_PATH': '/data/app.db'}
    return {'metadata': {'namespace': ns, 'name': name}, 'spec': {'values': {'controllers': {'main': {
        'containers': {'main': container}}}}}, 'status': {'conditions': [{'type': 'Ready', 'status': 'True', 'message': 'ok'}]}}


def running(releases):
    """The pods and workloads of releases that run as asked: one ready replica each."""
    def owned(item, **more):
        meta = item['metadata']
        return {'metadata': {'namespace': meta['namespace'], 'name': meta['name'],
                             'labels': {'app.kubernetes.io/instance': meta['name']}}, **more}
    pods = [owned(r, status={'containerStatuses': [{'name': 'main', 'ready': True, 'imageID': 'docker.io/x/web@sha256:abc'}]})
            for r in releases]
    workloads = [owned(r, kind='Deployment', spec={'replicas': 1}, status={'readyReplicas': 1}) for r in releases]
    return {'items': pods}, {'items': workloads}


UNITS = [unit('cluster-sources'), unit('infra-base'), unit('platform-oauth2-proxy', ['infra-base']),
         unit('app-web-prod', ['infra-base', 'platform-oauth2-proxy']),
         unit('app-my-api-staging', ['infra-base'], ready='False', message='<script>x</script>', suspend=True)]


RELEASES = [release('web-prod', 'web')]


TRAEFIK_REDIRECTS = {'spec': {'template': {'spec': {'containers': [
    {'args': ['--entryPoints.web.http.redirections.entryPoint.scheme=https']}]}}}}
CHECKS = {'kubectl -n console get cronjob console-dashboards': json.dumps({
              'spec': {}, 'status': {'lastSuccessfulTime': dt.datetime.now(dt.UTC).isoformat()}}),
          'kubectl get --raw=/version': '{}',
          'kubectl -n kube-system get deploy traefik': json.dumps(TRAEFIK_REDIRECTS),
          'kubectl -n flux-system get imagepolicies.image.toolkit.fluxcd.io': json.dumps({'items': []}),
          'kubectl -n flux-system get alerts.notification.toolkit.fluxcd.io': json.dumps({'items': [
              {'metadata': {'name': n}, 'spec': {'providerRef': {'name': 'p'}}} for n in ('failures',)]}),
          'kubectl -n flux-system get providers.notification.toolkit.fluxcd.io': json.dumps({'items': [{'metadata': {'name': 'p'}}]})}
CHECKS['kubectl -n console get cronjob console-notifications'] = CHECKS['kubectl -n console get cronjob console-dashboards']
CHECKS['kubectl -n console get helmrelease console'] = json.dumps({'spec': {'values': {'controllers': {'main': {'containers': {
    'main': {'image': {'tag': 'src-abc'}}}}}}}})
CHECKS['kubectl -n notify get deployment notify'] = json.dumps({'status': {'readyReplicas': 1}, 'spec': {'template': {'spec': {
    'containers': [{'image': 'ghcr.io/samclement/swhurl-console:src-abc@sha256:abc'}]}}}})
"""Answers for the cluster-only platform checks (Overview and Platform run them)."""


def fake(units=UNITS, releases=RELEASES, **extra):
    runner = FakeRunner()
    runner.on('kubectl', 'get', 'namespace', stdout=json.dumps({'metadata': {'labels': {'platform.swhurl.com/team': 'payments'}}}))
    if not any(key.startswith('kubectl get namespaces') for key in extra):
        runner.on('kubectl', 'get', 'namespaces', stdout=json.dumps({'items': []}))
    runner.on(*cluster.UNITS[:5], stdout=json.dumps({'items': units}))
    runner.on(*cluster.RELEASES[:3], stdout=json.dumps({'items': releases}))
    for prefix, stdout in extra.items():
        runner.on(*prefix.split(), stdout=stdout)
    # What an instance's state is judged from, unless the test says otherwise: everything runs as asked.
    pods, workloads = running(releases)
    runner.on(*cluster.SOURCE[:6], stdout=json.dumps({'status': {'artifact': {'revision': REV}}}))
    runner.on(*cluster.PODS[:4], stdout=json.dumps(pods))
    runner.on(*cluster.WORKLOADS[:4], stdout=json.dumps(workloads))
    runner.on('kubectl', '-n', 'console', 'get', 'configmap', 'app-dashboards', stdout='')  # no dashboard recorded
    return runner


def client(runner, **kwargs):
    kwargs.setdefault('repo_opener', lambda request: ({}, b'version: 1\nkind: worker\nstack: typescript\n'))  # no test here reaches GitHub
    return TestClient(server.create_app(runner, **kwargs))


class IdentityTests(unittest.TestCase):
    def test_pages_need_the_oauth2_proxy_identity(self):
        runner = fake()
        for path in ('/', '/apps', '/units', '/units/infra-base', '/platform', '/activity', '/apps/web/prod'):
            with self.subTest(path):
                response = client(runner).get(path)
                self.assertEqual(response.status_code, 401)
        self.assertEqual(runner.calls, [], 'an unauthenticated request must not reach the cluster')

    def test_blank_identity_is_refused(self):
        self.assertEqual(client(fake()).get('/', headers={'X-Auth-Request-Email': '  '}).status_code, 401)

    def test_healthz_needs_nothing_and_reads_nothing(self):
        runner = fake()
        response = client(runner).get('/healthz')
        self.assertEqual((response.status_code, response.text), (200, 'ok\n'))
        self.assertEqual(runner.calls, [])

    def test_identity_is_shown(self):
        self.assertIn('sam@swhurl.com', client(fake()).get('/apps', headers=WHO).text)
        self.assertIn(server.DEV_IDENTITY, client(fake(), dev_identity=server.DEV_IDENTITY).get('/apps').text)

    def test_dev_identity_only_binds_to_loopback(self):
        with redirect_stderr(io.StringIO()) as err:
            self.assertEqual(server.main(['--dev', '--host', '192.168.1.10']), 2)
        self.assertIn('only binds to loopback', err.getvalue())


class ConnectionsTests(unittest.TestCase):
    """One list of apps serves every reader: team and connections on the list, a map, and Connect on the app page."""

    def cluster(self):
        def web(ns, name, variables=None, host=None):
            doc = release(ns, name)
            values = doc['spec']['values']
            values['service'] = {'main': {'ports': {'http': {'port': 9000}}}}
            values['controllers']['main']['containers']['main']['env'] = variables or {}
            if host:
                values['ingress'] = {'main': {'hosts': [{'host': host}]}}
            return doc
        releases = [
            web('orders-staging', 'orders', {'SWHURL_ALLOWED_CALLERS': 'reports', 'SWHURL_API_PATH': '/openapi.json',
                                             'SWHURL_EVENTS_PUBLISH': 'created,paid'},
                host='staging-orders.homelab.swhurl.com'),
            web('orders-prod', 'orders', {'SWHURL_API_PATH': '/openapi.json'}),
            web('reports-staging', 'reports', {'SWHURL_SERVICE_ORDERS_URL': 'http://orders.orders-$(SWHURL_ENV).svc.cluster.local:9000'}),
            release('job-staging', 'job'),
        ]
        releases[-1]['spec']['values']['controllers']['main']['containers']['main']['env'] = {
            'SWHURL_EVENTS_SUBSCRIBE': 'orders.created'}
        units = [unit(f'app-{name}') for name in ('orders-staging', 'orders-prod', 'reports-staging', 'job-staging')]
        namespaces = {'items': [{'metadata': {'name': 'orders-staging', 'labels': {'platform.swhurl.com/team': 'payments'}}},
                                {'metadata': {'name': 'job-staging', 'labels': {'platform.swhurl.com/team': 'payments'}}},
                                {'metadata': {'name': 'reports-staging', 'labels': {'platform.swhurl.com/team': '<b>insights'}}}]}
        return fake(units=units, releases=releases, **{'kubectl get namespaces -l platform.swhurl.com/app': json.dumps(namespaces)})

    def test_the_list_shows_each_apps_team_and_filters_by_team(self):
        runner = self.cluster()
        text = client(runner).get('/apps', headers=WHO).text
        self.assertIn('title="Show only team payments">payments</a>', text)
        self.assertIn('&lt;b&gt;insights', text, 'cluster values are escaped')
        self.assertNotIn('class="compact connections"', text, 'the list is about what is deployed: connections are on the app page and the map')
        self.assertNotIn('/disconnect', text, 'changes are made on the app page')
        self.assertIn('>Staging only</span>', text)
        self.assertIn('<a href="/apps/reports/staging/promotion">Review promotion</a>', text)
        self.assertIn('<a class="open" href="https://staging-orders.homelab.swhurl.com" title="Open staging-orders.homelab.swhurl.com">', text)
        self.assertIn('<span class="pill">TypeScript</span>', text, 'the language, from the app\'s own repository')
        self.assertIn('ServiceName%3A%22orders%22', text, 'logs and traces from the row')
        self.assertIn('href="https://github.com/samclement/orders"', text)
        self.assertNotIn('href="/services"', text, 'there is one list of apps, not two')
        payments = client(runner).get('/apps?team=payments', headers=WHO).text
        self.assertIn('<strong>orders</strong>', payments)
        self.assertNotIn('<strong>reports</strong>', payments)
        self.assertTrue(all(call[:2] == ('kubectl', 'get') or call[:4] == ('kubectl', '-n', 'flux-system', 'get')
                            or call[:6] == ('kubectl', '-n', 'console', 'get', 'configmap', 'app-dashboards')
                            for call in runner.calls), 'the pages only read')

    def test_the_map_draws_calls_and_events_left_to_right(self):
        found = cluster.app_map(cluster.apps(self.cluster()), 'staging')
        positions = {node.row.instance.app: (node.x, node.y) for node in found.nodes}
        self.assertEqual(set(positions), {'orders', 'reports', 'job'}, 'staging only')
        self.assertLess(positions['reports'][0], positions['orders'][0], 'a caller is left of its callee')
        self.assertLess(positions['orders'][0], positions['job'][0], 'a publisher is left of its subscriber')
        self.assertEqual(sorted((kind, source, target) for kind, _, source, target in found.edges),
                         [('call', 'reports', 'orders'), ('event', 'orders', 'job')])
        text = client(self.cluster()).get('/apps?view=map', headers=WHO).text
        self.assertIn('<svg class="map"', text)
        self.assertEqual(text.count('<path class="call"'), 1)
        self.assertEqual(text.count('<path class="event"'), 1)
        self.assertIn('<a href="/apps/orders/staging"><rect', text)
        self.assertIn('<title>reports calls orders</title>', text)
        self.assertIn('No app is deployed in prod for team nobody', client(self.cluster()).get(
            '/apps?view=map&env=prod&team=nobody', headers=WHO).text)
        self.assertEqual(cluster.app_map([], 'staging').nodes, [])

    def test_the_old_services_address_goes_to_the_map(self):
        response = client(self.cluster()).get('/services', headers=WHO, follow_redirects=False)
        self.assertEqual((response.status_code, response.headers['location']), (301, '/apps?view=map'))

    def test_connections_read_the_same_whichever_side_declares_them(self):
        rows = {(row.instance.app, row.instance.env): row.connections for row in cluster.apps(self.cluster())}
        orders = rows['orders', 'staging']
        self.assertEqual((orders.address, orders.api), ('http://orders.orders-staging.svc.cluster.local:9000',
                                                        'https://staging-orders.homelab.swhurl.com/openapi.json'))
        self.assertEqual((orders.callers, orders.publishes, orders.subscribers),
                         (('reports',), ('created', 'paid'), (('created', 'job'),)))
        self.assertEqual(rows['orders', 'prod'].api, 'http://orders.orders-prod.svc.cluster.local:9000/openapi.json',
                         'no route: the address inside the cluster')
        self.assertEqual(rows['orders', 'prod'].subscribers, (), 'production has its own connections')
        self.assertEqual((rows['job', 'staging'].address, rows['job', 'staging'].subscribes), ('', ('orders.created',)))
        self.assertEqual(rows['reports', 'staging'].summary, 'calls 1')
        self.assertEqual([(r.direction, r.kind, r.app, r.event, r.team) for r in orders.rows],
                         [('out', 'event', '', 'created', ''), ('out', 'event', '', 'paid', ''),
                          ('in', 'call', 'reports', '', '<b>insights'), ('in', 'event', 'job', 'created', 'payments')],
                         'what it declared first, then what others declared about it')
        self.assertEqual([(r.role, r.app, r.event) for r in rows['job', 'staging'].rows], [('subscribes', 'orders', 'created')])

    def test_the_app_page_lists_connections_and_offers_what_is_left_to_connect(self):
        def page(app):
            runner = self.cluster()
            runner.on('kubectl', '-n', 'flux-system', 'get', 'kustomization', stdout=json.dumps(unit(f'app-{app}-staging')))
            runner.on('kubectl', '-n', f'{app}-staging', 'get', 'helmrelease', stdout=json.dumps(
                next(r for r in runner.json(list(cluster.RELEASES))['items'] if r['metadata']['namespace'] == f'{app}-staging')))
            runner.on('kubectl', '-n', f'{app}-staging', 'get', stdout=json.dumps({'items': []}))
            runner.on('kubectl', '-n', 'flux-system', 'get', 'gitrepository', stdout=json.dumps({'status': {'artifact': {'revision': 'main@sha1:abc'}}}))
            return client(runner).get(f'/apps/{app}/staging', headers=WHO).text
        text = page('reports')
        self.assertIn('<table class="compact connections">', text)
        self.assertIn('<input type="hidden" name="role" value="calls"><input type="hidden" name="app" value="orders">', text)
        self.assertNotIn('name="calls" value="orders"', text, 'what it calls already is not offered again')
        self.assertNotIn('name="calls" value="reports"', text, 'nor itself')
        self.assertIn('name="subscribe" value="orders.created"', text)
        self.assertIn('name="subscribe" value="orders.paid"', text)
        orders = page('orders')
        self.assertIn('name="calls" value="reports"', orders)
        self.assertNotIn('name="calls" value="job"', orders, 'a worker cannot be called')
        self.assertEqual(orders.count('/apps/orders/staging/disconnect'), 2, 'only what it declared: its two events')
        self.assertIn('Publish an event</summary>', orders)
        self.assertNotIn('Describes itself', orders)
        expected = {orders: [('← incoming', 'call', 'reports', 'calls this app', '&lt;b&gt;insights'),
                             ('→ outgoing', 'event', 'any subscriber', 'publishes paid', ''),
                             ('← incoming', 'event', 'job', 'receives created', 'payments')],
                    text: [('→ outgoing', 'call', 'orders', 'its API', 'payments')],
                    page('job'): [('→ outgoing', 'event', 'orders', 'receives created', 'payments')]}
        for shown, rows in expected.items():
            table = re.search(r'<table class="compact connections">.*?</table>', shown, re.S)[0]
            listed = [tuple(re.sub(r'<[^>]+>', '', cell).strip() for cell in re.findall(r'<td>.*?</td>', row, re.S))
                      for row in re.findall(r'<tr>.*?</tr>', table, re.S)]
            for row in rows:
                self.assertIn(row, listed, 'one connection per row, the same columns for each')

    def test_an_app_that_describes_its_events_declares_them_in_its_repository(self):
        runner = self.cluster()
        described = next(r for r in runner.json(list(cluster.RELEASES))['items'] if r['metadata']['namespace'] == 'orders-staging')
        described['metadata']['annotations'] = {'platform.swhurl.com/offers': 'abc1234'}
        runner._rules.insert(0, (tuple(cluster.RELEASES[:3]), Result((), 0, json.dumps({'items': [described]}), '')))
        self.assertEqual({row.instance.app: row.connections.offers for row in cluster.apps(runner)}['orders'], 'abc1234')
        runner.on('kubectl', '-n', 'flux-system', 'get', 'kustomization', stdout=json.dumps(unit('app-orders-staging')))
        runner.on('kubectl', '-n', 'orders-staging', 'get', 'helmrelease', stdout=json.dumps(described))
        runner.on('kubectl', '-n', 'orders-staging', 'get', stdout=json.dumps({'items': []}))
        runner.on('kubectl', '-n', 'flux-system', 'get', 'gitrepository', stdout=json.dumps({'status': {'artifact': {'revision': 'main@sha1:abc'}}}))
        text = client(runner).get('/apps/orders/staging', headers=WHO).text
        self.assertIn('href="https://github.com/samclement/orders/tree/abc1234"', text)
        self.assertNotIn('Publish an event</summary>', text, 'the document is the declaration')
        self.assertIn('Connect to an app</summary>', text)
        self.assertEqual(cluster.Connections().summary, '')

    ASYNCAPI = """asyncapi: 3.0.0
info: {title: orders, version: 0.1.0}
channels:
  created:
    address: created
    description: An order was <b>accepted</b>
    messages:
      created:
        payload: {type: object, required: [id], properties: {id: {type: string}}}
        examples: [{payload: {id: o-1}}]
"""
    OPENAPI = '{"openapi": "3.1.0", "info": {"title": "orders", "description": "Create and read orders"}, "paths": {"/orders/{id}": {"get": {"summary": "Read one order"}}}}'

    def described_cluster(self, commit='abc1234'):
        runner = self.cluster()
        releases = runner.json(list(cluster.RELEASES))['items']
        next(r for r in releases if r['metadata']['namespace'] == 'orders-staging')['metadata']['annotations'] = {
            'platform.swhurl.com/offers': commit}
        runner._rules.insert(0, (tuple(cluster.RELEASES[:3]), Result((), 0, json.dumps({'items': releases}), '')))
        return runner

    def documents(self, commit='abc1234', reads=None):
        files = {'apps/orders/staging/offers/source.yaml': f'repository: samclement/orders\ncommit: {commit}\napi: openapi.json\nevents: asyncapi.yaml\n',
                 'apps/orders/staging/offers/openapi.json': self.OPENAPI, 'apps/orders/staging/offers/asyncapi.yaml': self.ASYNCAPI}

        def reader(path):
            if reads is not None:
                reads.append(path)
            return files.get(path)
        return reader

    FLAGS = {'shout': {'type': 'boolean', 'description': 'Say hello in capitals', 'default': False, 'staging': True},
             'max-items': {'type': 'integer', 'min': 1, 'max': 100, 'default': 20},
             'banner': {'type': 'string', 'enum': ['none', 'sale'], 'default': 'none'}}

    class Flags:
        """Stands in for the console's NATS key-value store."""
        def __init__(self, overrides=None, fail=None):
            self.stored, self.fail, self.calls = dict(overrides or {}), fail, []

        def overrides(self, env, app):
            from swhurl.console import flagstore
            if self.fail:
                raise self.fail
            return {n: v if isinstance(v, flagstore.Override) else flagstore.Override(v, dt.datetime.now(dt.UTC))
                    for (e, a, n), v in self.stored.items() if (e, a) == (env, app)}

        def set(self, env, app, name, value):
            self.calls.append(('set', env, app, name, value))
            self.stored[(env, app, name)] = value

        def clear(self, env, app, name):
            self.calls.append(('clear', env, app, name))
            self.stored.pop((env, app, name), None)

    def flags_page_cluster(self):
        runner = self.described_cluster()
        release = next(r for r in runner.json(list(cluster.RELEASES))['items'] if r['metadata']['namespace'] == 'orders-staging')
        runner.on('kubectl', '-n', 'flux-system', 'get', 'kustomization', stdout=json.dumps(unit('app-orders-staging')))
        runner.on('kubectl', '-n', 'orders-staging', 'get', 'helmrelease', stdout=json.dumps(release))
        runner.on('kubectl', '-n', 'orders-staging', 'get', stdout=json.dumps({'items': []}))
        runner.on('kubectl', '-n', 'flux-system', 'get', 'gitrepository', stdout=json.dumps({'status': {'artifact': {'revision': 'main@sha1:abc'}}}))
        return runner

    def flag_documents(self):
        files = {'apps/orders/staging/offers/source.yaml': 'repository: samclement/orders\ncommit: abc1234\nflags: flags.json\n',
                 'apps/orders/staging/offers/flags.json': json.dumps(self.FLAGS)}
        return files.get

    def test_the_app_page_shows_each_declared_flag_with_its_override(self):
        store = self.Flags({('staging', 'orders', 'shout'): False})
        text = client(self.flags_page_cluster(), documents=self.flag_documents(), flags=store).get('/apps/orders/staging', headers=WHO).text
        self.assertIn('<h2>Feature flags</h2>', text)
        row = text[text.index('<code>shout</code>'):text.index('<code>max-items</code>')]
        self.assertIn('Say hello in capitals', row)
        self.assertIn('<td><code>true</code></td>', row, 'declared for staging')
        self.assertIn('<span class="pill attention" title="Set from the console; clear it to return to the declared value">false</span>', row)
        self.assertIn('value="clear"', row, 'an override can be cleared')
        others = text[text.index('<code>max-items</code>'):text.index('</table>', text.index('<code>max-items</code>'))]
        self.assertIn('min="1" max="100"', others, 'a number is bounded as declared')
        self.assertIn('<option selected>none</option><option>sale</option>', others, 'a string with an enum is a choice')
        self.assertNotIn('value="clear"', others, 'nothing to clear without an override')

    def test_a_flag_is_set_and_cleared_as_its_declared_type_and_audited(self):
        store = self.Flags()
        c = client(self.flags_page_cluster(), documents=self.flag_documents(), flags=store)
        with self.assertLogs('swhurl.console.audit', 'INFO') as logged:
            reply = c.post('/apps/orders/staging/flags/max-items', data={'action': 'set', 'value': '42'}, headers=ORIGIN, follow_redirects=False)
            c.post('/apps/orders/staging/flags/shout', data={'action': 'clear'}, headers=ORIGIN, follow_redirects=False)
        self.assertEqual((reply.status_code, reply.headers['location']), (303, '/apps/orders/staging?flag=max-items#flags'))
        self.assertEqual(store.calls, [('set', 'staging', 'orders', 'max-items', 42), ('clear', 'staging', 'orders', 'shout')])
        self.assertEqual([r.getMessage() for r in logged.records],
                         ['[AUDIT] sam@swhurl.com flag orders/staging max-items = 42', '[AUDIT] sam@swhurl.com flag orders/staging shout cleared'])
        for name, value, message in (('max-items', '500', 'outside min and max'), ('max-items', '1.5', 'give a whole number'),
                                     ('banner', 'loud', 'must be one of none, sale'), ('shout', 'yes', 'choose true or false'),
                                     ('gone', 'true', 'does not declare gone')):
            with self.subTest(name=name, value=value):
                reply = c.post(f'/apps/orders/staging/flags/{name}', data={'action': 'set', 'value': value}, headers=ORIGIN)
                self.assertEqual(reply.status_code, 400)
                self.assertIn(message, reply.text)
        self.assertEqual(len(store.calls), 2, 'a refused value is not written')
        self.assertEqual(c.post('/apps/orders/staging/flags/shout', data={'action': 'clear'}, headers=WHO).status_code, 403,
                         'only from a console page')

    def test_an_old_override_and_one_the_image_no_longer_declares_are_marked_and_can_be_cleared(self):
        from swhurl.console import flagstore
        old = dt.datetime.now(dt.UTC) - dt.timedelta(days=20)
        store = self.Flags({('staging', 'orders', 'max-items'): flagstore.Override(50, old),
                            ('staging', 'orders', 'shout'): flagstore.Override(False, dt.datetime.now(dt.UTC)),
                            ('staging', 'orders', 'old-banner'): flagstore.Override('sale', old)})
        c = client(self.flags_page_cluster(), documents=self.flag_documents(), flags=store)
        text = c.get('/apps/orders/staging', headers=WHO).text
        shout = text[text.index('<code>shout</code>'):text.index('<code>max-items</code>')]
        self.assertIn('<span class="muted">set today</span>', shout)
        items = text[text.index('<code>max-items</code>'):text.index('<code>banner</code>')]
        self.assertIn('<span class="warn" title="Older than 14 days', items)
        self.assertIn('set 20 days ago', items)
        gone = text[text.index('<code>old-banner</code>'):text.index('</table>', text.index('<code>old-banner</code>'))]
        self.assertIn('not declared</span>', gone)
        self.assertIn('<span class="muted">unused</span>', gone)
        self.assertNotIn('value="set"', gone, 'an undeclared flag has no type to set')
        self.assertIn('value="clear"', gone)
        reply = c.post('/apps/orders/staging/flags/old-banner', data={'action': 'clear'}, headers=ORIGIN, follow_redirects=False)
        self.assertEqual(reply.status_code, 303)
        self.assertEqual(store.calls, [('clear', 'staging', 'orders', 'old-banner')])
        self.assertEqual(c.post('/apps/orders/staging/flags/old-banner', data={'action': 'set', 'value': 'x'}, headers=ORIGIN).status_code, 400)
        self.assertEqual(c.post('/apps/orders/staging/flags/a.b', data={'action': 'clear'}, headers=ORIGIN).status_code, 400,
                         'a clear names one key, never a wildcard')

    def test_without_its_nats_key_the_console_says_so_and_writes_nothing(self):
        from swhurl.console import flagstore
        c = client(self.flags_page_cluster(), documents=self.flag_documents())
        with mock.patch.dict('os.environ', {flagstore.SEED_VARIABLE: ''}):
            c = client(self.flags_page_cluster(), documents=self.flag_documents())
            text = c.get('/apps/orders/staging', headers=WHO).text
            self.assertIn('This console has no NATS key', text)
            self.assertIn('<button type="submit" disabled>Set</button>', text)
            self.assertEqual(c.post('/apps/orders/staging/flags/shout', data={'action': 'clear'}, headers=ORIGIN).status_code, 409)
        broken = client(self.flags_page_cluster(), documents=self.flag_documents(), flags=self.Flags(fail=flagstore.FlagStoreError('NATS did not answer in time')))
        self.assertIn('Overrides could not be read: NATS did not answer in time', broken.get('/apps/orders/staging', headers=WHO).text)

    def test_the_catalogue_lists_every_api_and_event_with_what_its_app_says_about_it(self):
        from swhurl.console import catalogue
        reads = []
        documents = catalogue.Documents(self.documents(reads=reads))
        found = catalogue.entries(cluster.apps(self.described_cluster()), 'staging', documents)
        self.assertEqual([(e.kind, e.name, e.described, e.description, e.used_by) for e in found], [
            ('api', 'orders', True, 'Create and read orders', ('reports',)),
            ('api', 'reports', False, '', ()),
            ('event', 'orders.created', True, 'An order was <b>accepted</b>', ('job',)),
            ('event', 'orders.paid', False, '', ())], 'APIs first; a worker offers no API; what is not described is still listed')
        api, _, created, paid = found
        self.assertEqual(api.operations, (('GET', '/orders/{id}', 'Read one order'),))
        self.assertEqual((json.loads(created.schema)['required'], json.loads(created.example)), (['id'], {'id': 'o-1'}))
        self.assertEqual([name for name, _ in api.could_use], ['job'], 'not itself, not who calls it already')
        self.assertEqual([name for name, _ in paid.could_use], ['job', 'reports'])
        catalogue.entries(cluster.apps(self.described_cluster()), 'staging', documents)
        self.assertEqual(len(reads), 3, 'the documents of a commit are read once')
        self.assertEqual(catalogue.entries(cluster.apps(self.described_cluster()), 'prod', documents)[0].described, False,
                         'production has its own image and documents')

    def test_the_catalogue_groups_offers_by_app_and_says_what_each_app_uses(self):
        from swhurl.console import catalogue
        rows = cluster.apps(self.described_cluster())
        found = catalogue.entries(rows, 'staging', catalogue.Documents(self.documents()))
        groups = catalogue.groups(rows, 'staging', found)
        self.assertEqual([(g.app, [e.name for e in g.entries]) for g in groups],
                         [('orders', ['orders', 'orders.created', 'orders.paid']), ('reports', ['reports']), ('job', [])],
                         'apps that offer something first; an app that only uses is still listed')
        uses = {g.app: g.uses for g in groups}
        self.assertEqual((uses['reports'], uses['job']), ((('api', 'orders'),), (('event', 'orders.created'),)))
        schema = {'type': 'object', 'required': ['id', 'caller'], 'properties': {
            'id': {'type': 'string', 'format': 'uuid', 'description': 'Its id'},
            'caller': {'type': 'object', 'required': ['type'], 'properties': {'type': {'enum': ['app', 'user']}, 'id': {'type': ['string', 'null']}}},
            'lines': {'type': 'array', 'items': {'type': 'object', 'required': ['item'], 'properties': {'item': {'type': 'string'}}}}}}
        self.assertEqual([(f.name, f.type, f.required, f.meaning) for f in catalogue.fields(schema)], [
            ('id', 'string (uuid)', True, 'Its id'), ('caller', 'object', True, ''), ('caller.type', 'one of app, user', True, ''),
            ('caller.id', 'string or null', False, ''), ('lines', 'list of object', False, ''), ('lines[].item', 'string', False, '')],
            'one row per field; a field of an optional parent is not required')
        self.assertEqual(catalogue.fields({'type': 'string'}), (), 'not an object: the schema is shown instead')
        self.assertEqual(catalogue.Entry('api', 'a', 'a', '', operations=(('GET', '/', ''), ('GET', '/o/{id}', ''), ('POST', '/o', ''), ('GET', '/o', ''))).sample_path, '/o')
        self.assertEqual(catalogue.Entry('api', 'a', 'a', '').sample_path, '/')

    def test_the_catalogue_page_shows_routes_payload_fields_and_one_choice_of_app_to_use_it(self):
        c = client(self.described_cluster(), documents=self.documents())
        text = c.get('/catalogue', headers=WHO).text
        self.assertIn('2 APIs · 2 events · 2 described by their app', text)
        self.assertIn('<td><span class="pill">GET</span></td><td><code>/orders/{id}</code></td><td>Read one order</td>', text)
        self.assertIn('An order was &lt;b&gt;accepted&lt;/b&gt;', text, 'what an app says about itself is escaped')
        self.assertIn('<tr><td><code>id</code></td><td>string</td><td>yes</td><td>—</td></tr>', text, 'the payload, one row per field')
        self.assertIn('&#34;required&#34;: [', text, 'the full schema is still there, closed')
        self.assertIn('<section class="card" id="app-reports">', text, 'one card per app')
        self.assertIn('<h2><a href="/apps/reports/staging">reports</a></h2>', text)
        self.assertIn('href="/catalogue?env=staging#api-orders" title="Show orders in the catalogue">API orders</a>', text, 'what reports uses')
        self.assertIn('href="/catalogue?env=staging#event-orders.created" title="Show orders.created in the catalogue">event orders.created</a>', text)
        self.assertIn('Offers nothing: no API and no event.', text, 'job only uses')
        self.assertEqual(text.count('<form class="inline use" method="post" action="/catalogue/staging/use"'), 4, 'one form per offer')
        self.assertIn('<input type="hidden" name="subscribe" value="orders.paid">', text)
        self.assertIn('<input type="hidden" name="calls" value="orders">', text)
        api = text[text.index('id="api-orders"'):text.index('id="event-orders.created"')]
        self.assertIn('<option value="job">job (team ', api)
        self.assertNotIn('<option value="orders">', api, 'not itself')
        self.assertNotIn('<option value="reports">', api, 'not who calls it already')
        self.assertIn('<a href="/apps/reports/staging">reports</a> <span class="muted">team ', api, 'who uses it, by name')
        self.assertIn('service("orders").fetch("/")', api)
        self.assertIn('href="https://github.com/samclement/orders/tree/abc1234"', text)
        self.assertIn('reports does not describe its API', text)
        self.assertIn('<a href="/catalogue" class="here">Catalogue</a>', text)
        events = c.get('/catalogue?kind=event&team=payments', headers=WHO).text
        self.assertIn('0 APIs · 2 events', events)
        self.assertNotIn('Offers nothing', events, 'a kind was chosen: apps that offer neither are left out')
        self.assertIn('Nothing is offered in prod by team nobody', c.get('/catalogue?env=prod&team=nobody', headers=WHO).text)
        self.assertEqual(c.get('/catalogue').status_code, 401)

    def test_descriptions_that_cannot_be_read_leave_the_catalogue_standing(self):
        from swhurl.console import catalogue

        def broken(path):
            raise OSError('GitHub is away')
        clock = [0.0]
        for reader, message in ((broken, 'could not be read from Git: OSError'), (None, 'no GitHub token'),
                                (lambda path: None, 'is not in Git yet'),
                                (self.documents(commit='0000000'), '')):
            with self.subTest(message=message):
                documents = catalogue.Documents(reader, clock=lambda: clock[0])
                found = catalogue.entries(cluster.apps(self.described_cluster()), 'staging', documents)
                self.assertEqual(len(found), 4)
                self.assertIn(message, found[0].problem)
                self.assertEqual(documents.kept, {}, 'only what matches the running commit is kept for good')
        calls = []
        documents = catalogue.Documents(lambda path: calls.append(path) or broken(path), clock=lambda: clock[0])
        for _ in range(3):
            documents.of('orders', 'staging', 'abc1234')
        self.assertEqual(len(calls), 1, 'a failure is not retried for five minutes')
        clock[0] = 301
        documents.of('orders', 'staging', 'abc1234')
        self.assertEqual(len(calls), 2)
        text = client(self.described_cluster(), documents=broken).get('/catalogue', headers=WHO).text
        self.assertIn('Its description could not be shown: could not be read from Git: OSError.', text)

    def test_the_connect_picker_says_what_each_choice_is(self):
        runner = self.described_cluster()
        runner.on('kubectl', '-n', 'flux-system', 'get', 'kustomization', stdout=json.dumps(unit('app-reports-staging')))
        runner.on('kubectl', '-n', 'reports-staging', 'get', 'helmrelease', stdout=json.dumps(
            next(r for r in runner.json(list(cluster.RELEASES))['items'] if r['metadata']['namespace'] == 'reports-staging')))
        runner.on('kubectl', '-n', 'reports-staging', 'get', stdout=json.dumps({'items': []}))
        runner.on('kubectl', '-n', 'flux-system', 'get', 'gitrepository', stdout=json.dumps({'status': {'artifact': {'revision': 'main@sha1:abc'}}}))
        text = client(runner, documents=self.documents()).get('/apps/reports/staging', headers=WHO).text
        self.assertIn('<code>orders.created</code> <span class="muted">from orders, team payments · An order was &lt;b&gt;accepted&lt;/b&gt;</span>', text)
        self.assertIn('<code>orders.paid</code> <span class="muted">from orders, team payments</span>', text)
        self.assertIn('<a href="/catalogue?env=staging">catalogue</a>', text)

    def test_an_account_that_cannot_list_namespaces_still_gets_the_apps(self):
        runner = fake(**{'kubectl get namespaces -l platform.swhurl.com/app': ''})
        runner._rules.insert(0, (('kubectl', 'get', 'namespaces'), Result((), 1, '', 'forbidden')))
        rows = cluster.apps(runner)
        self.assertTrue(rows)
        self.assertEqual({row.team for row in rows}, {''})


class AppsTests(unittest.TestCase):
    def test_lists_app_units_only_with_state_and_image(self):
        text = client(fake()).get('/apps', headers=WHO).text
        self.assertIn('href="/apps/web/prod"', text)
        self.assertIn('docker.io/x/web:1.0', text)
        self.assertIn('href="/apps/my-api/staging"', text)
        self.assertIn('Suspended', text)
        self.assertIn('no HelmRelease', text)
        self.assertNotIn('infra-base</a>', text)

    def test_the_egress_page_lists_each_instances_port_and_grants(self):
        granted = release('web-prod', 'web')
        granted['metadata']['annotations'] = {'platform.swhurl.com/egress': 'api.stripe.com:443,*.example.com:8443'}
        granted['spec']['values']['controllers']['main']['containers']['main']['env'] = {'HTTPS_PROXY':
            'http://egress.egress.svc.cluster.local:30111'}
        units = [unit('app-web-prod'), unit('app-api-staging')]
        text = client(fake(units=units, releases=[granted, release('api-staging', 'api')])).get('/egress', headers=WHO).text
        self.assertIn('1 of 2 instances is granted a host outside the cluster', text)
        self.assertIn('<code>30111</code></td>\n    <td><code>api.stripe.com:443</code> <code>*.example.com:8443</code></td>', text)
        self.assertIn('<a href="/apps/api/staging#connect">api/staging</a>', text)
        self.assertIn('instance%3Dweb-prod+granted%3D-', text, "the link opens that instance's requests without a grant")
        self.assertIn('<a href="/platform" class="here">Platform</a>', text)
        self.assertIn('<a href="/egress">Egress</a>', client(fake(**CHECKS)).get('/platform', headers=WHO).text)

    def test_cluster_values_are_escaped(self):
        units = [unit('app-x-prod', ready='False', message='<script>x</script>')]
        text = client(fake(units=units)).get('/apps', headers=WHO).text
        self.assertNotIn('<script>x</script>', text)
        self.assertIn('&lt;script&gt;', text)

    def test_a_row_opens_itself_when_an_environment_is_not_healthy_and_the_filter_shows_those_rows(self):
        units = [unit('app-web-staging'), unit('app-web-prod', ready='False', message='boom'),
                 unit('app-idle-staging', suspend=True), unit('app-fine-staging'),
                 unit('app-wait-staging', ready='False', message="dependency 'flux-system/infra-base' is not ready")]
        releases = [release('web-staging', 'web'), release('fine-staging', 'fine'), release('wait-staging', 'wait'),
                    release('idle-staging', 'idle')]
        text = client(fake(units=units, releases=releases)).get('/apps', headers=WHO).text
        for app, opened in (('web', True), ('idle', True), ('fine', False), ('wait', False)):
            self.assertEqual(f'<details class="openrow" id="app-{app}" open>' in text, opened, app)
            self.assertIn(f'<details class="openrow" id="app-{app}"', text, 'every row can be opened, and stays open when the page is re-read')
        self.assertIn('<p class="reason bad">boom</p>', text)
        self.assertIn('hx-post="/units/app-web-prod/reconcile"', text)
        self.assertIn('action="/units/app-idle-staging/resume"', text)
        self.assertNotIn('action="/units/app-idle-staging/reconcile"', text, 'a suspended instance is resumed, not reconciled')
        shown = client(fake(units=units, releases=releases)).get('/apps?show=attention', headers=WHO).text
        self.assertIn('<strong>web</strong>', shown)
        self.assertIn('<strong>wait</strong>', shown)
        self.assertNotIn('<strong>fine</strong>', shown)
        self.assertIn('Every environment is healthy', client(fake(units=[unit('app-fine-staging')], releases=releases)).get(
            '/apps?show=attention', headers=WHO).text)

    def test_database_is_visible_per_environment_and_unknown_without_a_release(self):
        units = [unit('app-web-staging'), unit('app-web-prod'), unit('app-pending-staging')]
        releases = [release('web-staging', 'web', sqlite=True), release('web-prod', 'web')]
        text = client(fake(units=units, releases=releases)).get('/apps', headers=WHO).text
        self.assertEqual(text.count('<span class="pill">SQLite</span>'), 1, 'said only where there is one')
        self.assertEqual(text.count('release unknown'), 1)

    def test_unreadable_cluster_is_a_502_page_not_a_crash(self):
        runner = FakeRunner().on('kubectl', returncode=1, stderr='connection refused')
        response = client(runner).get('/apps', headers=WHO)
        self.assertEqual(response.status_code, 502)
        self.assertIn('connection refused', response.text)


class PolishTests(unittest.TestCase):
    def test_long_hashes_are_shortened_with_the_full_value_on_hover_and_still_escaped(self):
        sha = '1edf37052ebd0a94814cac0407f3380aa6a9c614'
        self.assertEqual(str(server.short_hashes(f'Applied revision: main@sha1:{sha}')),
                         f'Applied revision: main@sha1:<span class="hash" title="{sha}">1edf37052ebd…</span>')
        self.assertEqual(str(server.short_hashes('<script>1edf370</script>')), '&lt;script&gt;1edf370&lt;/script&gt;')

    def test_header_breadcrumbs_and_updated_time(self):
        c, jobs, _ = operate(fake())
        jobs._jobs[3] = actions.Job(3, 'reconcile', 'app-web-prod', 'sam@swhurl.com', jobs.now())
        job = c.get('/jobs/3', headers=WHO).text
        self.assertIn('<a href="/activity">Activity</a> › Job #3', job)
        self.assertIn('Refresh</a>', job)
        self.assertRegex(job, r'Read at \d\d:\d\d')
        self.assertNotIn('hx-trigger', job, 'a job page streams its own output; it is not re-read on a timer')
        overview = c.get('/apps', headers=WHO).text
        self.assertIn('hx-trigger="every 30s [canRefresh()]"', overview, 'pages that only show state re-read themselves')
        self.assertIn('and again every 30 seconds', overview)
        self.assertIn('/static/console.css?v=', overview)


class AssetTests(unittest.TestCase):
    def test_a_list_page_starts_under_its_title_with_its_explanation_one_click_away(self):
        for path in ('/', '/apps', '/platform', '/activity'):
            text = client(fake(**CHECKS)).get(path, headers=WHO).text
            self.assertEqual(text.count('<details class="about" id="about"><summary>'), 1, path)
            self.assertLess(text.index('<p class="page-meta">'), text.index('<h1>'), 'the read time sits beside the title')

    def test_the_stylesheet_fonts_and_scripts_are_served_from_the_image(self):
        c = client(fake())
        for path, kind in (('/static/console.css', 'text/css'), ('/static/console.js', 'javascript'), ('/static/htmx.min.js', 'javascript'),
                           ('/static/fonts/ibm-plex-sans-latin-400-normal.woff2', 'font/woff2')):
            with self.subTest(path=path):
                response = c.get(path, headers=WHO)
                self.assertEqual(response.status_code, 200)
                self.assertIn(kind, response.headers['content-type'])
        self.assertEqual(c.get('/static/console.css').status_code, 401, 'nothing is served without an identity')
        page = c.get('/apps', headers=WHO).text
        self.assertNotIn('<style>', page, 'one cached stylesheet, not a copy in every page')
        self.assertNotRegex(page, r'https?://(?!github\.com)[a-z0-9.-]+/[^"]*\.(css|js|woff2)', 'no asset comes from another host')

    def test_a_state_is_a_shape_and_a_word_and_only_problems_are_loud(self):
        units = [unit('app-web-staging'), unit('app-api-staging', ready='False', message='boom')]
        releases = [release('web-staging', 'web'), release('api-staging', 'api')]
        page = client(fake(units=units, releases=releases)).get('/apps', headers=WHO).text
        self.assertIn('<span class="status failing"><span class="mark failing" aria-hidden="true"></span><span class="state failing">Failing</span>', page)
        self.assertIn('<span class="state healthy sr-only">Healthy</span>', page, 'in a list, healthy is the mark alone; the word stays for screen readers')
        self.assertLess(page.index('<strong>api</strong>'), page.index('<strong>web</strong>'), 'what needs attention is listed first')
        self.assertIn('1 needs attention', page)
        self.assertIn('<strong>1</strong>&nbsp;healthy', page)


class StateTests(unittest.TestCase):
    def status(self, **changes):
        digest = 'sha256:' + 'a' * 64
        status = cluster.ops.InstanceStatus(Instance('web', 'staging'), 'r', 'r', ('True', ''),
                                            ('True', ''), f'x:1@{digest}', [f'x@{digest}'],
                                            [cluster.ops.Replicas('Deployment/web', 1, 1)], [], [], [])
        return replace(status, **changes)

    def test_health_requires_release_revision_workloads_and_running_image(self):
        cases = (({}, 'healthy'), ({'release': None}, 'unknown'),
                 ({'desired_revision': '', 'applied_revision': ''}, 'unknown'),
                 ({'replicas': []}, 'unknown'), ({'running_images': []}, 'unknown'),
                 ({'replicas': [cluster.ops.Replicas('Deployment/web', 0, None)]}, 'unknown'),
                 ({'replicas': [cluster.ops.Replicas('Deployment/web', 0, 1)]}, 'updating'),
                 ({'replicas': [cluster.ops.Replicas('Deployment/web', 0, 0)], 'running_images': []}, 'stopped'),
                 ({'unit_suspended': True}, 'suspended'), ({'release_suspended': True}, 'suspended'))
        for overrides, expected in cases:
            with self.subTest(overrides=overrides):
                self.assertEqual(state.verdict(self.status(**overrides)).key, expected)

    def test_a_ready_unit_with_no_release_is_not_a_healthy_app(self):
        row = cluster.apps(fake(units=[unit('app-web-prod')], releases=[]))[0]
        self.assertEqual((row.state.key, row.image), ('unknown', 'no HelmRelease'))

    def test_the_list_and_the_page_give_one_verdict_from_the_same_facts(self):
        units, releases = [unit('app-web-prod')], [release('web-prod', 'web')]
        pods, workloads = running(releases)
        workloads['items'][0]['status']['readyReplicas'] = 0
        runner = fake(units=units, releases=releases, **{
            'kubectl get pods --all-namespaces': json.dumps(pods), 'kubectl -n web-prod get pods': json.dumps(pods),
            'kubectl get deploy,statefulset,daemonset,cronjob --all-namespaces': json.dumps(workloads),
            'kubectl -n web-prod get deploy,statefulset,daemonset,cronjob': json.dumps(workloads),
            'kubectl -n flux-system get kustomization app-web-prod': json.dumps(units[0]),
            'kubectl -n web-prod get helmrelease web': json.dumps(releases[0]),
            'kubectl -n web-prod get ingress': '{}', 'kubectl -n web-prod get certificate': '{}'})
        listed = cluster.apps(runner)[0].state
        self.assertEqual(listed, state.State('updating', 'waiting for the requested replicas to be ready'),
                         'a Ready unit is not a healthy app while its replicas are not ready')
        self.assertEqual(state.verdict(cluster.ops.gather_status(runner, Instance('web', 'prod'))), listed)
        page = client(runner).get('/apps/web/prod', headers=WHO).text
        self.assertIn('<span class="state updating">Updating</span>', page)
        self.assertIn('<span class="state updating', client(runner).get('/apps', headers=WHO).text)

    def test_one_vocabulary_and_waiting_is_updating_not_failing(self):
        unit_state = state.unit_state
        self.assertEqual(unit_state(('True', 'Applied')), state.State('healthy'))
        self.assertEqual(unit_state(('False', "dependency 'flux-system/platform-oauth2-proxy' is not ready"), reason='DependencyNotReady'),
                         state.State('updating', 'waiting for platform-oauth2-proxy'))
        self.assertEqual(unit_state(('False', 'Reconciliation in progress'), reason='Progressing').key, 'updating')
        self.assertEqual(unit_state(('Unknown', 'Running health checks')).key, 'updating')
        self.assertEqual(unit_state(('False', 'health check failed'), reason='HealthCheckFailed'), state.State('failing', 'health check failed'))
        self.assertEqual(unit_state(('True', ''), suspended=True).key, 'suspended')

    def test_a_page_reads_each_thing_once_and_reports_checks_in_their_order(self):
        for path in ('/', '/platform', '/apps', '/apps/web/prod'):
            with self.subTest(path):
                runner = fake(**CHECKS, **{
                    'kubectl -n flux-system get kustomization app-web-prod': json.dumps(unit('app-web-prod')),
                    'kubectl -n web-prod get': '{}'})
                self.assertEqual(client(runner).get(path, headers=WHO).status_code, 200)
                self.assertEqual(len(runner.calls), len(set(runner.calls)), 'no cluster read is made twice for one page')
        runner = fake(**CHECKS)
        apart = cluster.platform_checks(runner)
        shared = SharedReads(runner)
        with shared.once():
            at_once = cluster.platform_checks(shared)
        self.assertEqual(at_once, apart, 'checks run at once are reported as when run one after another')
        self.assertGreater(apart.count, 3)

    def test_every_read_a_page_makes_is_one_the_pod_can_make_without_kubectl(self):
        """A read ``kube.api_read`` does not know still works, through kubectl and ten times slower."""
        for path in ('/', '/platform', '/apps', '/apps?view=map', '/apps/web/prod', '/catalogue', '/teams', '/units/app-web-prod'):
            runner = fake(**CHECKS, **{
                'kubectl -n flux-system get kustomization app-web-prod': json.dumps(unit('app-web-prod')),
                'kubectl -n web-prod get': '{}'})
            self.assertEqual(client(runner).get(path, headers=WHO).status_code, 200)
            self.assertTrue(runner.calls, path)
            for call in runner.calls:
                with self.subTest(path=path, call=' '.join(call)):
                    self.assertTrue(is_read(call) and kube.api_read(call))

    def test_a_dependency_wait_is_not_a_problem(self):
        waiting = unit('app-web-prod', ready='False', message="dependency 'flux-system/infra-base' is not ready")
        text = client(fake(units=[unit('infra-base'), waiting], **CHECKS)).get('/', headers=WHO).text
        self.assertIn('<strong>Updating</strong>', text)
        self.assertIn('<a href="/apps/web/prod">web/prod: waiting for infra-base</a>', text)
        self.assertNotIn('Needs attention', text)


class AppSummaryTests(unittest.TestCase):
    def row(self, env, tag, digest='', app='hello-ts'):
        return cluster.AppRow(Instance(app, env), state.State('healthy'), False, f'ghcr.io/x/{app}:{tag}', tag, digest)

    def test_compares_staging_with_prod(self):
        cases = {('5-aaaaaaa', 'sha256:1', '4-bbbbbbb', 'sha256:2'): 'ahead',
                 ('4-bbbbbbb', 'sha256:2', '5-aaaaaaa', 'sha256:1'): 'differs',
                 ('1.27', 'sha256:1', '1.27', 'sha256:1'): 'same',
                 ('1.28', 'sha256:3', '1.27', 'sha256:1'): 'differs'}
        for (st, sd, pt, pd), expected in cases.items():
            with self.subTest(staging=st, prod=pt):
                (summary,) = cluster.summaries([self.row('staging', st, sd), self.row('prod', pt, pd)])
                self.assertEqual(summary.comparison, expected)
        (only,) = cluster.summaries([self.row('staging', '1')])
        self.assertEqual(only.comparison, '')

    def test_apps_list_offers_promote_when_staging_differs(self):
        units = [unit('app-web-staging'), unit('app-web-prod')]
        releases = [release('web-staging', 'web', tag='2.0'), release('web-prod', 'web', tag='1.0')]
        text = client(fake(units=units, releases=releases), github=changes.GitHub('x/y', 'token')).get('/apps', headers=WHO).text
        self.assertIn('>Images differ</span>', text)
        self.assertIn('<span class="cell act"><a href="/apps/web/staging/promotion">Review promotion</a></span>', text)
        self.assertNotIn('/staging/promote', text, 'review the exact running image on the app page first')

    def test_an_apps_dashboard_is_linked_where_the_dashboard_job_recorded_one(self):
        record = {'kubectl -n console get configmap app-dashboards': json.dumps({'data': {'dashboards.json': json.dumps(
            {'web': 'a' * 24, 'my-api': 'javascript:alert(1)'})}})}
        link = '<a href="https://clickstack.homelab.swhurl.com/dashboards/' + 'a' * 24 + '"'
        text = client(fake(**record)).get('/apps', headers=WHO).text
        self.assertEqual(text.count(link), 1, 'web has one; what is not a dashboard id never becomes a link')
        self.assertNotIn('javascript:', text)
        self.assertNotIn('Dashboard <svg', client(fake()).get('/apps', headers=WHO).text, 'no record: no link')

    def test_promotion_is_one_of_four_labels_each_with_its_own_colour(self):
        def page(staging_tag, prod_tag):
            units = [unit('app-web-staging')] + ([unit('app-web-prod')] if prod_tag else [])
            releases = [release('web-staging', 'web', tag=staging_tag)] + ([release('web-prod', 'web', tag=prod_tag)] if prod_tag else [])
            return client(fake(units=units, releases=releases)).get('/apps', headers=WHO).text
        for tags, label in ((('5-aaaaaaa', None), '<span class="pill" title="Not in production yet">Staging only</span>'),
                            (('5-aaaaaaa', '5-aaaaaaa'), 'class="pill ok" title="Staging and production run the same image">In sync</span>'),
                            (('5-aaaaaaa', '4-bbbbbbb'), 'class="pill attention" title="Staging runs a newer build than production">Prod behind</span>'),
                            (('4-bbbbbbb', '5-aaaaaaa'), '>Images differ</span>')):
            with self.subTest(tags=tags):
                self.assertIn(label, page(*tags))
        self.assertNotIn('Review promotion', page('5-aaaaaaa', '5-aaaaaaa'), 'nothing to promote when in sync')

    def test_every_page_names_app_instances_the_same_way(self):
        self.assertEqual(cluster.target('app-hello-staging'), ('hello/staging', '/apps/hello/staging'))
        self.assertEqual(cluster.target('hello/staging'), ('hello/staging', '/apps/hello/staging'))
        self.assertEqual(cluster.target('infra-base'), ('infra-base', '/units/infra-base'))


class AppDetailTests(unittest.TestCase):
    def test_read_failures_show_an_error_instead_of_not_found(self):
        for detail in ('Forbidden', 'connection refused'):
            with self.subTest(detail=detail):
                runner = FakeRunner().on('kubectl', returncode=1, stderr=detail)
                response = client(runner).get('/apps/web/prod', headers=WHO)
                self.assertEqual(response.status_code, 502)
                self.assertIn(detail, response.text)
                self.assertNotIn('No such app instance', response.text)

    def test_the_scale_form_shows_the_minimum_the_running_image_declares(self):
        asked = []

        def opener(request):
            asked.append(request.full_url)
            return {}, b'version: 1\nkind: web\nhealthPath: /h\nresources: {cpu: 100m, memoryLimit: 384Mi}\n'
        def answers(tag):
            found = {f'kubectl -n {"flux-system" if kind in ("kustomization", "gitrepository") else "web-prod"} get {kind}': json.dumps(value)
                     for kind, value in (('kustomization', unit('app-web-prod')),
                                         ('gitrepository', {'status': {'artifact': {'revision': 'main@sha1:fff0000'}}}),
                                         ('helmrelease', {**release('web-prod', 'web', tag=tag), 'status': unit('web')['status']}),
                                         ('pods', {'items': []}), ('deploy,statefulset,daemonset,cronjob', {'items': []}),
                                         ('ingress', {'items': []}), ('certificate', {'items': []}))}
            return fake(**found)
        app = client(answers('12-abc1234'), repo_opener=opener)
        for _ in range(2):
            text = app.get('/apps/web/prod', headers=WHO).text
            self.assertIn("The image's minimum, from its <code>swhurl.yaml</code>: cpu request 100m, memory limit 384Mi.", text)
        self.assertEqual([url for url in asked if 'ref=abc1234' in url],
                         ['https://api.github.com/repos/samclement/web/contents/swhurl.yaml?ref=abc1234'], 'a commit is read once')
        text = client(answers('1.0'), repo_opener=opener).get('/apps/web/prod', headers=WHO).text
        self.assertNotIn("image's minimum", text, 'a tag that names no commit says nothing')
        self.assertIn('Calls, subscriptions and hosts outside the cluster are granted here', text)

    def test_a_later_read_failure_does_not_show_partial_health(self):
        runner = (FakeRunner().on('kubectl', '-n', 'flux-system', 'get', 'kustomization',
                                  stdout=json.dumps(unit('app-web-prod')))
                  .on('kubectl', '-n', 'flux-system', 'get', 'gitrepository', returncode=1, stderr='Forbidden'))
        response = client(runner).get('/apps/web/prod', headers=WHO)
        self.assertEqual(response.status_code, 502)
        self.assertIn('Forbidden', response.text)
        self.assertNotIn('<strong>Healthy</strong>', response.text)

    def test_shows_gathered_status(self):
        def get(kind, value):
            return (f'kubectl -n {"flux-system" if kind in ("kustomization", "gitrepository") else "web-prod"} get {kind}',
                    json.dumps(value))
        answers = dict([
            get('kustomization', unit('app-web-prod')),
            get('gitrepository', {'status': {'artifact': {'revision': 'main@sha1:fff0000'}}}),
            get('helmrelease', {**release('web-prod', 'web', sqlite=True), 'status': unit('web')['status']}),
            get('pods', {'items': [{'metadata': {'name': 'web-1'}, 'status': {'containerStatuses': [
                {'ready': False, 'state': {'waiting': {'reason': 'CrashLoopBackOff'}}}]}}]}),
            get('deploy,statefulset,daemonset,cronjob', {'items': []}),
            get('ingress', {'items': [{'spec': {'rules': [{'host': 'web.homelab.swhurl.com'}]}}]}),
            get('certificate', {'items': []}),
        ])
        text = client(fake(**answers)).get('/apps/web/prod', headers=WHO).text
        self.assertIn('not yet applied', text)
        self.assertIn('id="replicas" name="replicas" value="" placeholder="1"', text)
        self.assertEqual(text.count('<form class="wizard"'), 5, 'the app page uses the New app form layout')
        self.assertIn('action="/apps/web/prod/egress"', text)
        self.assertEqual(text.count('action="/apps/web/prod/connect"'), 2, 'connect to an app, and publish an event')
        self.assertNotIn('<table class="compact connections">', text, 'nothing to list')
        self.assertIn('No connections: it calls nothing', text)
        self.assertLess(text.index('>Use</h2>'), text.index('>Connect</h2>'))
        self.assertLess(text.index('>Connect</h2>'), text.index('>Operate</h2>'))
        self.assertEqual(text.count('<h1>web '), 1, 'the app is named once, with its language; the environment is the tab')
        self.assertIn('<h1>web <span class="pill"', text)
        for part in ('use', 'connect', 'operate'):
            self.assertIn(f'<section class="card" id="{part}"', text, 'each part is a card of its own')
        self.assertEqual(text.count('<details class="act" name="app-form"'), 5, 'each change opens in its card, one at a time')
        self.assertIn('<div><span>State</span>', text, 'the summary: one fact per column')
        self.assertIn('hx-post="/units/app-web-prod/reconcile" hx-target="this" hx-select="unset"', text, 'Reconcile answers in place')
        self.assertIn('href="https://github.com/samclement/web"', text)
        self.assertIn('Logs and traces', text)
        self.assertIn('search?where=ServiceName%3A%22web%22', text)
        self.assertIn('action="/apps/web/prod/remove"', text)
        self.assertIn('<section class="card danger"', text)
        self.assertIn('<span class="status failing">', text, 'a failing pod makes the verdict')
        self.assertIn('<span class="state failing">Failing</span>', text)
        self.assertNotIn('/promote', text, 'promote is offered only on staging')
        self.assertIn('https://web.homelab.swhurl.com', text)
        self.assertIn('CrashLoopBackOff', text)
        self.assertIn('Public (no sign-in)', text, 'a route without the sign-in middleware reads as public')
        self.assertIn('action="/apps/web/prod/expose"', text)
        self.assertIn('name="exposure" value="public" checked>', text)
        self.assertIn('<strong>Public</strong> <span class="muted">(now)</span>', text)
        self.assertIn('<div><span>Database</span><span class="pill">SQLite</span></div>', text)
        self.assertIn('<span class="pill">SQLite</span> <code>/data/app.db</code>', text)

    def test_unknown_or_invalid_instance_is_404_without_odd_kubectl_calls(self):
        runner = fake().on('kubectl', '-n', 'flux-system', 'get', 'kustomization', 'app-nope-prod',
                           stdout='')
        self.assertEqual(client(runner).get('/apps/nope/prod', headers=WHO).status_code, 404)
        before = len(runner.calls)
        for path in ('/apps/web/dev', '/apps/Web/prod', '/apps/--all/prod'):
            self.assertEqual(client(runner).get(path, headers=WHO).status_code, 404, path)
        self.assertEqual(len(runner.calls), before, 'invalid names must not reach kubectl')


class UnitsTests(unittest.TestCase):
    def test_columns_follow_depends_on(self):
        levels = {u.name: u.level for u in cluster.units(fake())}
        self.assertEqual(levels, {'cluster-sources': 0, 'infra-base': 0, 'platform-oauth2-proxy': 1,
                                  'app-web-prod': 2, 'app-my-api-staging': 1})

    def test_missing_dependency_and_cycles_do_not_break_levels(self):
        units = [unit('a', ['b']), unit('b', ['a']), unit('c', ['gone'])]
        self.assertEqual({u.name: u.level for u in cluster.units(fake(units=units))}, {'a': 1, 'b': 0, 'c': 0})

    def test_platform_groups_units_by_layer_and_a_unit_opens_to_its_details_and_actions(self):
        text = client(fake(**CHECKS)).get('/platform', headers=WHO).text
        for layer in ('Cluster', 'Infrastructure', 'Platform services', 'Deployments'):
            self.assertIn(f'<div class="group">{layer} ', text)
        self.assertIn('href="/units/infra-base"', text)
        self.assertIn('abc1234', text)
        self.assertIn('infra-base, platform-oauth2-proxy', text, 'what each unit waits for')
        self.assertIn('<details class="section" id="units-list" open>', text, 'a unit not Ready opens the units list')
        self.assertIn('<details class="openrow" id="unit-infra-base">', text, 'a healthy unit stays one line until opened')
        self.assertIn('<details class="openrow" id="unit-app-my-api-staging" open>', text, 'a unit that needs you is open')
        self.assertIn('hx-post="/units/infra-base/reconcile" hx-target="this" hx-select="unset"', text,
                      'a unit is acted on from its row; the answer is a fragment, not a page to pick main out of')
        self.assertIn('Safe: it applies what Git already says', text, 'each action says what it does, beside its button')
        self.assertIn('action="/units/infra-base/suspend"', text)
        self.assertIn('action="/units/app-my-api-staging/resume"', text, 'a suspended unit offers Resume alone')
        self.assertNotIn('action="/units/app-my-api-staging/suspend"', text)
        self.assertNotIn('action="/units/cluster-sources/', text, 'the root units have no actions')
        self.assertIn('<a class="mono" href="#unit-platform-oauth2-proxy">', text, 'needed by: a jump within the list')
        self.assertNotIn('<strong>Flux Kustomizations</strong>', text, 'the units list replaces that check group')
        self.assertIn('my-api/staging is suspended', text)

    def test_unit_page_offers_its_actions_and_explains_them(self):
        c = client(fake())
        text = c.get('/units/infra-base', headers=WHO).text
        self.assertIn('action="/units/infra-base/reconcile"', text)
        self.assertIn('action="/units/infra-base/suspend"', text)
        self.assertIn('href="/units/platform-oauth2-proxy"', text, 'needed by')
        resume = c.get('/units/app-my-api-staging', headers=WHO).text
        self.assertIn('action="/units/app-my-api-staging/resume"', resume)
        self.assertIn('href="/apps/my-api/staging"', resume)
        root = c.get('/units/cluster-sources', headers=WHO).text
        self.assertNotIn('<form', root)
        self.assertIn('make flux-bootstrap', root)
        self.assertEqual(c.get('/units/nope', headers=WHO).status_code, 404)

    def test_old_addresses_redirect(self):
        c = client(fake())
        for old, new in (('/units', '/platform#units'), ('/jobs', '/activity')):
            response = c.get(old, headers=WHO, follow_redirects=False)
            self.assertEqual((response.status_code, response.headers['location']), (301, new))


class OverviewTests(unittest.TestCase):
    def test_lists_what_needs_attention_with_links(self):
        text = client(fake(**CHECKS)).get('/', headers=WHO).text
        self.assertIn('things need attention', text)
        self.assertIn('<a href="/apps/my-api/staging">my-api/staging is not Ready', text)
        self.assertIn('my-api/staging is suspended', text)
        self.assertNotIn('app-my-api-staging', text, 'Overview names app instances <app>/<env>')
        self.assertIn('2 app instances', text)
        self.assertIn('more checks need your terminal', text)

    def test_healthy_cluster_says_so(self):
        text = client(fake(units=[unit('infra-base')], **CHECKS)).get('/', headers=WHO).text
        self.assertIn('Everything is healthy', text)
        self.assertNotIn('Needs attention', text)


class PlatformTests(unittest.TestCase):
    def test_runs_only_cluster_checks_and_says_what_it_skipped(self):
        traefik = {'spec': {'template': {'spec': {'containers': [{'args': []}]}}}}
        runner = fake(**{'kubectl -n console get cronjob console-dashboards': CHECKS['kubectl -n console get cronjob console-dashboards'],
                         'kubectl -n console get cronjob console-notifications': CHECKS['kubectl -n console get cronjob console-notifications'],
                         'kubectl get --raw=/version': '{}',
                         **{key: CHECKS[key] for key in ('kubectl -n console get helmrelease console',
                                                         'kubectl -n notify get deployment notify')},
                         'kubectl -n kube-system get deploy traefik': json.dumps(traefik),
                         'kubectl -n flux-system get alerts.notification.toolkit.fluxcd.io': json.dumps({'items': []}),
                         'kubectl -n flux-system get providers.notification.toolkit.fluxcd.io': json.dumps({'items': []})})
        response = client(runner).get('/platform', headers=WHO)
        self.assertEqual(response.status_code, 200)
        self.assertIn('<span class="mark failing" aria-hidden="true"></span> <strong>Ingress</strong> <span class="muted">·</span> '
                      'Traefik does not redirect HTTP to HTTPS', response.text, 'a group of one check is that check, on one line')
        self.assertIn('18 more checks read Secrets, run commands in pods or need your machine', response.text)
        self.assertFalse([c for c in runner.calls if 'secret' in c or 'exec' in c], runner.calls)

    def test_a_unit_is_what_one_read_says_it_is(self):
        """The checks read the units a moment before the units list does: a unit that became Ready in
        between was once shown as failing beside a list that said all healthy."""
        from swhurl.report import Entry
        checks = cluster.Checks(False, [(cluster.FLUX_SECTION, [
            Entry(cluster.FLUX_SECTION, 'bad', 'app-web-prod is not Ready: Reconciliation in progress')])], [])
        ready = cluster.units(fake(units=[unit('app-web-prod')]))
        self.assertEqual(cluster.problems(checks, ready), [])
        failing = cluster.units(fake(units=[unit('app-web-prod', ready='False', message='boom')]))
        self.assertEqual(cluster.problems(cluster.Checks(True, [], []), failing),
                         [cluster.Problem('failing', 'web/prod is not Ready: boom', '/apps/web/prod')])

    def test_an_apps_image_check_names_the_instance_and_times_read_as_how_long_ago(self):
        from swhurl.report import Entry
        group = [Entry('Image Automation', 'ok', 'web-staging: newest image 36-6ec5f63, deployed'),
                 Entry('Image Automation', 'bad', 'ImagePolicy api-staging is not Ready: boom'),
                 Entry('Image Automation', 'warn', 'api-staging: newest image 2-abc, staging has 1-abc in Git (...)')]
        self.assertEqual([(e.level, e.message) for e in cluster.condensed(group)],
                         [('ok', 'web/staging: newest image 36-6ec5f63, deployed'), ('bad', 'ImagePolicy api-staging is not Ready: boom'),
                          ('warn', 'api/staging: newest image 2-abc, staging has 1-abc in Git (...)')])
        now = dt.datetime(2026, 10, 11, 14, 37, 39, tzinfo=dt.UTC)
        for seconds, text in ((30, 'just now'), (600, '10 min ago'), (3 * 3600, '3 h ago'), (5 * 86400, '5 days ago')):
            self.assertEqual(server.ago(now - dt.timedelta(seconds=seconds), now), text)
        self.assertEqual(server.ago('not a time'), 'not a time')
        self.assertEqual([server.ago(day, now) for day in ('2026-10-11', '2026-10-10', '2026-10-01')],
                         ['today', 'yesterday', '10 days ago'], 'a date alone has no hour to count from')

    def test_unreachable_cluster_is_a_502(self):
        runner = FakeRunner().on('kubectl', 'get', '--raw=/version', returncode=1, stderr='refused')
        with redirect_stderr(io.StringIO()):
            self.assertEqual(client(runner).get('/platform', headers=WHO).status_code, 502)


ORIGIN = {**WHO, 'Origin': 'http://testserver'}


class FluxAPI:
    """Answers the console's kubectl patches, annotations and gets like Flux's controllers.

    An annotated object reports the request handled after ``polls`` more gets,
    with its Ready condition set to ``ready``."""

    def __init__(self, runner, ready='True', message='', polls=0, suspended=False):
        self.ready, self.message, self.polls, self.suspended = ready, message, polls, suspended
        self.tokens, self.gets = {}, {}
        runner.on('kubectl', '-n', 'flux-system', 'annotate', handler=self.annotate)
        runner.on('kubectl', '-n', 'flux-system', 'patch', handler=lambda args, _: Result(args, 0, 'patched\n'))
        runner.on('kubectl', '-n', 'flux-system', 'get', handler=self.get)

    def annotate(self, args, _input):
        self.tokens[args[5]] = args[6].split('=', 1)[1]
        return Result(args, 0)

    def get(self, args, _input):
        target = args[4]
        if target.startswith(actions.KUSTOMIZATION) and target not in self.tokens:
            return Result(args, 0, json.dumps({'spec': {'suspend': self.suspended, 'sourceRef': {
                'kind': 'GitRepository', 'name': 'swhurl-platform'}}}))
        self.gets[target] = self.gets.get(target, 0) + 1
        handled = self.gets[target] > self.polls
        status = {'observedGeneration': 3, 'lastAppliedRevision': REV, 'artifact': {'revision': REV},
                  'conditions': [{'type': 'Ready', 'status': self.ready if handled else 'Unknown', 'message': self.message}]}
        if handled:
            status['lastHandledReconcileAt'] = self.tokens[target]
        return Result(args, 0, json.dumps({'metadata': {'generation': 3}, 'status': status}))


def operate(runner, **flux):
    """A client whose jobs run inline against a FluxAPI, with audit lines and sleeps collected."""
    audit, sleeps = [], []
    api = FluxAPI(runner, **flux)
    jobs = actions.Jobs(runner, audit=audit.append, inline=True, sleep=sleeps.append)
    jobs.api, jobs.sleeps = api, sleeps
    return client(runner, jobs=jobs), jobs, audit


def mutations(runner):
    return [call[3:] for call in runner.calls if call[:1] == ('kubectl',) and call[3] in ('annotate', 'patch')]


class ActionTests(unittest.TestCase):
    def test_reconcile_requests_source_then_unit_records_output_and_audits(self):
        runner = fake()
        c, jobs, audit = operate(runner)
        response = c.post('/units/app-web-prod/reconcile', headers=ORIGIN, follow_redirects=False)
        self.assertEqual((response.status_code, response.headers['location']), (303, '/jobs/1'))
        source, unit = 'gitrepositories.source.toolkit.fluxcd.io/swhurl-platform', f'{actions.KUSTOMIZATION}/app-web-prod'
        self.assertEqual([m[:3] for m in mutations(runner)], [('annotate', '--overwrite', source),
                                                             ('annotate', '--overwrite', unit)])
        self.assertTrue(mutations(runner)[0][3].startswith('reconcile.fluxcd.io/requestedAt='))
        self.assertEqual(jobs.get(1).state, 'succeeded', jobs.get(1).lines)
        self.assertEqual(jobs.get(1).lines[-1], f'✔ applied revision {REV}')
        self.assertIn(f'✔ fetched revision {REV}', jobs.get(1).lines)
        self.assertEqual(audit, ['[AUDIT] sam@swhurl.com reconcile app-web-prod: started (job 1)',
                                 '[AUDIT] sam@swhurl.com reconcile app-web-prod: succeeded (job 1)'])
        page = c.get('/jobs/1', headers=WHO).text
        self.assertIn('✔ applied revision', page)
        self.assertNotIn('http-equiv="refresh"', page)
        self.assertIn('Reconcile <a href="/apps/web/prod">web/prod</a>', c.get('/activity', headers=WHO).text,
                      'an app instance is <app>/<env> on every page, not its unit name')
        self.assertFalse([call for call in runner.calls if call[0] == 'flux'])

    def test_suspend_patches_only_and_resume_patches_then_waits(self):
        runner = fake()
        c, jobs, _ = operate(runner, polls=2)
        c.post('/units/infra-base/suspend', headers=ORIGIN)
        self.assertEqual(mutations(runner), [('patch', f'{actions.KUSTOMIZATION}/infra-base', '--type=merge',
                                              '--patch', '{"spec": {"suspend": true}}')])
        self.assertEqual((jobs.get(1).state, jobs.sleeps), ('succeeded', []))
        self.assertEqual(jobs.get(1).lines[0], '► suspending Kustomization infra-base')
        c.post('/units/infra-base/resume', headers=ORIGIN)
        self.assertEqual(mutations(runner)[1][-1], '{"spec": {"suspend": false}}')
        self.assertEqual(mutations(runner)[2][:3], ('annotate', '--overwrite', f'{actions.KUSTOMIZATION}/infra-base'))
        self.assertEqual(jobs.get(2).state, 'succeeded', jobs.get(2).lines)
        self.assertEqual(jobs.get(2).lines[0], '► resuming Kustomization infra-base')
        self.assertEqual(jobs.sleeps, [actions.POLL, actions.POLL], 'polled until the request was handled')

    def test_failure_is_shown_and_audited_as_error(self):
        c, jobs, audit = operate(fake(), ready='False', message='health check failed after 5m')
        c.post('/units/infra-base/resume', headers=ORIGIN)
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertEqual(jobs.get(1).lines[-1],
                         '[ERROR] Kustomization infra-base reconciliation failed: health check failed after 5m')
        self.assertTrue(audit[-1].startswith('[ERROR] sam@swhurl.com resume infra-base: failed'))

    def test_timeout_fails_the_job(self):
        runner = fake()
        FluxAPI(runner, polls=10**6)
        ticks = iter(range(0, 10**4, 60))
        jobs = actions.Jobs(runner, audit=lambda _: None, inline=True, sleep=lambda _: None, clock=lambda: next(ticks))
        jobs.start('resume', 'infra-base', 'sam@swhurl.com')
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertIn('timed out after 10m waiting for Kustomization infra-base', jobs.get(1).lines[-1])

    def test_a_button_in_a_list_gets_its_answer_in_place_instead_of_the_job_page(self):
        runner = fake()
        c, jobs, _ = operate(runner)
        started = c.post('/units/infra-base/reconcile', follow_redirects=False,
                         headers={**ORIGIN, 'HX-Request': 'true', 'HX-Current-URL': 'http://testserver/platform'})
        self.assertEqual(started.status_code, 200)
        self.assertIn('<a class="started" href="/jobs/1" hx-get="/platform" hx-trigger="load delay:10s"', started.text,
                      'and the page it is on is read again, so the row shows what the job did')
        self.assertIn('reconcile started · job #1', started.text)
        refused = c.post('/units/cluster-stack/reconcile', headers={**ORIGIN, 'HX-Request': 'true'})
        self.assertEqual(refused.status_code, 200, 'htmx swaps only a 2xx answer, so the refusal is one')
        self.assertIn('<span class="bad" role="alert">cluster-stack is applied by make flux-bootstrap', refused.text)
        self.assertEqual(c.post('/units/infra-base/reconcile', headers=ORIGIN, follow_redirects=False).status_code, 303)

    def test_reconciling_a_suspended_unit_is_refused(self):
        runner = fake()
        c, jobs, _ = operate(runner, suspended=True)
        c.post('/units/app-my-api-staging/reconcile', headers=ORIGIN)
        self.assertEqual(jobs.get(1).state, 'failed')
        self.assertIn('is suspended; resume it instead', jobs.get(1).lines[-1])
        self.assertEqual(mutations(runner), [])

    def test_refused_unknown_or_busy_actions_run_nothing(self):
        runner = fake()
        c, jobs, audit = operate(runner)
        cases = {'/units/cluster-stack/suspend': 'make flux-bootstrap', '/units/cluster-sources/reconcile': 'make flux-bootstrap',
                 '/units/nope/reconcile': 'no Flux unit', '/units/infra-base/delete': 'unknown action'}
        for path, message in cases.items():
            with self.subTest(path=path):
                response = c.post(path, headers=ORIGIN)
                self.assertEqual(response.status_code, 409)
                self.assertIn(message, response.text)
        busy = actions.Job(99, 'reconcile', 'infra-base', 'x', jobs.now())
        jobs._jobs[99] = busy
        self.assertIn('already running', c.post('/units/infra-base/suspend', headers=ORIGIN).text)
        self.assertEqual(mutations(runner), [])
        self.assertEqual(audit, [])

    def test_posts_must_come_from_a_console_page(self):
        runner = fake()
        c, _, _ = operate(runner)
        for headers in (WHO, {**WHO, 'Origin': 'https://evil.example'}, {**WHO, 'Origin': 'null'}):
            with self.subTest(origin=headers.get('Origin')):
                self.assertEqual(c.post('/units/infra-base/reconcile', headers=headers).status_code, 403)
        self.assertEqual(c.post('/units/infra-base/reconcile', headers={'Origin': 'http://testserver'}).status_code, 401)
        self.assertEqual(runner.calls, [])

    def test_running_job_page_uses_eventsource_and_noscript_refresh(self):
        c, jobs, _ = operate(fake())
        jobs._jobs[7] = actions.Job(7, 'resume', 'infra-base', 'sam@swhurl.com', jobs.now())
        page = c.get('/jobs/7', headers=WHO).text
        self.assertIn("new EventSource('/jobs/7/events?after=0')", page)
        self.assertIn('<noscript><meta http-equiv="refresh" content="2"></noscript>', page)
        self.assertIn('output updates live', page)
        self.assertIn('es.onerror = () => { es.close(); setTimeout(() => location.reload(), 3000); };', page,
                      'a restart cuts the stream: the page is read again from the pod that answers')
        jobs._jobs[7].state, jobs._jobs[7].finished = 'succeeded', jobs.now()
        finished = c.get('/jobs/7', headers=WHO).text
        self.assertNotIn('EventSource', finished)
        self.assertNotIn('http-equiv="refresh"', finished)
        self.assertEqual(c.get('/jobs/8', headers=WHO).status_code, 404)

    def test_job_events_stream_escaped_lines_and_completion(self):
        c, jobs, _ = operate(fake())
        job = actions.Job(7, 'resume', 'infra-base', 'sam@swhurl.com', jobs.now(),
                          lines=['a', '<b>'], state='succeeded', finished=jobs.now(),
                          link='https://github.com/samclement/swhurl-platform/pull/42')
        jobs._jobs[7] = job
        with mock.patch.object(server, 'STREAM_POLL', 0.01):
            response = c.get('/jobs/7/events', headers=WHO)
        self.assertEqual(response.status_code, 200)
        self.assertIn('text/event-stream', response.headers['content-type'])
        self.assertEqual(response.headers['x-accel-buffering'], 'no')
        self.assertIn('id: 1\nevent: line\ndata: {"html": "a", "stage": "resume"}', response.text)
        self.assertIn('id: 2\nevent: line\ndata: {"html": "&lt;b&gt;", "stage": "resume"}', response.text)
        done = response.text.split('event: done\ndata: ', 1)[1].split('\n', 1)[0]
        payload = json.loads(done)
        self.assertEqual(payload['state'], 'succeeded')
        self.assertRegex(payload['finished'], r'^\d{2}:\d{2}:\d{2}$')
        self.assertEqual(payload['link'], 'https://github.com/samclement/swhurl-platform/pull/42')

    def test_job_events_resume_from_header_or_query_and_clamp_positions(self):
        c, jobs, _ = operate(fake())
        jobs._jobs[7] = actions.Job(7, 'resume', 'infra-base', 'sam@swhurl.com', jobs.now(),
                                    lines=['first', 'second'], state='succeeded', finished=jobs.now())
        cases = [('/jobs/7/events?after=1', {}, 'second', 'not first'),
                 ('/jobs/7/events?after=0', {'Last-Event-ID': '1'}, 'second', 'not first'),
                 ('/jobs/7/events?after=1', {'Last-Event-ID': 'x'}, 'first', None),
                 ('/jobs/7/events?after=-5', {}, 'first', None),
                 ('/jobs/7/events?after=99', {}, None, 'first')]
        for path, headers, present, absent in cases:
            with self.subTest(path=path, headers=headers):
                body = c.get(path, headers={**WHO, **headers}).text
                if present:
                    self.assertIn(present, body)
                if absent:
                    self.assertNotIn(absent, body)
        self.assertEqual(c.get('/jobs/99/events', headers=WHO).status_code, 404)
        self.assertEqual(c.get('/jobs/7/events').status_code, 401)

    def test_job_events_shorten_hashes_and_do_not_lose_final_line(self):
        c, jobs, _ = operate(fake())
        job = actions.Job(7, 'resume', 'infra-base', 'sam@swhurl.com', jobs.now(),
                          lines=['commit 1234567890abcdef1234567890abcdef12345678'])
        jobs._jobs[7] = job
        timer = threading.Timer(0.03, lambda: (job.lines.append('last line'), setattr(job, 'state', 'succeeded'),
                                               setattr(job, 'finished', jobs.now())))
        timer.start()
        with mock.patch.object(server, 'STREAM_POLL', 0.005):
            body = c.get('/jobs/7/events', headers=WHO).text
        timer.join()
        self.assertIn('1234567890abcdef1234567890abcdef12345678', body)
        self.assertIn('1234567890ab…', body)
        self.assertIn('last line', body)
        self.assertIn('event: done', body)

    def test_running_job_shows_in_the_header_and_activity(self):
        c, jobs, _ = operate(fake())
        jobs._jobs[7] = actions.Job(7, 'resume', 'infra-base', 'sam@swhurl.com', jobs.now())
        self.assertIn('1 running</span>', c.get('/apps', headers=WHO).text)
        activity = c.get('/activity', headers=WHO).text
        self.assertIn('href="/jobs/7"', activity)
        self.assertIn('<span class="state updating">In progress</span>', activity, 'the state leads the row, as on every list')
        self.assertIn('<details class="openrow change" id="job-7">', activity, 'a job opens to its output without leaving the list')
        self.assertIn('Now: Resume. Ends: Applied.', activity, 'where it will end is said before it gets there')
        self.assertIn('no output yet', activity)
        self.assertIn('just now', activity)
        self.assertIn('href="/units/infra-base">Back to infra-base', c.get('/jobs/7', headers=WHO).text)

    def test_a_change_is_followed_past_its_job_to_where_it_ends(self):
        import httpx
        url, asked = 'https://github.com/samclement/swhurl-platform/pull/42', []

        def handle(request):
            asked.append(request.url.path.rsplit('/', 1)[1])
            if request.url.path.endswith('/pulls'):
                return httpx.Response(200, json=[])
            if '/compare/merged...' in request.url.path:  # what Flux has applied contains the merge, and nothing of theirs since
                return httpx.Response(200, json={'status': 'ahead', 'files': [{'filename': 'docs/x.md'}]})
            return httpx.Response(200, json={'state': 'closed', 'merged_at': dt.datetime.now(dt.UTC).isoformat(), 'merge_commit_sha': 'merged'})

        runner = fake()
        jobs = actions.Jobs(runner, inline=True)
        jobs._jobs[3] = actions.Job(3, 'promote', 'web/prod', 'sam@swhurl.com', jobs.now(), ['Downloading', 'Opened'], 'succeeded',
                                    jobs.now(), url, marks=[('prepare', 0), ('open', 1)])
        jobs._jobs[4] = actions.Job(4, 'remove', 'gone/staging', 'sam@swhurl.com', jobs.now(), [], 'succeeded', jobs.now(), url)
        c = client(runner, jobs=jobs, github=changes.GitHub('samclement/swhurl-platform', 'tok', transport=httpx.MockTransport(handle)))
        activity = c.get('/activity', headers=WHO).text
        self.assertIn('<span class="state healthy">Live in prod</span>', activity, 'the job opened a pull request; the change ended in production')
        self.assertIn('<li class="stage reached"><span class="node"><span class="mark reached" aria-hidden="true"></span>'
                      '<span class="what"><span class="name">Live in prod<span class="sr-only"> (reached)</span>', activity)
        self.assertIn('merged just now', activity)
        self.assertIn('<span class="state healthy">Removed from staging</span>', activity)
        self.assertLess(activity.index('<h2>Finished</h2>'), activity.index('id="job-3"'))
        self.assertEqual(asked.count('pulls'), 1, 'the open pull requests are listed once for the page')
        page = c.get('/jobs/3', headers=WHO).text
        self.assertIn('Live in prod', page)
        self.assertIn('<details class="stage-log" id="stage-open" open>', page)
        self.assertNotIn('hx-trigger="every 30s"', page, 'a change that has ended is not read again')

    def test_dry_run_plans_the_patches_and_does_not_wait(self):
        runner = fake()
        runner.dry_run = True
        c, jobs, _ = operate(runner)
        c.post('/units/infra-base/suspend', headers=ORIGIN)
        c.post('/units/infra-base/resume', headers=ORIGIN)
        self.assertEqual([p[3] for p in runner.planned], ['patch', 'patch', 'annotate'])
        self.assertEqual(mutations(runner), [])
        self.assertEqual((jobs.get(2).state, jobs.sleeps), ('succeeded', []))

if __name__ == '__main__':
    unittest.main()

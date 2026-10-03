"""Lifecycle evidence, incident clocks, durable delivery and restricted runner/RBAC."""
import copy
import datetime as dt
import json
import unittest
from unittest import mock

import httpx
import yaml

from swhurl import ROOT, verify
from swhurl import notifications as n
from swhurl.report import Report
from swhurl.run import CommandError, FakeRunner, Result

BASE = 'https://console.example.test'
NOW = dt.datetime(2026, 10, 3, 12, tzinfo=dt.UTC).timestamp()
PIN = 'sha256:' + 'a' * 64


def healthy(console=False):
    app, env = ('console', '') if console else ('example', 'staging')
    ns = 'console' if console else f'{app}-{env}'
    unit = {'metadata': {'name': 'platform-console' if console else 'app-example-staging'},
            'spec': {'path': './platform/console' if console else './apps/example/staging'},
            'status': {'conditions': [{'type': 'Ready', 'status': 'True'}]}}
    release = {'metadata': {'namespace': ns, 'name': app, 'uid': 'hr1', 'generation': 2},
               'spec': {'values': {'controllers': {'main': {'containers': {'main': {'image': {
                   'repository': 'repo/app', 'tag': '2-abcdef0', 'digest': PIN}}}}}}},
               'status': {'conditions': [{'type': 'Ready', 'status': 'True', 'observedGeneration': 2,
                                          'reason': 'UpgradeSucceeded', 'message': 'upgrade success'}]}}
    labels = {'app.kubernetes.io/instance': app, 'app.kubernetes.io/controller': 'main'}
    workload = {'kind': 'Deployment', 'metadata': {'namespace': ns, 'name': app, 'labels': labels},
                'spec': {'replicas': 1}, 'status': {'readyReplicas': 1}}
    pod = {'metadata': {'name': 'pod1', 'namespace': ns, 'labels': labels},
           'spec': {'containers': [{'name': 'main', 'image': f'repo/app:2-abcdef0@{PIN}'}]},
           'status': {'containerStatuses': [{'name': 'main', 'imageID': f'repo/app@{PIN}', 'ready': True}]}}
    return {'units': [unit], 'releases': [release], 'workloads': [workload], 'pods': [pod], 'events': []}


def event(data, reason='UpgradeSucceeded', when=NOW+1, uid='ev1'):
    release = data['releases'][0]
    return {'metadata': {'uid': uid, 'creationTimestamp': dt.datetime.fromtimestamp(when, dt.UTC).isoformat()},
            'lastTimestamp': dt.datetime.fromtimestamp(when, dt.UTC).isoformat(), 'count': 1,
            'involvedObject': {'kind': 'HelmRelease', **release['metadata']},
            'reportingComponent': 'helm-controller', 'reason': reason, 'message': 'upgrade success'}


def acknowledged(state, now):
    state = copy.deepcopy(state)
    for notice in state['pending']:
        state['seen'][notice['key']] = now + n.HISTORY
    state['pending'] = []
    return state


class LifecycleTests(unittest.TestCase):
    def test_deployment_waits_for_ready_even_if_helm_wait_was_disabled(self):
        data = healthy()
        state = n.evaluate(None, data, NOW, BASE)
        data['events'] = [event(data)]
        data['workloads'][0]['status']['readyReplicas'] = 0
        state = n.evaluate(state, data, NOW+60, BASE)
        self.assertEqual(state['pending'], [])
        data['workloads'][0]['status']['readyReplicas'] = 1
        state = n.evaluate(state, data, NOW+120, BASE)
        self.assertEqual(state['pending'][0]['title'], 'example/staging deployed')

    def test_scale_to_zero_does_not_claim_an_image_was_deployed(self):
        data = healthy()
        state = n.evaluate(None, data, NOW, BASE)
        data['releases'][0]['spec']['values']['controllers']['main']['replicas'] = 0
        data['events'] = [event(data)]
        self.assertEqual(n.evaluate(state, data, NOW+60, BASE)['pending'], [])

    def test_same_pass_upgrade_failure_and_rollback_are_combined(self):
        data = healthy()
        state = n.evaluate(None, data, NOW, BASE)
        data['events'] = [event(data, 'UpgradeFailed', uid='fail'),
                          event(data, 'RollbackSucceeded', when=NOW+2, uid='rollback')]
        state = n.evaluate(state, data, NOW+60, BASE)
        self.assertEqual([x['title'] for x in state['pending']], ['example/staging rolled back'])
        self.assertIn('requested upgrade failed', state['pending'][0]['message'])

    def test_app_template_fixtures_use_the_same_discovery(self):
        data = healthy()
        data['units'][0]['spec']['path'] = './tests/fixtures/apps/apps/example/staging'
        state = n.evaluate(None, data, NOW, BASE)
        self.assertIn('example-staging/example', state['instances'])

    def test_baseline_does_not_replay_history_and_noop_is_silent(self):
        data = healthy()
        data['events'] = [event(data, when=NOW-1)]
        state = n.evaluate(None, data, NOW, BASE)
        self.assertEqual(state['pending'], [])
        self.assertEqual(n.evaluate(state, data, NOW+60, BASE)['pending'], [])

    def test_lifecycle_titles_environments_and_priorities(self):
        for console in (False, True):
            for reason, action in n.SUCCESS.items():
                with self.subTest(console=console, reason=reason):
                    data = healthy(console)
                    state = n.evaluate(None, data, NOW, BASE)
                    data['events'] = [event(data, reason)]
                    state = n.evaluate(state, data, NOW+60, BASE)
                    self.assertEqual(len(state['pending']), 1)
                    notice = state['pending'][0]
                    title = 'console' if console else 'example/staging'
                    self.assertEqual(notice['title'], f'{title} {action}')
                    self.assertEqual(notice['priority'], 4 if action == 'rolled back' else 3)
                    if reason == 'UpgradeSucceeded':
                        self.assertIn(PIN, notice['message'])
                    state = acknowledged(state, NOW+60)
                    self.assertEqual(n.evaluate(state, data, NOW+120, BASE)['pending'], [])

    def test_filters_tests_pushes_other_controllers_unmanaged_and_old_uids(self):
        data = healthy()
        state = n.evaluate(None, data, NOW, BASE)
        for reason in ('TestSucceeded', 'NewArtifact', 'Succeeded', 'HealthCheckFailed'):
            data['events'].append(event(data, reason, uid=reason))
        alien = event(data, uid='alien')
        alien['reportingComponent'] = 'some-other-controller'
        unmanaged = event(data, uid='unmanaged')
        unmanaged['involvedObject']['namespace'] = 'unmanaged'
        old = event(data, uid='old')
        old['involvedObject']['uid'] = 'old-release'
        data['events'] += [alien, unmanaged, old]
        self.assertEqual(n.evaluate(state, data, NOW+60, BASE)['pending'], [])

    def test_uninstall_survives_unit_namespace_and_event_removal_and_keeps_storage_context(self):
        data = healthy()
        data['releases'][0]['spec']['values']['persistence'] = {'data': {'type': 'persistentVolumeClaim'}}
        state = n.evaluate(None, data, NOW, BASE)
        removed = {k: [] for k in data}
        state = n.evaluate(state, removed, NOW+60, BASE)
        self.assertEqual([x['title'] for x in state['pending']], ['example/staging uninstalled'])
        self.assertIn('Storage is retained', state['pending'][0]['message'])
        self.assertEqual(state['pending'][0]['click'], f'{BASE}/activity')
        state = acknowledged(state, NOW+60)
        self.assertEqual(n.evaluate(state, removed, NOW+120, BASE)['pending'], [])

    def test_removal_does_not_announce_while_release_finalizer_is_pending(self):
        data = healthy()
        state = n.evaluate(None, data, NOW, BASE)
        data['units'] = []
        data['releases'][0]['metadata']['deletionTimestamp'] = '2026-10-03T12:00:30Z'
        self.assertEqual(n.evaluate(state, data, NOW+600, BASE)['pending'], [])

    def test_failure_retries_are_grouped_and_not_duplicated_by_health(self):
        data = healthy()
        data['workloads'][0]['status']['readyReplicas'] = 0
        state = n.evaluate(None, data, NOW, BASE)
        data['events'] = [event(data, 'UpgradeFailed')]
        state = n.evaluate(state, data, NOW+60, BASE)
        self.assertEqual([x['title'] for x in state['pending']], ['example/staging deployment failed'])
        state = acknowledged(state, NOW+60)
        data['events'][0]['count'] = 2
        data['events'][0]['lastTimestamp'] = dt.datetime.fromtimestamp(NOW+350, dt.UTC).isoformat()
        self.assertEqual(n.evaluate(state, data, NOW+400, BASE)['pending'], [])


class IncidentTests(unittest.TestCase):
    def test_grace_hourly_reminder_and_two_minute_recovery(self):
        data = healthy()
        data['workloads'][0]['status']['readyReplicas'] = 0
        state = n.evaluate(None, data, NOW, BASE)
        state = n.evaluate(state, data, NOW+299, BASE)
        self.assertEqual(state['pending'], [])
        state = n.evaluate(state, data, NOW+300, BASE)
        self.assertEqual(state['pending'][0]['title'], 'example/staging unhealthy')
        state = acknowledged(state, NOW+300)
        self.assertEqual(n.evaluate(state, data, NOW+3899, BASE)['pending'], [])
        state = n.evaluate(state, data, NOW+3900, BASE)
        self.assertEqual(len(state['pending']), 1)
        state = acknowledged(state, NOW+3900)
        data['workloads'][0]['status']['readyReplicas'] = 1
        state = n.evaluate(state, data, NOW+4000, BASE)
        self.assertEqual(n.evaluate(state, data, NOW+4119, BASE)['pending'], [])
        state = n.evaluate(state, data, NOW+4120, BASE)
        self.assertEqual(state['pending'][0]['title'], 'example/staging recovered')

    def test_console_allowance_and_flapping(self):
        data = healthy(True)
        data['workloads'][0]['status']['readyReplicas'] = 0
        state = n.evaluate(None, data, NOW, BASE)
        self.assertEqual(n.evaluate(state, data, NOW+599, BASE)['pending'], [])
        state = n.evaluate(state, data, NOW+600, BASE)
        self.assertEqual(state['pending'][0]['title'], 'console unhealthy')
        state = acknowledged(state, NOW+600)
        data['workloads'][0]['status']['readyReplicas'] = 1
        state = n.evaluate(state, data, NOW+700, BASE)
        data['workloads'][0]['status']['readyReplicas'] = 0
        state = n.evaluate(state, data, NOW+800, BASE)
        data['workloads'][0]['status']['readyReplicas'] = 1
        state = n.evaluate(state, data, NOW+850, BASE)
        self.assertEqual(n.evaluate(state, data, NOW+969, BASE)['pending'], [])

    def test_stopped_suspended_and_blocked(self):
        data = healthy()
        data['releases'][0]['spec']['values']['controllers']['main']['replicas'] = 0
        data['workloads'][0]['status']['readyReplicas'] = 0
        state = n.evaluate(None, data, NOW, BASE)
        self.assertEqual(n.evaluate(state, data, NOW+1000, BASE)['pending'], [])
        data = healthy()
        data['units'][0]['spec']['suspend'] = True
        data['releases'][0]['status']['conditions'][0]['status'] = 'False'
        state = n.evaluate(None, data, NOW, BASE)
        self.assertEqual(n.evaluate(state, data, NOW+1000, BASE)['pending'], [])
        data['workloads'][0]['status']['readyReplicas'] = 0
        state = n.evaluate(state, data, NOW+1100, BASE)
        self.assertEqual(n.evaluate(state, data, NOW+1400, BASE)['pending'][0]['title'], 'example/staging unhealthy')
        data = healthy()
        data['units'][0]['status']['conditions'][0]['status'] = 'False'
        state = n.evaluate(None, data, NOW, BASE)
        self.assertEqual(n.evaluate(state, data, NOW+300, BASE)['pending'][0]['title'],
                         'example/staging deployment stalled')

    def test_old_healthy_image_stalls_and_git_only_revision_does_not_reset_clock(self):
        data = healthy()
        data['pods'][0]['spec']['containers'][0]['image'] = 'repo/app@sha256:' + 'b'*64
        state = n.evaluate(None, data, NOW, BASE)
        data['units'][0]['status']['lastAppliedRevision'] = 'unrelated-docs-commit'
        state = n.evaluate(state, data, NOW+300, BASE)
        self.assertEqual(state['pending'][0]['title'], 'example/staging deployment stalled')

    def test_completed_rollback_recovers_availability_without_claiming_requested_image_deployed(self):
        data = healthy()
        state = n.evaluate(None, data, NOW, BASE)
        data['events'] = [event(data, 'RollbackSucceeded')]
        data['releases'][0]['status']['conditions'] = [
            {'type': 'Ready', 'status': 'False', 'reason': 'UpgradeFailed'},
            {'type': 'Remediated', 'status': 'True', 'reason': 'RollbackSucceeded'}]
        data['units'][0]['status']['conditions'][0]['status'] = 'False'
        state = acknowledged(n.evaluate(state, data, NOW+60, BASE), NOW+60)
        state = n.evaluate(state, data, NOW+120, BASE)
        state = n.evaluate(state, data, NOW+240, BASE)
        self.assertEqual([x['title'] for x in state['pending']], ['example/staging recovered'])


def fake_runner(state=None, data=None, *, dry_run=False, fails=()):
    data = data or healthy()
    runner = FakeRunner(dry_run=dry_run)
    saved = []
    for prefix in fails:
        runner.on(*prefix, returncode=1, stderr="API down")
    def response(value):
        return lambda args, _input: Result(args, 0, json.dumps(value))
    runner.on('kubectl', '-n', 'console', 'get', 'configmap', handler=response(
        {'data': {'state.json': json.dumps(state)}} if state is not None else {}))
    runner.on('kubectl', '-n', 'flux-system', 'get', handler=response({'items': data['units']}))
    for resource, key in [('helmreleases.helm.toolkit.fluxcd.io', 'releases'),
                          ('deployments,statefulsets,daemonsets', 'workloads'), ('pods', 'pods'), ('events', 'events')]:
        runner.on('kubectl', 'get', resource, handler=response({'items': data[key]}))
    def patch(args, text):
        saved.append(json.loads(json.loads(text)['data']['state.json']))
        return Result(args, 0, '{}')
    runner.on('kubectl', '-n', 'console', 'patch', 'configmap', handler=patch)
    return runner, saved


class DeliveryTests(unittest.TestCase):
    def test_outbox_saved_before_send_and_checkpointed_after_acceptance(self):
        data = healthy()
        state = n.evaluate(None, data, NOW, BASE)
        data['events'] = [event(data)]
        runner, saved = fake_runner(state, data)
        def send(notice):
            self.assertEqual(saved[-1]['pending'][0], notice)
        result = n.check(runner, now=NOW+60, base=BASE, sender=send)
        self.assertEqual(result['pending'], [])
        self.assertEqual(len(saved), 2)
        self.assertFalse([c for c in runner.calls if 'secret' in c or 'exec' in c])

    def test_send_failure_keeps_pending_for_next_run_and_never_acknowledges(self):
        data = healthy()
        state = n.evaluate(None, data, NOW, BASE)
        data['events'] = [event(data)]
        runner, saved = fake_runner(state, data)
        def fail(_notice):
            raise n.NotificationError('delivery failed')
        with self.assertRaises(n.NotificationError):
            n.check(runner, now=NOW+60, base=BASE, sender=fail)
        self.assertEqual(len(saved[-1]['pending']), 1)
        self.assertEqual(saved[-1]['seen'], {})
        runner, saved = fake_runner(saved[-1], data)
        sent = []
        n.check(runner, now=NOW+120, base=BASE, sender=sent.append)
        self.assertEqual(len(sent), 1)
        self.assertEqual(saved[-1]['pending'], [])

    def test_failed_checkpoint_stops_before_network(self):
        runner, _saved = fake_runner(fails=[('kubectl', '-n', 'console', 'patch', 'configmap')])
        sent = []
        with self.assertRaises(CommandError):
            n.check(runner, now=NOW, base=BASE, sender=sent.append)
        self.assertEqual(sent, [])

    def test_dry_run_has_no_posts_or_state_mutations(self):
        runner, saved = fake_runner(dry_run=True)
        sent = []
        n.check(runner, now=NOW, base=BASE, sender=sent.append)
        self.assertEqual(saved, [])
        self.assertEqual(sent, [])
        self.assertFalse([c for c in runner.calls if 'patch' in c])

    def test_unknown_snapshot_does_not_recover_and_monitor_alerts_after_five_minutes(self):
        state = n.evaluate(None, healthy(), NOW, BASE)
        incident = state['instances']['example-staging/example']['incident']
        incident.update(since=NOW-300, notified=True, last_notice=NOW, healthy_since=NOW-100)
        for offset in (1, 301):
            runner, saved = fake_runner(state, fails=[('kubectl', 'get', 'pods')])
            sent = []
            with self.assertRaises(n.NotificationError):
                n.check(runner, now=NOW+offset, base=BASE, sender=sent.append)
            state = saved[-1]
            self.assertNotIn('healthy_since', state['instances']['example-staging/example']['incident'])
        self.assertEqual(sent[0]['title'], 'notification monitor cannot read cluster health')

    def test_corrupt_and_oversize_state_refuse_silent_reset(self):
        runner, _saved = fake_runner({'version': 999})
        with self.assertRaises(n.NotificationError):
            n.read_state(runner)
        state = n.empty_state(NOW)
        state['pending'] = [{'message': 'x' * n.MAX_STATE_BYTES}]
        with self.assertRaises(n.NotificationError):
            n.save_state(runner, state)

    def test_http_errors_do_not_expose_destination_or_response(self):
        url = 'https://ntfy.example/private-secret-topic'
        request = httpx.Request('POST', url)
        response = httpx.Response(401, request=request, text='private-secret-response')
        with mock.patch.object(n.httpx, 'post', return_value=response):
            with self.assertRaises(n.NotificationError) as error:
                n.publish(url, {'title': 'test', 'message': 'test'})
        self.assertNotIn('private-secret', str(error.exception))


class WiringTests(unittest.TestCase):
    def test_state_not_owned_by_flux_and_account_has_only_named_state_writes(self):
        state = yaml.safe_load((ROOT / 'platform/console/notification-state.yaml').read_text())
        self.assertNotIn('data', state)
        self.assertEqual(state['metadata']['annotations']['kustomize.toolkit.fluxcd.io/ssa'], 'Merge')
        docs = list(yaml.safe_load_all((ROOT / 'platform/console/notification-rbac.yaml').read_text()))
        cluster = next(d for d in docs if d['kind'] == 'ClusterRole')
        for rule in cluster['rules']:
            self.assertEqual(rule['verbs'], ['list'])
            self.assertFalse(set(rule['resources']) & {'secrets', 'pods/exec'})
        role = next(d for d in docs if d['kind'] == 'Role')
        self.assertEqual(role['rules'][0]['resourceNames'], ['notification-state'])
        self.assertEqual(set(role['rules'][0]['verbs']), {'get', 'patch'})

    def test_source_alert_does_not_duplicate_app_console_or_pin_notifications(self):
        alerts = [d for d in yaml.safe_load_all((ROOT / 'platform/alerts/alerts.yaml').read_text())
                  if d['kind'] == 'Alert']
        self.assertEqual([d['metadata']['name'] for d in alerts], ['failures'])
        sources = alerts[0]['spec']['eventSources']
        kustomizations = [s for s in sources if s['kind'] == 'Kustomization']
        self.assertEqual(kustomizations, [{'kind': 'Kustomization', 'name': '*',
                         'matchLabels': {'platform.swhurl.com/alert': 'failures'}}])
        for file in ('clusters/home/infra.yaml', 'clusters/home/platform.yaml',
                     'clusters/home/flux-system/kustomizations.yaml'):
            for unit in yaml.safe_load_all((ROOT/file).read_text()):
                labels = unit['metadata'].get('labels', {})
                if unit['metadata']['name'] == 'platform-console':
                    self.assertNotIn('platform.swhurl.com/alert', labels)
                else:
                    self.assertEqual(labels.get('platform.swhurl.com/alert'), 'failures')
        for file in (ROOT/'clusters/home').glob('app-*.yaml'):
            for unit in yaml.safe_load_all(file.read_text()):
                self.assertNotIn('platform.swhurl.com/alert', unit['metadata'].get('labels', {}))
        for path in (ROOT / 'apps').glob('*/prod/kustomization.yaml'):
            self.assertNotIn('production-alert.yaml', yaml.safe_load(path.read_text())['resources'])

    def test_recent_missing_stale_or_suspended_checker(self):
        now = dt.datetime.fromtimestamp(NOW, dt.UTC)
        for age, suspend, expected in ((0, False, 0), (360, False, 1), (None, False, 1), (0, True, 1)):
            job = {'spec': {'suspend': suspend}, 'status': {}}
            if age is not None:
                job['status']['lastSuccessfulTime'] = dt.datetime.fromtimestamp(NOW-age, dt.UTC).isoformat()
            runner = FakeRunner().on('kubectl', handler=lambda args, _input, job=job: Result(args, 0, json.dumps(job)))
            report = Report()
            verify.check_notifications(runner, report, now)
            self.assertEqual(report.exit_code(), expected)

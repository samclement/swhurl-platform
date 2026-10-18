"""verify-platform and check-repo's required Secrets and settings: every branch, offline, against the fake cluster (fakecluster.py)."""
import datetime as dt
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr
from pathlib import Path
from unittest import mock

import yaml
from fakecluster import CONSOLE_TAG, KEY, TOKEN, b64, fresh_backup, healthy

from swhurl import ROOT, clickstack, dashboards, images, platform, recovery, validate, verify
from swhurl.apps import calling, channels, contract
from swhurl.apps import checks as app_checks
from swhurl.notifications import checks as notification_checks
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

OTHER = 'fixture-other-ingestion-key-9999'


def run(runner, **kwargs):
    out, err = io.StringIO(), io.StringIO()
    report = Report(out)
    backups = Path(tempfile.mkdtemp())
    (backups / fresh_backup()).write_bytes(b'x')
    env = {'BACKUP_DIR': str(backups), 'BACKUP_S3_URI': 's3://bucket/clickstack-mongodb/'}
    try:
        with redirect_stderr(err), mock.patch.dict('os.environ', env):
            code = verify.verify_platform(runner, report, **kwargs)
    finally:
        shutil.rmtree(backups)
    return code, report, out.getvalue() + err.getvalue()


class VerifyPlatformTests(unittest.TestCase):
    def assertNoKeys(self, text):
        for value in (KEY, OTHER):
            for form in (value, b64(value), b64(b64(value))):
                self.assertNotIn(form, text)

    def test_healthy_cluster_passes_in_order(self):
        code, report, text = run(healthy())
        self.assertEqual(code, 0, text)
        self.assertEqual(report.lines[:3], ['\n== Flux Kustomizations ==', '[OK] homelab-a', '[OK] homelab-b'])
        self.assertEqual([line for line in report.lines if line.startswith('\n==')],
                         ['\n== Flux Kustomizations ==', '\n== Flux Controllers ==', '\n== Runtime Secrets ==', '\n== Ingestion Key Sync ==',
                          '\n== ClickStack Sign-up ==', '\n== Ingress ==', '\n== Image Automation ==', '\n== App isolation ==', '\n== Call identity ==', '\n== App secrets ==', '\n== Events ==', '\n== Egress proxy ==', '\n== Alerts ==', '\n== Notification channels ==', '\n== Notify service ==', '\n== Retention ==', '\n== Backups ==', '\n== App SQLite backups ==',
                      '\n== Push Webhook ==', '\n== Image Webhooks ==', '\n== App dashboards and alerts ==', '\n== Notifications ==', '\n== Notification heartbeat ==',
                      '\n== Console ==', '\n== Console GitHub Token =='])
        self.assertEqual(report.failures, 0)
        self.assertTrue(text.rstrip().endswith('Validation passed.'))
        self.assertNoKeys(text)

    def test_each_failure_is_reported_and_fails(self):
        cases = {
            'unit not ready': ({'units': {'items': [{'metadata': {'name': 'homelab-x'}, 'status': {'conditions': [
                {'type': 'Ready', 'status': 'False', 'message': 'dependency not ready'}]}}]}},
                'homelab-x is not Ready: dependency not ready'),
            'no units': ({'units': {'items': []}}, 'no Flux kustomizations in flux-system'),
            'units unreadable': ({'units': Result((), 1, '', 'forbidden')}, 'cannot read Flux kustomizations'),
            'secret missing': ({'secret': {'data': {}}}, 'CLICKSTACK_INGESTION_KEY is empty'),
            'secret unreadable': ({'secret': Result((), 1, '', 'NotFound')}, 'CLICKSTACK_INGESTION_KEY is empty'),
            'team key unreadable': ({'team': Result((), 2, '', 'quit(2)')}, 'cannot read a unique ClickStack team'),
            'two team keys': ({'team': Result((), 0, 'RESULT ' + json.dumps({'keys': [KEY, OTHER]}))},
                              'cannot read a unique ClickStack team'),
            'registration open': ({'installation': Result((), 0, 'RESULT ' + json.dumps(
                {'status': 200, 'body': {'isTeamExisting': False}}))}, 'registration is open'),
            'api unreachable': ({'installation': Result((), 1, '', 'no pod')}, 'cannot ask the HyperDX API'),
            'invalid base64': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(KEY) + '!'}}}, 'invalid base64'),
            'different key': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(OTHER)}}}, 'does not match'),
            'double encoded': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(b64(KEY))}}}, 'does not match'),
            'trailing newline': ({'secret': {'data': {'CLICKSTACK_INGESTION_KEY': b64(KEY + '\n')}}}, 'does not match'),
            'no redirect': ({'traefik': {'spec': {'template': {'spec': {'containers': [{'args': []}]}}}}},
                            'does not redirect HTTP to HTTPS'),
            'telemetry ttl drift': ({'ttl30': Result((), 0, '8\t9\n')}, '1 of 9 telemetry tables have no 30-day TTL'),
            'no telemetry tables': ({'ttl30': Result((), 0, '0\t0\n')}, 'have no 30-day TTL'),
            'clickhouse down': ({'ttl30': Result((), 1, '', 'connection refused')}, 'cannot read ClickHouse telemetry'),
            'system logs untimed': ({'untimed': Result((), 0, '3\n')}, '3 ClickHouse system log table(s) have no TTL'),
            'merges failing': ({'merges': Result((), 0, '3765\tsystem.metric_log\n')},
                               '3765 ClickHouse merge(s) failed in the last hour (for example system.metric_log)'),
            'part_log unreadable': ({'merges': Result((), 1, '', 'UNKNOWN_TABLE')}, 'cannot read ClickHouse merge results'),
            'pv delete': ({'pv': {'spec': {'persistentVolumeReclaimPolicy': 'Delete'}}}, 'data volume is not Retain'),
            'pvc missing': ({'pvc': Result((), 1, '', 'NotFound')}, 'data volume is not Retain'),
            'console missing': ({'console': Result((), 1, '', 'NotFound')}, 'cannot read the console HelmRelease'),
            'token placeholder': ({'token': {'data': {'GITHUB_TOKEN': b64('REPLACE_ME')}}}, 'is not set'),
            'token secret missing': ({'token': Result((), 1, '', 'NotFound')}, 'is not set'),
            'token rejected': ({'github': Result((), 0, 'HTTP/2 401\r\n\r\n')}, 'did not accept the console token'),
            'github unreachable': ({'github': Result((), 6, '', 'could not resolve')}, 'did not accept'),
        }
        for label, (overrides, expected) in cases.items():
            with self.subTest(label):
                code, report, text = run(healthy(**overrides))
                self.assertEqual(code, 1, text)
                self.assertTrue(any(line.startswith('[BAD]') and expected in line for line in report.lines), text)
                self.assertNotIn('Validation passed.', text)
                self.assertNoKeys(text)

    def test_mismatch_prints_fix_hint(self):
        _, report, _ = run(healthy(secret={'data': {'CLICKSTACK_INGESTION_KEY': b64(OTHER)}}))
        self.assertTrue(any('make clickstack-bootstrap' in line for line in report.lines))

    def test_team_key_failure_output_never_leaks(self):
        runner = healthy(team=Result((), 1, KEY, f'error near {KEY}'))
        code, _, text = run(runner)
        self.assertEqual(code, 1)
        self.assertNoKeys(text)

    def test_unreachable_cluster_stops_early(self):
        runner = healthy(version=Result((), 1, '', 'connection refused'))
        code, report, text = run(runner)
        self.assertEqual(code, 1)
        self.assertIn('kubectl cannot reach a cluster', text)
        self.assertEqual(runner.calls, [('kubectl', 'get', '--raw=/version')])

    def test_every_call_is_read_only(self):
        """kubectl only gets or execs, and what an exec runs is allowlisted: exec itself can write."""
        runner = healthy()
        run(runner)
        verbs = {c[c.index('get') if 'get' in c else c.index('exec')] for c in runner.calls if c[0] == 'kubectl'}
        self.assertLessEqual(verbs, {'get', 'exec'})
        for args, _ in runner.github:
            self.assertFalse({'--request', '-X', '--data', '--data-binary'} & set(args), f'GitHub call is not a GET: {args}')
        read_only_scripts = (clickstack.TEAM_KEYS,)
        self.assertTrue(runner.stdin, 'verify-platform sends nothing into pods? the allowlist would be untested')
        for args, program in runner.stdin:
            if clickstack.MONGO_POD in args:
                self.assertTrue(program.rstrip('\n').endswith(read_only_scripts), f'unlisted mongosh script: {program}')
            else:
                request = json.loads(program.split(', ', 1)[1].split(');\n', 1)[0])
                self.assertEqual(request['method'], 'GET', f'HyperDX API call is not a GET: {program}')


class ConsoleCheckTests(unittest.TestCase):
    def test_current_image_passes_and_compares_only_image_inputs(self):
        runner = healthy()
        _, report, _ = run(runner)
        self.assertIn('[OK] console image b63d2af is built from the current tooling', report.lines)
        diff = next(c for c in runner.calls if c[0] == 'git')
        self.assertEqual(diff[diff.index('--') + 1:], platform.CONSOLE_IMAGE_INPUTS)

    def test_changed_or_unknown_tooling_warns_without_failing(self):
        for code, expected in ((1, 'the console runs image b63d2af, older than the tooling'), (128, 'cannot compare')):
            with self.subTest(code=code):
                exit_code, report, text = run(healthy(**{'git-diff': Result((), code)}))
                self.assertEqual(exit_code, 0, text)
                self.assertTrue(any(line.startswith('[WARN]') and expected in line for line in report.lines), text)

    def test_a_missing_job_record_fails(self):
        exit_code, report, text = run(healthy(**{'console-jobs': Result((), 1, '', 'NotFound')}))
        self.assertEqual(exit_code, 1, text)
        self.assertIn('[BAD] console/console-jobs is missing: the console forgets its jobs on restart '
                      '(make reconcile UNIT=platform-console)', report.lines)

    def test_publish_workflow_hashes_the_same_inputs_in_the_same_order(self):
        workflow = (ROOT / '.github/workflows/validate.yml').read_text()
        listed = workflow.split('paths=(', 1)[1].split(')', 1)[0].split()
        self.assertEqual(tuple(listed), platform.CONSOLE_IMAGE_INPUTS, 'the order changes the hash')

    def test_content_tag_is_compared_exactly(self):
        for current, expected in (('src-abc', '[OK] console image src-abc is built from the current tooling'),
                                  ('src-new', '[WARN] the console runs image src-abc, older than the tooling')):
            with self.subTest(current=current):
                runner = healthy(console={'spec': {'values': {'controllers': {'main': {'containers': {'main': {
                    'image': {'tag': 'src-abc'}}}}}}}})
                with mock.patch.object(images, 'content_tag', return_value=current):
                    _, report, _ = run(runner)
                self.assertTrue(any(line.startswith(expected) for line in report.lines), report.lines)


class ConsoleTokenTests(unittest.TestCase):
    def test_expiry_soon_warns_and_token_never_leaks(self):
        soon = (dt.datetime.now(dt.UTC) + dt.timedelta(days=5)).strftime('%Y-%m-%d %H:%M:%S UTC')
        runner = healthy(github=Result((), 0, f'HTTP/2 200\r\ngithub-authentication-token-expiration: {soon}\r\n'))
        code, report, text = run(runner)
        self.assertEqual(code, 0, text)
        self.assertTrue(any(line.startswith('[WARN]') and 'expires' in line for line in report.lines), text)
        self.assertNotIn(TOKEN, text)
        ((args, stdin),) = runner.github
        self.assertNotIn(TOKEN, ' '.join(args))
        self.assertEqual(stdin, f'header = "Authorization: Bearer {TOKEN}"\n')

    def test_flux_controllers_without_git_settings_warn(self):
        plain = {'metadata': {'labels': {'app.kubernetes.io/version': 'v0.0.1'}},
                 'spec': {'template': {'spec': {'containers': [{'args': ['--log-level=info']}]}}}}
        code, report, text = run(healthy(**{'kustomize-controller': plain}))
        self.assertEqual(code, 0, text)
        warning = next(line for line in report.lines if line.startswith('[WARN] kustomize-controller'))
        self.assertIn('is v0.0.1 with 0/1 Git settings', warning)
        self.assertIn('make flux-install', warning)

    def test_unreadable_flux_controller_fails(self):
        code, report, text = run(healthy(**{'kustomize-controller': Result((), 0, '')}))
        self.assertEqual(code, 1, text)
        self.assertIn('[BAD] cannot read flux-system/kustomize-controller (kubectl -n flux-system get deploy kustomize-controller)', report.lines)

    def test_image_automation_problems(self):
        from fakecluster import policy, staging_tag
        tag = staging_tag('demo-shop')
        cases = {
            'policy': ({'policies': {'items': [policy('demo-shop', tag, {'conditions': [
                {'type': 'Ready', 'status': 'False', 'message': 'no tags match'}]})]}},
                       '[BAD] ImagePolicy demo-shop-staging is not Ready, so staging gets no new images: no tags match'),
            'behind': ({'policies': {'items': [policy('demo-shop', '999-abcdef0')]}},
                       f'[WARN] demo-shop-staging: newest image 999-abcdef0 is not deployed; staging in Git runs {tag}'),
        }
        for name, (overrides, expected) in cases.items():
            with self.subTest(name=name):
                _, report, text = run(healthy(**overrides))
                self.assertTrue(any(line.startswith(expected) for line in report.lines), text)
        _, report, _ = run(healthy())
        self.assertIn(f'[OK] demo-shop-staging: newest image {tag}, deployed', report.lines)

    def test_isolation_problems(self):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root)
        (root / 'apps/w/staging').mkdir(parents=True)
        (root / 'apps/w/staging/helmrelease.yaml').write_text('')

        def isolation(**overrides):
            report = Report(io.StringIO())
            app_checks.check_isolation(healthy(**overrides), report, root)
            return report.lines
        present = {'items': [{'metadata': {'name': 'w-staging', 'labels': {
            'platform.swhurl.com/app': 'w', contract.TEAM: 'demo'}}}]}
        good = {'app-namespaces': present,
                'quotas': {'items': [{'metadata': {'namespace': 'w-staging'}, 'spec': {'hard': contract.QUOTA}}]},
                'network-policies': {'items': [{'metadata': {'namespace': 'w-staging'}, 'spec': {
                    'podSelector': {'matchLabels': {'app.kubernetes.io/instance': 'w'}}}}]}}
        self.assertIn('[OK] 1 app namespace(s) have a registered team, the platform quota and a NetworkPolicy',
                      isolation(**good))
        namespace = {'items': [{'metadata': {'name': 'w-staging', 'labels': {'platform.swhurl.com/app': 'w'}}},
                               # kept for its data after its app was removed: not an instance, not checked
                               {'metadata': {'name': 'gone-prod', 'labels': {'platform.swhurl.com/app': 'gone'}}}]}
        every_pod = {'items': [{'metadata': {'namespace': 'w-staging'}, 'spec': {'podSelector': {}}}]}
        lines = isolation(**{'app-namespaces': namespace, 'quotas': {'items': []}, 'network-policies': every_pod})
        self.assertEqual(lines[1:], ["[BAD] w-staging: team None is not in apps/teams.yaml (make app-team APP=w ARGS=\"--team <team>\"); "
                                     "no platform ResourceQuota; no NetworkPolicy on the app's pods "
                                     "(make check-apps, then make app-reconcile APP=w ENV=staging)"])
        self.assertTrue(any(line.startswith('[BAD] w-staging: the namespace does not exist, so the instance is not running')
                            for line in isolation(**{'app-namespaces': {'items': []}})))
        self.assertIn('[BAD] cannot read app namespaces, ResourceQuotas or NetworkPolicies, so app isolation is unchecked '
                      '(kubectl get namespaces -l platform.swhurl.com/app)',
                      isolation(**{**good, 'quotas': Result((), 1, '', 'forbidden')}))

    def test_signing_key_problems(self):
        _, report, text = run(healthy())
        self.assertIn("[OK] apps/signing-keys.json holds the cluster's 1 signing key(s)", report.lines, text)
        self.assertIn(f'[OK] token issuer is {calling.ISSUER}', report.lines, text)
        code, report, text = run(healthy(jwks={'keys': [{'kid': 'rotated'}]}))
        self.assertEqual(code, 1, text)
        self.assertTrue(any(line.startswith("[BAD] apps/signing-keys.json is not the cluster's signing key set")
                            and 'make platform-signing-keys' in line for line in report.lines), text)
        code, report, text = run(healthy(openid={'issuer': 'https://elsewhere'}))
        self.assertEqual(code, 1, text)
        self.assertTrue(any(line.startswith('[BAD] token issuer is https://elsewhere') for line in report.lines), text)
        _, report, text = run(healthy(jwks=Result((), 1, '', 'forbidden')))
        self.assertIn("[BAD] cannot read the cluster's token issuer or signing keys (kubectl get --raw /openid/v1/jwks)", report.lines, text)

    def test_event_problems(self):
        _, report, text = run(healthy())
        self.assertIn('[OK] NATS is running', report.lines, text)
        self.assertIn('[OK] 1 declared stream(s) and consumer(s) exist in NATS', report.lines, text)
        code, report, text = run(healthy(nats={'status': {}}))
        self.assertEqual(code, 1, text)
        self.assertTrue(any(line.startswith('[BAD] NATS is not running') for line in report.lines), text)
        pending = {'items': [{'kind': 'Consumer', 'metadata': {'namespace': 'w-staging', 'name': 'orders-created'},
                              'status': {'conditions': [{'type': 'Ready', 'status': 'False', 'message': 'stream not found'}]}}]}
        code, report, text = run(healthy(streams=pending))
        self.assertEqual(code, 1, text)
        self.assertIn('[BAD] Consumer w-staging/orders-created is not Ready, so its events are not kept or delivered: stream not found '
                      '(kubectl -n w-staging describe consumer orders-created)', report.lines, text)
        _, report, text = run(healthy(streams=Result((), 1, '', 'no such resource')))
        self.assertIn('[BAD] cannot read NATS, its controller, or the streams and consumers (make reconcile UNIT=platform-nats)', report.lines, text)

    def test_alert_problems(self):
        _, report, text = run(healthy(alerts={'items': []}))
        self.assertIn('[WARN] alert failures is missing: no push notifications for it (make reconcile UNIT=platform-alerts)',
                      report.lines, text)
        _, report, text = run(healthy(providers={'items': []}))
        self.assertIn('[BAD] alert failures points at provider ntfy-platform, which does not exist, so its notifications are lost '
                      '(make reconcile UNIT=platform-alerts)', report.lines, text)

    def test_channel_problems(self):
        _, report, text = run(healthy())
        self.assertIn(f'[OK] {len(channels.team_channels(ROOT))} team(s) notify a channel, as in Git', report.lines, text)
        _, report, text = run(healthy(routes={'items': [{'metadata': {'name': channels.ROUTES_NAME}, 'data': {
            channels.ROUTES_KEY: json.dumps({'teams': {'ghosts': ['nowhere']}})}}]}))
        self.assertIn('[BAD] the team routes in the cluster are not those of apps/teams.yaml: make app-sync, commit, push '
                      'and make flux-reconcile', report.lines, text)
        _, report, text = run(healthy(routes={'items': []}))
        self.assertTrue(any('team routes in the cluster are not' in line for line in report.lines), text)
        _, report, text = run(healthy(routes=Result((), 1, '', 'forbidden')))
        self.assertIn('[BAD] could not read the notification routes and channels (make reconcile UNIT=platform-notify)',
                      report.lines, text)
        cluster = healthy()  # no channel Secret in the cluster
        with mock.patch.object(channels, 'channels', return_value={'payments-phone': 'ntfy'}):
            _, report, text = run(cluster)
        self.assertIn('[BAD] channel payments-phone has no Secret in the cluster: its teams get no notifications '
                      '(make reconcile UNIT=platform-notify)', report.lines, text)

    def test_a_team_that_owns_apps_and_has_no_channel_is_named(self):
        _, report, text = run(healthy())
        owned = {}
        for path in (ROOT / 'apps').glob('*/*/namespace.yaml'):
            owned.setdefault(yaml.safe_load(path.read_text())['metadata']['labels'][contract.TEAM], set()).add(path.parent.parent.name)
        routes = channels.team_channels(ROOT)
        for team, apps in owned.items():
            line = (f'[WARN] team {team} owns {len(apps)} app(s) and has no notification channel: their alerts and messages '
                    f'reach nobody (make team-channel NAME={team})')
            (self.assertNotIn if routes.get(team) else self.assertIn)(line, report.lines, text)
        cluster = healthy()
        with mock.patch.object(channels, 'team_channels', return_value={team: [team] for team in owned}):
            _, report, text = run(cluster)
        self.assertFalse([line for line in report.lines if 'has no notification channel' in line], text)

    def test_notify_service_problems(self):
        _, report, text = run(healthy())
        self.assertIn(f'[OK] the notify service is running {CONSOLE_TAG}, as the console', report.lines, text)
        stopped = {'spec': {'template': {'spec': {'containers': [{'image': f'x:{CONSOLE_TAG}'}]}}}, 'status': {}}
        _, report, text = run(healthy(notify=stopped))
        self.assertTrue(any(line.startswith('[BAD] the notify service is not running') for line in report.lines), text)
        other = {'spec': {'template': {'spec': {'containers': [{'image': 'x:src-old@sha256:abc'}]}}}, 'status': {'readyReplicas': 1}}
        _, report, text = run(healthy(notify=other))
        self.assertIn(f"[WARN] the notify service runs src-old, not the console's {CONSOLE_TAG}: make console-image pins both, "
                      'then commit and push', report.lines, text)
        _, report, text = run(healthy(notify=Result((), 1, '', 'NotFound')))
        self.assertTrue(any(line.startswith('[BAD] cannot read the notify service (notify/notify)') for line in report.lines), text)

    def test_image_webhook_problems(self):
        url = {'url': f'https://flux-webhook.{platform.base_domain()}/hook/abc'}
        events = ['package', 'registry_package']
        _, report, text = run(healthy())
        self.assertIn('[OK] demo-shop: image webhook on samclement/demo-shop, latest delivery returned 200', report.lines, text)
        cases = {
            'receiver': ({'receiver': {'status': {'conditions': [{'type': 'Ready', 'status': 'False', 'message': 'no secret'}]}}},
                         '[BAD] Flux receiver flux-system/app-images is not Ready: no secret'),
            'no hook': ({'hooks': []}, '[WARN] demo-shop: no image webhook on samclement/demo-shop; Flux finds its new images '
                                       'within the hour (make app-hooks)'),
            'unused': ({'hooks': [{'active': True, 'events': events, 'config': url, 'last_response': {'code': None}}]},
                       '[OK] demo-shop: image webhook on samclement/demo-shop, no delivery yet'),
            'failing': ({'hooks': [{'active': True, 'events': events, 'config': url, 'last_response': {'code': 404}}]},
                        '[WARN] demo-shop: the latest image webhook delivery from samclement/demo-shop returned 404'),
            'events': ({'hooks': [{'active': True, 'events': ['push'], 'config': url}]}, 'is disabled or lacks its events'),
        }
        for name, (overrides, expected) in cases.items():
            with self.subTest(name=name):
                _, report, text = run(healthy(**overrides))
                self.assertTrue(any(expected in line for line in report.lines), text)

    def test_push_webhook_problems(self):
        url = {'url': f'https://flux-webhook.{platform.base_domain()}/hook/abc'}
        cases = {
            'receiver': ({'receiver': {'status': {'conditions': [{'type': 'Ready', 'status': 'False', 'message': 'no secret'}]}}},
                         '[BAD] Flux receiver flux-system/github is not Ready: no secret'),
            'no hook': ({'hooks': []}, '[WARN] no GitHub webhook on'),
            'failing': ({'hooks': [{'active': True, 'config': url, 'last_response': {'code': 502, 'message': 'Bad Gateway'}}]},
                        "[WARN] GitHub's latest delivery to flux-webhook."),
            'disabled': ({'hooks': [{'active': False, 'config': url, 'last_response': {'code': 200}}]}, 'is disabled'),
            'gh fails': ({'hooks': Result((), 1, '', 'gh: not logged in')}, '[WARN] could not list the GitHub webhooks'),
        }
        for name, (overrides, expected) in cases.items():
            with self.subTest(name=name):
                _, report, text = run(healthy(**overrides))
                self.assertTrue(any(expected in line for line in report.lines), text)

    def test_the_repository_token_is_optional_and_checked_when_set(self):
        _, report, _ = run(healthy())
        self.assertTrue(any(line.startswith('[WARN] console/console-github.APP_REPOS_TOKEN is not set') for line in report.lines))
        code, report, text = run(healthy(token={'data': {'GITHUB_TOKEN': b64(TOKEN), 'APP_REPOS_TOKEN': b64('github_pat_two')}}))
        self.assertEqual(code, 0, text)
        self.assertTrue(any('GitHub accepts the console repository token; it expires 2099-01-01' in line for line in report.lines))
        self.assertNotIn('github_pat_two', text)

    def test_no_expiry_passes(self):
        _, report, _ = run(healthy(github=Result((), 0, 'HTTP/2 200\r\n\r\n')))
        self.assertIn('[OK] GitHub accepts the console token (no expiry date)', report.lines)


class AllowedChecksTests(unittest.TestCase):
    def test_cluster_only_reads_no_secrets_execs_nothing_and_names_what_it_skipped(self):
        runner = healthy()
        code, report, text = run(runner, allowed=frozenset({'cluster'}))
        self.assertEqual(code, 0, text)
        self.assertEqual([e.section for e in report.entries if e.level != 'info'],
                         ['Flux Kustomizations'] * 2 + ['Ingress'] + ['Alerts', 'Notify service'] + ['App dashboards and alerts', 'Notifications'])
        self.assertFalse([c for c in runner.calls if 'secret' in c or 'exec' in c or c[0] != 'kubectl'], runner.calls)
        self.assertIn('[INFO] skipped (need more than cluster): flux-controllers, ingestion-key, registration, image-automation, isolation, signing-keys, secrets, events, egress, channels, retention, backups, sqlite-backups, push-webhook, image-webhooks, notification-heartbeat, console, console-token',
                      report.lines)

    def test_every_check_names_only_known_needs(self):
        self.assertEqual(verify.NEEDS, {'cluster', 'secret', 'exec', 'host'})
        self.assertEqual(len({check.name for check in verify.CHECKS}), len(verify.CHECKS))

    def test_the_operations_page_lists_the_checks_as_the_registry_does(self):
        page = (ROOT / 'docs/operations.md').read_text()
        self.assertIn(verify.table(), page, 'docs/operations.md: replace the table under "Check health" with:\n' + verify.table())

    def test_the_command_loads_with_the_system_interpreter(self):
        """make runs it without uv: no check's module may need httpx or ruamel to load."""
        code = "import sys; sys.modules['httpx'] = sys.modules['ruamel'] = None; import swhurl.verify, swhurl.validate"
        loaded = subprocess.run([sys.executable, '-c', code], env=os.environ | {'PYTHONPATH': str(ROOT / 'tools')},
                                capture_output=True, text=True)
        self.assertEqual(loaded.returncode, 0, loaded.stderr)

    def test_entries_carry_section_and_level(self):
        _, report, _ = run(healthy(traefik={'spec': {'template': {'spec': {'containers': [{'args': []}]}}}}))
        bad = [e for e in report.entries if e.level == 'bad']
        self.assertEqual([(e.section, e.message[:28]) for e in bad], [('Ingress', 'Traefik does not redirect HT')])


class BackupAgeTests(unittest.TestCase):
    NOW = dt.datetime(2026, 9, 29, 12, 0, tzinfo=dt.UTC)

    def check(self, local=(), remote=(), uri='s3://bucket/p/', remote_rc=0, max_hours=None):
        folder = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, folder)
        for name in local:
            (folder / name).write_bytes(b'x')
        runner = FakeRunner().on('aws', 's3api', 'list-objects-v2', returncode=remote_rc, stderr='AccessDenied',
                                 stdout=json.dumps([f'p/{n}' for n in remote]))
        env = {'BACKUP_DIR': str(folder), 'BACKUP_S3_URI': uri, **({'BACKUP_MAX_AGE_HOURS': max_hours} if max_hours else {})}
        report = Report(io.StringIO())
        recovery.check_backups(runner, report, env, now=self.NOW)
        return report, runner

    def test_fresh_backups_pass(self):
        name = 'clickstack-mongodb-20260929T033000Z'
        report, _ = self.check(remote=[f'{name}.archive.gz.age', f'{name}.json'])
        self.assertEqual(report.failures, 0, report.lines)

    def test_stale_missing_incomplete_or_unlistable_backups_fail(self):
        old, new = 'clickstack-mongodb-20260928T030000Z', 'clickstack-mongodb-20260929T033000Z'
        cases = {
            'stale': ({'remote': [f'{old}.archive.gz.age', f'{old}.json']}, 's3://bucket/p/ is 33 h old'),
            'none': ({'remote': []}, 'no MongoDB backup in s3://bucket/p/'),
            'archive without its metadata': ({'remote': [f'{new}.archive.gz.age', f'{old}.archive.gz.age', f'{old}.json']},
                                             'is 33 h old'),
            'local only': ({'local': [f'{new}.archive.gz.age', f'{new}.json'], 'remote': []}, 'no MongoDB backup in s3'),
            'unlistable': ({'remote_rc': 255}, 'cannot list backups in s3://bucket/p/'),
            'tighter limit': ({'remote': [f'{new}.archive.gz.age', f'{new}.json'], 'max_hours': '6'}, 'is 8 h old'),
            'no location': ({'uri': ''}, 'no S3 location'),
        }
        for label, (kwargs, message) in cases.items():
            with self.subTest(label):
                report, _ = self.check(**kwargs)
                self.assertTrue(any(line.startswith('[BAD]') and message in line for line in report.lines), report.lines)

    def test_expected_sqlite_databases_come_from_the_releases_in_git(self):
        from swhurl import sqlite_backup
        names = [each.namespace for each in sqlite_backup.expected()]
        self.assertIn('demo-orders-prod', names)
        self.assertNotIn('demo-shop-prod', names)
        runner = FakeRunner().on('aws', 's3api', 'list-objects-v2', stdout='[]')
        report = Report(io.StringIO())
        sqlite_backup.check_sqlite_backups(runner, report, {}, now=self.NOW)
        self.assertEqual(report.failures, len(names), 'no backup in S3: each expected database fails')
        self.assertTrue(all(call[:3] == ('aws', 's3api', 'list-objects-v2') for call in runner.calls), 'reads no cluster')


class VerifyConfigTests(unittest.TestCase):
    def make_root(self, *, secret=True, settings='  BASE_DOMAIN: example.test\n  CERT_ISSUER: letsencrypt-prod\n'):
        root = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, root)
        (root / 'clusters/home/flux-system/sources').mkdir(parents=True)
        (root / 'clusters/home/flux-system/kustomizations.yaml').write_text('')
        (root / 'clusters/home/platform.yaml').write_text(
            'apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata: {name: homelab-svc}\n'
            'spec: {path: ./svc, decryption: {provider: sops}}\n')
        (root / 'svc').mkdir()
        if secret:
            (root / 'svc/secret.sops.yaml').write_text('kind: Secret\n')
        (root / platform.SETTINGS).write_text(
            'apiVersion: v1\nkind: ConfigMap\nmetadata: {name: platform-settings}\ndata:\n' + settings)
        return root

    def run_config(self, root=None):
        out = io.StringIO()
        try:
            validate.check_config(Report(out), **({'root': root} if root else {}))
        except validate.ValidationError as error:
            return 1, str(error)
        return 0, out.getvalue()

    def test_passes_on_this_repo(self):
        self.assertEqual(self.run_config()[0], 0)

    def test_passes_on_a_minimal_repo(self):
        self.assertEqual(self.run_config(self.make_root())[0], 0)

    def test_required_secrets_come_from_decrypting_units(self):
        code, out = self.run_config(self.make_root(secret=False))
        self.assertEqual(code, 1)
        self.assertIn('homelab-svc decrypts SOPS but its path has no *.sops.yaml Secret', out)

    def test_missing_settings_fail(self):
        code, out = self.run_config(self.make_root(settings='  CERT_ISSUER: letsencrypt-prod\n'))
        self.assertEqual(code, 1)
        self.assertIn('BASE_DOMAIN missing', out)


if __name__ == '__main__':
    unittest.main()


class DashboardHealthTests(unittest.TestCase):
    def test_recent_stale_missing_and_suspended(self):
        now = dt.datetime(2026, 10, 2, 20, tzinfo=dt.UTC)
        for spec, taken, expected in (({}, now, 0), ({}, now - dt.timedelta(minutes=6), 1),
                                      ({}, None, 1), ({'suspend': True}, now, 1)):
            with self.subTest(spec=spec, taken=taken):
                job = {'spec': spec, 'status': {'lastSuccessfulTime': taken.isoformat()} if taken else {}}
                out = io.StringIO()
                report = Report(out)
                dashboards.check_dashboards(healthy(dashboards=job), report, now)
                self.assertEqual(report.exit_code(), expected, out.getvalue())


class NotificationHeartbeatHostVerifyTests(unittest.TestCase):
    def verify(self, *, age=60, timer='active', result='success', status='0', timestamp=True):
        now = dt.datetime.now(dt.UTC)
        exit_time = (dt.datetime.fromtimestamp(now.timestamp()-age).astimezone().strftime('%a %Y-%m-%d %H:%M:%S %Z')
                     if timestamp else '')
        details = (f'Result={result}\nExecMainStatus={status}\n'
                   f'ExecMainExitTimestamp={exit_time}\n')
        runner = FakeRunner().on('systemctl', 'is-active', stdout=timer+'\n').on(
            'systemctl', 'show', stdout=details)
        out = io.StringIO()
        report = Report(out)
        notification_checks.check_notification_heartbeat(runner, report, now)
        return report.exit_code(), out.getvalue()

    def test_active_timer_and_recent_success_pass(self):
        code, output = self.verify()
        self.assertEqual(code, 0, output)
        self.assertIn('succeeded within 15 minutes', output)

    def test_inactive_timer_failed_run_missing_timestamp_and_stale_run_fail(self):
        for options in ({'timer': 'inactive'}, {'result': 'exit-code'}, {'status': '1'},
                        {'timestamp': False}, {'age': 16*60}):
            with self.subTest(options=options):
                code, output = self.verify(**options)
                self.assertEqual(code, 1, output)

    def test_check_is_registered_as_host_scoped(self):
        check = next(c for c in verify.CHECKS if c.name == 'notification-heartbeat')
        self.assertEqual(check.needs, frozenset({'host'}))

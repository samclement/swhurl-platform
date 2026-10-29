"""Git semantics come from records even when rendered output disagrees or has not arrived."""
import io
import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from dataclasses import replace
from pathlib import Path
from unittest import mock

from swhurl import ROOT, dashboards, sqlite_backup, yamlio
from swhurl.apps import channels, checks, cli, edit, egress, events, hooks, teams
from swhurl.apps.instance import Instance
from swhurl.apps.model import generated, store
from swhurl.apps.model.records import Image
from swhurl.notifications import checks as notification_checks
from swhurl.report import Report
from swhurl.run import FakeRunner


class RecordReaderTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name)
        shutil.copytree(ROOT / 'tests/fixtures/instance/apps', self.root / 'apps')
        (self.root / events.CONTROLLER_KEY_FILE).parent.mkdir(parents=True)
        (self.root / events.CONTROLLER_KEY_FILE).write_text('UCONTROLLER')
        (self.root / events.CONSOLE_KEY_FILE).write_text('UCONSOLE')
        world, errors = store.world(self.root)
        self.assertEqual(errors, [])
        for each, entry in world.items():
            world[each] = replace(entry, settings=replace(entry.settings, allocations=replace(
                entry.settings.allocations, events_key=f'U{each.env.upper()}', egress_port=30101 if each.env == 'staging' else 30102)))
            (each.folder(self.root) / events.SECRET_FILE).write_text('sops: {}')
        for path, text in store.documents(world).items():
            (self.root / path).write_text(text)
        world, _ = store.world(self.root)

    def break_rendered(self):
        (self.root / 'apps/hello/staging/helmrelease.yaml').write_text('invalid: [')
        (self.root / 'apps/hello/prod/helmrelease.yaml').unlink()

    def test_shared_checks_use_records_and_preserve_user_and_port_order(self):
        self.break_rendered()
        self.assertEqual(egress.problems(self.root), [])
        # The NATS user list is v2's output, so a changed key is not stale here: make check-v2 compares the
        # committed file with what the records render.
        settings = self.root / 'apps/hello/staging/instance.yaml'
        doc = yamlio.load(settings.read_text())
        doc['allocations']['eventsKey'] = 'UCHANGED'
        settings.write_text(yamlio.dump(doc))
        doc['egress'] = ['api.example.com:443']
        settings.write_text(yamlio.dump(doc))
        self.assertEqual(egress.problems(self.root), [])

    def test_invalid_world_becomes_existing_policy_report_and_cli_errors(self):
        (self.root / 'apps/hello/prod/instance.yaml').write_text('invalid: [')
        for problems in (generated.problems(self.root), egress.problems(self.root)):
            self.assertTrue(any('cannot read its records' in line for line in problems), problems)
        runner = (FakeRunner().on('kubectl', '-n', 'egress', 'get', stdout=json.dumps({'spec': {}, 'status': {'readyReplicas': 1}}))
                  .on('kubectl', '-n', 'messaging', 'get', stdout=json.dumps({'status': {'readyReplicas': 1}}))
                  .on('kubectl', 'get', stdout=json.dumps({'items': []}))
                  .on('kubectl', '-n', 'flux-system', 'get', stdout=json.dumps({'items': []})))
        for check in (checks.check_events, checks.check_egress, checks.check_secrets, checks.check_image_automation, checks.check_image_webhooks):
            report = Report(io.StringIO())
            with mock.patch.object(hooks, 'webhook_host', return_value='flux.example.test'):
                check(runner, report, self.root)
            self.assertTrue(any('cannot read its records' in line for line in report.lines), report.lines)
        with redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as err:
            self.assertEqual(cli.main_offers(['--all', 'staging', '--root', str(self.root)]), 2)
            self.assertEqual(cli.main_promote(['hello', '--root', str(self.root)], runner), 2)
            self.assertEqual(cli.main_deploy(['--root', str(self.root)]), 2)
            self.assertEqual(teams.main_remove(['demo', '--root', str(self.root)]), 2)
        self.assertIn('cannot read its records', err.getvalue())
        with mock.patch.object(hooks, 'ROOT', self.root), mock.patch.object(hooks, 'auto_deploy_repositories',
                side_effect=hooks.HookError('cannot read its records')), redirect_stderr(io.StringIO()):
            self.assertEqual(hooks.main([], FakeRunner()), 1)
        with mock.patch.object(dashboards, 'apps', side_effect=ValueError('cannot read its records')):
            report = Report(io.StringIO())
            self.assertEqual(dashboards.main([], FakeRunner(), report), 1)
            self.assertIn('[BAD] cannot read its records', report.lines)

    def test_events_edit_uses_subscription_records_before_render_guard(self):
        settings = self.root / 'apps/hello/staging/instance.yaml'
        doc = yamlio.load(settings.read_text())
        doc['subscriptions'] = ['orders.created']
        settings.write_text(yamlio.dump(doc))
        self.break_rendered()
        with self.assertRaisesRegex(edit.EditError, 'differ from what the platform renders'):
            edit.set_events(self.root, 'hello', 'staging', remove_subscribe=['orders.created'])
        with self.assertRaisesRegex(edit.EditError, 'already there: orders.created'):
            edit.set_events(self.root, 'hello', 'staging', add_subscribe=['orders.created'])

    def test_offers_all_and_latest_image_check_use_image_records(self):
        record = self.root / 'apps/hello/staging/release.yaml'
        world, _ = store.world(self.root)
        entry = world[Instance('hello', 'staging')]
        record.write_text(store.text(replace(entry.release, image=Image('ghcr.io/example/hello', '7-abcdef0', entry.release.image.digest)).document()))
        self.break_rendered()
        with mock.patch.object(cli, 'set_offers') as offered:
            self.assertEqual(cli.main_offers(['--all', 'staging', '--root', str(self.root)]), 0)
        self.assertEqual(offered.call_args.args[:3], (self.root, 'hello', 'staging'))
        report = Report(io.StringIO())
        runner = FakeRunner().on('kubectl', stdout=json.dumps({'items': [{
            'metadata': {'name': 'hello-staging', 'labels': {'platform.swhurl.com/app': 'hello'}},
            'status': {'conditions': [{'type': 'Ready', 'status': 'True'}], 'latestRef': {'tag': '7-abcdef0'}}}]}))
        checks.check_image_automation(runner, report, self.root)
        self.assertIn('[OK] hello-staging: newest image 7-abcdef0, deployed', report.lines)
        (self.root / 'apps/teams.yaml').write_text('teams: {demo: {}}\n')
        with self.assertRaisesRegex(edit.EditError, 'team demo still owns hello'):
            teams.remove(self.root, 'demo')

    def test_deploy_and_webhook_inventory_use_recorded_repository_not_flux_yaml(self):
        record = self.root / 'apps/hello/staging/release.yaml'
        doc = yamlio.load(record.read_text())
        doc['image']['repository'] = 'ghcr.io/samclement/hello'
        record.write_text(yamlio.dump(doc))
        self.break_rendered()
        self.assertEqual(hooks.auto_deploy_repositories(self.root), {'hello': 'samclement/hello'})
        with mock.patch.object(edit.images, 'newest', return_value=edit.images.Image('7-abcdef0', 'sha256:' + 'a' * 64)) as newest:
            selected = edit.newest_image(self.root, 'hello')
        chosen = newest.call_args.args[0]
        self.assertEqual((chosen.repository, chosen.extract), ('samclement/hello', 'run'))
        self.assertIsNotNone(chosen.pattern.fullmatch('7-abcdef0'))
        self.assertIsNone(chosen.pattern.fullmatch('latest'))
        with mock.patch.object(edit, 'newest_image', return_value=selected), mock.patch.object(edit, 'deploy_staging') as deploy:
            self.assertEqual(cli.main_deploy(['--root', str(self.root)]), 0)
        self.assertEqual(deploy.call_args.args[:2], (self.root, 'hello'))

    def test_settings_only_instance_is_an_error_not_absence(self):
        folder = self.root / 'apps/hello/prod'
        (folder / 'release.yaml').unlink()
        (folder / 'helmrelease.yaml').unlink()
        _, errors = store.world(self.root)
        self.assertTrue(any('hello/prod' in error and 'cannot read its records' in error for error in errors), errors)

    def notification_runner(self):
        (self.root / channels.NOTIFY_DIR).mkdir(parents=True, exist_ok=True)
        configmap = {'metadata': {'name': channels.ROUTES_NAME},
                     'data': {channels.ROUTES_KEY: json.dumps(channels.routes(self.root))}}
        return (FakeRunner().on('kubectl', '-n', 'notify', 'get', 'configmaps', stdout=json.dumps({'items': [configmap]}))
                .on('kubectl', '-n', 'notify', 'get', 'secrets', stdout=''))

    def test_notification_ownership_uses_records_with_stale_or_missing_output(self):
        app = self.root / 'apps/hello/app.yaml'
        doc = yamlio.load(app.read_text())
        doc['team'] = 'payments'
        app.write_text(yamlio.dump(doc))
        for state in ('stale', 'record-only'):
            for env in ('staging', 'prod'):
                folder = self.root / f'apps/hello/{env}'
                if state == 'stale':
                    (folder / 'namespace.yaml').write_text('metadata: {labels: {platform.swhurl.com/team: old}}')
                else:
                    (folder / 'namespace.yaml').unlink()
                    (folder / 'helmrelease.yaml').unlink()
            with self.subTest(state=state):
                report = Report(io.StringIO())
                notification_checks.check_channels(self.notification_runner(), report, self.root)
                self.assertEqual(report.failures, 0, report.lines)
                warnings = [line for line in report.lines if 'has no notification channel' in line]
                self.assertEqual(warnings, ['[WARN] team payments owns 1 app(s) and has no notification channel: '
                                            'their alerts and messages reach nobody (make team-channel NAME=payments)'])

    def test_sqlite_inventory_includes_record_only_release_and_ignores_stale_output(self):
        release = self.root / 'apps/hello/prod/release.yaml'
        doc = yamlio.load(release.read_text())
        doc['database'] = 'sqlite'
        release.write_text(yamlio.dump(doc))
        self.break_rendered()
        self.assertEqual(sqlite_backup.expected(self.root), [Instance('hello', 'prod')])
        report = Report(io.StringIO())
        with mock.patch.object(sqlite_backup.backupset, 'check_backup_set') as check:
            sqlite_backup.check_sqlite_backups(FakeRunner(), report, {}, root=self.root)
        self.assertEqual(check.call_count, 1)
        self.assertEqual(check.call_args.kwargs['prefix'], 'hello-prod: ')
        self.assertNotIn('[OK] no app has a SQLite database', report.lines)

    def test_notification_and_sqlite_inventory_report_malformed_records(self):
        (self.root / 'apps/hello/prod/release.yaml').write_text('invalid: [')
        for check, runner in ((notification_checks.check_channels, self.notification_runner()),
                              (sqlite_backup.check_sqlite_backups, FakeRunner())):
            with self.subTest(check=check.__name__):
                report = Report(io.StringIO())
                check(runner, report, root=self.root)
                self.assertEqual(report.failures, 1, report.lines)
                self.assertEqual(runner.calls, [], 'bad records stop inventory checks before live reads')
                self.assertTrue(any('cannot read its records' in line for line in report.lines), report.lines)
                self.assertFalse(any('no app has a SQLite database' in line or 'has no notification channel' in line
                                     for line in report.lines), report.lines)

    def test_edit_error_import_uses_only_the_standard_library(self):
        result = subprocess.run([sys.executable, '-S', '-c',
                                 'import sys; from swhurl.apps.errors import EditError; '
                                 'assert issubclass(EditError, Exception); '
                                 'assert not any(n.startswith(("ruamel", "yaml")) for n in sys.modules)'],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_policy_dependency_graph_does_not_import_ruamel(self):
        result = subprocess.run([sys.executable, '-c',
                                 'import sys; from swhurl.apps import policy; '
                                 'assert not any(n.startswith("ruamel") for n in sys.modules)'],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)

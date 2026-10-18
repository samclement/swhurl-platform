"""Scheduled jobs: the CronJob app-new writes, app-schedule, and what is refused for a job; offline."""
import io
import shutil
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

import yaml

from swhurl import ROOT
from swhurl.apps import contract, declaration, edit, helmrelease, new
from swhurl.apps.yaml_file import EditError

MANIFESTS = ROOT / 'tests/fixtures/manifests'
IMAGE = 'ghcr.io/example/nightly:2-abcdef0@sha256:' + 'a' * 64


class ScheduleTests(unittest.TestCase):
    def test_five_cron_fields_or_a_name(self):
        for good in ('17 3 * * *', '*/10 * * * *', '0 9-17 * * 1-5', '0,30 * * * *', '@daily', '@hourly'):
            with self.subTest(good=good):
                self.assertEqual(contract.schedule_problem(good), '')
        for bad in ('', '* * * *', '17 3 * * * *', 'every day', '@reboot', '1;2 * * * *', '$(x) * * * *'):
            with self.subTest(bad=bad):
                self.assertIn('is not five cron fields', contract.schedule_problem(bad))

    def test_a_job_keeps_no_database(self):
        self.assertEqual(declaration.of({'version': 1, 'kind': 'job'}).seed['exposure'], 'private')
        with self.assertRaisesRegex(declaration.DeclarationError, 'database does not apply to kind job'):
            declaration.of({'version': 1, 'kind': 'job', 'database': 'sqlite'})
        with self.assertRaisesRegex(declaration.DeclarationError, 'port and healthPath apply to kind web only'):
            declaration.of({'version': 1, 'kind': 'job', 'port': 8080})


class JobInstanceTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for rel in ('clusters/home/kustomization.yaml', 'platform/reloader/helmrelease.yaml', 'apps/teams.yaml'):
            (self.root / rel).parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(ROOT / rel, self.root / rel)

    def generate(self, *extra, manifest='job'):
        args = new.parse_args(['nightly', '--manifest', str(MANIFESTS / f'{manifest}.yaml'), '--team', 'demo',
                               '--env', 'staging', '--image', IMAGE, '--no-auto-deploy', *extra])
        new.generate(args, self.root)
        return yaml.safe_load((self.root / 'apps/nightly/staging/helmrelease.yaml').read_text())

    def test_a_job_runs_as_a_cronjob_with_no_service_probes_or_route(self):
        release = self.generate()
        values = release['spec']['values']
        main = values['controllers']['main']
        self.assertEqual(main['type'], 'cronjob')
        self.assertEqual(main['cronjob'], {'schedule': '17 3 * * *', 'timeZone': 'Etc/UTC', 'concurrencyPolicy': 'Forbid',
                                           'startingDeadlineSeconds': 300, 'activeDeadlineSeconds': 3600,
                                           'backoffLimit': 0, 'successfulJobsHistory': 1, 'failedJobsHistory': 1})
        self.assertNotIn('probes', main['containers']['main'])
        self.assertNotIn('replicas', main)
        self.assertNotIn('service', values)
        self.assertNotIn('ingress', values)
        self.assertEqual((helmrelease.kind(release), helmrelease.schedule(release)), ('job', '17 3 * * *'))
        namespace = yaml.safe_load((self.root / 'apps/nightly/staging/namespace.yaml').read_text())
        self.assertEqual(namespace['metadata']['labels'][contract.EXPOSURE], 'private')

    def test_what_a_job_refuses_at_creation(self):
        for extra, message in ((['--schedule', 'every day'], 'is not five cron fields'),
                               (['--exposure', 'authenticated-web', '--host', 'n.homelab.swhurl.com'], 'a job has no Service or route'),
                               (['--persistence', '1Gi'], 'a job keeps no volume or database')):
            with self.subTest(extra=extra), self.assertRaisesRegex(new.GenerationError, message):
                self.generate(*extra)
        with self.assertRaisesRegex(new.GenerationError, '--schedule applies to kind job only'):
            self.generate('--schedule', '@daily', manifest='worker')

    def test_app_schedule_changes_when_it_runs_and_scale_keeps_to_resources(self):
        self.generate()
        with redirect_stdout(io.StringIO()) as out:
            edit.schedule(self.root, 'nightly', 'staging', '*/10 * * * *')
        self.assertIn("schedule '17 3 * * *' -> '*/10 * * * *' (Etc/UTC)", out.getvalue())
        release = yaml.safe_load((self.root / 'apps/nightly/staging/helmrelease.yaml').read_text())
        self.assertEqual(helmrelease.schedule(release), '*/10 * * * *')
        for when, message in (('*/10 * * * *', 'already runs on'), ('soon', 'is not five cron fields')):
            with self.subTest(when=when), self.assertRaisesRegex(EditError, message):
                edit.schedule(self.root, 'nightly', 'staging', when)
        with self.assertRaisesRegex(EditError, 'is a job: it runs one pod per run'):
            edit.scale(self.root, 'nightly', 'staging', replicas=2)
        with redirect_stdout(io.StringIO()):
            edit.scale(self.root, 'nightly', 'staging', memory='96Mi')

    def test_only_a_job_has_a_schedule(self):
        self.generate(manifest='worker')
        with self.assertRaisesRegex(EditError, 'is not a job: only a job runs on a schedule'):
            edit.schedule(self.root, 'nightly', 'staging', '@daily')


if __name__ == '__main__':
    unittest.main()

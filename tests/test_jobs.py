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
        with self.assertRaisesRegex(declaration.DeclarationError, 'database applies to kind web only: a job keeps'):
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
                               (['--database', 'sqlite'], 'a job keeps no database')):
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


class JobStateTests(unittest.TestCase):
    """A job's state from its CronJob's record (state.job_verdict), as app-status and the console show it."""
    DIGEST = 'sha256:' + 'b' * 64

    def status(self, cron_status: dict | None, suspend=False, pods=()):
        from swhurl.apps import ops
        from swhurl.apps.instance import Instance
        ready = {'status': {'conditions': [{'type': 'Ready', 'status': 'True', 'message': 'ok'}]}}
        unit = {'status': {**ready['status'], 'lastAppliedRevision': 'main@sha1:abc'}}
        release = {**ready, 'spec': {'values': {'controllers': {'main': {
            'type': 'cronjob', 'cronjob': {'schedule': '*/10 * * * *'},
            'containers': {'main': {'image': {'repository': 'ghcr.io/x/nightly', 'tag': '2-abc', 'digest': self.DIGEST}}}}}}}}
        workloads = {'items': [] if cron_status is None else [
            {'kind': 'CronJob', 'metadata': {'name': 'nightly'}, 'spec': {'schedule': '*/10 * * * *', 'suspend': suspend},
             'status': cron_status}]}
        return ops.status_of(Instance('nightly', 'staging'), unit, {'status': {'artifact': {'revision': 'main@sha1:abc'}}},
                             release, {'items': list(pods)}, workloads)

    def pod(self, phase, started, digest):
        return {'metadata': {'name': f'run-{started}'}, 'spec': {'containers': [{'name': 'main', 'image': f'ghcr.io/x/nightly:2-abc@{digest}'}]},
                'status': {'phase': phase, 'startTime': started,
                           'containerStatuses': [{'ready': False, 'imageID': f'ghcr.io/x/nightly@{digest}'}]}}

    def test_each_run_state(self):
        from swhurl.apps.state import State, verdict
        cases = (
            ({}, State('healthy', 'waiting for its first run (*/10 * * * *, Etc/UTC)')),
            ({'lastScheduleTime': '2026-10-18T10:00:00Z', 'lastSuccessfulTime': '2026-10-18T10:00:20Z'},
             State('healthy', 'the last run succeeded at 2026-10-18T10:00:20Z')),
            ({'lastScheduleTime': '2026-10-18T10:10:00Z', 'lastSuccessfulTime': '2026-10-18T10:00:20Z', 'active': [{}]},
             State('updating', 'a run is in progress (started 2026-10-18T10:10:00Z)')),
            ({'lastScheduleTime': '2026-10-18T10:10:00Z', 'lastSuccessfulTime': '2026-10-18T10:00:20Z'},
             State('failing', 'the run started 2026-10-18T10:10:00Z did not succeed; the next runs on schedule (*/10 * * * *)')),
            ({'lastScheduleTime': '2026-10-18T10:10:00Z'},
             State('failing', 'the run started 2026-10-18T10:10:00Z did not succeed; the next runs on schedule (*/10 * * * *)')),
        )
        for record, expected in cases:
            with self.subTest(record=record):
                self.assertEqual(verdict(self.status(record)), expected)
        self.assertEqual(verdict(self.status({}, suspend=True)).key, 'suspended')
        self.assertEqual(verdict(self.status(None)), State('unknown', 'its CronJob is missing'))

    def test_a_failed_run_pod_is_not_a_failing_pod_and_the_image_is_the_last_successful_runs(self):
        from swhurl.apps import ops, state
        old = 'sha256:' + 'c' * 64
        found = self.status({'lastScheduleTime': '2026-10-18T10:10:00Z', 'lastSuccessfulTime': '2026-10-18T10:10:30Z'},
                            pods=[self.pod('Succeeded', '2026-10-18T10:00:00Z', old), self.pod('Failed', '2026-10-18T10:05:00Z', old),
                                  self.pod('Succeeded', '2026-10-18T10:10:00Z', self.DIGEST)])
        self.assertEqual(found.problems, [])
        self.assertEqual(found.image_state, 'matches', 'the newest successful run ran the image Git pins')
        self.assertTrue(state.runs(found), 'so a promotion may take it')
        lines = '\n'.join(ops.status_lines(found))
        self.assertIn('Schedule   */10 * * * * (Etc/UTC)', lines)
        self.assertIn('Last run   2026-10-18T10:10:00Z', lines)
        self.assertNotIn('Replicas', lines)

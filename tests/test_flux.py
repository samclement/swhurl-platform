"""flux-wait and flux-install, offline, with FakeRunner."""
import io
import json
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

from swhurl import flux
from swhurl.report import Report
from swhurl.run import FakeRunner, Result

REV = 'main@sha1:new'


def unit(name, ready='True', applied=REV, attempted=None, reason='ReconciliationSucceeded', message='',
         suspend=False, generation=1, observed=1, reconciling=False):
    conditions = [{'type': 'Ready', 'status': ready, 'reason': reason, 'message': message}]
    if reconciling:
        conditions.append({'type': 'Reconciling', 'status': 'True'})
    return {'metadata': {'name': name, 'generation': generation},
            'spec': {'sourceRef': {'kind': 'GitRepository', 'name': 'swhurl-platform'}, 'suspend': suspend},
            'status': {'observedGeneration': observed, 'lastAppliedRevision': applied,
                       'lastAttemptedRevision': attempted or applied, 'conditions': conditions}}


class StateTests(unittest.TestCase):
    def test_states(self):
        cases = {
            'ready': unit('a'),
            'waiting': unit('a', applied='main@sha1:old'),  # Ready, but at the previous revision
            'suspended': unit('a', suspend=True, ready='False'),
            'failed': unit('a', ready='False', applied='main@sha1:old', attempted=REV, reason='HealthCheckFailed'),
        }
        for expected, obj in cases.items():
            with self.subTest(expected=expected):
                self.assertEqual(flux.state(obj, REV), expected)

    def test_not_yet_is_not_failure(self):
        for obj in (unit('a', ready='False', attempted=REV, applied='old', reason='DependencyNotReady'),
                    unit('a', ready='False', attempted=REV, applied='old', reason='HealthCheckFailed', reconciling=True),
                    unit('a', ready='False', attempted='main@sha1:old', applied='old', reason='HealthCheckFailed'),
                    unit('a', generation=2, observed=1)):
            with self.subTest(obj=obj['status']):
                self.assertEqual(flux.state(obj, REV), 'waiting')


class WaitTests(unittest.TestCase):
    def run_wait(self, *polls, timeout=60):
        """Each poll is the list of units one `kubectl get` returns; the last repeats."""
        answers = list(polls)

        def kustomizations(argv, _input):
            items = answers.pop(0) if len(answers) > 1 else answers[0]
            return Result(argv, 0, json.dumps({'items': items}))

        runner = (FakeRunner()
                  .on('kubectl', '-n', 'flux-system', 'get', 'gitrepositories.source.toolkit.fluxcd.io',
                      stdout=json.dumps({'status': {'artifact': {'revision': REV}}}))
                  .on('kubectl', '-n', 'flux-system', 'get', flux.KUSTOMIZATIONS, handler=kustomizations))
        now = [0.0]
        out = io.StringIO()
        code = flux.wait(runner, Report(out=out), timeout, interval=5, clock=lambda: now[0],
                         sleep=lambda s: now.__setitem__(0, now[0] + s))
        return code, out.getvalue(), runner

    def test_waits_until_every_unit_applies_the_revision(self):
        code, out, runner = self.run_wait([unit('a'), unit('b', applied='main@sha1:old')], [unit('a'), unit('b')])
        self.assertEqual(code, 0, out)
        self.assertEqual(out.count('[OK] a'), 1)
        self.assertIn('[OK] b (5s)', out)
        self.assertEqual(sum(flux.KUSTOMIZATIONS in c for c in runner.calls), 2)

    def test_stops_at_the_first_failure_with_its_message(self):
        failed = unit('b', ready='False', applied='old', attempted=REV, reason='BuildFailed', message='kustomize build')
        code, out, _ = self.run_wait([unit('a'), failed, unit('c', applied='old')])
        self.assertEqual(code, 1)
        self.assertIn('[BAD] b failed: kustomize build', out)

    def test_suspended_units_are_skipped_with_a_warning(self):
        code, out, _ = self.run_wait([unit('a'), unit('s', suspend=True, ready='False')])
        self.assertEqual(code, 0, out)
        self.assertIn('[WARN] s is suspended; skipped', out)

    def test_times_out_naming_the_units_still_waiting(self):
        code, out, _ = self.run_wait([unit('a'), unit('slow', applied='old')], timeout=12)
        self.assertEqual(code, 1)
        self.assertIn('timed out after 12s waiting for: slow', out)

    def test_source_without_artifact_fails(self):
        runner = FakeRunner().on('kubectl', stdout='{"status": {}}')
        self.assertEqual(flux.wait(runner, Report(out=io.StringIO()), 60), 1)


class InstallTests(unittest.TestCase):
    def runner(self, client=None, rendered_extra=True, dry_run=False):
        version = flux.pinned_version()
        seen = {}

        def kustomize(argv, _input):
            directory = Path(argv[-1])
            seen['components'] = (directory / 'gotk-components.yaml').read_text()
            seen['files'] = sorted(p.name for p in directory.iterdir())
            args = flux.required_args()['kustomize-controller'] if rendered_extra else []
            return Result(argv, 0, 'kind: Deployment\nargs: ' + ' '.join(args) + '\n')

        runner = (FakeRunner(dry_run=dry_run)
                  .on('flux', 'version', '--client', stdout=client or f'flux: v{version}\n')
                  .on('flux', 'install', '--export', stdout='# exported components\n')
                  .on('kubectl', 'kustomize', handler=kustomize)
                  .on('kubectl', 'diff', stdout='+ --requeue-dependency=5s\n', returncode=1)
                  .on('kubectl', stdout=''))
        return runner, seen

    def install(self, runner):
        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            code = flux.install_main([], runner)
        return code, out.getvalue() + err.getvalue()

    def test_renders_the_export_with_the_git_patches_and_applies_as_flux(self):
        runner, seen = self.runner()
        code, text = self.install(runner)
        self.assertEqual(code, 0, text)
        self.assertEqual(seen['components'], '# exported components\n')
        self.assertEqual(seen['files'], ['gotk-components.yaml', 'kustomization.yaml'])
        apply = next(c for c in runner.calls if c[:2] == ('kubectl', 'apply'))
        self.assertEqual(apply, ('kubectl', 'apply', '--server-side', '--field-manager=flux', '--force-conflicts',
                                 '-f', '-'))
        self.assertEqual(sum('rollout' in c for c in runner.calls), len(flux.CONTROLLERS))
        export = next(c for c in runner.calls if c[:3] == ('flux', 'install', '--export'))
        self.assertIn('--components-extra=image-reflector-controller,image-automation-controller', export)

    def test_refuses_a_different_flux_cli(self):
        runner, _ = self.runner(client='flux: v9.9.9\n')
        code, text = self.install(runner)
        self.assertEqual(code, 1)
        self.assertIn('flux CLI is flux: v9.9.9', text)
        self.assertFalse([c for c in runner.calls if c[:2] == ('flux', 'install')])

    def test_refuses_output_missing_a_patch(self):
        runner, _ = self.runner(rendered_extra=False)
        code, text = self.install(runner)
        self.assertEqual(code, 1)
        self.assertIn('rendered kustomize-controller lacks --requeue-dependency', text)
        self.assertFalse([c for c in runner.calls if c[:2] == ('kubectl', 'apply')])

    def test_dry_run_shows_the_live_diff_and_changes_nothing(self):
        runner, _ = self.runner(dry_run=True)
        code, text = self.install(runner)
        self.assertEqual(code, 0, text)
        self.assertIn('+ --requeue-dependency=5s', text)
        self.assertFalse([c for c in runner.calls if c[:2] == ('kubectl', 'apply') or 'rollout' in c])
        self.assertEqual(len(runner.planned), 1 + len(flux.CONTROLLERS))  # apply, then each rollout

    def test_required_args_come_from_the_patch_files(self):
        self.assertEqual(flux.required_args(), {'kustomize-controller': ['--requeue-dependency=5s']})


if __name__ == '__main__':
    unittest.main()

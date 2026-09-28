"""suspend / resume / destroy-data: every refusal and the destructive order, offline."""
import io
import json
import sys
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / 'tools'))

from swhurl import lifecycle  # noqa: E402
from swhurl.run import FakeRunner, Result  # noqa: E402

PVC = 'pvc/apps/data'


def pod(claim, phase='Running'):
    return {'status': {'phase': phase}, 'spec': {'volumes': [{'name': 'd', 'persistentVolumeClaim': {'claimName': claim}}]}}


def cluster(*, pvc=None, pods=(), helm_secret='', owner_exists=False, pv_phase='Released', pv_missing=False,
            dry_run=False, delete_rc=0):
    pvc = pvc if pvc is not None else {'metadata': {}, 'spec': {'volumeName': 'pv-1'}}
    fake = FakeRunner(dry_run=dry_run)
    fake.on('kubectl', '-n', 'apps', 'get', 'pvc', 'data',
            handler=lambda a, _i: Result(a, 0, json.dumps(pvc)) if pvc else Result(a, 1, '', 'NotFound'))
    fake.on('kubectl', '-n', 'apps', 'get', 'pods', stdout=json.dumps({'items': list(pods)}))
    fake.on('kubectl', '-n', 'apps', 'get', 'secret', stdout=helm_secret)
    fake.on('kubectl', '-n', 'flux-system', 'get', 'kustomization', 'homelab-app-x', returncode=0 if owner_exists else 1)
    fake.on('kubectl', 'get', 'pv', handler=lambda a, _i: Result(a, 1, '', 'NotFound') if pv_missing
            else Result(a, 0, json.dumps({'status': {'phase': pv_phase}})))
    fake.on('kubectl', '-n', 'apps', 'delete', 'pvc', returncode=delete_rc, stderr='delete failed')
    fake.on('kubectl', 'patch', 'pv').on('kubectl', 'wait')
    fake.on('kubectl', '-n', 'flux-system', 'get', 'kustomization', 'homelab-a').on('kubectl', '-n', 'ns', 'get')
    fake.on('flux')
    return fake


def run(runner, *argv, confirm=''):
    out, err = io.StringIO(), io.StringIO()
    with mock.patch.dict('os.environ', {'CONFIRM': confirm}), redirect_stdout(out), redirect_stderr(err):
        code = lifecycle.main(list(argv), runner)
    return code, out.getvalue(), err.getvalue()


MUTATIONS = {'patch', 'delete', 'wait'}


def mutations(runner):
    return [c for c in runner.calls if len(c) > 1 and ({c[1], *c[2:4]} & MUTATIONS)]


class TargetTests(unittest.TestCase):
    def test_shapes(self):
        ok = {('suspend', 'kustomization/a'): ('kustomization', 'flux-system', 'a'),
              ('resume', 'helmrelease/ns/r'): ('helmrelease', 'ns', 'r'),
              ('destroy-data', 'pvc/ns/c'): ('pvc', 'ns', 'c'),
              ('destroy-data', 'pv/v'): ('pv', '', 'v')}
        for (action, text), expected in ok.items():
            target = lifecycle.parse_target(action, text)
            self.assertEqual((target.kind, target.namespace, target.name), expected)
        bad = [('suspend', 'kustomization/a/b', 'Use kustomization/<name>'),
               ('suspend', 'helmrelease/r', 'Use helmrelease/<namespace>/<name>'),
               ('destroy-data', 'pvc/c', 'Use pvc/<namespace>/<name>'),
               ('destroy-data', 'pv/ns/v', 'Use pv/<name>'),
               ('destroy-data', 'pvc//c', 'Use pvc/<namespace>/<name>'),
               ('suspend', 'a/b/c/d', 'Invalid target'),
               ('suspend', 'pvc/ns/c', 'suspend'),
               ('destroy-data', 'kustomization/a', 'destroy-data')]
        for action, text, message in bad:
            with self.subTest(text=text), self.assertRaisesRegex(lifecycle.LifecycleError, message):
                lifecycle.parse_target(action, text)


class DestroyDataTests(unittest.TestCase):
    def test_requires_exact_confirm_before_any_call(self):
        for confirm in ('', 'pvc/apps/other', 'pvc/apps/data '):
            with self.subTest(confirm=confirm):
                runner = cluster()
                code, _, err = run(runner, 'destroy-data', PVC, confirm=confirm)
                self.assertEqual(code, 2)
                self.assertIn(f'Re-run with CONFIRM={PVC}', err)
                self.assertEqual(runner.calls, [])

    def test_refusals_never_mutate(self):
        managed = {'metadata': {'annotations': {'meta.helm.sh/release-name': 'app'}}, 'spec': {'volumeName': 'pv-1'}}
        flux_owned = {'metadata': {'labels': {'kustomize.toolkit.fluxcd.io/name': 'homelab-app-x'}},
                      'spec': {'volumeName': 'pv-1'}}
        cases = {
            'claim missing': (dict(pvc={}), 'PVC apps/data not found'),
            'mounted by a running pod': (dict(pods=[pod('data')]), 'mounted by a pod'),
            'mounted by a pending pod': (dict(pods=[pod('data', 'Pending')]), 'mounted by a pod'),
            'helm release installed': (dict(pvc=managed, helm_secret='secret/sh.helm.release.v1.app.v1'),
                                       'Helm release apps/app still exists'),
            'flux unit manages it': (dict(pvc=flux_owned, owner_exists=True), 'Flux Kustomization homelab-app-x'),
        }
        for label, (kwargs, message) in cases.items():
            with self.subTest(label):
                runner = cluster(**kwargs)
                code, _, err = run(runner, 'destroy-data', PVC, confirm=PVC)
                self.assertEqual(code, 2, err)
                self.assertIn(message, err)
                self.assertEqual(mutations(runner), [])

    def test_finished_pods_and_retired_owners_do_not_block(self):
        flux_owned = {'metadata': {'labels': {'kustomize.toolkit.fluxcd.io/name': 'homelab-app-x'},
                                   'annotations': {'meta.helm.sh/release-name': 'app'}},
                      'spec': {'volumeName': 'pv-1'}}
        runner = cluster(pvc=flux_owned, pods=[pod('data', 'Succeeded'), pod('other')], owner_exists=False)
        self.assertEqual(run(runner, 'destroy-data', PVC, confirm=PVC)[0], 0)

    def test_destroys_in_order(self):
        runner = cluster()
        code, out, _ = run(runner, 'destroy-data', PVC, confirm=PVC)
        self.assertEqual(code, 0)
        self.assertEqual(mutations(runner), [
            ('kubectl', 'patch', 'pv', 'pv-1', '-p', '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}'),
            ('kubectl', '-n', 'apps', 'delete', 'pvc', 'data', '--wait=true', '--timeout=2m'),
            ('kubectl', 'wait', '--for=delete', 'pv/pv-1', '--timeout=2m'),
        ])
        self.assertEqual(out, '[OK] Destroyed pvc/apps/data (PV pv-1 and its data)\n')

    def test_dry_run_checks_but_does_not_mutate(self):
        runner = cluster(dry_run=True)
        code, out, _ = run(runner, 'destroy-data', PVC, confirm=PVC)
        self.assertEqual(code, 0)
        self.assertEqual(mutations(runner), [])
        self.assertEqual(len(runner.planned), 3)
        self.assertIn('Plan (destroy-data pvc/apps/data): all checks passed', out)
        self.assertNotIn('[OK] Destroyed', out)

    def test_released_pv_path(self):
        runner = cluster()
        self.assertEqual(run(runner, 'destroy-data', 'pv/pv-9', confirm='pv/pv-9')[0], 0)
        self.assertEqual([c[1:3] for c in mutations(runner)], [('patch', 'pv'), ('wait', '--for=delete')])
        for kwargs, message in ((dict(pv_phase='Bound'), 'is not Released'), (dict(pv_missing=True), 'not found')):
            runner = cluster(**kwargs)
            code, _, err = run(runner, 'destroy-data', 'pv/pv-9', confirm='pv/pv-9')
            self.assertEqual(code, 2)
            self.assertIn(message, err)
            self.assertEqual(mutations(runner), [])

    def test_failed_deletion_exits_1(self):
        code, _, err = run(cluster(delete_rc=1), 'destroy-data', PVC, confirm=PVC)
        self.assertEqual(code, 1)
        self.assertIn('delete failed', err)


class SuspendResumeTests(unittest.TestCase):
    def test_suspend_kustomization_explains_helm_caveat(self):
        runner = cluster()
        code, out, _ = run(runner, 'suspend', 'kustomization/homelab-a')
        self.assertEqual(code, 0)
        self.assertIn(('flux', 'suspend', 'kustomization', 'homelab-a', '-n', 'flux-system'), runner.calls)
        self.assertIn('suspend them separately to freeze Helm', out)

    def test_resume_helmrelease_is_quiet(self):
        runner = cluster()
        code, out, _ = run(runner, 'resume', 'helmrelease/ns/r')
        self.assertEqual((code, out), (0, ''))
        self.assertIn(('flux', 'resume', 'helmrelease', 'r', '-n', 'ns'), runner.calls)

    def test_missing_target_refused(self):
        runner = FakeRunner().on('kubectl', returncode=1)
        code, _, err = run(runner, 'suspend', 'kustomization/nope')
        self.assertEqual(code, 2)
        self.assertIn('kustomization flux-system/nope not found', err)

    def test_dry_run_plans_flux(self):
        runner = cluster(dry_run=True)
        code, out, _ = run(runner, 'suspend', 'kustomization/homelab-a')
        self.assertEqual(code, 0)
        self.assertNotIn('flux', {c[0] for c in runner.calls})
        self.assertTrue(out.startswith('Plan (suspend kustomization/homelab-a):\n'))

    def test_usage(self):
        for argv in ([], ['suspend'], ['suspend', ''], ['explode', 'pvc/a/b']):
            with self.subTest(argv=argv):
                runner = FakeRunner()
                self.assertEqual(run(runner, *argv)[0], 2)
                self.assertEqual(runner.calls, [])


if __name__ == '__main__':
    unittest.main()

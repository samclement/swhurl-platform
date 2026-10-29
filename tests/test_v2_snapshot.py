"""The v2 snapshot export: one pinned revision, records and object identities, no secret material."""
import json
import unittest

from swhurl import v2snapshot
from swhurl.run import FakeRunner

REVISION = 'b' * 40

PATHS = [
    'apps/hello/app.yaml',
    'apps/hello/staging/instance.yaml',
    'apps/hello/staging/release.yaml',
    'apps/hello/staging/helmrelease.yaml',
    'apps/hello/staging/namespace.yaml',
    'apps/hello/staging/networkpolicy.yaml',
    'apps/hello/staging/image-automation.yaml',
    'apps/hello/staging/secret.sops.yaml',
    'apps/orphan/prod/namespace.yaml',
    'clusters/home/app-hello-staging.yaml',
    'clusters/home/app-orphan-prod.yaml',
]

FILES = {
    'apps/hello/app.yaml': 'version: 1\nteam: demo\n',
    'apps/hello/staging/instance.yaml': 'version: 1\nexposure: private\nreplicas: 1\n',
    'apps/hello/staging/release.yaml': 'version: 1\nimage:\n  repository: docker.io/x/y\n  digest: sha256:' + 'a' * 64 + '\nkind: web\nuid: 101\nport: 8080\nhealthPath: /\n',
    'apps/hello/staging/helmrelease.yaml': 'apiVersion: helm.toolkit.fluxcd.io/v2\nkind: HelmRelease\nmetadata:\n  name: hello\n  namespace: hello-staging\nspec:\n  values:\n    controllers:\n      main:\n        replicas: 1\n        containers:\n          main:\n            resources:\n              limits:\n                memory: 128Mi\n',
    'apps/hello/staging/namespace.yaml': 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: hello-staging\n',
    'apps/hello/staging/networkpolicy.yaml': 'apiVersion: networking.k8s.io/v1\nkind: NetworkPolicy\nmetadata:\n  name: hello\n  namespace: hello-staging\n',
    'apps/hello/staging/image-automation.yaml': 'apiVersion: image.toolkit.fluxcd.io/v1\nkind: ImageRepository\nmetadata:\n  name: hello\n  namespace: flux-system\n---\napiVersion: image.toolkit.fluxcd.io/v1\nkind: ImagePolicy\nmetadata:\n  name: hello\n  namespace: flux-system\n',
    'apps/hello/staging/secret.sops.yaml': 'sops:\n  age: [not read]\n',
    'apps/orphan/prod/namespace.yaml': 'apiVersion: v1\nkind: Namespace\nmetadata:\n  name: orphan-prod\n',
    'clusters/home/app-hello-staging.yaml': 'apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: app-hello-staging\n  namespace: flux-system\n',
    'clusters/home/app-orphan-prod.yaml': 'apiVersion: kustomize.toolkit.fluxcd.io/v1\nkind: Kustomization\nmetadata:\n  name: app-orphan-prod\n  namespace: flux-system\n',
}


def fake() -> FakeRunner:
    runner = FakeRunner().on('git', 'rev-parse', stdout=REVISION + '\n').on('git', 'ls-tree', stdout='\n'.join(PATHS) + '\n')
    for path, text in FILES.items():
        runner.on('git', 'show', f'{REVISION}:{path}', stdout=text)
    return runner


def export(tmp_path: str, runner: FakeRunner | None = None) -> dict:
    runner = runner or fake()
    out = f'{tmp_path}/snapshot.json'
    assert v2snapshot.main(['--revision', 'HEAD', '--out', out], runner=runner) == 0
    with open(out, encoding='utf-8') as handle:
        return json.load(handle)


class SnapshotExportTests(unittest.TestCase):
    def test_records_objects_and_residual_units(self, tmp_path='/tmp') -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            document = export(directory)
        self.assertEqual(document['revision'], REVISION)
        self.assertEqual([(i['application'], i['environment']) for i in document['instances']], [('hello', 'staging')])
        instance = document['instances'][0]
        self.assertEqual(sorted(instance['records']), ['app', 'helmrelease', 'instance', 'release'])
        self.assertEqual(instance['missing'], [])
        # Records, the HelmRelease it commits, the unit that applies it, and every document in a
        # multi-document manifest: nothing an instance declares is left out of the inventory.
        self.assertEqual([(entry['kind'], entry['namespace'], entry['name']) for entry in instance['objects']],
                         [('HelmRelease', 'hello-staging', 'hello'), ('ImagePolicy', 'flux-system', 'hello'),
                          ('ImageRepository', 'flux-system', 'hello'), ('Kustomization', 'flux-system', 'app-hello-staging'),
                          ('Namespace', '', 'hello-staging'), ('NetworkPolicy', 'hello-staging', 'hello')])
        # A unit with no instance record is ownership evidence too, and is not silently dropped.
        self.assertEqual([entry['unit'] for entry in document['residual']], ['app-orphan-prod'])
        self.assertEqual(document['residual'][0]['object']['name'], 'app-orphan-prod')

    def test_reads_only_the_pinned_revision(self) -> None:
        runner = fake()
        export('/tmp', runner)
        shown = [call for call in runner.calls if call[:2] == ('git', 'show')]
        self.assertTrue(shown)
        self.assertTrue(all(call[2].startswith(REVISION + ':') for call in shown))
        # The encrypted file is never read, and neither is anything outside Git.
        self.assertFalse(any('secret.sops.yaml' in call[2] for call in shown))
        self.assertFalse(any(call[0] != 'git' for call in runner.calls))

    def test_a_missing_record_is_named_not_invented(self) -> None:
        absent = 'apps/hello/staging/release.yaml'
        runner = FakeRunner().on('git', 'rev-parse', stdout=REVISION + '\n').on(
            'git', 'ls-tree', stdout='\n'.join(p for p in PATHS if p != absent) + '\n')
        for path, text in FILES.items():
            if path in PATHS and path != absent and not path.endswith('.sops.yaml'):
                runner.on('git', 'show', f'{REVISION}:{path}', stdout=text)
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            document = export(directory, runner)
        instance = document['instances'][0]
        self.assertEqual(instance['missing'], ['apps/hello/staging/release.yaml'])
        self.assertNotIn('release', instance['records'])

    def test_a_revision_that_is_not_a_commit_is_refused(self) -> None:
        runner = FakeRunner().on('git', 'rev-parse', stdout='not a commit\n')
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            self.assertEqual(v2snapshot.main(['--revision', 'nope', '--out', f'{directory}/s.json'], runner=runner), 2)

    def test_usage_and_dry_run(self) -> None:
        runner = FakeRunner(dry_run=True)
        self.assertEqual(v2snapshot.main(['--revision', 'HEAD', '--out', '/tmp/unused.json'], runner=runner), 0)
        self.assertEqual(runner.calls, [])
        self.assertEqual(v2snapshot.main([], runner=runner), 2)
        self.assertEqual(v2snapshot.main(['--revision', 'HEAD', '--revision', 'HEAD', '--out', 'x'], runner=runner), 2)

    def test_the_export_is_reproducible(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            first, second = f'{directory}/a.json', f'{directory}/b.json'
            self.assertEqual(v2snapshot.main(['--revision', 'HEAD', '--out', first], runner=fake()), 0)
            self.assertEqual(v2snapshot.main(['--revision', 'HEAD', '--out', second], runner=fake()), 0)
            with open(first, 'rb') as one, open(second, 'rb') as two:
                self.assertEqual(one.read(), two.read())


if __name__ == '__main__':
    unittest.main()


class OwnedInstanceTests(unittest.TestCase):
    """An instance v2 owns keeps its records in v2's tree, and the export reads them like any other."""

    PATHS = [
        'v2-records/owned/staging/instance.json',
        'v2-records/owned/staging/release.json',
        'v2-apps/instances/owned/staging/namespace.json',
        'v2-apps/instances/owned/staging/kustomization.yaml',
        'apps/owned/app.yaml',
        'clusters/home/app-owned-staging.json',
    ]
    FILES = {
        'v2-records/owned/staging/instance.json': json.dumps({
            'schemaVersion': 1, 'application': 'owned', 'environment': 'staging',
            'release': 'rel-sha256:' + 'c' * 64, 'configuration': {'replicas': 1, 'memoryMiB': 128},
            'grants': {'exposure': 'authenticated-web'}, 'bindings': {}, 'allows': ['caller']}),
        'v2-records/owned/staging/release.json': json.dumps({
            'schemaVersion': 1, 'application': 'owned', 'source': {'repository': 'x/y', 'commit': 'd' * 40},
            'artifact': {'image': 'docker.io/x/y', 'digest': 'sha256:' + 'a' * 64,
                         'mediaType': 'application/vnd.oci.image.manifest.v1+json', 'platform': 'linux/amd64'},
            'runtime': {'kind': 'web', 'port': 8080, 'healthPath': '/healthz', 'uid': 65532},
            'database': 'sqlite', 'releaseId': 'rel-sha256:' + 'c' * 64}),
        'apps/owned/app.yaml': 'version: 1\nteam: demo\n',
        'v2-apps/instances/owned/staging/namespace.json': '{"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": "owned-staging"}}',
        'v2-apps/instances/owned/staging/kustomization.yaml': 'apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources: []\n',
        'clusters/home/app-owned-staging.json': '{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": {"name": "app-owned-staging", "namespace": "flux-system"}}',
    }

    def runner(self) -> FakeRunner:
        runner = FakeRunner().on('git', 'rev-parse', stdout=REVISION + '\n').on('git', 'ls-tree', stdout='\n'.join(self.PATHS) + '\n')
        for path, text in self.FILES.items():
            runner.on('git', 'show', f'{REVISION}:{path}', stdout=text)
        return runner

    def test_a_v2_owned_instance_is_read_from_v2s_tree(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            document = export(directory, self.runner())
        self.assertEqual([(i['application'], i['environment']) for i in document['instances']], [('owned', 'staging')])
        instance = document['instances'][0]
        # The app record still lives where v1 keeps it, while the instance's own records are v2's.
        self.assertEqual(instance['format'], 'v2')
        self.assertEqual(sorted(instance['records']), ['app', 'instance', 'release'])
        self.assertEqual(instance['records']['instance']['allows'], ['caller'])
        self.assertEqual(instance['missing'], [])
        # Its compiled output is the evidence of what it declares, and its .json unit is its own, not residual.
        self.assertEqual([(entry['kind'], entry['name']) for entry in instance['objects']],
                         [('Kustomization', 'app-owned-staging'), ('Namespace', 'owned-staging')])
        self.assertEqual(document['residual'], [])


class OwnedAppRecordTests(unittest.TestCase):
    """A v2-owned app keeps one app record at the root, and the export reads it without a v1 app.yaml."""

    PATHS = [
        'v2-records/owned/app.json',
        'v2-records/owned/staging/instance.json',
        'v2-records/owned/staging/release.json',
    ]
    FILES = {
        'v2-records/owned/app.json': json.dumps({'schemaVersion': 1, 'application': 'owned', 'team': 'insights'}),
        'v2-records/owned/staging/instance.json': json.dumps({
            'schemaVersion': 1, 'application': 'owned', 'environment': 'staging',
            'release': 'rel-sha256:' + 'c' * 64, 'configuration': {'replicas': 1, 'memoryMiB': 128},
            'grants': {'exposure': 'private'}, 'bindings': {}}),
        'v2-records/owned/staging/release.json': json.dumps({
            'schemaVersion': 1, 'application': 'owned', 'source': {'repository': 'x/y', 'commit': 'd' * 40},
            'artifact': {'image': 'docker.io/x/y', 'digest': 'sha256:' + 'a' * 64,
                         'mediaType': 'application/vnd.oci.image.manifest.v1+json', 'platform': 'linux/amd64'},
            'runtime': {'kind': 'web', 'port': 8080, 'healthPath': '/healthz', 'uid': 65532},
            'releaseId': 'rel-sha256:' + 'c' * 64}),
    }

    def runner(self) -> FakeRunner:
        runner = FakeRunner().on('git', 'rev-parse', stdout=REVISION + '\n').on('git', 'ls-tree', stdout='\n'.join(self.PATHS) + '\n')
        for path, text in self.FILES.items():
            runner.on('git', 'show', f'{REVISION}:{path}', stdout=text)
        return runner

    def test_the_v2_app_record_is_read_from_the_app_root(self) -> None:
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            document = export(directory, self.runner())
        instance = document['instances'][0]
        self.assertEqual(instance['format'], 'v2')
        self.assertEqual(instance['records']['app']['team'], 'insights')
        self.assertFalse(any(path.endswith('app.yaml') for path in self.FILES))


# A v2-owned instance keeps its JSON pair and nothing else: neither a leftover v1 record nor a second shape.
class V2OwnedRecordTests(unittest.TestCase):
    """A v2-owned instance keeps its JSON pair and nothing else: no leftover v1 record, and no second shape."""

    def test_a_leftover_v1_record_under_v2s_tree_is_refused(self) -> None:
        owned = OwnedInstanceTests()
        paths = owned.PATHS + ['v2-records/owned/staging/instance.yaml']
        with self.assertRaises(v2snapshot.SnapshotError):
            v2snapshot.snapshot(owned.runner(), REVISION, paths)

    def test_two_record_shapes_for_one_instance_is_refused(self) -> None:
        owned = OwnedInstanceTests()
        paths = owned.PATHS + ['apps/owned/staging/instance.yaml']
        with self.assertRaises(v2snapshot.SnapshotError):
            v2snapshot.snapshot(owned.runner(), REVISION, paths)

    def test_the_repository_keeps_no_yaml_record_for_a_v2_owned_instance(self) -> None:
        # The guard that keeps the duplication from returning: a v2-owned instance's records are its pair.
        import pathlib
        root = pathlib.Path(v2snapshot.ROOT) / 'v2-records'
        leftovers = sorted(str(path.relative_to(root)) for path in root.glob('*/*/instance.yaml'))
        self.assertEqual(leftovers, [])

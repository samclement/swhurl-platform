"""The command line runs as ``python3 -m codegraph`` from any directory, and keeps its exit and stdout contract."""
import hashlib
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from codegraph import snapshot as snapshots
from tests.gitrepo import git, make_repo

PACKAGE_ROOT = Path(__file__).resolve().parents[1]


def codegraph(*args, cwd=None):
    """Run the CLI in a fresh process, by default from an empty directory, so it relies on neither the checkout nor the cwd."""
    with tempfile.TemporaryDirectory() as empty:
        return subprocess.run([sys.executable, '-m', 'codegraph', *args], cwd=cwd or empty, capture_output=True,
                              env={**os.environ, 'PYTHONPATH': str(PACKAGE_ROOT)})


class UsageTests(unittest.TestCase):
    def test_help_succeeds(self):
        result = codegraph('--help')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(b'usage: codegraph', result.stdout)

    def test_usage_errors_exit_2_with_empty_stdout(self):
        for args, message in (((), b'a command is required'), (('no-such-command',), b'invalid choice'),
                              (('snapshot', 'HEAD'), b'--scope'), (('snapshot', 'HEAD', '--scope', '../x'), b'below the')):
            with self.subTest(args=args):
                result = codegraph(*args)
                self.assertEqual((result.returncode, result.stdout), (2, b''))
                self.assertIn(message, result.stderr)


class SnapshotCommandTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.tmp = Path(tmp.name)
        self.repo = self.tmp / 'repo'
        self.marker = self.tmp / 'executed'
        # Importing or running any of these would write the marker or fail loudly.
        effect = f'import pathlib\npathlib.Path({str(self.marker)!r}).write_text("ran")\nraise SystemExit("executed")\n'
        make_repo(self.repo, {
            'src/pkg/__init__.py': effect,
            'src/pkg/a.py': effect + 'def f():\n    pass\n',
            'src/pkg/b.py': 'from pkg.a import f\n\ndef g():\n    f()\n',
            'src/pkg/broken.py': 'def (\n',
            'src/pkg/data.txt': 'not python\n',
            'src/setup.py': effect,
        })
        # Uncommitted changes the snapshot must neither read nor disturb.
        (self.repo / 'src/pkg/b.py').write_text('def g():\n    pass\n')
        (self.repo / 'src/pkg/untracked.py').write_text('def u():\n    pass\n')

    def state(self):
        return (git(self.repo, 'status', '--porcelain=v1', '-uall'), git(self.repo, 'rev-parse', 'HEAD'),
                hashlib.sha256((self.repo / '.git/index').read_bytes()).hexdigest())

    def snapshot(self, *extra):
        return codegraph('snapshot', 'HEAD', '--scope', 'src/pkg', '--repo', str(self.repo), *extra)

    def test_the_committed_tree_becomes_a_snapshot_without_running_it(self):
        before = self.state()
        result = self.snapshot()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(self.marker.exists(), 'analysed code was executed')
        self.assertEqual(self.state(), before, 'the checkout or index changed')
        loaded = snapshots.load(result.stdout)
        ids = {node.id for node in loaded.nodes}
        self.assertIn('py:pkg.a:f', ids)
        self.assertNotIn('py:pkg.untracked', ids)
        self.assertIn(('py:pkg.b:g', 'py:pkg.a:f', 'calls'), {(e.source, e.target, e.kind) for e in loaded.edges},
                      'the committed b.py, not the working tree')
        self.assertEqual([item.path for item in loaded.unparsed], ['src/pkg/broken.py'])
        self.assertEqual((loaded.scope, loaded.import_root), ('src/pkg', 'src'))

    def test_two_runs_give_identical_bytes(self):
        first, second = self.snapshot(), self.snapshot()
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(first.stdout, second.stdout)

    def test_the_cwd_is_not_the_repository(self):
        self.assertEqual(codegraph('snapshot', 'HEAD', '--scope', 'src/pkg', cwd=self.repo).stdout, self.snapshot().stdout)

    def test_text_summarises_the_same_snapshot(self):
        result = self.snapshot('--text')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(b'4 modules, 0 classes, 2 functions', result.stdout)
        self.assertIn(b'src/pkg/broken.py: SyntaxError at line 1', result.stdout)

    def test_an_unknown_ref_fails_with_empty_stdout(self):
        result = codegraph('snapshot', 'no-such-ref', '--scope', 'src/pkg', '--repo', str(self.repo))
        self.assertEqual((result.returncode, result.stdout), (1, b''))
        self.assertEqual(result.stderr.count(b'\n'), 1)
        self.assertIn(b'codegraph: unknown-ref:', result.stderr)

    def test_a_directory_that_is_not_a_repository_fails(self):
        result = codegraph('snapshot', 'HEAD', '--scope', 'src/pkg', '--repo', str(self.tmp))
        self.assertEqual((result.returncode, result.stdout), (1, b''))
        self.assertIn(b'codegraph: git-failed:', result.stderr)


if __name__ == '__main__':
    unittest.main()

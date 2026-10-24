"""The command line runs as ``python3 -m codegraph`` from any directory, and keeps its exit and stdout contract."""
import hashlib
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from codegraph import snapshot as snapshots
from tests.gitrepo import git, make_repo

PACKAGE_ROOT = Path(__file__).resolve().parents[1]


def codegraph(*args, cwd=None, stdin=b''):
    """Run the CLI in a fresh process, by default from an empty directory, so it relies on neither the checkout nor the cwd."""
    with tempfile.TemporaryDirectory() as empty:
        return subprocess.run([sys.executable, '-m', 'codegraph', *args], cwd=cwd or empty, capture_output=True,
                              input=stdin, env={**os.environ, 'PYTHONPATH': str(PACKAGE_ROOT)})


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


CLI = """\
from starlette.routing import Route


def main():
    def change():
        helper()

    run(change)


def run(work):
    work()


def helper():
    pass


def page(request):
    helper()


ROUTES = [Route('/page', page)]
COMMANDS = {'go': 'pkg.cli:main', 'gone': 'cli:nothing'}

if __name__ == '__main__':
    main()
"""


class AnalysisCommandTests(unittest.TestCase):
    """entries, reach and journeys read documents from a path or stdin, never Git."""

    @classmethod
    def setUpClass(cls):
        tmp = tempfile.TemporaryDirectory()
        cls.addClassCleanup(tmp.cleanup)
        cls.tmp = Path(tmp.name)
        make_repo(cls.tmp / 'repo', {'src/pkg/__init__.py': '', 'src/pkg/cli.py': CLI})
        result = codegraph('snapshot', 'HEAD', '--scope', 'src/pkg', '--repo', str(cls.tmp / 'repo'))
        assert result.returncode == 0, result.stderr
        cls.snapshot = cls.tmp / 'snapshot.json'
        cls.snapshot.write_bytes(result.stdout)
        cls.entries = cls.tmp / 'entries.json'
        cls.entries.write_bytes(codegraph('entries', str(cls.snapshot)).stdout)

    def test_entries_from_stdin(self):
        result = codegraph('entries', '-', stdin=self.snapshot.read_bytes())
        self.assertEqual(result.returncode, 0, result.stderr)
        document = json.loads(result.stdout)
        self.assertEqual(document['kind'], 'entries')
        self.assertEqual(document['inputs'][0]['digest'], 'sha256:' + hashlib.sha256(self.snapshot.read_bytes()).hexdigest())
        self.assertEqual({entry['node']: sorted(item['rule'] for item in entry['evidence']) for entry in document['entries']},
                         {'py:pkg.cli:main': ['entry-string', 'main-block'], 'py:pkg.cli:page': ['starlette-route']})
        self.assertEqual([item['value'] for item in document['unresolved']], ['cli:nothing'])
        self.assertEqual(codegraph('entries', str(self.snapshot)).stdout, result.stdout, 'a path and stdin agree')

    def test_entries_text(self):
        result = codegraph('entries', str(self.snapshot), '--text')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(b"py:pkg.cli:page  starlette-route  src/pkg/cli.py:23  '/page'", result.stdout)

    def test_reach_follows_the_nested_function_passed_as_a_value(self):
        result = codegraph('reach', str(self.snapshot), 'py:pkg.cli:main')
        self.assertEqual(result.returncode, 0, result.stderr)
        document = json.loads(result.stdout)
        self.assertEqual(document['reached'], ['py:pkg.cli:helper', 'py:pkg.cli:main', 'py:pkg.cli:main.change', 'py:pkg.cli:run'])
        self.assertEqual([(step['line'], step['kind']) for step in document['spine']], [(8, 'references'), (8, 'calls')],
                         'within a line, by target ID')
        self.assertEqual(document['unresolvedCalls'], {'dynamic': 1}, 'run() calls its parameter')
        text = codegraph('reach', str(self.snapshot), 'py:pkg.cli:main', '--text').stdout
        self.assertIn(b'reached    3 nodes besides itself', text)

    def test_journeys_chains_snapshot_and_entries(self):
        result = codegraph('journeys', str(self.snapshot), '-', stdin=self.entries.read_bytes())
        self.assertEqual(result.returncode, 0, result.stderr)
        document = json.loads(result.stdout)
        self.assertEqual([item['kind'] for item in document['inputs']], ['snapshot', 'entries'])
        self.assertEqual(document['inputs'][1]['digest'], 'sha256:' + hashlib.sha256(self.entries.read_bytes()).hexdigest())
        self.assertEqual([[member['node'] for member in item['members']] for item in document['candidates']],
                         [['py:pkg.cli:main'], ['py:pkg.cli:page']], 'helper is all they share, reached by both: weight 0')
        uniform = json.loads(codegraph('journeys', str(self.snapshot), str(self.entries), '--weighting', 'uniform',
                                       '--threshold', '0.2').stdout)
        self.assertEqual(uniform['params']['weighting'], 'uniform')
        (candidate,) = uniform['candidates']
        self.assertEqual(candidate['links'], [{'a': 'py:pkg.cli:main', 'b': 'py:pkg.cli:page', 'similarity': 0.2}])
        text = codegraph('journeys', str(self.snapshot), str(self.entries), '--weighting', 'uniform', '--threshold', '0.2',
                         '--text', '--limit', '0').stdout
        self.assertIn(b'candidate 1: 2 entry points, 1 links (similarity 0.2 to 0.2), 1 functions shared by all', text)
        self.assertIn(b"py:pkg.cli:page  '/page'  (reach 2, unresolved 0, nearest 0.2 py:pkg.cli:main)", text)

    def test_failures_exit_1_with_empty_stdout(self):
        other = json.loads(self.entries.read_bytes())
        other['inputs'][0]['digest'] = 'sha256:' + '0' * 64
        for args, stdin, code in ((('reach', '-', 'py:pkg.cli:nothing'), self.snapshot.read_bytes(), b'unknown-node'),
                                  (('journeys', str(self.snapshot), '-'), json.dumps(other).encode(), b'invalid-document'),
                                  (('journeys', str(self.snapshot), str(self.snapshot)), b'', b'invalid-document'),
                                  (('entries', str(self.tmp / 'missing.json')), b'', b'usage'),
                                  (('entries', '-'), b'{"kind": "entries"}', b'invalid-document')):
            with self.subTest(args=args):
                result = codegraph(*args, stdin=stdin)
                self.assertEqual((result.returncode, result.stdout), (1, b''))
                self.assertEqual(result.stderr.count(b'\n'), 1)
                self.assertIn(b'codegraph: ' + code + b':', result.stderr)

    def test_usage_errors_exit_2(self):
        for args in (('reach', '-'), ('journeys', '-'), ('journeys', '-', '-', '--threshold', '0'),
                     ('journeys', '-', '-', '--threshold', 'nan'), ('journeys', '-', '-', '--weighting', 'squared')):
            with self.subTest(args=args):
                result = codegraph(*args)
                self.assertEqual((result.returncode, result.stdout), (2, b''))


if __name__ == '__main__':
    unittest.main()

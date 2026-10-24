"""Layer A reads a revision's files from Git objects only, and fails whole."""
import os
import tempfile
import unittest
from pathlib import Path

from codegraph import git
from codegraph.errors import CodegraphError
from tests.gitrepo import git as run_git
from tests.gitrepo import make_repo


class GitTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name) / 'repo'
        self.first, self.second = make_repo(
            self.repo,
            {'pkg/__init__.py': '', 'pkg/a.py': 'A = 1\n', 'pkgextra/b.py': '', 'other/c.py': ''},
            {'pkg/a.py': 'A = 2\n', 'pkg/new.py': ''})

    def test_resolve_names_the_full_commit(self):
        self.assertEqual(git.resolve(self.repo, 'HEAD'), git.Revision(self.second))
        self.assertEqual(git.resolve(self.repo, 'main~1'), git.Revision(self.first))
        self.assertEqual(git.resolve(self.repo, self.first[:10]), git.Revision(self.first))

    def test_an_unknown_or_option_like_ref_is_unknown(self):
        for ref in ('no-such-branch', '', '-h', '--all', 'HEAD~9'):
            with self.subTest(ref=ref), self.assertRaises(CodegraphError) as raised:
                git.resolve(self.repo, ref)
            self.assertEqual(raised.exception.code, 'unknown-ref')

    def test_a_directory_that_is_not_a_repository_fails(self):
        with self.assertRaises(CodegraphError) as raised:
            git.resolve(Path(self.tmp.name), 'HEAD')
        self.assertEqual(raised.exception.code, 'git-failed')

    def test_tree_lists_the_files_under_the_scope_at_that_revision(self):
        paths = [item.path for item in git.tree(self.repo, git.Revision(self.first), 'pkg')]
        self.assertEqual(paths, ['pkg/__init__.py', 'pkg/a.py'], 'a sibling with the same prefix (pkgextra) is not in scope')
        later = [item.path for item in git.tree(self.repo, git.Revision(self.second), 'pkg')]
        self.assertEqual(later, ['pkg/__init__.py', 'pkg/a.py', 'pkg/new.py'])

    def test_a_scope_absent_at_a_revision_has_no_files(self):
        self.assertEqual(git.tree(self.repo, git.Revision(self.first), 'missing'), ())

    def test_blobs_are_the_committed_bytes_not_the_working_tree(self):
        (self.repo / 'pkg/a.py').write_text('A = "uncommitted"\n')
        blobs = {item.path: item for item in git.tree(self.repo, git.Revision(self.second), 'pkg')}
        self.assertEqual(git.blob(self.repo, blobs['pkg/a.py'].id), b'A = 2\n')
        old = {item.path: item for item in git.tree(self.repo, git.Revision(self.first), 'pkg')}
        self.assertEqual(git.blob(self.repo, old['pkg/a.py'].id), b'A = 1\n')

    def test_symlinks_are_left_out(self):
        os.symlink('a.py', self.repo / 'pkg/link.py')
        run_git(self.repo, 'add', '-A')
        run_git(self.repo, 'commit', '-q', '-m', 'link')
        paths = [item.path for item in git.tree(self.repo, git.resolve(self.repo, 'HEAD'), 'pkg')]
        self.assertNotIn('pkg/link.py', paths)

    def test_a_pathspec_character_in_the_scope_is_literal(self):
        self.assertEqual(git.tree(self.repo, git.Revision(self.first), 'pk*'), ())


if __name__ == '__main__':
    unittest.main()

"""``swhurl.docs`` compares the workstream record with Git, so a stale record fails a check.

Each rule gets a small tree that satisfies it and one that breaks it: the point of the
command is that it fails, so a test that only shows the passing case proves nothing.
"""
import io
import tempfile
import unittest
from pathlib import Path

from swhurl import ROOT, docs
from swhurl.report import Report
from swhurl.run import FakeRunner, Result, Runner

HEAD = ('| Workstream | Kind | State | Current slice | Next gate | Needs | Page | Paths |\n'
        '| --- | --- | --- | --- | --- | --- | --- | --- |\n')
BLOCK = 'Status: approved\nGate: a decision\nUncommitted: none\n'
PAGE = '# Alpha: status\n\n' + BLOCK + '\nWhere it stands.\n'


def row(name='Alpha', kind='main', state='approved', gate='a decision', needs='operator approval',
        page='[alpha status](alpha-status.md)', paths='`v2/`'):
    return f'| {name} | {kind} | {state} | a slice | {gate} | {needs} | {page} | {paths} |\n'


def fake(status='', diff='', catfile=None, pages='docs/alpha-status.md\n'):
    """A runner answering the four Git reads; ``catfile`` lists the commits that exist."""
    def batch(argv, stdin):
        known = catfile or set()
        answer = ''.join(f'{sha} commit 40\n' if sha in known else f'{sha} missing\n'
                         for sha in (stdin or '').splitlines() if sha)
        return Result(argv, 0, answer)
    return (FakeRunner()
            .on('git', 'status', '--porcelain', stdout=status)
            .on('git', 'diff', '--name-only', stdout=diff)
            .on('git', 'cat-file', '--batch-check', handler=batch)
            .on('git', 'ls-files', stdout=pages))


class Tree(unittest.TestCase):
    """Builds `docs/` in a temporary root and runs the check against it."""

    def check(self, table=None, page=PAGE, *, runner=None, files=('v2',), extra=None):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name)
        (root / 'docs').mkdir()
        (root / 'docs' / 'workstreams.md').write_text(
            '# Workstreams\n\n## Workstreams\n\n' + (table if table is not None else HEAD + row()))
        if page is not None:
            (root / 'docs' / 'alpha-status.md').write_text(page)
        for name in files:
            (root / name).mkdir(parents=True, exist_ok=True)
        for name, text in (extra or {}).items():
            (root / 'docs' / name).write_text(text)
        out = io.StringIO()
        report = Report(out=out)
        docs.check(runner or fake(), report, root=root)
        return report, out.getvalue()


class RegistryTests(Tree):
    def test_a_consistent_registry_passes(self):
        report, output = self.check(page=PAGE)
        self.assertEqual(report.failures, 0, output)

    def test_the_table_must_have_the_stated_columns(self):
        report, output = self.check(table=HEAD.replace('| Kind |', '| Sort |') + row())
        self.assertIn('columns are', output)
        self.assertGreaterEqual(report.failures, 1)

    def test_the_table_must_exist_once(self):
        report, output = self.check(table='No table here.\n')
        self.assertIn('expected exactly one table headed "| Workstream |", found 0', output)

    def test_a_row_must_have_every_cell(self):
        report, output = self.check(table=HEAD + row().replace('| a slice |', ''))
        self.assertIn('not 8', output)

    def test_kind_and_state_must_be_known(self):
        report, output = self.check(table=HEAD + row(kind='extra', state='nearly'))
        self.assertIn('Kind is', output)
        self.assertIn('State is', output)

    def test_a_row_without_a_next_gate_or_need_fails(self):
        report, output = self.check(table=HEAD + row(gate='—', needs=''))
        self.assertIn('Next gate is empty', output)
        self.assertIn('Needs is empty', output)

    def test_a_page_or_path_that_does_not_exist_fails(self):
        report, output = self.check(table=HEAD + row(page='[alpha status](missing.md)', paths='`absent/`'))
        self.assertIn('Page missing.md does not exist', output)
        self.assertIn('absent/', output)

    def test_a_named_heading_must_exist(self):
        report, output = self.check(table=HEAD + row(page='[alpha status](alpha-status.md#no-such-heading)'))
        self.assertIn('has no heading', output)

    def test_only_one_workstream_may_be_in_progress(self):
        two = HEAD + row(state='in progress') + row(name='Beta', state='in progress',
                                                    page='[beta status](alpha-status.md)')
        report, output = self.check(table=two, page=PAGE.replace('Status: approved', 'Status: in progress'))
        self.assertIn('2 workstreams are in progress', output)


class PageTests(Tree):
    def test_a_status_page_must_state_its_block(self):
        report, output = self.check(page='# Alpha: status\n\nNo block here.\n')
        self.assertIn('missing Status, Gate, Uncommitted', output)

    def test_the_page_and_registry_must_agree(self):
        page = PAGE.replace('Status: approved', 'Status: blocked').replace('Gate: a decision', 'Gate: something else')
        report, output = self.check(page=page)
        self.assertIn("Status is 'blocked' but the registry says 'approved'", output)
        self.assertIn("Gate is 'something else'", output)

    def test_a_status_page_no_row_names_fails(self):
        listed = 'docs/alpha-status.md\ndocs/other-status.md\n'
        report, output = self.check(page=PAGE, extra={'other-status.md': PAGE}, runner=fake(pages=listed))
        self.assertIn('docs/other-status.md: a status page no row of', output)

    def test_a_plain_page_needs_no_status_block(self):
        report, output = self.check(table=HEAD + row(page='[plan](plan.md)'), page=None, runner=fake(pages=''),
                                    extra={'plan.md': '# Plan\n\nNo status block.\n'})
        self.assertEqual(report.failures, 0, output)


class GitTests(Tree):
    def test_uncommitted_none_with_dirty_paths_fails(self):
        report, output = self.check(page=PAGE, runner=fake(status=' M v2/kernel/render.go\n'))
        self.assertIn('says none, but Alpha has uncommitted work', output)

    def test_named_uncommitted_paths_must_be_dirty(self):
        page = PAGE.replace('Uncommitted: none', 'Uncommitted: `v2/kernel/render.go`')
        report, output = self.check(page=page)
        self.assertIn('but nothing under v2/', output)

    def test_uncommitted_must_not_omit_dirty_paths(self):
        page = PAGE.replace('Uncommitted: none', 'Uncommitted: `v2/other.go`')
        report, output = self.check(page=page, runner=fake(status=' M v2/kernel/render.go\n'))
        self.assertIn('Uncommitted omits v2/kernel/render.go', output)

    def test_named_uncommitted_paths_that_match_pass(self):
        page = PAGE.replace('Uncommitted: none', 'Uncommitted: `v2/kernel/render.go`')
        report, output = self.check(page=page, runner=fake(status=' M v2/kernel/render.go\n'))
        self.assertEqual(report.failures, 0, output)

    def test_a_cited_commit_must_exist(self):
        page = PAGE + '\nCommitted in `abcdef1234`.\n'
        report, output = self.check(page=page, runner=fake(catfile={'deadbeef'}))
        self.assertIn('cites abcdef1234, which is not a commit', output)
        report, output = self.check(page=page, runner=fake(catfile={'abcdef1234'}))
        self.assertEqual(report.failures, 0, output)

    def test_changing_a_workstream_without_its_page_fails(self):
        report, output = self.check(page=PAGE, runner=fake(diff='v2/kernel/render.go\n'))
        self.assertIn('changed without docs/alpha-status.md', output)
        report, output = self.check(page=PAGE, runner=fake(diff='v2/kernel/render.go\ndocs/alpha-status.md\n'))
        self.assertEqual(report.failures, 0, output)

    def test_a_change_elsewhere_needs_no_page(self):
        report, output = self.check(page=PAGE, runner=fake(diff='apps/demo/instance.yaml\n'))
        self.assertEqual(report.failures, 0, output)


class RepositoryTests(unittest.TestCase):
    """The real tree: the registry and its status pages must satisfy every rule."""

    def test_this_repository_passes(self):
        report = Report(out=io.StringIO())
        docs.check(Runner(cwd=ROOT), report)
        self.assertEqual(report.failures, 0, '\n'.join(report.lines))

    def test_every_workstream_page_is_listed(self):
        rows, problems = docs.registry_rows((ROOT / docs.REGISTRY).read_text())
        self.assertEqual(problems, [])
        self.assertGreaterEqual(len(rows), 3)
        for row in rows:
            self.assertTrue(docs.page_file(row, ROOT).is_file(), row.page)

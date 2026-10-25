"""Keep the workstream record honest: one table, one page per workstream, checked against Git.

Progress drifts because status is prose: a page says "pending review" after the review, or
a sidequest quietly becomes the main line, and only re-reading finds it. So the state lives
once, in a table — `docs/workstreams.md` — and this command compares it with the repository:

  ERROR  the table's columns, a row's kind or state, or an empty Next gate or Needs
  ERROR  more than one workstream in progress at a time
  ERROR  a Page that does not resolve (file, and heading when it names one), or Paths that do not exist
  ERROR  a `docs/*-status.md` page no row names, or a row/page disagreement on state or gate
  ERROR  "Uncommitted: none" while that workstream's paths are dirty, or a named path that is clean
  ERROR  a commit cited on a status page that is not in this history
  ERROR  a change under a workstream's paths that does not also change its page

Nothing here judges whether a contract is good. It catches the record going stale, which is
what makes a long migration hard to resume.

Exit status is non-zero when an ERROR is found.
"""
from __future__ import annotations

import re
import sys
from dataclasses import dataclass
from pathlib import Path

from swhurl import ROOT
from swhurl.report import Report
from swhurl.run import CommandError, Runner
from swhurl.validate import LINK, anchors

REGISTRY = 'docs/workstreams.md'
COLUMNS = ('Workstream', 'Kind', 'State', 'Current slice', 'Next gate', 'Needs', 'Page', 'Paths')
KINDS = ('main', 'sidequest')
STATES = ('proposed', 'approved', 'in progress', 'blocked', 'parked', 'done')
# A status page states these; the registry must agree with the two it also states.
BLOCK_FIELDS = ('Status', 'Gate', 'Uncommitted')
STATUS_SUFFIX = '-status.md'
CITED = re.compile(r'`([0-9a-f]{7,40})`')
HEAD = 25  # a status page states its block near the top
EMPTY = ('', '—', '-')


@dataclass(frozen=True)
class Row:
    name: str
    kind: str
    state: str
    current: str
    gate: str
    needs: str
    page: str  # docs/-relative path, plus an optional #anchor
    paths: tuple[str, ...]


def page_file(row: Row, root: Path) -> Path:
    return root / 'docs' / row.page.partition('#')[0]


def status_page(row: Row, root: Path) -> bool:
    return page_file(row, root).name.endswith(STATUS_SUFFIX)


def cells(line: str) -> list[str]:
    return [cell.strip() for cell in line.strip().strip('|').split('|')]


def registry_rows(text: str) -> tuple[list[Row], list[str]]:
    """Parse the one table headed COLUMNS; return its rows and any structural problem."""
    lines = text.splitlines()
    starts = [i for i, line in enumerate(lines) if line.strip().startswith('| Workstream |')]
    if len(starts) != 1:
        return [], [f'{REGISTRY}: expected exactly one table headed "| Workstream |", found {len(starts)}']
    header = cells(lines[starts[0]])
    if tuple(header) != COLUMNS:
        return [], [f'{REGISTRY}: columns are {", ".join(COLUMNS)}; found {", ".join(header)}']
    problems, rows = [], []
    for line in lines[starts[0] + 2:]:  # past the header and its separator
        if not line.strip().startswith('|'):
            break
        row = cells(line)
        if len(row) != len(COLUMNS):
            problems.append(f'{REGISTRY}: a row has {len(row)} cells, not {len(COLUMNS)}: {line.strip()[:80]}')
            continue
        name, kind, state, current, gate, needs, page, paths = row
        found = LINK.search(page)
        if not found:
            problems.append(f'{REGISTRY}: {name}: Page must be a Markdown link, found {page!r}')
            continue
        wanted = [p.strip().strip('`') for p in paths.split(',')]
        rows.append(Row(name, kind, state, current, gate, needs, found.group(1),
                        tuple(p for p in wanted if p not in EMPTY)))
    if not rows and not problems:
        problems.append(f'{REGISTRY}: the table has no rows')
    return rows, problems


def status_block(page: Path) -> dict[str, str]:
    """The `Key: value` lines a status page states near the top, first occurrence winning."""
    stated = {}
    for line in page.read_text().splitlines()[:HEAD]:
        key, sep, value = line.partition(':')
        if sep and key in BLOCK_FIELDS and key not in stated:
            stated[key] = value.strip()
    return stated


def row_problems(row: Row, root: Path = ROOT) -> list[str]:
    found, where = [], f'{REGISTRY}: {row.name}'
    if row.kind not in KINDS:
        found.append(f'{where}: Kind is {row.kind!r}; expected one of {", ".join(KINDS)}')
    if row.state not in STATES:
        found.append(f'{where}: State is {row.state!r}; expected one of {", ".join(STATES)}')
    for label, value in (('Next gate', row.gate), ('Needs', row.needs)):
        if value in EMPTY:
            found.append(f'{where}: {label} is empty; state what finishes it or what it waits for')
    path, _, anchor = row.page.partition('#')
    target = root / 'docs' / path
    if not target.is_file():
        found.append(f'{where}: Page {path} does not exist')
    elif anchor and anchor not in anchors(target):
        found.append(f'{where}: Page {path} has no heading {anchor}')
    for tracked in row.paths:
        if not (root / tracked).exists():
            found.append(f'{where}: Paths names {tracked}, which does not exist')
    return found


def block_problems(row: Row, root: Path = ROOT) -> list[str]:
    """A status page states its own state and gate; the registry must agree with it."""
    where = str(page_file(row, root).relative_to(root))
    stated = status_block(page_file(row, root))
    missing = [key for key in BLOCK_FIELDS if key not in stated]
    if missing:
        return [f'{where}: a status page states {", ".join(BLOCK_FIELDS)} in its first {HEAD} lines; missing {", ".join(missing)}']
    found = []
    if stated['Status'] != row.state:
        found.append(f'{where}: Status is {stated["Status"]!r} but the registry says {row.state!r}')
    if stated['Gate'] != row.gate:
        found.append(f'{where}: Gate is {stated["Gate"]!r} but the registry says {row.gate!r}')
    return found


def tracked_problems(rows: list[Row], runner: Runner, root: Path = ROOT) -> list[str]:
    """Every path a row owns must hold a tracked file, not merely exist on this disk.

    An empty directory can exist in one checkout and nowhere else, so a claim it satisfies would
    pass locally and fail on a fresh clone -- which is exactly what a reader does.
    """
    wanted = [(row.name, path) for row in rows for path in row.paths]
    if not wanted:
        return []
    files = runner.output(['git', 'ls-files', '--', *[path for _, path in wanted]]).splitlines()
    return [f'{REGISTRY}: {name}: Paths names {path}, which holds no tracked file'
            for name, path in wanted
            if not any(file == path or file.startswith(path.rstrip('/') + '/') for file in files)]


def uncommitted_problems(rows: list[Row], runner: Runner, root: Path = ROOT) -> list[str]:
    """`Uncommitted: none` must match a clean workstream, and a named path must be dirty."""
    found = []
    for row in (r for r in rows if status_page(r, root) and page_file(r, root).is_file()):
        stated = status_block(page_file(row, root)).get('Uncommitted', '')
        scope = list(row.paths) or ['.']
        dirty = [line[3:].strip() for line in runner.output(['git', 'status', '--porcelain', '--', *scope]).splitlines()]
        where = f'{page_file(row, root).relative_to(root)}: Uncommitted'
        if stated in EMPTY or stated == 'none':
            if dirty:
                found.append(f'{where} says none, but {row.name} has uncommitted work: {", ".join(dirty[:5])}')
            continue
        if not dirty:
            found.append(f'{where} says {stated!r}, but nothing under {", ".join(scope)} is uncommitted')
            continue
        named = [p.strip().strip('`') for p in re.split(r'[,;]', stated)]
        stray = [d for d in dirty if not any(d == n or d.startswith(n.rstrip("/") + "/") for n in named)]
        if stray:
            found.append(f'{where} omits {", ".join(stray[:5])}')
    return found


def shallow(runner: Runner) -> bool:
    """Whether the checkout has no history to check a citation against (a shallow CI clone)."""
    return runner.output(['git', 'rev-parse', '--is-shallow-repository']).strip() == 'true'


def cited_problems(rows: list[Row], runner: Runner, root: Path = ROOT) -> list[str]:
    """Every commit a status page cites must be reachable from a ref, not merely in the object store.

    A rewritten commit still resolves locally through the reflog, so `git cat-file` says it exists
    while no reader of the repository can find it. Reachability is what a reader has.
    """
    cited: dict[str, str] = {}
    for row in (r for r in rows if status_page(r, root) and page_file(r, root).is_file()):
        for sha in CITED.findall(page_file(row, root).read_text()):
            cited.setdefault(sha, str(page_file(row, root).relative_to(root)))
    if not cited:
        return []
    reachable = runner.output(['git', 'rev-list', '--all']).splitlines()
    return [f'{page}: cites {sha}, which no ref reaches (rewritten by a rebase, or never committed)'
            for sha, page in cited.items() if not any(commit.startswith(sha) for commit in reachable)]


def changed_problems(rows: list[Row], runner: Runner, root: Path = ROOT) -> list[str]:
    """A change under a workstream's paths must also update that workstream's page."""
    changed = runner.output(['git', 'diff', '--name-only', 'HEAD']).splitlines()
    found = []
    for row in (r for r in rows if r.paths):
        touched = [c for c in changed if any(c == p or c.startswith(p.rstrip('/') + '/') for p in row.paths)]
        if touched and str(page_file(row, root).relative_to(root)) not in changed:
            found.append(f'{", ".join(touched[:3])} changed without {page_file(row, root).relative_to(root)}: '
                         'record the slice on its page in the same change')
    return found


def orphan_problems(rows: list[Row], runner: Runner, root: Path = ROOT) -> list[str]:
    """A `*-status.md` page no row names is a second, unchecked record of progress."""
    pages = {row.page.partition('#')[0] for row in rows}
    return [f'{name}: a status page no row of {REGISTRY} names'
            for name in runner.output(['git', 'ls-files', 'docs/*' + STATUS_SUFFIX]).split()
            if name.removeprefix('docs/') not in pages]


def summarize(report: Report, problems: list[str], okay: str) -> None:
    for problem in problems:
        report.bad(problem)
    if not problems:
        report.ok(okay)


def check(runner: Runner, report: Report, root: Path = ROOT) -> None:
    registry = root / REGISTRY
    if not registry.is_file():
        report.bad(f'{REGISTRY} is missing')
        return
    rows, problems = registry_rows(registry.read_text())
    summarize(report, problems, f'{REGISTRY} declares {len(rows)} workstreams')
    summarize(report, [p for row in rows for p in row_problems(row, root)],
              'every workstream names a page that resolves, a next gate and what it waits for')
    working = [row.name for row in rows if row.state == 'in progress']
    summarize(report, [f'{len(working)} workstreams are in progress ({", ".join(working)}); one at a time']
              if len(working) > 1 else [], f'{len(working)} workstream(s) in progress')
    pages = [row for row in rows if status_page(row, root) and page_file(row, root).is_file()]
    summarize(report, [p for row in pages for p in block_problems(row, root)],
              f'{len(pages)} status page(s) agree with the registry')
    summarize(report, tracked_problems(rows, runner, root), 'every owned path holds a tracked file')
    summarize(report, uncommitted_problems(rows, runner, root), 'uncommitted work matches each status page')
    if shallow(runner):
        report.warn('the checkout is shallow, so cited commits were not checked against history')
    else:
        summarize(report, cited_problems(rows, runner, root), 'every cited commit is reachable from a ref')
    summarize(report, changed_problems(rows, runner, root), 'no workstream code changed without its page')
    summarize(report, orphan_problems(rows, runner, root), 'every status page is named by the registry')


def main(argv: list[str] | None = None, runner: Runner | None = None) -> int:
    runner = runner or Runner(cwd=ROOT)
    report = Report(redact=runner.redact)
    try:
        check(runner, report)
    except CommandError as exc:
        print(f'[ERROR] {exc}', file=sys.stderr)
        return 2
    print(f'\n{report.failures} error(s), {report.warnings} warning(s).')
    return 1 if report.failures else 0

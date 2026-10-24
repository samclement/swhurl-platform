"""The command line (layer F): one subcommand per primitive that produces a document.

Each subcommand writes exactly one document (or, with ``--text``, a rendering of it) to stdout.
Analysis subcommands read documents from a path or ``-`` (stdin) and never read Git. Exit 0 on
success, 1 on failure and 2 on a usage error; on failure stdout stays empty and stderr has one
line with the error code.
"""
from __future__ import annotations

import argparse
import posixpath
import sys
from collections import Counter
from collections.abc import Sequence
from pathlib import Path

from codegraph import documents, git, python_ast
from codegraph import entries as entry_points
from codegraph import reach as reaches
from codegraph import snapshot as snapshots
from codegraph.entries import Entries, Evidence
from codegraph.errors import CodegraphError
from codegraph.reach import Reach
from codegraph.snapshot import Snapshot


def build_snapshot(repo: Path, ref: str, scope: str) -> Snapshot:
    """The composition in docs/visual-change-tool.md section 4: resolve, list, read, extract, link."""
    revision = git.resolve(repo, ref)
    import_root = posixpath.dirname(scope)
    files = (python_ast.extract(python_ast.module_name(item.path, import_root), item.path, git.blob(repo, item.id))
             for item in git.tree(repo, revision, scope) if item.path.endswith('.py'))
    return python_ast.link(revision.commit, scope, import_root, files)


def scope_path(value: str) -> str:
    """A '/'-separated directory or file below the repository root, without '.', '..' or a leading '/'."""
    parts = value.strip('/').split('/') if value.strip('/') else []
    if not parts or value.startswith('/') or any(part in ('', '.', '..') for part in parts):
        raise argparse.ArgumentTypeError('expected a path below the repository root, such as tools/swhurl')
    return '/'.join(parts)


def read_snapshot(source: str) -> Snapshot:
    """The snapshot document at a path, or on stdin for '-'."""
    try:
        data = sys.stdin.buffer.read() if source == '-' else Path(source).read_bytes()
    except OSError as error:
        raise CodegraphError('usage', f'cannot read {source}: {error.strerror}') from error
    return snapshots.load(data)


def snapshot_text(snapshot: Snapshot) -> str:
    kinds = Counter(node.kind for node in snapshot.nodes)
    edges = Counter(edge.kind for edge in snapshot.edges)
    unresolved: Counter[str] = Counter()
    for node in snapshot.nodes:
        unresolved.update(dict(node.unresolved_calls))
    external = sum(node.external_calls for node in snapshot.nodes)
    lines = [
        f'snapshot {snapshot.revision}  scope {snapshot.scope}  ({snapshot.extractor} {snapshot.extractor_version}, '
        f'Python {snapshot.python})',
        f'nodes      {kinds["module"]} modules, {kinds["class"]} classes, {kinds["function"]} functions',
        'edges      ' + (', '.join(f'{kind} {edges[kind]}' for kind in sorted(edges)) or 'none'),
        f'calls      external {external}, unresolved {sum(unresolved.values())}'
        + (' (' + ', '.join(f'{category} {count}' for category, count in sorted(unresolved.items())) + ')' if unresolved else ''),
        f'facts      object references {len(snapshot.object_references)}, arguments {len(snapshot.arguments)}, '
        f'main blocks {sum(len(node.main_blocks) for node in snapshot.nodes)}',
        f'unparsed   {len(snapshot.unparsed)}',
        *(f'  {item.path}: {item.error}' for item in snapshot.unparsed),
    ]
    return '\n'.join(lines) + '\n'


def entries_text(result: Entries) -> str:
    rules = Counter(item.rule for entry in result.entries for item in entry.evidence)

    def where(evidence: Evidence) -> str:
        value = f'  {evidence.value!r}' if evidence.value is not None else ''
        return f'{evidence.rule}  {evidence.path}:{evidence.line}{value}'

    lines = [
        f'entries    snapshot {result.snapshot.revision}  rules {", ".join(result.rules)}',
        f'{len(result.entries)} entry points; evidence ' + ', '.join(f'{rule} {rules[rule]}' for rule in result.rules),
        *(f'  {entry.node}  ' + '; '.join(where(item) for item in entry.evidence) for entry in result.entries),
        f'ambiguous  {len(result.ambiguous)}',
        *(f'  {where(match.evidence)} -> {", ".join(match.candidates)}' for match in result.ambiguous),
        f'unresolved {len(result.unresolved)}',
        *(f'  {where(match.evidence)}' for match in result.unresolved),
    ]
    return '\n'.join(lines) + '\n'


def reach_text(result: Reach) -> str:
    unresolved = dict(result.unresolved_calls)
    lines = [
        f'reach      {result.node}  snapshot {result.snapshot.revision}  over {", ".join(reaches.EDGE_KINDS)}',
        f'reached    {len(result.reached) - 1} nodes besides itself; external calls {result.external_calls}; '
        f'unresolved calls {sum(unresolved.values())}'
        + (' (' + ', '.join(f'{category} {count}' for category, count in unresolved.items()) + ')' if unresolved else '')
        + ': a lower bound',
        f'spine      {len(result.spine)} steps',
        *(f'  {step.line:>5}  {step.kind:<10}  {step.target}' for step in result.spine),
        'reached',
        *(f'  {node}' for node in result.reached if node != result.node),
    ]
    return '\n'.join(lines) + '\n'


def run_snapshot(args: argparse.Namespace) -> bytes:
    result = build_snapshot(args.repo, args.ref, args.scope)
    return snapshot_text(result).encode() if args.text else snapshots.dump(result)


def run_entries(args: argparse.Namespace) -> bytes:
    result = entry_points.entries(read_snapshot(args.snapshot))
    return entries_text(result).encode() if args.text else documents.dump(entry_points.to_document(result))


def run_reach(args: argparse.Namespace) -> bytes:
    result = reaches.reach(read_snapshot(args.snapshot), args.node)
    return reach_text(result).encode() if args.text else documents.dump(reaches.to_document(result))


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(
        prog='codegraph',
        description='Derive dependency graphs, journeys, hotspots and changes between commits from source in Git.')
    commands = result.add_subparsers(dest='command', metavar='<command>')
    snapshot = commands.add_parser('snapshot', help="One revision's modules, classes, functions and their edges",
                                   description='Read a revision from Git objects and write its snapshot document.')
    snapshot.add_argument('ref', help='A commit, branch or tag; resolved once to a commit ID')
    snapshot.add_argument('--scope', required=True, type=scope_path,
                          help="The package directory to read, such as tools/swhurl; modules are named from its parent")
    snapshot.add_argument('--repo', type=Path, default=Path('.'), help='The repository (default: the current directory)')
    snapshot.add_argument('--text', action='store_true', help='A short summary for a person instead of the document')
    snapshot.set_defaults(run=run_snapshot)

    entries = commands.add_parser('entries', help='The entry points of a snapshot, each found by a named rule',
                                  description='Find the functions the outside world starts; write the entries document.')
    entries.add_argument('snapshot', help="A snapshot document's path, or - for stdin")
    entries.add_argument('--text', action='store_true', help='A listing for a person instead of the document')
    entries.set_defaults(run=run_entries)

    reach = commands.add_parser('reach', help='What one node reaches over calls and references, and its spine',
                                description='Follow calls and references from a node; write the reach document.')
    reach.add_argument('snapshot', help="A snapshot document's path, or - for stdin")
    reach.add_argument('node', help='A node ID, such as py:swhurl.apps.cli:main_promote')
    reach.add_argument('--text', action='store_true', help='A listing for a person instead of the document')
    reach.set_defaults(run=run_reach)
    return result


def main(argv: Sequence[str] | None = None) -> int:
    args = parser().parse_args(argv)  # argparse exits 2 on a usage error, with the message on stderr
    if args.command is None:
        parser().error('a command is required')
    try:
        output = args.run(args)
    except CodegraphError as error:
        print(f'codegraph: {error}', file=sys.stderr)
        return 1
    sys.stdout.buffer.write(output)
    sys.stdout.flush()
    return 0

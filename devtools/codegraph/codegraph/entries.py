"""Layer E: entry points, the functions the outside world starts, each found by a named rule.

The rules are generic Python or framework knowledge, one function each in ``RULES``. A rule yields
matches: a match with exactly one candidate is an entry point, with none it is unresolved and with
more it is ambiguous. Both are reported, never chosen. Several matches for one function become
one entry point with all their evidence.
"""
from __future__ import annotations

from collections import defaultdict
from collections.abc import Callable, Iterator, Mapping
from dataclasses import dataclass
from typing import Any

from codegraph import snapshot as snapshots
from codegraph.documents import Input
from codegraph.snapshot import Node, Snapshot

KIND = 'entries'
VERSION = 1
ROUTE_CALLEES = frozenset({'starlette.routing.Route'})
ROUTE_HANDLER_SLOTS = frozenset({'1', 'endpoint'})


@dataclass(frozen=True)
class Evidence:
    rule: str
    path: str
    line: int
    value: str | None  # what the rule matched (the string, the route path); None when there is nothing to show


@dataclass(frozen=True)
class Match:
    evidence: Evidence
    candidates: tuple[str, ...]  # node IDs, sorted


@dataclass(frozen=True)
class Entry:
    node: str
    evidence: tuple[Evidence, ...]


@dataclass(frozen=True)
class Entries:
    snapshot: Input
    rules: tuple[str, ...]
    entries: tuple[Entry, ...]
    ambiguous: tuple[Match, ...]
    unresolved: tuple[Match, ...]


def entry_strings(snapshot: Snapshot, nodes: Mapping[str, Node]) -> Iterator[Match]:
    """``'pkg.module:function'`` string literals (setuptools entry points, command tables). The module part
    may be a suffix of the module's name (``'cli:main'`` matches ``pkg.cli`` and ``pkg.apps.cli``)."""
    functions = [node for node in nodes.values() if node.kind == 'function']
    for item in snapshot.object_references:
        module, qualname = item.value.split(':')
        candidates = tuple(sorted(node.id for node in functions if node.qualname == qualname
                                  and (node.module == module or node.module.endswith('.' + module))))
        yield Match(Evidence('entry-string', nodes[item.source].path, item.line, item.value), candidates)


def main_blocks(snapshot: Snapshot, nodes: Mapping[str, Node]) -> Iterator[Match]:
    """A function called from a module's ``if __name__ == '__main__':`` block."""
    for edge in snapshot.edges:
        source, target = nodes[edge.source], nodes[edge.target]
        if edge.kind != 'calls' or source.kind != 'module' or target.kind != 'function':
            continue
        lines = [line for line in edge.lines if any(start <= line <= end for start, end in source.main_blocks)]
        if lines:
            yield Match(Evidence('main-block', source.path, lines[0], None), (target.id,))


def starlette_routes(snapshot: Snapshot, nodes: Mapping[str, Node]) -> Iterator[Match]:
    """``Route('<path>', handler)`` with a function in scope as the handler. A handler built by a call
    (``Route('/x', wrap(f))``) or a class endpoint is not a match."""
    for item in snapshot.arguments:
        if item.callee in ROUTE_CALLEES and item.slot in ROUTE_HANDLER_SLOTS and nodes[item.target].kind == 'function':
            literals = dict(item.literals)
            yield Match(Evidence('starlette-route', nodes[item.source].path, item.line,
                                 literals.get('0', literals.get('path'))), (item.target,))


RULES: tuple[tuple[str, Callable[[Snapshot, Mapping[str, Node]], Iterator[Match]]], ...] = (
    ('entry-string', entry_strings),
    ('main-block', main_blocks),
    ('starlette-route', starlette_routes),
)


def entries(snapshot: Snapshot) -> Entries:
    nodes = {node.id: node for node in snapshot.nodes}
    found: dict[str, set[Evidence]] = defaultdict(set)
    ambiguous: set[Match] = set()
    unresolved: set[Match] = set()
    for _, rule in RULES:
        for match in rule(snapshot, nodes):
            if len(match.candidates) == 1:
                found[match.candidates[0]].add(match.evidence)
            else:
                (ambiguous if match.candidates else unresolved).add(match)
    return Entries(snapshots.as_input(snapshot), tuple(name for name, _ in RULES),
                   tuple(Entry(node, tuple(sorted(evidence, key=_evidence_key))) for node, evidence in sorted(found.items())),
                   tuple(sorted(ambiguous, key=_match_key)), tuple(sorted(unresolved, key=_match_key)))


def to_document(result: Entries) -> dict[str, Any]:
    return {
        'kind': KIND, 'version': VERSION, 'inputs': [result.snapshot.to_document()], 'params': {'rules': list(result.rules)},
        'entries': [{'node': entry.node, 'evidence': [_evidence(item) for item in entry.evidence]} for entry in result.entries],
        'ambiguous': [{**_evidence(match.evidence), 'candidates': list(match.candidates)} for match in result.ambiguous],
        'unresolved': [_evidence(match.evidence) for match in result.unresolved],
    }


def _evidence(evidence: Evidence) -> dict[str, Any]:
    result = {'rule': evidence.rule, 'path': evidence.path, 'line': evidence.line}
    if evidence.value is not None:
        result['value'] = evidence.value
    return result


def _evidence_key(evidence: Evidence) -> tuple[str, str, int, str]:
    return evidence.rule, evidence.path, evidence.line, evidence.value or ''


def _match_key(match: Match) -> tuple[tuple[str, str, int, str], tuple[str, ...]]:
    return _evidence_key(match.evidence), match.candidates

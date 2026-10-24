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

from codegraph import documents
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


def as_input(result: Entries) -> Input:
    return Input(KIND, documents.digest(to_document(result)))


def to_document(result: Entries) -> dict[str, Any]:
    return {
        'kind': KIND, 'version': VERSION, 'inputs': [result.snapshot.to_document()], 'params': {'rules': list(result.rules)},
        'entries': [{'node': entry.node, 'evidence': [_evidence(item) for item in entry.evidence]} for entry in result.entries],
        'ambiguous': [{**_evidence(match.evidence), 'candidates': list(match.candidates)} for match in result.ambiguous],
        'unresolved': [_evidence(match.evidence) for match in result.unresolved],
    }


def from_document(document: Mapping[str, Any]) -> Entries:
    """The entries in a loaded envelope (``documents.load(data, KIND)``); refuses anything malformed. That the
    nodes belong to the named snapshot is the reader's check: this document alone cannot show it."""
    top = documents.fields(document, 'document', {'kind': str, 'version': int, 'inputs': list, 'params': dict,
                                                  'entries': list, 'ambiguous': list, 'unresolved': list})
    if len(top['inputs']) != 1:
        documents.invalid('inputs', 'expected the one snapshot the entries come from')
    source = Input.from_document(top['inputs'][0], 'inputs[0]')
    if source.kind != snapshots.KIND:
        documents.invalid('inputs[0].kind', 'expected a snapshot')
    rules = documents.fields(top['params'], 'params', {'rules': list})['rules']
    if not all(isinstance(rule, str) for rule in rules) or len(set(rules)) != len(rules):
        documents.invalid('params.rules', 'expected distinct rule names')

    def evidence(value: Any, where: str, extra: Mapping[str, type] | None = None) -> tuple[Evidence, dict]:
        item = documents.fields(value, where, {'rule': str, 'path': str, 'line': int, **(extra or {})}, {'value': str})
        if item['rule'] not in rules:
            documents.invalid(f'{where}.rule', 'not one of params.rules')
        if item['line'] < 1:
            documents.invalid(f'{where}.line', 'expected a positive line number')
        return Evidence(item['rule'], item['path'], item['line'], item.get('value')), item

    found = []
    for index, value in enumerate(top['entries']):
        item = documents.fields(value, f'entries[{index}]', {'node': str, 'evidence': list})
        if not item['evidence']:
            documents.invalid(f'entries[{index}].evidence', 'an entry point has at least one piece of evidence')
        found.append(Entry(item['node'], tuple(sorted(
            (evidence(part, f'entries[{index}].evidence[{position}]')[0] for position, part in enumerate(item['evidence'])),
            key=_evidence_key))))
    if len({entry.node for entry in found}) != len(found):
        documents.invalid('entries', 'an entry point appears twice')
    ambiguous = []
    for index, value in enumerate(top['ambiguous']):
        where = f'ambiguous[{index}]'
        place, item = evidence(value, where, {'candidates': list})
        candidates = item['candidates']
        if len(candidates) < 2 or not all(isinstance(node, str) for node in candidates) or len(set(candidates)) != len(candidates):
            documents.invalid(f'{where}.candidates', 'expected two or more distinct node IDs')
        ambiguous.append(Match(place, tuple(sorted(candidates))))
    unresolved = [Match(evidence(value, f'unresolved[{index}]')[0], ()) for index, value in enumerate(top['unresolved'])]
    if len(set(ambiguous)) != len(ambiguous) or len(set(unresolved)) != len(unresolved):
        documents.invalid('document', 'an ambiguous or unresolved match appears twice')
    return Entries(source, tuple(rules), tuple(sorted(found, key=lambda entry: entry.node)),
                   tuple(sorted(ambiguous, key=_match_key)), tuple(sorted(unresolved, key=_match_key)))


def load(data: bytes) -> Entries:
    return from_document(documents.load(data, KIND))


def _evidence(evidence: Evidence) -> dict[str, Any]:
    result = {'rule': evidence.rule, 'path': evidence.path, 'line': evidence.line}
    if evidence.value is not None:
        result['value'] = evidence.value
    return result


def _evidence_key(evidence: Evidence) -> tuple[str, str, int, str]:
    return evidence.rule, evidence.path, evidence.line, evidence.value or ''


def _match_key(match: Match) -> tuple[tuple[str, str, int, str], tuple[str, ...]]:
    return _evidence_key(match.evidence), match.candidates

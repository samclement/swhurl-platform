"""Layer E: journey candidates, groups of entry points that share distinctive reach.

Each entry point's reach (over ``reach.EDGE_KINDS``) is reduced to the functions in it. A function
weighs ``ln(N / k)``, where N is the number of entry points and k how many of them reach it, so
plumbing reached by every entry point weighs nothing (``uniform`` weighting gives every function 1).
Two entry points' similarity is the weighted Jaccard of their functions, rounded to 4 places. A pair
is linked when each is the other's nearest (its similarity is the highest either has; ties all count)
and it is at least the threshold. Each connected group is a candidate, an unlinked entry point a
candidate of one: groups beyond pairs come only from ties, so a shared subsystem does not chain entry
points together. Evidence of shared code, not of a shared user goal; names are not inferred.
"""
from __future__ import annotations

import math
from collections import Counter
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

from codegraph import documents
from codegraph import entries as entry_points
from codegraph import reach as reaches
from codegraph import snapshot as snapshots
from codegraph.documents import Input
from codegraph.entries import Entries
from codegraph.errors import CodegraphError
from codegraph.graph import Graph, closure, graph
from codegraph.snapshot import Snapshot

KIND = 'journeys'
VERSION = 1
SIMILARITY = 'weighted-jaccard'
GROUPING = 'mutual-nearest'
WEIGHTINGS = ('inverse', 'uniform')
"""``inverse``: ln(N / k), k the entry points reaching the function; ``uniform``: 1 (plain Jaccard)."""


@dataclass(frozen=True)
class Params:
    threshold: float = 0.3
    weighting: str = 'inverse'


DEFAULTS = Params()


@dataclass(frozen=True)
class Nearest:
    nodes: tuple[str, ...]  # every other entry point at the highest similarity, sorted
    similarity: float  # above 0


@dataclass(frozen=True)
class Member:
    node: str
    values: tuple[str, ...]  # what its entry rules matched (command strings, route paths), sorted
    reached: int  # functions reached, itself included
    unresolved_calls: tuple[tuple[str, int], ...]  # (category, count) across everything reached: a lower bound
    nearest: Nearest | None  # None when no other entry point shares any weighted reach (or there is none)


@dataclass(frozen=True)
class Link:
    a: str  # a < b
    b: str
    similarity: float


@dataclass(frozen=True)
class Shared:
    node: str
    entry_points: int  # how many entry points reach it, across the snapshot


@dataclass(frozen=True)
class Candidate:
    members: tuple[Member, ...]  # by node ID
    links: tuple[Link, ...]  # by (a, b)
    shared: tuple[Shared, ...]  # functions every member reaches, fewest entry points first, then by ID


@dataclass(frozen=True)
class Journeys:
    snapshot: Input
    entries: Input
    params: Params
    candidates: tuple[Candidate, ...]  # largest first, then by first member


def journeys(snapshot: Snapshot, entries: Entries, params: Params = DEFAULTS) -> Journeys:
    if not 0 < params.threshold <= 1:
        raise CodegraphError('usage', 'the threshold must be above 0 and at most 1')
    if params.weighting not in WEIGHTINGS:
        raise CodegraphError('usage', f'weighting must be one of {", ".join(WEIGHTINGS)}')
    source = snapshots.as_input(snapshot)
    if entries.snapshot.digest != source.digest:
        raise CodegraphError('invalid-document', 'entries: derived from a different snapshot')
    nodes = {node.id: node for node in snapshot.nodes}
    view = graph(snapshot, reaches.EDGE_KINDS)
    reached = {entry.node: closure(view, [entry.node]) for entry in entries.entries}
    functions = {entry: frozenset(item for item in items if nodes[item].kind == 'function') for entry, items in reached.items()}
    counts = Counter(item for items in functions.values() for item in items)
    total = len(functions)
    weight = {item: (math.log(total / count) if params.weighting == 'inverse' else 1.0) for item, count in counts.items()}

    def similarity(a: str, b: str) -> float:
        union = sum(weight[item] for item in sorted(functions[a] | functions[b]))
        return round(sum(weight[item] for item in sorted(functions[a] & functions[b])) / union, 4) if union else 0.0

    ordered = sorted(functions)
    pairs = {(a, b): similarity(a, b) for index, a in enumerate(ordered) for b in ordered[index + 1:]}
    best = dict.fromkeys(ordered, 0.0)
    for (a, b), score in pairs.items():
        best[a], best[b] = max(best[a], score), max(best[b], score)
    nearest = {entry: Nearest(tuple(other for other in ordered if other != entry
                                    and pairs[min(entry, other), max(entry, other)] == best[entry]), best[entry])
               for entry in ordered if best[entry] > 0}
    links = [Link(a, b, score) for (a, b), score in sorted(pairs.items())
             if score >= params.threshold and score == best[a] == best[b]]

    values = {entry.node: tuple(sorted({item.value for item in entry.evidence if item.value is not None}))
              for entry in entries.entries}

    def member(entry: str) -> Member:
        unresolved: Counter[str] = Counter()
        for item in reached[entry]:
            unresolved.update(dict(nodes[item].unresolved_calls))
        return Member(entry, values[entry], len(functions[entry]), tuple(sorted(unresolved.items())), nearest.get(entry))

    candidates = []
    for group in _groups(ordered, links):
        common = frozenset.intersection(*(functions[entry] for entry in group))
        candidates.append(Candidate(tuple(member(entry) for entry in group),
                                    tuple(link for link in links if link.a in group),
                                    tuple(Shared(item, counts[item]) for item in sorted(common, key=lambda n: (counts[n], n)))))
    candidates.sort(key=lambda candidate: (-len(candidate.members), candidate.members[0].node))
    return Journeys(source, entry_points.as_input(entries), params, tuple(candidates))


def to_document(result: Journeys) -> dict[str, Any]:
    return {
        'kind': KIND, 'version': VERSION, 'inputs': [result.snapshot.to_document(), result.entries.to_document()],
        'params': {'edgeKinds': list(reaches.EDGE_KINDS), 'nodeKind': 'function', 'weighting': result.params.weighting,
                   'similarity': SIMILARITY, 'grouping': GROUPING, 'threshold': result.params.threshold},
        'candidates': [{
            'members': [_member(item) for item in candidate.members],
            'links': [{'a': link.a, 'b': link.b, 'similarity': link.similarity} for link in candidate.links],
            'shared': [{'node': item.node, 'entryPoints': item.entry_points} for item in candidate.shared],
        } for candidate in result.candidates],
    }


def from_document(document: Mapping[str, Any]) -> Journeys:
    """The journeys in a loaded envelope (``documents.load(data, KIND)``); refuses anything malformed. That
    the nodes belong to the named snapshot is the reader's check: this document alone cannot show it."""
    top = documents.fields(document, 'document', {'kind': str, 'version': int, 'inputs': list, 'params': dict,
                                                  'candidates': list})
    if len(top['inputs']) != 2:
        documents.invalid('inputs', 'expected the snapshot and the entries document')
    source, found = (Input.from_document(item, f'inputs[{index}]') for index, item in enumerate(top['inputs']))
    if (source.kind, found.kind) != (snapshots.KIND, entry_points.KIND):
        documents.invalid('inputs', 'expected the snapshot, then the entries document')
    params = documents.fields(top['params'], 'params', {'edgeKinds': list, 'nodeKind': str, 'weighting': str,
                                                         'similarity': str, 'grouping': str, 'threshold': float})
    if (params['edgeKinds'], params['nodeKind'], params['similarity'], params['grouping']) != (
            list(reaches.EDGE_KINDS), 'function', SIMILARITY, GROUPING) or params['weighting'] not in WEIGHTINGS \
            or not 0 < params['threshold'] <= 1:
        documents.invalid('params', 'not parameters this version computes')

    def score(value: Any, where: str) -> float:
        if not documents.is_number(value) or not 0 <= value <= 1:
            documents.invalid(where, 'expected a similarity from 0 to 1')
        return float(value)

    def ids(value: Any, where: str) -> tuple[str, ...]:
        if not isinstance(value, list) or not all(isinstance(item, str) for item in value) or len(set(value)) != len(value):
            documents.invalid(where, 'expected distinct strings')
        return tuple(sorted(value))

    def member(value: Any, where: str) -> Member:
        item = documents.fields(value, where, {'node': str, 'values': list, 'reached': int, 'unresolvedCalls': dict},
                                {'nearest': dict})
        nearest = None
        if 'nearest' in item:
            near = documents.fields(item['nearest'], f'{where}.nearest', {'nodes': list, 'similarity': float})
            nearest = Nearest(ids(near['nodes'], f'{where}.nearest.nodes'), score(near['similarity'], f'{where}.nearest.similarity'))
            if not nearest.nodes or nearest.similarity == 0:
                documents.invalid(f'{where}.nearest', 'omitted unless some entry point is similar above 0')
        unresolved = item['unresolvedCalls']
        if not all(category in snapshots.UNRESOLVED and documents.is_int(count) and count > 0
                   for category, count in unresolved.items()):
            documents.invalid(f'{where}.unresolvedCalls', 'unknown category or count below 1')
        if item['reached'] < 1:
            documents.invalid(f'{where}.reached', 'an entry point reaches at least itself')
        return Member(item['node'], ids(item['values'], f'{where}.values'), item['reached'],
                      tuple(sorted(unresolved.items())), nearest)

    candidates = []
    for index, value in enumerate(top['candidates']):
        where = f'candidates[{index}]'
        item = documents.fields(value, where, {'members': list, 'links': list, 'shared': list})
        members = tuple(sorted((member(part, f'{where}.members[{position}]') for position, part in enumerate(item['members'])),
                               key=lambda part: part.node))
        names = {part.node for part in members}
        links = []
        for position, part in enumerate(item['links']):
            link = documents.fields(part, f'{where}.links[{position}]', {'a': str, 'b': str, 'similarity': float})
            if not link['a'] < link['b'] or not {link['a'], link['b']} <= names:
                documents.invalid(f'{where}.links[{position}]', 'expected two members, a before b')
            links.append(Link(link['a'], link['b'], score(link['similarity'], f'{where}.links[{position}].similarity')))
        shared = []
        for position, part in enumerate(item['shared']):
            entry = documents.fields(part, f'{where}.shared[{position}]', {'node': str, 'entryPoints': int})
            if entry['entryPoints'] < len(members):
                documents.invalid(f'{where}.shared[{position}].entryPoints', 'every member reaches it')
            shared.append(Shared(entry['node'], entry['entryPoints']))
        if not members or len(names) != len(members) or (len(members) > 1) != bool(links):
            documents.invalid(where, 'expected distinct members, linked when more than one')
        candidates.append(Candidate(members, tuple(sorted(links, key=lambda link: (link.a, link.b))),
                                    tuple(sorted(shared, key=lambda part: (part.entry_points, part.node)))))
    every = [part.node for candidate in candidates for part in candidate.members]
    if len(set(every)) != len(every):
        documents.invalid('candidates', 'an entry point is in two candidates')
    candidates.sort(key=lambda candidate: (-len(candidate.members), candidate.members[0].node))
    return Journeys(source, found, Params(float(params['threshold']), params['weighting']), tuple(candidates))


def load(data: bytes) -> Journeys:
    return from_document(documents.load(data, KIND))


def _member(item: Member) -> dict[str, Any]:
    result = {'node': item.node, 'values': list(item.values), 'reached': item.reached,
              'unresolvedCalls': dict(item.unresolved_calls)}
    if item.nearest is not None:
        result['nearest'] = {'nodes': list(item.nearest.nodes), 'similarity': item.nearest.similarity}
    return result


def _groups(ordered: list[str], links: list[Link]) -> list[tuple[str, ...]]:
    """The connected groups of entry points under the links, each sorted."""
    view = Graph(frozenset(ordered), frozenset(pair for link in links for pair in ((link.a, link.b), (link.b, link.a))))
    groups: dict[str, tuple[str, ...]] = {}
    for start in ordered:
        if start not in groups:
            group = tuple(sorted(closure(view, [start])))
            groups.update(dict.fromkeys(group, group))
    return sorted(set(groups.values()))

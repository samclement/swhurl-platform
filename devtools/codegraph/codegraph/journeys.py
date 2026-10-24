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
from dataclasses import dataclass
from typing import Any

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

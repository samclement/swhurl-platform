"""Layer D: graph views of a snapshot and the algorithms over them.

Generic: nodes are opaque IDs and edges are pairs; nothing here knows Python or what a node is.
Primitives are added with the first feature that needs each.
"""
from __future__ import annotations

from collections import defaultdict
from collections.abc import Iterable
from dataclasses import dataclass

from codegraph.errors import CodegraphError
from codegraph.snapshot import EDGE_KINDS, Snapshot


@dataclass(frozen=True)
class Graph:
    nodes: frozenset[str]
    edges: frozenset[tuple[str, str]]  # (source, target)


def graph(snapshot: Snapshot, kinds: Iterable[str]) -> Graph:
    """Every node of the snapshot, with the edges of the chosen kinds (several edges of one pair become one)."""
    chosen = frozenset(kinds)
    if not chosen <= EDGE_KINDS:
        raise CodegraphError('usage', f'unknown edge kind: {", ".join(sorted(chosen - EDGE_KINDS))}')
    return Graph(frozenset(node.id for node in snapshot.nodes),
                 frozenset((edge.source, edge.target) for edge in snapshot.edges if edge.kind in chosen))


def closure(graph: Graph, starts: Iterable[str]) -> frozenset[str]:
    """The starts and every node reachable from them; a start that is not a node is refused."""
    starts = frozenset(starts)
    if not starts <= graph.nodes:
        raise CodegraphError('unknown-node', ', '.join(sorted(starts - graph.nodes)))
    successors: dict[str, list[str]] = defaultdict(list)
    for source, target in graph.edges:
        successors[source].append(target)
    seen = set(starts)
    pending = list(starts)
    while pending:
        for target in successors[pending.pop()]:
            if target not in seen:
                seen.add(target)
                pending.append(target)
    return frozenset(seen)

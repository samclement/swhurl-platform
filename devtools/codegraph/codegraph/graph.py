"""Layer D: graph views of a snapshot and the algorithms over them.

Generic: nodes are opaque IDs and edges are pairs; nothing here knows Python or what a node is.
Primitives are added with the first feature that needs each.
"""
from __future__ import annotations

from collections import defaultdict
from collections.abc import Iterable, Mapping
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
    successors = _successors(graph)
    seen = set(starts)
    pending = list(starts)
    while pending:
        for target in successors[pending.pop()]:
            if target not in seen:
                seen.add(target)
                pending.append(target)
    return frozenset(seen)


def components(graph: Graph) -> tuple[frozenset[str], ...]:
    """The strongly connected components, each node in exactly one (a node outside any cycle alone),
    ordered by their smallest node."""
    successors = _successors(graph)
    index: dict[str, int] = {}
    low: dict[str, int] = {}
    stack: list[str] = []
    on_stack: set[str] = set()
    found = []
    for root in sorted(graph.nodes):  # Tarjan's algorithm, iteratively
        if root in index:
            continue
        work = [(root, iter(successors[root]))]
        index[root] = low[root] = len(index)
        stack.append(root)
        on_stack.add(root)
        while work:
            node, pending = work[-1]
            target = next(pending, None)
            if target is None:
                work.pop()
                if work:
                    low[work[-1][0]] = min(low[work[-1][0]], low[node])
                if low[node] == index[node]:
                    component = set()
                    while True:
                        member = stack.pop()
                        on_stack.discard(member)
                        component.add(member)
                        if member == node:
                            break
                    found.append(frozenset(component))
            elif target not in index:
                index[target] = low[target] = len(index)
                stack.append(target)
                on_stack.add(target)
                work.append((target, iter(successors[target])))
            elif target in on_stack:
                low[node] = min(low[node], index[target])
    return tuple(sorted(found, key=min))


def rank(graph: Graph) -> Mapping[str, int]:
    """Each node's depth: 0 when it reaches nothing outside its own component, otherwise one more than
    the deepest component it has an edge to. The members of a cycle share one rank."""
    groups = components(graph)
    group_of = {node: number for number, group in enumerate(groups) for node in group}
    below: dict[int, set[int]] = defaultdict(set)
    for source, target in graph.edges:
        if group_of[source] != group_of[target]:
            below[group_of[source]].add(group_of[target])
    depth: dict[int, int] = {}
    for start in range(len(groups)):  # depth first over the condensed graph, which has no cycles
        work = [start]
        while work:
            group = work[-1]
            pending = [other for other in below[group] if other not in depth]
            if pending:
                work.extend(pending)
                continue
            work.pop()
            depth[group] = 1 + max((depth[other] for other in below[group]), default=-1)
    return {node: depth[group_of[node]] for node in sorted(graph.nodes)}


def _successors(graph: Graph) -> Mapping[str, list[str]]:
    successors: dict[str, list[str]] = defaultdict(list)
    for source, target in sorted(graph.edges):
        successors[source].append(target)
    return successors

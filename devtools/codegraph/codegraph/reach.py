"""Layer E: what one node reaches over ``calls`` and ``references``, and its spine.

Reach is a lower bound: it carries the external and unresolved calls of every node reached, since
an unresolved call may lead anywhere. The spine is the node's own resolved calls and references in
source order (within a line, by target ID): a readable outline, not an execution trace.
"""
from __future__ import annotations

from collections import Counter
from dataclasses import dataclass
from typing import Any

from codegraph import snapshot as snapshots
from codegraph.documents import Input
from codegraph.errors import CodegraphError
from codegraph.graph import closure, graph
from codegraph.snapshot import Snapshot

KIND = 'reach'
VERSION = 1
EDGE_KINDS = ('calls', 'references')


@dataclass(frozen=True)
class Step:
    line: int
    target: str
    kind: str


@dataclass(frozen=True)
class Reach:
    snapshot: Input
    node: str
    reached: tuple[str, ...]  # sorted; includes the node itself
    external_calls: int  # across every node reached
    unresolved_calls: tuple[tuple[str, int], ...]  # (category, count) across every node reached, sorted
    spine: tuple[Step, ...]


def reach(snapshot: Snapshot, node: str) -> Reach:
    nodes = {item.id: item for item in snapshot.nodes}
    if node not in nodes:
        raise CodegraphError('unknown-node', node)
    reached = sorted(closure(graph(snapshot, EDGE_KINDS), [node]))
    unresolved: Counter[str] = Counter()
    for item in reached:
        unresolved.update(dict(nodes[item].unresolved_calls))
    spine = sorted((Step(line, edge.target, edge.kind) for edge in snapshot.edges
                    if edge.source == node and edge.kind in EDGE_KINDS for line in edge.lines),
                   key=lambda step: (step.line, step.target, step.kind))
    return Reach(snapshots.as_input(snapshot), node, tuple(reached), sum(nodes[item].external_calls for item in reached),
                 tuple(sorted(unresolved.items())), tuple(spine))


def to_document(result: Reach) -> dict[str, Any]:
    return {
        'kind': KIND, 'version': VERSION, 'inputs': [result.snapshot.to_document()],
        'params': {'node': result.node, 'edgeKinds': list(EDGE_KINDS)},
        'node': result.node, 'reached': list(result.reached), 'externalCalls': result.external_calls,
        'unresolvedCalls': dict(result.unresolved_calls),
        'spine': [{'line': step.line, 'target': step.target, 'kind': step.kind} for step in result.spine],
    }

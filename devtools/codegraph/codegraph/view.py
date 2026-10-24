"""Layer F: one self-contained HTML page that renders a snapshot, its entries and its journeys.

A renderer, not an analysis: it shows what the three documents hold, plus each entry point's reach
and spine (``reach``) and each module's depth over module-level ``imports`` (``graph.rank``). The page
embeds its data, loads nothing from the network and runs no code it was given; identical documents
give identical bytes.
"""
from __future__ import annotations

import json
from importlib import resources
from typing import Any

from codegraph import entries as entry_points
from codegraph import reach as reaches
from codegraph import snapshot as snapshots
from codegraph.entries import Entries
from codegraph.errors import CodegraphError
from codegraph.graph import Graph, rank
from codegraph.journeys import Journeys
from codegraph.snapshot import Snapshot

RANK_EDGE_KINDS = ('imports',)
"""Module depth follows module-level imports; lazy imports are drawn but do not place a module."""


def model(snapshot: Snapshot, entries: Entries, journeys: Journeys) -> dict[str, Any]:
    """The page's data: modules, nodes and entry points as tables, referred to by index."""
    source = snapshots.as_input(snapshot)
    if entries.snapshot.digest != source.digest or journeys.snapshot.digest != source.digest:
        raise CodegraphError('invalid-document', 'entries and journeys must be derived from the snapshot given')
    if journeys.entries.digest != entry_points.as_input(entries).digest:
        raise CodegraphError('invalid-document', 'journeys: derived from different entries')
    modules = sorted(node.module for node in snapshot.nodes if node.kind == 'module')
    module_index = {name: index for index, name in enumerate(modules)}
    module_edges = {(edge.source.removeprefix('py:'), edge.target.removeprefix('py:'), edge.kind) for edge in snapshot.edges
                    if edge.kind in ('imports', 'lazy-imports')}
    depth = rank(Graph(frozenset(modules), frozenset((a, b) for a, b, kind in module_edges if kind in RANK_EDGE_KINDS)))
    paths = {node.module: node.path for node in snapshot.nodes if node.kind == 'module'}
    members = [node for node in snapshot.nodes if node.kind != 'module']
    node_index = {node.id: index for index, node in enumerate(members)}
    entry_index = {entry.node: index for index, entry in enumerate(entries.entries)}

    reached = {result.node: result for result in reaches.many(snapshot, [item.node for item in entries.entries])}

    def entry(item: entry_points.Entry) -> dict[str, Any]:
        result = reached[item.node]
        return {'node': node_index[item.node],
                'rules': sorted({evidence.rule for evidence in item.evidence}),
                'values': sorted({evidence.value for evidence in item.evidence if evidence.value is not None}),
                'reached': [node_index[node] for node in result.reached if node in node_index],
                'externalCalls': result.external_calls, 'unresolvedCalls': dict(result.unresolved_calls),
                'spine': [[step.line, node_index[step.target], step.kind] for step in result.spine if step.target in node_index]}

    return {
        'revision': snapshot.revision, 'scope': snapshot.scope,
        'journeyParams': {'weighting': journeys.params.weighting, 'threshold': journeys.params.threshold},
        'rankEdgeKinds': list(RANK_EDGE_KINDS),
        'modules': [[name, paths[name], depth[name]] for name in modules],
        'imports': sorted([module_index[a], module_index[b], kind] for a, b, kind in module_edges),
        'nodes': [[module_index[node.module], node.qualname, node.kind, node.start,
                   sum(count for _, count in node.unresolved_calls)] for node in members],
        'entries': [entry(item) for item in entries.entries],
        'candidates': [{
            'members': [{'entry': entry_index[member.node],
                         'nearest': ([[entry_index[node] for node in member.nearest.nodes], member.nearest.similarity]
                                     if member.nearest else None)} for member in candidate.members],
            'links': [[entry_index[link.a], entry_index[link.b], link.similarity] for link in candidate.links],
            'shared': [[node_index[item.node], item.entry_points] for item in candidate.shared],
        } for candidate in journeys.candidates],
    }


def render(snapshot: Snapshot, entries: Entries, journeys: Journeys) -> bytes:
    data = json.dumps(model(snapshot, entries, journeys), sort_keys=True, separators=(',', ':'), ensure_ascii=False)
    # '<' appears only inside JSON strings, where \u003c means the same: the data cannot close its script element.
    page = resources.files('codegraph').joinpath('view.html').read_text(encoding='utf-8')
    return page.replace('__MODEL__', data.replace('<', '\\u003c'), 1).encode()

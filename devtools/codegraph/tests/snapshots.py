"""Hand-written snapshots for the layer D and E tests: nodes named by ID, edges as (source, target, kind, lines)."""
from __future__ import annotations

from codegraph.snapshot import Argument, Edge, Node, ObjectReference, Snapshot


def node(node_id: str, kind: str = 'function', main_blocks=(), external=0, unresolved=()) -> Node:
    """``py:pkg.a:f`` is function ``f`` of ``pkg/a.py``; ``py:pkg.a`` is the module."""
    _, module, *qualname = node_id.split(':')
    path = module.replace('.', '/') + '.py'
    return Node(node_id, 'module' if not qualname else kind, module, qualname[0] if qualname else '', path, 1, 100,
                external_calls=external, unresolved_calls=tuple(sorted(unresolved)), main_blocks=tuple(main_blocks))


def snapshot(nodes, edges=(), object_references=(), arguments=()) -> Snapshot:
    nodes = [item if isinstance(item, Node) else node(item) for item in nodes]
    return Snapshot('c' * 40, 'pkg', '', 'python-ast', 2, '3.12', tuple(sorted(nodes, key=lambda item: item.id)),
                    tuple(sorted((Edge(source, target, kind, tuple(lines)) for source, target, kind, lines in edges),
                       key=lambda edge: (edge.source, edge.target, edge.kind))),
                    (), tuple(sorted(ObjectReference(*item) for item in object_references)),
                    tuple(sorted(Argument(*item) for item in arguments)))

"""Layer C: the snapshot, one revision's nodes, edges and what could not be read, as values and as a document.

Language-neutral: an extractor chooses node IDs; this module checks only their shape, uniqueness
and that every edge, object reference and argument names nodes of the snapshot. Loading refuses
unknown fields: a field added later within version 1 must be optional when read (``mainBlocks``,
``objectReferences`` and ``arguments`` came with extractor version 2).
"""
from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

from codegraph import documents
from codegraph.errors import CodegraphError

KIND = 'snapshot'
VERSION = 1
NODE_KINDS = frozenset({'module', 'class', 'function'})
EDGE_KINDS = frozenset({'imports', 'lazy-imports', 'calls', 'references'})
"""``references``: a function or class used as a value (passed, stored, returned, decorating) outside call position."""
UNRESOLVED = frozenset({'value-attribute', 'dynamic', 'ambiguous', 'missing', 'unparsed'})
"""Why a call site was not resolved: a method on a value, a call of a computed value, a name bound
more than one way, a name that leads nowhere in scope, or a target in a file that did not parse."""


@dataclass(frozen=True)
class Node:
    id: str
    kind: str
    module: str
    qualname: str  # empty for a module
    path: str
    start: int
    end: int
    signature: str | None = None
    body_hash: str | None = None
    redefinitions: int = 0  # earlier definitions of the same name in the same scope, merged into this node
    external_calls: int = 0
    unresolved_calls: tuple[tuple[str, int], ...] = ()  # (category, count), sorted, counts above zero
    main_blocks: tuple[tuple[int, int], ...] = ()  # a module's `if __name__ == '__main__':` bodies, as line ranges


@dataclass(frozen=True)
class Edge:
    source: str
    target: str
    kind: str
    lines: tuple[int, ...]  # the source lines that give rise to it, sorted


@dataclass(frozen=True)
class Unparsed:
    path: str
    error: str


@dataclass(frozen=True, order=True)
class ObjectReference:
    """A string literal naming an object as ``module:qualname`` (packaging entry points, command tables), unresolved."""
    source: str  # the node whose body contains it
    line: int
    value: str


@dataclass(frozen=True, order=True)
class Argument:
    """A function or class of the snapshot passed directly as an argument of a call."""
    source: str  # the node whose body contains the call
    line: int
    callee: str  # absolute dotted name as the source file binds it ('starlette.routing.Route'); '' if unknown
    slot: str  # position ('0', '1', ...) or keyword
    target: str  # the node passed
    literals: tuple[tuple[str, str], ...]  # (slot, value) of the call's string literal arguments, sorted


@dataclass(frozen=True)
class Snapshot:
    revision: str
    scope: str
    import_root: str
    extractor: str
    extractor_version: int
    python: str  # the parser's major.minor: body hashes are comparable only between equal values
    nodes: tuple[Node, ...]
    edges: tuple[Edge, ...]
    unparsed: tuple[Unparsed, ...]
    object_references: tuple[ObjectReference, ...] = ()
    arguments: tuple[Argument, ...] = ()


def as_input(snapshot: Snapshot) -> documents.Input:
    return documents.Input(KIND, documents.digest(to_document(snapshot)), snapshot.revision)


def to_document(snapshot: Snapshot) -> dict[str, Any]:
    return {
        'kind': KIND, 'version': VERSION, 'inputs': [],
        'params': {'scope': snapshot.scope, 'importRoot': snapshot.import_root, 'extractor': snapshot.extractor,
                   'extractorVersion': snapshot.extractor_version, 'python': snapshot.python},
        'revision': snapshot.revision,
        'nodes': [_node_document(node) for node in sorted(snapshot.nodes, key=lambda node: node.id)],
        'edges': [{'source': edge.source, 'target': edge.target, 'kind': edge.kind, 'lines': list(edge.lines)}
                  for edge in sorted(snapshot.edges, key=_edge_key)],
        'unparsed': [{'path': item.path, 'error': item.error} for item in sorted(snapshot.unparsed, key=lambda u: u.path)],
        'objectReferences': [{'source': item.source, 'line': item.line, 'value': item.value}
                             for item in sorted(snapshot.object_references)],
        'arguments': [{'source': item.source, 'line': item.line, 'callee': item.callee, 'slot': item.slot,
                       'target': item.target, 'literals': dict(item.literals)} for item in sorted(snapshot.arguments)],
    }


def from_document(document: Mapping[str, Any]) -> Snapshot:
    """The snapshot in a loaded envelope (``documents.load(data, KIND)``); refuses anything malformed."""
    top = _fields(document, 'document', {'kind': str, 'version': int, 'inputs': list, 'params': dict, 'revision': str,
                                         'nodes': list, 'edges': list, 'unparsed': list},
                  {'objectReferences': list, 'arguments': list})
    if top['inputs']:
        _invalid('inputs', 'a snapshot has no input documents')
    params = _fields(top['params'], 'params', {'scope': str, 'importRoot': str, 'extractor': str,
                                                'extractorVersion': int, 'python': str})
    nodes = tuple(_node(item, f'nodes[{index}]') for index, item in enumerate(top['nodes']))
    ids = {node.id for node in nodes}
    if len(ids) != len(nodes):
        _invalid('nodes', 'node IDs must be unique')
    edges = tuple(_edge(item, f'edges[{index}]', ids) for index, item in enumerate(top['edges']))
    if len({_edge_key(edge) for edge in edges}) != len(edges):
        _invalid('edges', 'an edge appears twice')
    unparsed = tuple(Unparsed(**_fields(item, f'unparsed[{index}]', {'path': str, 'error': str}))
                     for index, item in enumerate(top['unparsed']))
    object_references = tuple(_object_reference(item, f'objectReferences[{index}]', ids)
                              for index, item in enumerate(top.get('objectReferences', [])))
    arguments = tuple(_argument(item, f'arguments[{index}]', ids) for index, item in enumerate(top.get('arguments', [])))
    if len(set(object_references)) != len(object_references) or len(set(arguments)) != len(arguments):
        _invalid('document', 'an object reference or argument appears twice')
    return Snapshot(top['revision'], params['scope'], params['importRoot'], params['extractor'],
                    params['extractorVersion'], params['python'], tuple(sorted(nodes, key=lambda node: node.id)),
                    tuple(sorted(edges, key=_edge_key)), tuple(sorted(unparsed, key=lambda item: item.path)),
                    tuple(sorted(object_references)), tuple(sorted(arguments)))


def load(data: bytes) -> Snapshot:
    return from_document(documents.load(data, KIND))


def dump(snapshot: Snapshot) -> bytes:
    return documents.dump(to_document(snapshot))


def _node_document(node: Node) -> dict[str, Any]:
    result = {'id': node.id, 'kind': node.kind, 'module': node.module, 'qualname': node.qualname, 'path': node.path,
              'lines': [node.start, node.end], 'externalCalls': node.external_calls,
              'unresolvedCalls': dict(node.unresolved_calls)}
    if node.signature is not None:
        result['signature'] = node.signature
    if node.body_hash is not None:
        result['bodyHash'] = node.body_hash
    if node.redefinitions:
        result['redefinitions'] = node.redefinitions
    if node.main_blocks:
        result['mainBlocks'] = [list(block) for block in node.main_blocks]
    return result


def _node(value: Any, where: str) -> Node:
    fields = _fields(value, where, {'id': str, 'kind': str, 'module': str, 'qualname': str, 'path': str, 'lines': list,
                                    'externalCalls': int, 'unresolvedCalls': dict},
                     {'signature': str, 'bodyHash': str, 'redefinitions': int, 'mainBlocks': list})
    if fields['kind'] not in NODE_KINDS:
        _invalid(f'{where}.kind', 'unknown node kind')
    lines = fields['lines']
    if len(lines) != 2 or not all(_is_int(line) and line >= 0 for line in lines):
        _invalid(f'{where}.lines', 'expected [start, end]')
    unresolved = fields['unresolvedCalls']
    for category, count in unresolved.items():
        if category not in UNRESOLVED or not _is_int(count) or count < 1:
            _invalid(f'{where}.unresolvedCalls', 'unknown category or count below 1')
    if fields['externalCalls'] < 0 or fields.get('redefinitions', 1) < 1:
        _invalid(where, 'counts must be positive (redefinitions is omitted when zero)')
    blocks = fields.get('mainBlocks', [])
    if 'mainBlocks' in fields and (not blocks or fields['kind'] != 'module' or not all(
            isinstance(block, list) and len(block) == 2 and all(_is_int(line) and line > 0 for line in block)
            and block[0] <= block[1] for block in blocks)):
        _invalid(f'{where}.mainBlocks', 'expected [start, end] line ranges on a module (omitted when none)')
    return Node(fields['id'], fields['kind'], fields['module'], fields['qualname'], fields['path'], lines[0], lines[1],
                fields.get('signature'), fields.get('bodyHash'), fields.get('redefinitions', 0),
                fields['externalCalls'], tuple(sorted(unresolved.items())), tuple(sorted(tuple(b) for b in blocks)))


def _edge(value: Any, where: str, ids: set[str]) -> Edge:
    fields = _fields(value, where, {'source': str, 'target': str, 'kind': str, 'lines': list})
    if fields['kind'] not in EDGE_KINDS:
        _invalid(f'{where}.kind', 'unknown edge kind')
    if fields['source'] not in ids or fields['target'] not in ids:
        _invalid(where, 'an edge must join two nodes of the snapshot')
    lines = fields['lines']
    if not lines or not all(_is_int(line) and line > 0 for line in lines) or sorted(set(lines)) != lines:
        _invalid(f'{where}.lines', 'expected sorted, distinct, positive line numbers')
    return Edge(fields['source'], fields['target'], fields['kind'], tuple(lines))


def _object_reference(value: Any, where: str, ids: set[str]) -> ObjectReference:
    fields = _fields(value, where, {'source': str, 'line': int, 'value': str})
    _check_site(fields, where, ids)
    return ObjectReference(fields['source'], fields['line'], fields['value'])


def _argument(value: Any, where: str, ids: set[str]) -> Argument:
    fields = _fields(value, where, {'source': str, 'line': int, 'callee': str, 'slot': str, 'target': str, 'literals': dict})
    _check_site(fields, where, ids)
    if fields['target'] not in ids:
        _invalid(f'{where}.target', 'must be a node of the snapshot')
    if not all(isinstance(item, str) for item in fields['literals'].values()):
        _invalid(f'{where}.literals', 'expected string values')
    return Argument(fields['source'], fields['line'], fields['callee'], fields['slot'], fields['target'],
                    tuple(sorted(fields['literals'].items())))


def _check_site(fields: Mapping[str, Any], where: str, ids: set[str]) -> None:
    if fields['source'] not in ids:
        _invalid(f'{where}.source', 'must be a node of the snapshot')
    if fields['line'] < 1:
        _invalid(f'{where}.line', 'expected a positive line number')


def _fields(value: Any, where: str, required: Mapping[str, type], optional: Mapping[str, type] | None = None) -> dict:
    optional = optional or {}
    if not isinstance(value, Mapping):
        _invalid(where, 'expected an object')
    unknown = sorted(set(value) - set(required) - set(optional))
    missing = sorted(set(required) - set(value))
    if unknown or missing:
        _invalid(where, ', '.join([*(f'unknown field {name}' for name in unknown), *(f'missing {name}' for name in missing)]))
    for name, expected in {**required, **optional}.items():
        if name in value and not (_is_int(value[name]) if expected is int else isinstance(value[name], expected)):
            _invalid(f'{where}.{name}', f'expected {expected.__name__}')
    return dict(value)


def _is_int(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool)


def _edge_key(edge: Edge) -> tuple[str, str, str]:
    return edge.source, edge.target, edge.kind


def _invalid(where: str, reason: str):
    raise CodegraphError('invalid-document', f'{where}: {reason}')

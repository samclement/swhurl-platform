"""Layer C: the document envelope, canonical bytes and digests.

Every document is a JSON object with ``kind``, ``version``, ``inputs`` and ``params``. Canonical
bytes are compact JSON with sorted keys, UTF-8, floats rounded to 4 places and a final newline;
the digest is SHA-256 of those bytes. Each kind's own fields are validated by its module.
"""
from __future__ import annotations

import hashlib
import json
from collections.abc import Mapping
from dataclasses import dataclass
from typing import Any

from codegraph.errors import CodegraphError

SUPPORTED = {'snapshot': frozenset({1}), 'entries': frozenset({1}), 'journeys': frozenset({1})}
"""The versions of each kind this code reads; anything else is refused, never upgraded silently."""

ENVELOPE = ('kind', 'version', 'inputs', 'params')


@dataclass(frozen=True)
class Input:
    """How a document names one it was derived from: an entry of its ``inputs``."""
    kind: str
    digest: str
    revision: str | None = None  # for a snapshot

    def to_document(self) -> dict[str, Any]:
        result = {'kind': self.kind, 'digest': self.digest}
        if self.revision is not None:
            result['revision'] = self.revision
        return result

    @classmethod
    def from_document(cls, value: Any, where: str) -> Input:
        item = fields(value, where, {'kind': str, 'digest': str}, {'revision': str})
        if (item['kind'] == 'snapshot') != ('revision' in item):
            invalid(where, 'a snapshot input, and only one, names its revision')
        return cls(item['kind'], item['digest'], item.get('revision'))


def dump(document: Mapping[str, Any]) -> bytes:
    try:
        text = json.dumps(_rounded(document), sort_keys=True, separators=(',', ':'), ensure_ascii=False, allow_nan=False)
    except ValueError as error:
        raise CodegraphError('invalid-document', 'a number is not finite') from error
    return (text + '\n').encode()


def digest(document: Mapping[str, Any]) -> str:
    return 'sha256:' + hashlib.sha256(dump(document)).hexdigest()


def load(data: bytes, kind: str) -> dict[str, Any]:
    """The document in ``data`` if it is a well-formed envelope of ``kind`` at a supported version."""
    try:
        value = json.loads(data, object_pairs_hook=_unique_keys, parse_constant=_no_constant)
    except (ValueError, UnicodeDecodeError) as error:
        raise CodegraphError('invalid-document', 'not a JSON document') from error
    if not isinstance(value, dict):
        raise CodegraphError('invalid-document', 'expected a JSON object')
    missing = [field for field in ENVELOPE if field not in value]
    if missing:
        raise CodegraphError('invalid-document', f'missing {", ".join(missing)}')
    if value['kind'] != kind:
        raise CodegraphError('invalid-document', f'expected kind {kind}')
    version = value['version']
    if not isinstance(version, int) or isinstance(version, bool) or version not in SUPPORTED.get(kind, ()):
        raise CodegraphError('unsupported-version', f'{kind} version is not supported')
    if not isinstance(value['inputs'], list) or not isinstance(value['params'], dict):
        raise CodegraphError('invalid-document', 'inputs must be a list and params an object')
    return value


def fields(value: Any, where: str, required: Mapping[str, type], optional: Mapping[str, type] | None = None) -> dict:
    """``value`` as a dict if it is an object with exactly the required fields, any of the optional ones,
    and each of the expected type (a boolean is not a number; an integer is a float); ``where`` names it in the error."""
    optional = optional or {}
    if not isinstance(value, Mapping):
        invalid(where, 'expected an object')
    unknown = sorted(set(value) - set(required) - set(optional))
    missing = sorted(set(required) - set(value))
    if unknown or missing:
        invalid(where, ', '.join([*(f'unknown field {name}' for name in unknown), *(f'missing {name}' for name in missing)]))
    for name, expected in {**required, **optional}.items():
        if name in value and not (is_int(value[name]) if expected is int else is_number(value[name]) if expected is float
                                  else isinstance(value[name], expected)):
            invalid(f'{where}.{name}', f'expected {expected.__name__}')
    return dict(value)


def is_int(value: Any) -> bool:
    return isinstance(value, int) and not isinstance(value, bool)


def is_number(value: Any) -> bool:
    return is_int(value) or isinstance(value, float)


def invalid(where: str, reason: str):
    raise CodegraphError('invalid-document', f'{where}: {reason}')


def _rounded(value: Any) -> Any:
    if isinstance(value, float):
        return round(value, 4)
    if isinstance(value, Mapping):
        return {key: _rounded(item) for key, item in value.items()}
    if isinstance(value, list | tuple):
        return [_rounded(item) for item in value]
    return value


def _unique_keys(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result = dict(pairs)
    if len(result) != len(pairs):
        raise ValueError('duplicate key')
    return result


def _no_constant(name: str) -> None:
    raise ValueError(f'{name} is not JSON')

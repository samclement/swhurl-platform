"""One JSON document kept in one key of a ConfigMap that Flux creates but does not fill.

Flux owns the object's metadata (``kustomize.toolkit.fluxcd.io/ssa: Merge``); the job that
calls this owns the key, so reconciliation never resets it. Callers validate the content.
"""
from __future__ import annotations

import json

from swhurl.run import Runner


def read_key(runner: Runner, *, namespace: str, name: str, key: str) -> str | None:
    """The raw value of ``key``, or None when the key has not been written yet."""
    doc = runner.json(['kubectl', '-n', namespace, 'get', 'configmap', name, '-o', 'json'])
    return (doc.get('data') or {}).get(key) or None


def write_key(runner: Runner, *, namespace: str, name: str, key: str, raw: str, field_manager: str) -> None:
    runner.run(['kubectl', '-n', namespace, 'patch', 'configmap', name,
                '--type=merge', '--patch-file=/dev/stdin', f'--field-manager={field_manager}'],
               input=json.dumps({'data': {key: raw}}), mutating=True)

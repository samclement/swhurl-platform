"""Persistent notification state and delivery outbox."""
from __future__ import annotations

import json

from swhurl import statestore
from swhurl.run import Runner

from .errors import NotificationError

STATE_NAME = 'notification-state'
STATE_NAMESPACE = 'console'
MAX_STATE_BYTES = 700_000

def empty_state(now: float) -> dict:
    return {'version': 1, 'started': now, 'last_success': 0, 'instances': {}, 'seen': {}, 'pending': [],
            'monitor': {}}


def read_state(runner: Runner) -> dict | None:
    raw = statestore.read_key(runner, namespace=STATE_NAMESPACE, name=STATE_NAME, key='state.json')
    if not raw:
        return None
    try:
        state = json.loads(raw)
        if (state['version'] != 1 or not isinstance(state['instances'], dict)
                or not isinstance(state['seen'], dict) or not isinstance(state['pending'], list)
                or not isinstance(state['monitor'], dict)):
            raise ValueError
        float(state['started'])
        float(state['last_success'])
        return state
    except (ValueError, KeyError, TypeError):
        raise NotificationError('notification state is invalid; refusing to reset incident or delivery history') from None


def save_state(runner: Runner, state: dict) -> None:
    raw = json.dumps(state, separators=(',', ':'))
    if len(raw.encode()) > MAX_STATE_BYTES:
        raise NotificationError('notification state exceeds its bounded size; delivery stopped without discarding history')
    statestore.write_key(runner, namespace=STATE_NAMESPACE, name=STATE_NAME, key='state.json', raw=raw,
                         field_manager='notification-check')



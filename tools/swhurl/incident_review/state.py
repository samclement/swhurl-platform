"""Reviewer state: incidents, baselines, spend and the notification outbox.

Kept in one ConfigMap key (docs/plan.md section 14, "State"). It holds numbers,
enums and already-validated notification text; never prompts, bundles or log rows.
Only the decide step writes it.
"""
from __future__ import annotations

import datetime as dt
import json
import math
from typing import Any

from swhurl import statestore
from swhurl.run import CommandError, Runner

from .errors import ReviewFailure

NAMESPACE = 'incident-review'
NAME = 'incident-review-state'
KEY = 'state.json'
FIELD_MANAGER = 'incident-review'
MAX_STATE_BYTES = 64_000
STATUSES = frozenset({'seen', 'analysed', 'suppressed-low-confidence', 'suppressed-uncited', 'no-change',
                      'no-diagnosis', 'notified'})
RESULTS = frozenset({'', 'quiet', 'collect-only', 'analysed', 'budget', 'failed'})
INCIDENT_FIELDS = {'app': str, 'env': str, 'signal': str, 'first_seen': float, 'last_seen': float,
                   'last_analysis': float, 'cooldown_until': float, 'status': str, 'coverage': str,
                   'cli': str, 'model': str, 'notified': float}
SPEND_FIELDS = {'month': str, 'cents': int, 'analyses': int, 'day': str, 'analyses_today': int}


def empty_state() -> dict:
    return {'version': 1, 'last_sweep': 0, 'last_result': '', 'last_window': '', 'incidents': {}, 'baselines': {},
            'spend': {'month': '', 'cents': 0, 'analyses': 0, 'day': '', 'analyses_today': 0},
            'seen': {}, 'pending': []}


def _number(value: Any) -> bool:
    return not isinstance(value, bool) and isinstance(value, (int, float)) and math.isfinite(value) and value >= 0


def _typed(value: Any, kind: type) -> bool:
    if kind is float:
        return _number(value)
    if kind is int:
        return _number(value) and isinstance(value, int)
    return isinstance(value, kind)


def validate(state: Any) -> dict:
    """Return ``state`` unchanged, or refuse it; a bad state is never repaired or reset."""
    try:
        if not isinstance(state, dict) or set(state) != set(empty_state()) or state['version'] != 1:
            raise ValueError
        if (not _number(state['last_sweep']) or state['last_result'] not in RESULTS
                or not isinstance(state['last_window'], str) or len(state['last_window']) > 40):
            raise ValueError
        for fingerprint, incident in state['incidents'].items():
            if (not isinstance(fingerprint, str) or not isinstance(incident, dict)
                    or set(incident) != {*INCIDENT_FIELDS, 'pr', 'check'}
                    or not all(_typed(incident[name], kind) for name, kind in INCIDENT_FIELDS.items())
                    or incident['status'] not in STATUSES or incident['coverage'] not in {'covered', 'alert-gap'}):
                raise ValueError
        for key, counts in state['baselines'].items():
            if not isinstance(key, str) or not isinstance(counts, list) or not all(_typed(c, int) for c in counts):
                raise ValueError
        spend = state['spend']
        if (not isinstance(spend, dict) or set(spend) != set(SPEND_FIELDS)
                or not all(_typed(spend[name], kind) for name, kind in SPEND_FIELDS.items())):
            raise ValueError
        if not isinstance(state['seen'], dict) or not all(_number(v) for v in state['seen'].values()):
            raise ValueError
        if not isinstance(state['pending'], list) or not all(
                isinstance(n, dict) and isinstance(n.get('key'), str) and isinstance(n.get('title'), str)
                for n in state['pending']):
            raise ValueError
    except (AttributeError, KeyError, TypeError, ValueError):
        raise ReviewFailure('state-corrupt') from None
    return state


def read_state(runner: Runner) -> dict:
    try:
        raw = statestore.read_key(runner, namespace=NAMESPACE, name=NAME, key=KEY)
    except (CommandError, AttributeError, ValueError):
        raise ReviewFailure('state-unavailable') from None
    if raw is None:
        return empty_state()
    try:
        doc = json.loads(raw)
        if isinstance(doc, dict):
            doc.setdefault('last_window', '')  # states saved before the field existed
        return validate(doc)
    except json.JSONDecodeError:
        raise ReviewFailure('state-corrupt') from None


def roll_spend(state: dict, now: float) -> dict:
    """Start the monthly and daily counters afresh when the UTC month or day has changed."""
    today = dt.datetime.fromtimestamp(now, dt.UTC)
    spend = state['spend']
    if spend['month'] != today.strftime('%Y-%m'):
        spend.update(month=today.strftime('%Y-%m'), cents=0, analyses=0)
    if spend['day'] != today.strftime('%Y-%m-%d'):
        spend.update(day=today.strftime('%Y-%m-%d'), analyses_today=0)
    return spend


def budget_block(state: dict, defaults: dict, now: float) -> str | None:
    """``monthly`` or ``daily`` when no further model call is allowed, else None."""
    spend = roll_spend(state, now)
    if spend['cents'] >= defaults['monthly_refuse_cents']:
        return 'monthly'
    if spend['analyses_today'] >= defaults['max_analyses_per_day']:
        return 'daily'
    return None


def baseline_mean(state: dict, key: str) -> float:
    counts = state['baselines'].get(key) or []
    return sum(counts) / len(counts) if counts else 0.0


def prune(state: dict, now: float, defaults: dict) -> None:
    horizon = now - defaults['incident_retention_days'] * 86400
    state['incidents'] = {k: v for k, v in state['incidents'].items() if v['last_seen'] >= horizon}
    state['baselines'] = {k: v[-defaults['baseline_hours']:] for k, v in state['baselines'].items()}
    state['seen'] = {k: v for k, v in state['seen'].items() if v > now}
    roll_spend(state, now)


def serialize(state: dict, now: float, defaults: dict) -> str:
    prune(state, now, defaults)
    raw = json.dumps(validate(state), separators=(',', ':'), sort_keys=True)
    if len(raw.encode()) > MAX_STATE_BYTES:
        raise ReviewFailure('state-size')
    return raw


def save_state(runner: Runner, state: dict, *, now: float, defaults: dict) -> None:
    raw = serialize(state, now, defaults)
    try:
        statestore.write_key(runner, namespace=NAMESPACE, name=NAME, key=KEY, raw=raw, field_manager=FIELD_MANAGER)
    except CommandError:
        raise ReviewFailure('state-unavailable') from None

"""Shared state/label primitive and the platform page's single-unit state."""
from __future__ import annotations

import re
from dataclasses import dataclass

from swhurl.flux import WAITING_REASONS


@dataclass(frozen=True)
class State:
    """The one status vocabulary (each shown with its own mark, not colour alone)."""
    key: str  # healthy, updating, failing, suspended, unknown or stopped
    detail: str = ''

    @property
    def label(self) -> str:
        return self.key.capitalize()


DEPENDENCY = re.compile(r"dependency '(?:[^/']+/)?([^']+)' is not ready")


def ready_reason(obj: dict) -> str:
    return next((c.get('reason', '') for c in (obj.get('status') or {}).get('conditions') or []
                 if c.get('type') == 'Ready'), '')


def unit_state(ready: tuple[str, str], suspended: bool = False, reason: str = '') -> State:
    """A Flux unit's state. Waiting for a dependency or applying a change is Updating, not Failing:
    every push re-checks every unit, and dependents wait a few seconds meanwhile."""
    status, message = ready
    if suspended:
        return State('suspended', 'Git changes are not applied until it is resumed')
    if status == 'True':
        return State('healthy')
    waiting = DEPENDENCY.search(message or '')
    if status == 'Unknown' or reason in WAITING_REASONS or waiting:
        return State('updating', f'waiting for {waiting[1]}' if waiting else (message or 'applying a change'))
    return State('failing', message)

"""An instance's live state (GLOSSARY.md): one verdict from the facts read off the cluster.

``ops.InstanceStatus`` holds the facts (the Flux unit, the Helm release, the Git revision, the image
and the replicas); ``verdict`` judges them. The console's Apps list and app page, ``make app-status``
and the promotion gate all ask here, so they cannot disagree about the same facts. ``unit_state`` is
the same vocabulary for one Flux unit on its own, which is all a shared unit has.
"""
from __future__ import annotations

import re
from dataclasses import dataclass
from typing import TYPE_CHECKING

from swhurl.flux import WAITING_REASONS

if TYPE_CHECKING:
    from swhurl.apps.ops import InstanceStatus


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


def verdict(status: InstanceStatus) -> State:
    """An instance's state, worst first, with the reason."""
    if status.unit_suspended:
        return State('suspended', 'Git changes are not applied until its Flux unit is resumed')
    if status.release_suspended:
        return State('suspended', 'the Helm release is suspended')
    if status.problems:
        return State('failing', f'{len(status.problems)} pod(s) failing; see below')
    unit = unit_state(status.unit, reason=status.unit_reason)
    if unit.key == 'failing':
        return unit
    if status.release and status.release[0] == 'False':
        return State('failing', status.release[1])
    if unit.key == 'updating':
        return unit
    if status.release is None:
        return State('unknown', 'the app release is missing')
    if not status.desired_revision or not status.applied_revision:
        return State('unknown', 'the desired or applied Git revision is missing')
    if not status.applied:
        return State('updating', 'a Git change is not applied yet')
    if status.image_state == 'different':
        return State('updating', 'a new image is rolling out (or failing to)')
    if status.release[0] != 'True':
        return State('updating', status.release[1])
    if not status.replicas or any(r.wanted is None for r in status.replicas):
        return State('unknown', 'workload replica counts are missing')
    if any(r.ready != r.wanted for r in status.replicas):
        return State('updating', 'waiting for the requested replicas to be ready')
    if all(r.wanted == 0 for r in status.replicas):
        return State('stopped', 'scaled to zero replicas')
    if status.image_state == 'none':
        return State('unknown', 'no running image could be observed')
    return State('healthy')


def runs(status: InstanceStatus) -> bool:
    """Healthy, and every pod runs the digest Git pins: what a promotion needs of its source."""
    return verdict(status).key == 'healthy' and status.image_state == 'matches'

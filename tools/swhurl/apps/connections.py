"""A connection between two apps, as one row (docs/apps.md#calling-another-app, #events).

Calls and events are declared as variables on each instance's HelmRelease (calling.py, events.py).
A host outside the cluster an instance may reach (egress.py) is one too, with no app on the other side.
Here each becomes the same thing whoever declared it, so every list, form and map shows one
shape: which way it goes, whether it is a call, an event or a host, and the app on the other side.
"""
from __future__ import annotations

from collections.abc import Iterable, Mapping
from dataclasses import dataclass

ROLES = ('calls', 'subscribes', 'publishes', 'reaches', 'called-by', 'subscriber')
"""In the order a list shows them: what this app declared first, then what others declared about it."""


@dataclass(frozen=True)
class Connection:
    role: str  # one of ROLES
    app: str = ''  # the app on the other side; '' for an event this app publishes to whoever subscribes
    event: str = ''  # the event's own name (created), for an event; host:port, for a host outside the cluster
    team: str = ''  # the other app's team; '' when unknown
    waiting: bool = False  # a call recorded in Git that renders nothing yet: the callee does not allow it

    @property
    def kind(self) -> str:
        return 'call' if self.role in ('calls', 'called-by') else 'host' if self.role == 'reaches' else 'event'

    @property
    def direction(self) -> str:
        """``out``: this app declared it and may remove it; ``in``: the other app did."""
        return 'in' if self.role in ('called-by', 'subscriber') else 'out'

    @property
    def what(self) -> str:
        """What happens over it, as the words before the event's name (if any)."""
        if self.waiting:
            return f'its API, waiting for {self.app} to allow it'
        return {'calls': 'its API', 'called-by': 'calls this app', 'publishes': 'publishes',
                'subscribes': 'receives', 'subscriber': 'receives', 'reaches': 'outside the cluster'}[self.role]


def rows(calls: Iterable[str] = (), callers: Iterable[str] = (), publishes: Iterable[str] = (),
         subscribes: Iterable[tuple[str, str]] = (), subscribers: Iterable[tuple[str, str]] = (),
         teams: Mapping[str, str] | None = None, reaches: Iterable[str] = (),
         waiting: Iterable[str] = ()) -> tuple[Connection, ...]:
    """One instance's connections. ``subscribes`` are ``(publisher, event)``, ``subscribers``
    ``(this app's event, the app receiving it)``; ``teams`` maps an app to its team; ``reaches`` are
    the ``host:port`` outside the cluster it may reach; ``waiting`` the apps it calls that do not allow it yet."""
    teams = teams or {}
    found = [Connection('calls', app, team=teams.get(app, '')) for app in calls]
    found += [Connection('calls', app, team=teams.get(app, ''), waiting=True) for app in waiting]
    found += [Connection('subscribes', app, event, teams.get(app, '')) for app, event in subscribes]
    found += [Connection('publishes', event=event) for event in publishes]
    found += [Connection('reaches', event=host) for host in reaches]
    found += [Connection('called-by', app, team=teams.get(app, '')) for app in callers]
    found += [Connection('subscriber', app, event, teams.get(app, '')) for event, app in subscribers]
    return tuple(found)

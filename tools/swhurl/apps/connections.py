"""A connection between two apps, as one row (docs/apps.md#calling-another-app, #events).

Live calls and events are observed on HelmReleases; Git intent comes from the relation evaluator.
A host outside the cluster an instance may reach (egress.py) is one too, with no app on the other side.
Here each becomes the same thing whoever declared it, so every list, form and map shows one
shape: which way it goes, whether it is a call, an event or a host, and the app on the other side.
"""
from __future__ import annotations

from collections.abc import Callable, Iterable, Mapping
from dataclasses import dataclass, replace

from swhurl import yamlio
from swhurl.apps.instance import Instance
from swhurl.apps.model import relations
from swhurl.apps.model.draft import Entry
from swhurl.apps.model.records import APP_FILE, RELEASE_FILE, SETTINGS_FILE, App, Release, Settings

ROLES = ('calls', 'subscribes', 'publishes', 'reaches', 'called-by', 'subscriber')
"""In the order a list shows them: what this app declared first, then what others declared about it."""


@dataclass(frozen=True)
class Connection:
    role: str  # one of ROLES
    app: str = ''  # the app on the other side; '' for an event this app publishes to whoever subscribes
    event: str = ''  # the event's own name (created), for an event; host:port, for a host outside the cluster
    team: str = ''  # the other app's team; '' when unknown
    status: str = ''  # Git pending reason or a difference from the live observation

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
        what = {'calls': 'its API', 'called-by': 'calls this app', 'publishes': 'publishes',
                'subscribes': 'receives', 'subscriber': 'receives', 'reaches': 'outside the cluster'}[self.role]
        return f'{what}, {self.status}' if self.status else what


def rows(calls: Iterable[str] = (), callers: Iterable[str] = (), publishes: Iterable[str] = (),
         subscribes: Iterable[tuple[str, str]] = (), subscribers: Iterable[tuple[str, str]] = (),
         teams: Mapping[str, str] | None = None, reaches: Iterable[str] = ()) -> tuple[Connection, ...]:
    """One instance's connections. ``subscribes`` are ``(publisher, event)``, ``subscribers``
    ``(this app's event, the app receiving it)``; ``teams`` maps an app to its team; ``reaches`` are
    the ``host:port`` outside the cluster it may reach."""
    teams = teams or {}
    found = [Connection('calls', app, team=teams.get(app, '')) for app in calls]
    found += [Connection('subscribes', app, event, teams.get(app, '')) for app, event in subscribes]
    found += [Connection('publishes', event=event) for event in publishes]
    found += [Connection('reaches', event=host) for host in reaches]
    found += [Connection('called-by', app, team=teams.get(app, '')) for app in callers]
    found += [Connection('subscriber', app, event, teams.get(app, '')) for event, app in subscribers]
    return tuple(found)


def intent(each: Instance, live: tuple[Connection, ...], source: Callable[[str], str | None] | None,
           *, together: Callable[..., list[Entry | None]] | None = None) -> tuple[tuple[Connection, ...], str]:
    """Overlay outgoing Git declarations on live rows. Failed reads never imply absent consent.

    Read only this instance and its declared targets, not a second live inventory. Both target records
    absent means pending; partial/unreadable records mean unavailable. Live-only rows remain visible.
    """
    try:
        if source is None:
            raise ValueError('no Git reader')

        def load(target: Instance) -> Entry | None:
            folder = f'apps/{target.app}/{target.env}'
            release, settings = source(f'{folder}/{RELEASE_FILE}'), source(f'{folder}/{SETTINGS_FILE}')
            if release is None and settings is None:
                return None
            app = source(f'apps/{target.app}/{APP_FILE}')
            if release is None or settings is None or app is None:
                raise ValueError('partial records')
            return Entry(App.parse(yamlio.load(app)), Release.parse(yamlio.load(release)), Settings.parse(yamlio.load(settings)))

        entry = load(each)
        if entry is None:
            raise ValueError('no records')
        world = {each: entry}
        targets = list(dict.fromkeys(Instance(relation.target, each.env) for relation in relations.declarations(each, entry)
                                     if Instance(relation.target, each.env) != each))
        reads = [lambda target=target: load(target) for target in targets]
        entries = together(*reads) if together else [read() for read in reads]
        world.update((target, found) for target, found in zip(targets, entries, strict=True) if found is not None)
        evaluated = relations.evaluate(each, world)
    except Exception:  # noqa: BLE001 (Git failure or invalid records: preserve observations, not a false verdict)
        return live, 'Git relation records unavailable; showing live connections only.'
    found = []
    for relation in evaluated:
        observed = any((r.role, r.app, r.event) == (relation.role, relation.target, relation.event) for r in live)
        status = relation.pending or ('' if observed else 'connected in Git, not yet observed live')
        if relation.pending and observed:
            status += '; still observed live'
        found.append(Connection(relation.role, relation.target, relation.event,
                                world[Instance(relation.target, each.env)].app.team
                                if Instance(relation.target, each.env) in world else '', status))
    declared = {(r.role, r.app, r.event) for r in found}
    found += [replace(r, status='removal in Git, still observed live') if r.role in ('calls', 'subscribes') else r
              for r in live if (r.role, r.app, r.event) not in declared]
    return tuple(found), ''

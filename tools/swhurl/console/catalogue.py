"""The catalogue: what the apps of one environment offer each other (docs/console.md#use-it).

One group per app, with what it uses; in it one entry for its API (a web app) and one per event it
publishes, whether or not the app describes it. The
facts come from the cluster, as every page's do (who runs, who publishes what, who uses it); the
descriptions come from the documents kept beside each instance in Git (apps/<app>/<env>/offers/,
docs/apps.md#what-an-app-offers), read through ``Documents`` and kept in memory per commit.
"""
from __future__ import annotations

import json
import time
from collections.abc import Callable
from dataclasses import dataclass, field

from swhurl.apps import offers
from swhurl.console.cluster import AppRow

Reader = Callable[[str], str | None]
"""A file of the platform repository's main branch by path; None if it is not there. Raises OSError-like
errors (any Exception) when the repository cannot be read."""


@dataclass(frozen=True)
class Described:
    """What one instance's documents say, or why they could not be read."""
    api: offers.Api | None = None
    events: tuple[offers.Event, ...] = ()
    problem: str = ''


class Documents:
    """Each instance's offers, read once per commit: the documents of a commit never change. A failure
    is kept for five minutes, so GitHub being away does not make every page wait."""

    def __init__(self, reader: Reader | None, clock: Callable[[], float] = time.monotonic):
        self.reader, self.clock = reader, clock
        self.kept: dict[tuple[str, str, str], Described] = {}
        self.failed: dict[tuple[str, str, str], tuple[float, Described]] = {}

    def of(self, app: str, env: str, commit: str) -> Described:
        key = (app, env, commit)
        if not commit:
            return Described()
        if key in self.kept:
            return self.kept[key]
        if key in self.failed and self.clock() - self.failed[key][0] < 300:
            return self.failed[key][1]
        found, keep = self.read(app, env, commit)
        if keep:
            self.kept[key] = found
        else:
            self.failed[key] = (self.clock(), found)
        return found

    def read(self, app: str, env: str, commit: str) -> tuple[Described, bool]:
        """``(what was read, whether it can be kept)``: not kept when Git holds another commit's documents
        than the cluster runs (a change on its way in), or when reading failed."""
        if self.reader is None:
            return Described(problem='the documents are in Git, which this console cannot read (no GitHub token)'), False
        folder = f'apps/{app}/{env}/{offers.OFFERS_DIR}'
        try:
            source_text = self.reader(f'{folder}/{offers.SOURCE_FILE}')
            if source_text is None:
                return Described(problem=f'{folder}/ is not in Git yet'), False
            source = offers.Source.parse(source_text)
            api_name, events_name = source.api, source.events
            api_text = self.reader(f'{folder}/{api_name}') if api_name else None
            events_text = self.reader(f'{folder}/{events_name}') if events_name else None
            found = Described(offers.api(api_text, api_name) if api_text else None,
                              tuple(offers.events(events_text, events_name)) if events_text else ())
        except offers.OfferError as error:
            return Described(problem=str(error)), False
        except Exception as error:  # noqa: BLE001 (the page renders without descriptions whatever the reader raised)
            return Described(problem=f'could not be read from Git: {type(error).__name__}'), False
        return found, source.commit == commit


@dataclass(frozen=True)
class Field:
    """One field of an event's payload, as a row: nested fields carry their path (``caller.id``)."""
    name: str
    type: str
    required: bool
    meaning: str = ''


def _type(schema: dict) -> str:
    """A JSON Schema's type as a few words: ``string (date-time)``, ``string or null``, ``one of a, b``."""
    if isinstance(schema.get('enum'), list) and schema['enum']:
        return 'one of ' + ', '.join(str(value) for value in schema['enum'])
    kind = schema.get('type')
    said = ' or '.join(str(each) for each in kind) if isinstance(kind, list) else str(kind or 'any')
    if said == 'array' and isinstance(schema.get('items'), dict):
        said = f'list of {_type(schema["items"])}'
    return f'{said} ({schema["format"]})' if schema.get('format') else said


def fields(schema: object, prefix: str = '', needed: bool = True) -> tuple[Field, ...]:
    """A payload schema's fields, one row each, nested objects and lists of objects below their parent.
    Empty for a schema that is not an object with properties: the page then shows the schema alone."""
    if not isinstance(schema, dict) or not isinstance(schema.get('properties'), dict):
        return ()
    required = schema.get('required') if isinstance(schema.get('required'), list) else []
    found: list[Field] = []
    for name, each in schema['properties'].items():
        each = each if isinstance(each, dict) else {}
        must = needed and name in required
        found.append(Field(prefix + str(name), _type(each), must, str(each.get('description') or '')))
        found.extend(fields(each, f'{prefix}{name}.', must))
        found.extend(fields(each.get('items'), f'{prefix}{name}[].', must))
    return tuple(found)


@dataclass(frozen=True)
class Entry:
    kind: str  # api or event
    name: str  # the app, or <app>.<event>
    app: str
    team: str
    description: str = ''
    described: bool = False  # whether the app's own document describes it
    used_by: tuple[str, ...] = ()
    address: str = ''  # an API: inside the cluster
    link: str = ''  # an API: where its description is served
    operations: tuple[tuple[str, str, str], ...] = ()
    schema: str = ''  # an event: its payload's JSON Schema, printed
    fields: tuple[Field, ...] = ()  # the same, one row per field
    example: str = ''
    commit: str = ''
    problem: str = ''
    could_use: tuple[tuple[str, str], ...] = field(default=())  # (app, team) that do not use it yet

    @property
    def event(self) -> str:
        return self.name.partition('.')[2]

    @property
    def sample_path(self) -> str:
        """A route of this API to show in a code sample: its first GET without a parameter, else ``/``."""
        return next((path for method, path, _ in self.operations if method == 'GET' and '{' not in path and path != '/'), '/')


@dataclass(frozen=True)
class Group:
    """One app of the environment: what it uses, and what it offers (nothing, for an app that only uses)."""
    app: str
    team: str
    uses: tuple[tuple[str, str], ...] = ()  # (api or event, its entry's name)
    entries: tuple[Entry, ...] = ()


def _printed(value: object) -> str:
    return json.dumps(value, indent=2, ensure_ascii=False) if value is not None else ''


def entries(rows: list[AppRow], env: str, documents: Documents) -> list[Entry]:
    """Every API and event of one environment, APIs first, each with who uses it and who could."""
    here = sorted((row for row in rows if row.instance.env == env), key=lambda row: row.instance.app)
    others = [(row.instance.app, row.team) for row in here]
    found: list[Entry] = []
    for row in here:
        app, c = row.instance.app, row.connections
        described = documents.of(app, env, c.offers)
        if c.address:
            api = described.api
            found.append(Entry('api', app, app, row.team, (api.description if api else ''), api is not None, c.callers, c.address, c.api,
                               api.operations if api else (), commit=c.offers, problem=described.problem,
                               could_use=tuple((other, team) for other, team in others if other != app and other not in c.callers)))
    for row in here:
        app, c = row.instance.app, row.connections
        described = documents.of(app, env, c.offers)
        by_name = {event.name: event for event in described.events}
        for name in c.publishes:
            event = by_name.get(name)
            users = tuple(subscriber for published, subscriber in c.subscribers if published == name)
            found.append(Entry('event', f'{app}.{name}', app, row.team, event.description if event else '', event is not None, users,
                               schema=_printed(event.payload) if event else '', fields=fields(event.payload) if event else (), example=_printed(event.example) if event else '',
                               commit=c.offers, problem=described.problem,
                               could_use=tuple((other, team) for other, team in others if other != app and other not in users)))
    return found


def groups(rows: list[AppRow], env: str, found: list[Entry]) -> list[Group]:
    """``found`` (entries, filtered or not) by app: the apps that offer something first, then those that
    only use; each with what it calls and receives, so both directions are on one page."""
    here = sorted((row for row in rows if row.instance.env == env), key=lambda row: row.instance.app)
    made = [Group(row.instance.app, row.team,
                  tuple(('api', name) for name in row.connections.calls) + tuple(('event', name) for name in row.connections.subscribes),
                  tuple(e for e in found if e.app == row.instance.app)) for row in here]
    return sorted(made, key=lambda group: not group.entries)

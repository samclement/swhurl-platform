"""What Git declares about an instance: v2's own records.

A page reads two kinds of fact. What the cluster runs comes from kubectl (``cluster.py``, ``ops.py``);
what Git says comes from here. Since 31 October 2026 every instance is v2-owned, so the declaration is
``v2-records/<app>/app.json`` beside ``v2-records/<app>/<env>/{instance,release}.json``; what v2 compiled
from them is in the cluster, and ``cluster.py`` reads it there like any other fact.

No SOPS file is read: a record's encrypted seed is a file v2 copies, not a fact this console shows.

One instance may still be v1's — the one ``make app-new`` created and nobody migrated yet (deletion plan,
decision 1b). Its declaration is read from ``apps/<app>/`` through v1's record grammar, the one place this
module imports ``swhurl.apps.model.records``. That branch goes when v2 gains ``create``.
"""
from __future__ import annotations

import json
from collections.abc import Callable
from dataclasses import dataclass, replace

from swhurl import yamlio
from swhurl.apps.connections import Connection
from swhurl.apps.model import records as legacy

Reader = Callable[[str], str | None]
"""A file of the platform repository's main branch by path; None when it is not there."""

V2_RECORDS = 'v2-records'
V1_RECORDS = 'apps'
TEAM_GRANT = legacy.TEAM_GRANT

V2, V1, ABSENT = 'v2', 'v1', ''
"""Which record shape declared the instance: v2's JSON, v1's YAML, or neither (nothing in Git)."""


@dataclass(frozen=True)
class Declared:
    """One instance's declared facts, or ``problem`` saying why they could not be read.

    Empty is a fact here, not a missing one: ``source`` says whether anything declared the instance, and
    a field v2 does not model stays empty rather than borrowing a default — a page shows the gap, not a
    guess.
    """
    source: str = ABSENT
    app: str = ''
    env: str = ''
    problem: str = ''
    team: str = ''
    kind: str = ''
    port: int | None = None
    calls: tuple[str, ...] = ()
    allows: tuple[str, ...] = ()
    subscriptions: tuple[str, ...] = ()
    publishes: tuple[str, ...] = ()
    minimum: dict | None = None
    """v1's record of the least the image needs; v2's release states no such thing, so it is None there."""

    @property
    def declared(self) -> bool:
        return self.source != ABSENT


def of(app: str, env: str, read: Reader | None) -> Declared:
    """One instance's declaration.

    A read failure, or part of the records, is a ``problem`` beside whatever was read: never an exception,
    never a false absence, and never a fact invented to fill the hole.
    """
    if read is None:
        return Declared(app=app, env=env, problem='this console has no Git reader (no GitHub token)')
    try:
        found = _v2(app, env, read)
        found = found if found is not None else _v1(app, env, read)
    except Exception as error:  # noqa: BLE001 (GitHub unavailable or an unreadable record: the page says so)
        return Declared(app=app, env=env, problem=f'its records could not be read: {error}')
    return replace(found, app=app, env=env)


def rows(declared: Declared, live: tuple[Connection, ...], read: Reader | None = None,
         together: Callable[..., list] | None = None) -> tuple[Connection, ...]:
    """This instance's connections, declared and observed as one list.

    What Git declares comes first, each row carrying why it does not connect yet — the same reasons v1's
    relation evaluator gave, because they are the platform's, not a renderer's: a target that is absent, a
    target that does not allow this app, an event the target does not publish. Then what only the cluster
    shows: a removal Git has made and the cluster has not applied, an egress grant (which v2 does not record)
    and the subscribers other apps declare.

    ``team:<name>`` grants are not rows: a grant names a rule, and which apps satisfy it is the resolver's
    answer, not this page's guess. The Allowed callers list shows them as the grants they are.
    """
    if not declared.declared:
        return live
    callers = tuple(name for name in declared.allows if not name.startswith(TEAM_GRANT))
    targets = list(dict.fromkeys([*declared.calls, *(app for app, _ in _pairs(declared.subscriptions)), *callers]))
    reads = [lambda name=name: of(name, declared.env, read) for name in targets]
    world = dict(zip(targets, together(*reads) if together else [each() for each in reads], strict=True))
    observed = {(row.role, row.app, row.event) for row in live}

    def status(pending: str, role: str, app: str, event: str = '') -> str:
        if pending:
            return f'{pending}; still observed live' if (role, app, event) in observed else pending
        return '' if (role, app, event) in observed else 'connected in Git, not yet observed live'

    shown = [Connection('calls', name, team=world[name].team,
                        status=status(_call_pending(declared, world[name], name), 'calls', name))
             for name in declared.calls]
    shown += [Connection('subscribes', app, event, team=world[app].team,
                         status=status(_event_pending(world[app], app, event, declared.env), 'subscribes', app, event))
              for app, event in _pairs(declared.subscriptions)]
    shown += [Connection('publishes', event=event) for event in declared.publishes]
    shown += [Connection('called-by', name, team=world[name].team) for name in callers]
    declared_keys = {(row.role, row.app, row.event) for row in shown}
    for row in live:
        if (row.role, row.app, row.event) in declared_keys:
            continue
        if row.role in ('calls', 'subscribes'):  # Git no longer declares it, the cluster still runs it
            shown.append(replace(row, status='removed in Git, still observed live'))
        elif row.role in ('reaches', 'subscriber'):  # observed only: v2 records no egress grant, no subscriber list
            shown.append(row)
    return tuple(shown)


def _call_pending(declared: Declared, target: Declared, name: str) -> str:
    """Why a call cannot connect yet, in the words the operator sees; '' when it connects."""
    if not target.declared:
        return f'waiting for {name}/{declared.env} to exist'
    if not target.kind:  # its app record is there, its release is not: nothing says what it serves
        return f'waiting for {name}/{declared.env} to declare a release'
    if target.kind != 'web' or not target.port:
        return f'a {target.kind}: it has no address to call'
    grants = set(target.allows)
    return '' if {declared.app, f'{TEAM_GRANT}{declared.team}'} & grants else f'waiting for {name} to allow it'


def _event_pending(target: Declared, name: str, event: str, env: str) -> str:
    if not target.declared or not target.kind:
        return f'waiting for {name}/{env} to exist' if not target.declared else \
            f'waiting for {name}/{env} to declare a release'
    return '' if event in target.publishes else f'waiting for {name} to publish {event}'



def _pairs(subscriptions: tuple[str, ...]) -> tuple[tuple[str, str], ...]:
    return tuple((name.partition('.')[0], name.partition('.')[2]) for name in subscriptions if '.' in name)


def _v2(app: str, env: str, read: Reader) -> Declared | None:
    """v2's three JSON records, or None when Git holds no instance record (so the instance is not v2's)."""
    instance_path = f'{V2_RECORDS}/{app}/{env}/instance.json'
    instance_text = read(instance_path)
    if instance_text is None:  # no instance record: not v2's, whatever else Git holds
        return None
    release_path, app_path = f'{V2_RECORDS}/{app}/{env}/release.json', f'{V2_RECORDS}/{app}/app.json'
    release_text, app_text = read(release_path), read(app_path)
    missing = [path for path, text in ((release_path, release_text), (app_path, app_text)) if text is None]
    instance, release, application = (json.loads(text or '{}') for text in (instance_text, release_text, app_text))
    runtime = release.get('runtime') or {}
    events = instance.get('events') or {}
    return Declared(
        source=V2, problem=f'v2 holds its instance record but not {", ".join(missing)}' if missing else '',
        team=str(application.get('team', '')), kind=str(runtime.get('kind', '')), port=runtime.get('port'),
        calls=tuple(instance.get('calls') or ()), allows=tuple(instance.get('allows') or ()),
        subscriptions=tuple(events.get('subscriptions') or ()),
        publishes=tuple(release.get('publishes') or ()))


def _v1(app: str, env: str, read: Reader) -> Declared:
    """v1's three YAML records, through v1's grammar: the branch decision 1b keeps until v2 has ``create``."""
    folder = f'{V1_RECORDS}/{app}/{env}'
    release_text = read(f'{folder}/{legacy.RELEASE_FILE}')
    settings_text = read(f'{folder}/{legacy.SETTINGS_FILE}')
    app_text = read(f'{V1_RECORDS}/{app}/{legacy.APP_FILE}')
    if release_text is None and settings_text is None and app_text is None:
        return Declared(problem='its records are not in Git, so what it declares is unknown')
    missing = [f'{folder}/{name}' for name, text in ((legacy.RELEASE_FILE, release_text),
                                                    (legacy.SETTINGS_FILE, settings_text)) if text is None]
    if app_text is None:
        missing.append(f'{V1_RECORDS}/{app}/{legacy.APP_FILE}')
    release = legacy.Release.parse(yamlio.load(release_text)) if release_text is not None else None
    settings = legacy.Settings.parse(yamlio.load(settings_text)) if settings_text is not None else None
    application = legacy.App.parse(yamlio.load(app_text)) if app_text is not None else None
    needs, grants = (release.resources if release is not None else None), (settings.grants if settings else None)
    return Declared(
        source=V1, problem=f'v1 holds part of its records but not {", ".join(missing)}' if missing else '',
        team=application.team if application is not None else '',
        kind=release.kind if release is not None else '', port=release.port if release is not None else None,
        calls=grants.calls if grants else (), allows=grants.allows if grants else (),
        subscriptions=grants.subscriptions if grants else (),
        publishes=release.publishes if release is not None else (),
        minimum={name: value for name, value in (('cpu', needs.cpu), ('memory', needs.memory),
                                                 ('memory_limit', needs.memory_limit)) if value}
        if needs is not None else None)

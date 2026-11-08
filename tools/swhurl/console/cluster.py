"""What the console reads from the cluster: read-only, through a Runner, no Secrets and no exec."""
from __future__ import annotations

import re
from dataclasses import dataclass, field

from swhurl.apps import connections as links
from swhurl.apps.instance import Instance
from swhurl.apps.state import State, ready_reason, unit_state
from swhurl.kube import ready_condition
from swhurl.report import Entry
from swhurl.run import CommandError, Runner

UNITS = ['kubectl', '-n', 'flux-system', 'get', 'kustomizations.kustomize.toolkit.fluxcd.io', '-o', 'json']


class ReadError(Exception):
    """The cluster could not be read; the message is already redacted."""


WORDING = {
    'callee_absent': 'waiting for {app}/{env} to exist',
    'no_consent': 'waiting for {app} to allow it',
    'callee_not_web': 'a worker: it has no address to call',
    'self_call': 'it calls itself',
    'publisher_not_ready': 'waiting for {app} to publish {event}',
}


@dataclass(frozen=True)
class Connections:
    """The rows an instance's records state, as `swhurl-v2 status --all` reports them: nothing derived here."""

    address: str = ''
    api: str = ''
    calls: tuple[str, ...] = ()
    callers: tuple[str, ...] = ()
    publishes: tuple[str, ...] = ()
    subscribes: tuple[str, ...] = ()
    subscribers: tuple[str, ...] = ()
    rows: tuple[links.Connection, ...] = ()
    offers: str = ''
    egress: tuple[str, ...] = ()
    proxy_port: int = 0

    @property
    def summary(self) -> str:
        """One line for a list: only what there is."""
        parts = [(len(self.calls), 'calls {}'), (len(self.callers), 'called by {}'), (len(self.publishes), 'publishes {}'),
                 (len(self.subscribes), 'subscribes to {}'), (len(self.egress), 'reaches {} outside')]
        return ' · '.join(text.format(count) for count, text in parts if count)

    @classmethod
    def of(cls, inventory: dict | None, teams: dict | None, env: str) -> Connections:
        """The block as rows: a waiting row says why, in the words the page already used."""
        if not inventory:
            return cls()
        rows = []
        for row in inventory['connections']:
            reason = row.get('reason', '')
            peer = row.get('app', '')
            status = WORDING[reason].format(app=peer, env=env, event=row.get('event', '')) if reason else ''
            # A host outside the cluster is a row too, and its host:port is the column an event name takes.
            rows.append(links.Connection(row['role'], app=peer, event=row.get('event') or row.get('host', ''),
                                         team=(teams or {}).get(peer, ''), status=status))
        return cls(address=inventory['address'], api=inventory['api'],
                   calls=tuple(inventory['calls']), callers=tuple(inventory['callers']),
                   publishes=tuple(inventory['publishes']), subscribes=tuple(inventory['subscriptions']),
                   subscribers=tuple((row.get('event', ''), row.get('app', '')) for row in inventory['connections'] if row['role'] == 'subscriber'),
                   rows=tuple(rows), offers=inventory['offers'],
                   egress=tuple(inventory['egress']['grants']), proxy_port=inventory['egress']['port'])


@dataclass(frozen=True)
class Declared:
    """One instance's own records, as the app page shows them: the block `status --all` reports for it."""

    inventory: dict | None = None
    V2 = 'v2'
    GIT = 'git'

    @property
    def source(self) -> str:
        return self.V2

    @property
    def problem(self) -> str:
        return '' if self.inventory is not None else 'its records are not in Git, so what it declares is unknown'

    @property
    def declared(self) -> bool:
        return self.inventory is not None

    @property
    def team(self) -> str:
        return self.value.get('team', '')

    @property
    def kind(self) -> str:
        return self.value.get('kind', '')

    @property
    def calls(self) -> tuple[str, ...]:
        return tuple(self.value.get('calls') or ())

    @property
    def subscriptions(self) -> tuple[str, ...]:
        return tuple(self.value.get('subscriptions') or ())

    @property
    def allows(self) -> tuple[str, ...]:
        return tuple(self.value.get('allows') or ())

    @property
    def database(self) -> str | None:
        return (self.value.get('database') or {}).get('kind') or None

    @property
    def image_reference(self) -> str:
        artifact = self.value.get('artifact') or {}
        return f'{artifact.get("image", "")}@{artifact.get("digest", "")}' if artifact.get('digest') else artifact.get('image', '')

    @property
    def value(self) -> dict:
        return self.inventory or {}


@dataclass(frozen=True)
class AppRow:
    """One deployed instance as the pages show it: the Go report, and the block its records state."""

    instance: Instance
    state: State
    suspended: bool | None = None
    inventory: dict | None = None
    applied_revision: str = ''
    report: dict | None = None
    teams: dict = field(default_factory=dict)

    @property
    def value(self) -> dict:
        return self.inventory or {}

    @property
    def team(self) -> str:
        return self.value.get('team', '')

    @property
    def image(self) -> str:
        return (self.value.get('artifact') or {}).get('image', '')

    @property
    def tag(self) -> str:
        return ''

    @property
    def digest(self) -> str:
        return (self.value.get('artifact') or {}).get('digest', '')

    @property
    def hosts(self) -> tuple[str, ...]:
        return tuple(self.value.get('hosts') or ())

    @property
    def database_path(self) -> str | None:
        """The declared path; ``None`` when nothing is known: no record, or no HelmRelease yet to run it."""
        if not self.value or (self.report or {}).get('live', {}).get('release', {}).get('status') == 'absent':
            return None
        return (self.value.get('database') or {}).get('path', '')

    @property
    def render_hash(self) -> str:
        return ''

    @property
    def connections(self) -> Connections:
        return Connections.of(self.value, self.teams, self.instance.env)


@dataclass(frozen=True)
class AppSummary:
    """One app across its environments, for the Apps list."""
    app: str
    envs: dict[str, AppRow]

    @property
    def team(self) -> str:
        return next((row.team for row in self.envs.values() if row.team), '')
    comparison: str  # 'same', 'differs', or '' with one environment


def compare(staging: AppRow, prod: AppRow) -> str:
    """The same image, by digest: v2 names no tag, so there is no run to count."""
    if (staging.digest or prod.digest) and staging.digest == prod.digest:
        return 'same'
    return 'same' if staging.image == prod.image and not (staging.digest or prod.digest) else 'differs'


def summaries(rows: list[AppRow]) -> list[AppSummary]:
    by_app: dict[str, dict[str, AppRow]] = {}
    for row in rows:
        by_app.setdefault(row.instance.app, {})[row.instance.env] = row
    out = []
    for app, envs in sorted(by_app.items()):
        both = 'staging' in envs and 'prod' in envs
        out.append(AppSummary(app, envs, compare(envs['staging'], envs['prod']) if both else ''))
    return out


@dataclass(frozen=True)
class Unit:
    name: str
    ready: tuple[str, str]
    suspended: bool
    depends_on: list[str]
    revision: str
    level: int  # 0 for units with no dependencies, else one more than the deepest dependency
    path: str = ''
    interval: str = ''
    needed_by: tuple[str, ...] = ()
    reason: str = ''

    @property
    def state(self) -> State:
        return unit_state(self.ready, self.suspended, self.reason)

    @property
    def layer(self) -> str:
        return layer_of(self.name)

    @property
    def healthy(self) -> bool:
        return self.state.key == 'healthy'


# Units by name prefix, in the order they build on each other: (prefix, title, what they hold).
LAYERS = (('cluster-', 'Cluster', "Flux's own Git source and unit list, applied by make flux-bootstrap"),
          ('infra-', 'Infrastructure', 'Namespaces, certificates and the ingress controller'),
          ('platform-', 'Platform services', 'Sign-in, observability, this console and automation'),
          ('app-', 'Deployments', 'One unit per app instance'))
OTHER_LAYER = 'Other'


def layer_of(name: str) -> str:
    return next((title for prefix, title, _ in LAYERS if name.startswith(prefix)), OTHER_LAYER)


def target(name: str) -> tuple[str, str]:
    """How every page names a job's or problem's subject: ``(label, link)``. An app instance is always
    ``<app>/<env>`` and links to its app page, whether given as its unit (``app-<app>-<env>``) or as
    ``<app>/<env>``; anything else is a Flux unit, by name."""
    if name.startswith('team:'):  # a change made on the Teams page
        return f'team {name[5:]}', f'/teams#{name[5:]}'
    found = Instance.of_unit(name)
    app, _, env = name.partition('/')
    found = found or (Instance.named(app, env) if env else None)
    if found:
        return f'{found.app}/{found.env}', f'/apps/{found.app}/{found.env}'
    return name, f'/units/{name}'


def items(runner: Runner, args: list[str]) -> list[dict]:
    try:
        return (runner.json(args) or {}).get('items', [])
    except CommandError as error:
        raise ReadError(str(error)) from None



@dataclass(frozen=True)
class MapNode:
    row: AppRow
    x: int
    y: int


@dataclass(frozen=True)
class AppMap:
    """The apps of one environment as boxes, with a line for every call and every event subscription."""
    nodes: list[MapNode]
    edges: list[tuple[str, str, str, str]]  # (kind: call or event, SVG path, from, to)
    width: int
    height: int
    box: tuple[int, int] = (210, 56)


def app_map(rows: list[AppRow], env: str) -> AppMap:
    """Lay the apps out left to right in the direction requests and events travel: an app sits one
    column right of the furthest app that calls it or whose events it receives."""
    here = {row.instance.app: row for row in rows if row.instance.env == env}
    links = [('call', app, callee) for app, row in here.items() for callee in row.connections.calls if callee in here]
    links += [('event', name.split('.')[0], app) for app, row in here.items() for name in row.connections.subscribes
              if name.split('.')[0] in here]
    sources: dict[str, set[str]] = {app: set() for app in here}
    for _, source, target in links:
        sources[target].add(source)
    columns: dict[str, int] = {}

    def column(app: str, seen: frozenset[str] = frozenset()) -> int:
        if app not in columns:
            columns[app] = 1 + max((column(s, seen | {app}) for s in sources[app] if s not in seen), default=-1)
        return columns[app]
    width, height, gap_x, gap_y, margin = 210, 56, 110, 20, 12
    filled: dict[int, int] = {}
    position: dict[str, tuple[int, int]] = {}
    # Connected apps first, so the apps nothing links to gather at the bottom of the first column.
    for app in sorted(here, key=lambda a: (not (sources[a] or any(a == s for _, s, _ in links)), a)):
        level = column(app)
        position[app] = (margin + level * (width + gap_x), margin + filled.get(level, 0) * (height + gap_y))
        filled[level] = filled.get(level, 0) + 1
    edges, lowest = [], 0
    for kind, source, target in links:
        (x1, y1), (x2, y2) = position[source], position[target]
        offset = -8 if kind == 'call' else 10  # a call and an event between the same two apps stay apart
        start, end = (x1 + width, y1 + height // 2 + offset), (x2, y2 + height // 2 + offset)
        bend = max(40, abs(end[0] - start[0]) // 2)
        # A line that skips a column would run behind the boxes between: send it under them instead.
        skipped = abs(columns[target] - columns[source]) - 1
        dip = (height // 2 + 14 + 10 * skipped + (0 if kind == 'call' else 8)) if skipped > 0 else 0
        lowest = max(lowest, max(start[1], end[1]) + dip)
        edges.append((kind, f'M {start[0]} {start[1]} C {start[0] + bend} {start[1] + dip * 2}, '
                            f'{end[0] - bend} {end[1] + dip * 2}, {end[0]} {end[1]}', source, target))
    return AppMap([MapNode(here[app], x, y) for app, (x, y) in sorted(position.items())], edges,
                  margin * 2 + (max(filled, default=0) + 1) * (width + gap_x) - gap_x,
                  max(margin * 2 + max(filled.values(), default=1) * (height + gap_y) - gap_y, lowest + margin),
                  (width, height))


def units(runner: Runner) -> list[Unit]:
    found = {u['metadata']['name']: u for u in items(runner, UNITS)}
    levels: dict[str, int] = {}

    def level(name: str, seen: frozenset[str] = frozenset()) -> int:
        if name not in levels:
            deps = [d['name'] for d in found[name]['spec'].get('dependsOn') or [] if d['name'] in found]
            levels[name] = 1 + max((level(d, seen | {name}) for d in deps if d not in seen), default=-1)
        return levels[name]

    needed_by: dict[str, list[str]] = {}
    for name, unit in found.items():
        for d in unit['spec'].get('dependsOn') or []:
            needed_by.setdefault(d['name'], []).append(name)
    return sorted((Unit(name, ready_condition(unit), bool(unit['spec'].get('suspend')),
                        [d['name'] for d in unit['spec'].get('dependsOn') or []],
                        (unit.get('status') or {}).get('lastAppliedRevision', '').split(':')[-1][:7],
                        level(name), unit['spec'].get('path', ''), unit['spec'].get('interval', ''),
                        tuple(sorted(needed_by.get(name, []))), ready_reason(unit))
                   for name, unit in found.items()), key=lambda u: (u.level, u.name))


@dataclass(frozen=True)
class Checks:
    passed: bool
    sections: list[tuple[str, list[Entry]]]  # in the order swhurl-v2 verify runs them

    @property
    def count(self) -> int:
        return sum(len(group) for _, group in self.sections)


NEWEST = re.compile(r'(\S+)-staging: (newest image .*)')


def condensed(group: list[Entry]) -> list[Entry]:
    """A check group as the console words it: an app's image check names the instance as every page
    does (``<app>/staging``) rather than its ImagePolicy."""
    return [Entry(e.section, e.level, f'{target(f"{m[1]}/staging")[0]}: {m[2]}') if (m := NEWEST.fullmatch(e.message)) else e
            for e in group]


FLUX_SECTION = 'Flux Kustomizations'  # the units list shows these; the check's failures still count as problems


def updating(found: list[Unit]) -> list[Problem]:
    """Units applying a change or waiting for a dependency: normal for a few seconds after every push."""
    out = []
    for u in found:
        if u.state.key == 'updating':
            label, link = target(u.name)
            out.append(Problem('updating', f'{label}: {u.state.detail}', link))
    return out


@dataclass(frozen=True)
class Problem:
    level: str  # a mark: failing, warn, suspended or updating
    text: str
    link: str = ''


def problems(checks: Checks, found: list[Unit]) -> list[Problem]:
    """What needs attention: failing or warning checks, then suspended units (which can still be Ready)."""
    names = {u.name for u in found}
    out = []
    for section, group in checks.sections:
        for entry in group:
            if entry.level in ('bad', 'warn'):
                if section == FLUX_SECTION and entry.message.partition(' ')[0] in names:
                    # The check read the units a moment before ``found`` did, and a unit can become Ready
                    # in between: ``found`` alone says what a unit is, so every page agrees with itself.
                    continue
                out.append(Problem('failing' if entry.level == 'bad' else 'warn', entry.message))
    for u in found:
        failing = unit_state(u.ready, reason=u.reason)  # suspended or not; waiting is not failing: updating() lists it
        if failing.key == 'failing':
            label, link = target(u.name)
            out.append(Problem('failing', f'{label} is not Ready' + (f': {failing.detail}' if failing.detail else ''), link))
    for u in found:
        if u.suspended:
            label, link = target(u.name)
            out.append(Problem('suspended', f'{label} is suspended: Git changes are not applied to it until it is resumed', link))
    return out

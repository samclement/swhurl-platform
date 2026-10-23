"""What the console reads from the cluster: read-only, through a Runner, no Secrets and no exec."""
from __future__ import annotations

import functools
import io
import re
from collections import defaultdict
from dataclasses import dataclass

from swhurl import interface
from swhurl.apps import calling, egress, events, helmrelease, ops
from swhurl.apps import connections as links
from swhurl.apps.contract import (
    AUTO_DEPLOY_TAG_PATTERN,
)
from swhurl.apps.instance import Instance
from swhurl.apps.state import State, ready_reason, unit_state, verdict
from swhurl.kube import ready_condition
from swhurl.report import Entry, Report
from swhurl.run import CommandError, Runner
from swhurl.verify import verify_platform

UNITS = ['kubectl', '-n', 'flux-system', 'get', 'kustomizations.kustomize.toolkit.fluxcd.io', '-o', 'json']
RELEASES = ['kubectl', 'get', 'helmreleases.helm.toolkit.fluxcd.io', '--all-namespaces', '-o', 'json']
APP_NAMESPACES = ['kubectl', 'get', 'namespaces', '-l', interface.APP, '-o', 'json']
SOURCE = ['kubectl', '-n', 'flux-system', 'get', 'gitrepository', ops.SOURCE, '-o', 'json']
PODS = ['kubectl', 'get', 'pods', '--all-namespaces', '-l', ops.INSTANCE_LABEL, '-o', 'json']
WORKLOADS = ['kubectl', 'get', ops.WORKLOAD_KINDS, '--all-namespaces', '-l', ops.INSTANCE_LABEL, '-o', 'json']
CONSOLE_CHECKS = frozenset({'cluster'})


class ReadError(Exception):
    """The cluster could not be read; the message is already redacted."""


@dataclass(frozen=True)
class Connections:
    """How an instance and the other apps of its environment reach each other."""
    address: str = ''  # inside the cluster; '' for a worker
    api: str = ''  # where its API description is, if it says so
    calls: tuple[str, ...] = ()
    callers: tuple[str, ...] = ()
    publishes: tuple[str, ...] = ()  # its own event names
    subscribes: tuple[str, ...] = ()  # <app>.<event>
    subscribers: tuple[tuple[str, str], ...] = ()  # (its event, the app receiving it)
    rows: tuple[links.Connection, ...] = ()  # the same, one row per connection: what every list shows
    offers: str = ''  # the commit its API and event documents were read at (make app-offers); '' if it has none
    egress: tuple[str, ...] = ()  # host:port outside the cluster it may reach (make app-egress)
    proxy_port: int | None = None  # its own port on the egress proxy; None when it has none

    @property
    def summary(self) -> str:
        """One line for a list: only what there is."""
        parts = [(len(self.calls), 'calls {}'), (len(self.callers), 'called by {}'), (len(self.publishes), 'publishes {}'),
                 (len(self.subscribes), 'subscribes to {}'), (len(self.egress), 'reaches {} outside')]
        return ' · '.join(text.format(count) for count, text in parts if count)


@dataclass(frozen=True)
class AppRow:
    instance: Instance
    state: State  # the verdict the app page gives from the same facts (apps/state.py)
    suspended: bool  # its Flux unit: what the row's button resumes
    image: str
    tag: str = ''
    digest: str = ''
    hosts: tuple[str, ...] = ()
    database_path: str | None = None  # None: release missing; empty: no SQLite configured
    team: str = ''  # from the namespace label; '' when unknown
    connections: Connections = Connections()
    applied_revision: str = ''  # the Git revision its Flux unit last applied
    render_hash: str = ''
    render_ready: bool = False
    render_state: State = State('unknown')  # the verdict once render_hash is proved to be Git's target (stages.progress)
    image_matches: bool = False


@dataclass(frozen=True)
class AppSummary:
    """One app across its environments, for the Apps list."""
    app: str
    envs: dict[str, AppRow]

    @property
    def team(self) -> str:
        return next((row.team for row in self.envs.values() if row.team), '')
    comparison: str  # 'same', 'ahead' (staging has a newer run), 'differs', or '' with one environment


def compare(staging: AppRow, prod: AppRow) -> str:
    if (staging.digest or prod.digest) and staging.digest == prod.digest or staging.image == prod.image:
        return 'same'
    runs = [re.match(AUTO_DEPLOY_TAG_PATTERN, row.tag) for row in (staging, prod)]
    if all(runs) and int(runs[0]['run']) > int(runs[1]['run']):
        return 'ahead'
    return 'differs'


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


NOT_READ = State('unknown', 'its state was not read for this page')


def fetched(runner: Runner) -> str:
    """The newest Git revision Flux has fetched; '' when it has none."""
    try:
        source = runner.json(SOURCE) or {}
    except CommandError as error:
        raise ReadError(str(error)) from None
    return ((source.get('status') or {}).get('artifact') or {}).get('revision', '')


def apps(runner: Runner, states: bool = True, *, include_removing: bool = False) -> list[AppRow]:
    """Every app instance, from what runs: its Flux unit, its HelmRelease (image, hosts and the
    platform's SWHURL_* variables, which say who it calls and what events it handles), its
    namespace's team label, and its state from the same facts as its own page (the Git source,
    its pods and its workloads). ``states=False`` skips those three reads for a page that shows
    no state: every row is then Unknown and says why."""
    def namespaces() -> list[dict]:
        try:
            return items(runner, APP_NAMESPACES)
        except ReadError:  # an account that may not list namespaces still gets the rest
            return []

    def git_source() -> dict:
        try:
            return runner.json(SOURCE) or {}
        except CommandError as error:
            raise ReadError(str(error)) from None
    lists = [functools.partial(items, runner, RELEASES), namespaces, functools.partial(items, runner, UNITS)]
    judged = [git_source, functools.partial(items, runner, PODS), functools.partial(items, runner, WORKLOADS)]
    read = runner.together(*lists, *(judged if states else ()))
    releases = {(r['metadata']['namespace'], r['metadata']['name']): r for r in read[0]}
    teams = {n['metadata']['name']: (n['metadata'].get('labels') or {}).get(interface.TEAM, '') for n in read[1]}
    found = [(instance, unit, releases.get((instance.namespace, instance.app)))
             for unit in read[2] if (instance := Instance.of_unit(unit['metadata']['name']))]
    # A unit may disappear before Helm finishes uninstalling its release. Keep it visible until both are gone.
    known = {instance for instance, _, _ in found}
    if include_removing:
        found += [(instance, {}, release) for (namespace, name), release in releases.items()
                  if (instance := Instance.of_namespace(namespace)) and instance.app == name and instance not in known]
    source = read[3] if states else {}
    owned: dict[str, dict[tuple[str, str], list[dict]]] = {}
    for kind, listed in (('pods', read[4:5]), ('workloads', read[5:6])):
        owned[kind] = defaultdict(list)
        for item in listed[0] if listed else ():
            metadata = item['metadata']
            owned[kind][metadata.get('namespace', ''), (metadata.get('labels') or {}).get(ops.INSTANCE_LABEL, '')].append(item)
    subscribed = [(instance.env, publisher, event, instance.app) for instance, _, release in found
                  for publisher, event in events.subscribed(helmrelease.variables(release))]
    rows = []
    for instance, unit, release in found:
        variables, port, image = helmrelease.variables(release), helmrelease.port(release), helmrelease.image(release)
        hosts = helmrelease.hosts(release)
        address = f'http://{instance.app}.{instance.namespace}.svc.cluster.local:{port}' if port else ''
        annotations = ((release or {}).get('metadata') or {}).get('annotations') or {}
        path = str(annotations.get(interface.API_ANNOTATION, '')) if address else ''
        calls, callers = calling.declared_calls(variables), calling.allowed_callers(variables)
        publishes, subscribes = events.published(variables), events.subscribed(variables)
        subscribers = [(event, subscriber) for env, publisher, event, subscriber in subscribed
                       if (env, publisher) == (instance.env, instance.app)]
        connections = Connections(
            address, (f'https://{hosts[0]}{path}' if hosts else address + path) if path else '',
            tuple(calls), tuple(callers), tuple(publishes), tuple('.'.join(pair) for pair in subscribes), tuple(subscribers),
            links.rows(calls, callers, publishes, subscribes, subscribers,
                       {other.app: teams.get(other.namespace, '') for other, _, _ in found if other.env == instance.env},
                       egress.granted(release)),
            str(annotations.get(interface.OFFERS_ANNOTATION, '')),
            tuple(egress.granted(release)), egress.port_of(variables))
        mine = (instance.namespace, instance.app)
        status = ops.status_of(instance, unit, source, release, {'items': owned['pods'][mine]},
                               {'items': owned['workloads'][mine]})
        rows.append(AppRow(instance, verdict(status) if states else NOT_READ, status.unit_suspended,
                           ops.desired_image(release) if release else 'no HelmRelease',
                           image.get('tag', ''), image.get('digest', ''), hosts,
                           str(variables.get(interface.DATABASE_PATH_VARIABLE, '')) if release else None,
                           teams.get(instance.namespace, ''), connections, status.applied_revision,
                           ((release or {}).get('metadata', {}).get('annotations') or {}).get(interface.RENDER_ANNOTATION, ''),
                           release_ready(release),
                           verdict(status, render_applied=True) if states else NOT_READ,
                           status.image_state == 'matches'))
    return sorted(rows, key=lambda row: (row.instance.app, row.instance.env))


def release_ready(release: dict | None) -> bool:
    if not release:
        return False
    generation = release.get('metadata', {}).get('generation')
    condition = next((c for c in (release.get('status') or {}).get('conditions') or [] if c.get('type') == 'Ready'), {})
    return (generation is not None and condition.get('status') == 'True'
            and condition.get('observedGeneration') == generation)


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
    sections: list[tuple[str, list[Entry]]]  # in the order verify-platform runs them
    notes: list[str]  # which checks were skipped, and why

    @property
    def skipped(self) -> list[str]:
        """The checks left to ``make verify-platform`` (they read Secrets, exec into pods or need the host)."""
        return [name.strip() for note in self.notes if ':' in note for name in note.split(':', 1)[1].split(',')]

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


def platform_checks(runner: Runner) -> Checks:
    """The verify-platform checks the console's account can run."""
    report = Report(io.StringIO())
    code = verify_platform(runner, report, allowed=CONSOLE_CHECKS)
    if code and not report.entries:
        raise ReadError('kubectl cannot reach the cluster')
    sections: dict[str, list[Entry]] = {}
    for entry in report.entries:
        if entry.level != 'info':
            sections.setdefault(entry.section, []).append(entry)
    return Checks(code == 0, list(sections.items()), [e.message for e in report.entries if e.level == 'info'])

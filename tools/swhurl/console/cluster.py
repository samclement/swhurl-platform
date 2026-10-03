"""What the console reads from the cluster: read-only, through a Runner, no Secrets and no exec."""
from __future__ import annotations

import io
import re
from dataclasses import dataclass

from swhurl.apps import ops, promotion
from swhurl.apps.contract import AUTO_DEPLOY_TAG_PATTERN, ENVIRONMENTS
from swhurl.apps.new import NAME_RE
from swhurl.flux import WAITING_REASONS
from swhurl.report import Entry, Report
from swhurl.run import CommandError, Runner
from swhurl.verify import ready_condition, verify_platform

UNITS = ['kubectl', '-n', 'flux-system', 'get', 'kustomizations.kustomize.toolkit.fluxcd.io', '-o', 'json']
RELEASES = ['kubectl', 'get', 'helmreleases.helm.toolkit.fluxcd.io', '--all-namespaces', '-o', 'json']
CONSOLE_CHECKS = frozenset({'cluster'})


class ReadError(Exception):
    """The cluster could not be read; the message is already redacted."""


@dataclass(frozen=True)
class State:
    """The one status vocabulary every page uses (each shown with its own mark, not colour alone)."""
    key: str  # healthy, updating, failing, suspended, unknown or stopped
    detail: str = ''

    @property
    def label(self) -> str:
        return self.key.capitalize()


DEPENDENCY = re.compile(r"dependency '(?:[^/']+/)?([^']+)' is not ready")


def ready_reason(obj: dict) -> str:
    return next((c.get('reason', '') for c in (obj.get('status') or {}).get('conditions') or []
                 if c.get('type') == 'Ready'), '')


def state_of(ready: tuple[str, str], suspended: bool = False, reason: str = '') -> State:
    """A Flux object's state. Waiting for a dependency or applying a change is Updating, not Failing:
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


@dataclass(frozen=True)
class AppRow:
    instance: ops.Instance
    ready: tuple[str, str]
    suspended: bool
    image: str
    tag: str = ''
    digest: str = ''
    hosts: tuple[str, ...] = ()
    reason: str = ''

    @property
    def state(self) -> State:
        state = state_of(self.ready, self.suspended, self.reason)
        if state.key == 'healthy' and self.image == 'no HelmRelease':
            return State('unknown', 'the app release is missing')
        return state


@dataclass(frozen=True)
class AppSummary:
    """One app across its environments, for the Apps list."""
    app: str
    envs: dict[str, AppRow]
    comparison: str  # 'same', 'ahead' (staging has a newer run), 'differs', or '' with one environment


def release_image(release: dict | None) -> dict:
    return (((((release or {}).get('spec') or {}).get('values') or {}).get('controllers') or {})
            .get('main', {}).get('containers', {}).get('main', {}).get('image') or {})


def release_hosts(release: dict | None) -> tuple[str, ...]:
    """The hosts Git gives the app's routes."""
    ingresses = (((release or {}).get('spec') or {}).get('values') or {}).get('ingress') or {}
    return tuple(h['host'] for ing in ingresses.values() for h in (ing or {}).get('hosts') or [] if h.get('host'))


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
        return state_of(self.ready, self.suspended, self.reason)

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
          ('app-', 'Apps', 'One unit per app instance'))
OTHER_LAYER = 'Other'


def layer_of(name: str) -> str:
    return next((title for prefix, title, _ in LAYERS if name.startswith(prefix)), OTHER_LAYER)


def instance(app: str, env: str) -> ops.Instance | None:
    """The instance for a URL's app and env, or None if they cannot name one."""
    if env in ENVIRONMENTS and NAME_RE.match(app):
        return ops.Instance(app, env)
    return None


def instance_of_unit(name: str) -> ops.Instance | None:
    app, _, env = name.removeprefix('app-').rpartition('-')
    return instance(app, env) if name.startswith('app-') else None


def target(name: str) -> tuple[str, str]:
    """How every page names a job's or problem's subject: ``(label, link)``. An app instance is always
    ``<app>/<env>`` and links to its app page, whether given as its unit (``app-<app>-<env>``) or as
    ``<app>/<env>``; anything else is a Flux unit, by name."""
    found = instance_of_unit(name)
    app, _, env = name.partition('/')
    found = found or (instance(app, env) if env else None)
    if found:
        return f'{found.app}/{found.env}', f'/apps/{found.app}/{found.env}'
    return name, f'/units/{name}'


def items(runner: Runner, args: list[str]) -> list[dict]:
    try:
        return (runner.json(args) or {}).get('items', [])
    except CommandError as error:
        raise ReadError(str(error)) from None


def apps(runner: Runner) -> list[AppRow]:
    releases = {(r['metadata']['namespace'], r['metadata']['name']): r for r in items(runner, RELEASES)}
    rows = []
    for unit in items(runner, UNITS):
        found = instance_of_unit(unit['metadata']['name'])
        if found:
            release = releases.get((found.namespace, found.app))
            image = release_image(release)
            rows.append(AppRow(found, ready_condition(unit), bool(unit['spec'].get('suspend')),
                               ops.desired_image(release) if release else 'no HelmRelease',
                               image.get('tag', ''), image.get('digest', ''), release_hosts(release), ready_reason(unit)))
    return sorted(rows, key=lambda row: (row.instance.app, row.instance.env))


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


FLUX_SECTION = 'Flux Kustomizations'  # the units list shows these; the check's failures still count as problems


def updating(found: list[Unit]) -> list[Problem]:
    """Units applying a change or waiting for a dependency: normal for a few seconds after every push."""
    out = []
    for u in found:
        if u.state.key == 'updating':
            label, link = target(u.name)
            out.append(Problem('updating', f'{label}: {u.state.detail}', link))
    return out


def instance_state(status: ops.InstanceStatus, row: AppRow | None) -> State:
    """An app instance's state from what its page shows, worst first."""
    if status.unit_suspended or row and row.suspended:
        return State('suspended', 'Git changes are not applied until its Flux unit is resumed')
    if status.release_suspended:
        return State('suspended', 'the Helm release is suspended')
    if status.problems:
        return State('failing', f'{len(status.problems)} pod(s) failing; see below')
    unit = state_of(status.unit, reason=row.reason if row else '')
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
    if status.release and status.release[0] != 'True':
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


def promotion_problem(status: ops.InstanceStatus | None, image: str, revision: str) -> str:
    """Why a reviewed staging image cannot be promoted, or an empty string."""
    return promotion.source_problem(status, image, revision)


@dataclass(frozen=True)
class Problem:
    level: str  # a mark: failing, warn, suspended or updating
    text: str
    link: str = ''


def problems(checks: Checks, found: list[Unit]) -> list[Problem]:
    """What needs attention: failing or warning checks, then suspended units (which can still be Ready)."""
    by_name = {u.name: u for u in found}
    names = set(by_name)
    out = []
    for section, group in checks.sections:
        for entry in group:
            if entry.level in ('bad', 'warn'):
                unit, _, rest = entry.message.partition(' ')
                if section == FLUX_SECTION and unit in names and by_name[unit].state.key == 'updating':
                    continue  # waiting, not failing: listed by updating()
                if section == FLUX_SECTION and unit in names:
                    label, link = target(unit)
                    out.append(Problem('failing' if entry.level == 'bad' else 'warn', f'{label} {rest}', link))
                else:
                    out.append(Problem('failing' if entry.level == 'bad' else 'warn', entry.message))
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

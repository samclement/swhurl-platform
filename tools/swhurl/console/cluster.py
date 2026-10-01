"""What the console reads from the cluster: read-only, through a Runner, no Secrets and no exec."""
from __future__ import annotations

import io
from dataclasses import dataclass

from swhurl.apps import ops
from swhurl.apps.contract import ENVIRONMENTS
from swhurl.apps.new import NAME_RE
from swhurl.report import Entry, Report
from swhurl.run import CommandError, Runner
from swhurl.verify import ready_condition, verify_platform

UNITS = ['kubectl', '-n', 'flux-system', 'get', 'kustomizations.kustomize.toolkit.fluxcd.io', '-o', 'json']
RELEASES = ['kubectl', 'get', 'helmreleases.helm.toolkit.fluxcd.io', '--all-namespaces', '-o', 'json']
CONSOLE_CHECKS = frozenset({'cluster'})


class ReadError(Exception):
    """The cluster could not be read; the message is already redacted."""


@dataclass(frozen=True)
class AppRow:
    instance: ops.Instance
    ready: tuple[str, str]
    suspended: bool
    image: str


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

    @property
    def layer(self) -> str:
        return layer_of(self.name)

    @property
    def healthy(self) -> bool:
        return self.ready[0] == 'True' and not self.suspended


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
            rows.append(AppRow(found, ready_condition(unit), bool(unit['spec'].get('suspend')),
                               ops.desired_image(release) if release else 'no HelmRelease'))
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
                        tuple(sorted(needed_by.get(name, []))))
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


@dataclass(frozen=True)
class Problem:
    level: str  # 'bad' or 'warn'
    text: str
    link: str = ''


def problems(checks: Checks, found: list[Unit]) -> list[Problem]:
    """What needs attention: failing or warning checks, then suspended units (which can still be Ready)."""
    names = {u.name for u in found}
    out = []
    for section, group in checks.sections:
        for entry in group:
            if entry.level in ('bad', 'warn'):
                unit = entry.message.split(' ', 1)[0] if section == FLUX_SECTION else ''
                out.append(Problem(entry.level, entry.message, f'/units/{unit}' if unit in names else ''))
    out += [Problem('warn', f'{u.name} is suspended: Git changes are not applied to it until it is resumed',
                    f'/units/{u.name}') for u in found if u.suspended]
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

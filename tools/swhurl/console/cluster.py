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

    return sorted((Unit(name, ready_condition(unit), bool(unit['spec'].get('suspend')),
                        [d['name'] for d in unit['spec'].get('dependsOn') or []],
                        (unit.get('status') or {}).get('lastAppliedRevision', '').split(':')[-1][:7],
                        level(name))
                   for name, unit in found.items()), key=lambda u: (u.level, u.name))


@dataclass(frozen=True)
class Checks:
    passed: bool
    sections: list[tuple[str, list[Entry]]]  # in the order verify-platform runs them
    notes: list[str]  # which checks were skipped, and why


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

"""The console's change kinds: what its forms may name, and the flags ``swhurl-v2 change`` builds the pull request from.

Every console change is a pull request on a ``console/<kind>-<name>[-<env>]-<sha>`` branch, and a person merges
every one. What a kind *is* — its branch, its title, and the files its pull request may carry — is v2's
(``v2/pages/console_change.go``), because the same table decides what Activity reads back. This module keeps what
the console's own forms and jobs need while Python still serves them: the environments a form may name, the job a
change runs as, the form's values as v2's flags, and the branch reading Activity uses. The kind names are
permanent: Activity reads the branches an older console opened too.
"""
from __future__ import annotations

import re
from collections.abc import Mapping
from dataclasses import dataclass

from swhurl.apps.model import records
from swhurl.apps.model.records import ALLOW_RE
from swhurl.console.actions import ActionError


@dataclass(frozen=True)
class Connections:
    """Platform grants: apps to call and ``<app>.<event>`` to receive.
    Publications belong to the image's AsyncAPI document, not a connection form."""
    calls: tuple[str, ...] = ()
    subscribe: tuple[str, ...] = ()

    @classmethod
    def from_form(cls, form: Mapping[str, str]) -> Connections:
        if 'publish' in form:
            raise ActionError('publishing choices are no longer supported: edit asyncapi.yaml named by provides.eventsFile')
        chosen = cls(*(tuple(name.strip() for name in form.get(field, '').split(',') if name.strip())
                       for field in ('calls', 'subscribe')))
        bad = [name for name in chosen.calls if not records.NAME_RE.match(name)]
        bad += [name for name in chosen.subscribe if not records.SUBSCRIPTION_RE.match(name)]
        if bad:
            raise ActionError(f'{", ".join(bad)}: a call names an app, a subscription <app>.<event>')
        return chosen

    def check_offered(self, callable_apps: set[str], offered_events: set[str]) -> None:
        """Refuse a choice outside the lists the form offered, before a repository is created for an
        app that could not then be added."""
        missing = [name for name in self.calls if name not in callable_apps]
        missing += [name for name in self.subscribe if name not in offered_events]
        if missing:
            raise ActionError(f'{", ".join(missing)}: not a web app, or an event, that staging has now; reload the form')

    def settings(self) -> tuple[str, ...]:
        """A new app's connections as v2's settings: its record starts with neither list, so these are the
        whole of what it calls and receives."""
        found = []
        if self.calls:
            found.append(f'calls={",".join(self.calls)}')
        if self.subscribe:
            found.append(f'subscriptions={",".join(self.subscribe)}')
        return tuple(found)


def v2_scale_settings(form: Mapping[str, str]) -> tuple[str, ...]:
    """v2's scale fields: replicas and memory in MiB. CPU requests and limits are not modelled."""
    settings = []
    replicas = form.get('replicas', '').strip()
    if replicas:
        if not re.fullmatch(r'[0-9]+', replicas):
            raise ActionError(f'Replicas {replicas!r} is not a whole number')
        settings.append(f'replicas={int(replicas)}')
    memory = form.get('memory', '').strip()
    if memory:
        found = re.fullmatch(r'([0-9]+)(?:Mi)?', memory)
        if not found:
            raise ActionError(f'Memory {memory!r} is not a whole number of MiB, such as 256 or 256Mi')
        settings.append(f'memoryMiB={int(found[1])}')
    if form.get('cpu', '').strip() or form.get('memory_limit', '').strip():
        raise ActionError('v2 does not model CPU requests or memory limits: change replicas and memory only')
    if not settings:
        raise ActionError('change replicas or memory')
    return tuple(settings)


def v2_exposure_settings(form: Mapping[str, str]) -> tuple[str, ...]:
    exposure = form.get('exposure', '').strip()
    if exposure not in records.EXPOSURES:
        raise ActionError(f'exposure must be one of {", ".join(records.EXPOSURES)}')
    if form.get('host', '').strip():
        raise ActionError('v2 derives the address from the app name and the exposure: it does not model a host')
    return (f'exposure={exposure}',)


def v2_names(value: str, pattern: re.Pattern, label: str) -> tuple[str, ...]:
    names = [name.strip() for name in value.split(',') if name.strip()]
    if bad := [name for name in names if not pattern.fullmatch(name)]:
        raise ActionError(f'{", ".join(bad)}: {label}')
    return tuple(names)


def v2_team_settings(form: Mapping[str, str]) -> tuple[str, ...]:
    team = form.get('team', '').strip()
    if not records.NAME_RE.fullmatch(team):
        raise ActionError('choose the team that owns the app')
    return (f'team={team}',)


def _pairs(flag: str, values: tuple[str, ...]) -> tuple[str, ...]:
    """One flag repeated for each value, in order."""
    return tuple(part for value in values for part in (flag, value))


def _team_name(name: str, rename: str = '') -> None:
    """A team's name and its new one are DNS labels: the branch and the registry both carry them."""
    for label, value in (('team name', name), ('new name', rename)):
        if (value or label == 'team name') and not records.NAME_RE.fullmatch(value):
            raise ActionError(f'{label}: lowercase letters, digits and hyphens, at most 40 characters')


def change_arguments(action: str, name: str, form: Mapping[str, str]) -> tuple[tuple[str, ...], str | None]:
    """``swhurl-v2 change``'s own flags for one form, and the value it reads from standard input.

    The form's checks are the console's, decided here as a 400 before any job; the values are v2's words, which
    v2 merges into the record at the commit it changes. A connection, an allow or a team's channel is therefore
    stated as what to add or remove, never as the whole list."""
    settings = {
        'v2-scale': v2_scale_settings,
        'v2-expose': v2_exposure_settings,
        'v2-team': v2_team_settings,
    }
    if action in settings:
        return _pairs('--set', settings[action](form)), None
    if action == 'new':
        return (*_pairs('--set', tuple(form['flags'])), '--image', form['image']), None
    if action == 'v2-connect':
        calls = v2_names(form.get('calls', ''), records.NAME_RE, 'a call names an app')
        subscriptions = v2_names(form.get('subscribe', ''), records.SUBSCRIPTION_RE,
                                 'a subscription names <app>.<event>')
        if not calls and not subscriptions:
            raise ActionError('choose an app to call or an event to receive')
        return (*_pairs('--call', calls), *_pairs('--subscribe', subscriptions)), None
    if action == 'v2-disconnect':
        role, other, event = (form.get(key, '').strip() for key in ('role', 'app', 'event'))
        if role == 'calls' and records.NAME_RE.fullmatch(other):
            return _pairs('--drop-call', (other,)), None
        if role == 'subscribes' and records.SUBSCRIPTION_RE.fullmatch(f'{other}.{event}'):
            return _pairs('--drop-subscription', (f'{other}.{event}',)), None
        if role == 'publishes':
            raise ActionError('publications come from the release: v2 does not remove one on the platform')
        if role == 'reaches':
            raise ActionError('an egress grant is set with swhurl-v2 edit --set egress=, so this cannot be removed here')
        raise ActionError('no such connection to remove')
    if action == 'v2-allow':
        add = v2_names(form.get('add', ''), ALLOW_RE, 'an allowed caller is an app name or team:<team>')
        remove = v2_names(form.get('remove', ''), ALLOW_RE, 'an allowed caller is an app name or team:<team>')
        if not add and not remove:
            raise ActionError('name an app or team:<team> to allow or stop allowing')
        return (*_pairs('--allow', add), *_pairs('--disallow', remove)), None
    if action == 'v2-promote':
        if not form.get('image'):
            raise ActionError('refresh the staging app page and review the image before promotion')
        return (), None
    if action.startswith('team-'):
        _team_name(name)
        if action == 'team-remove':
            return (), None
        if action == 'team-channel-remove':
            return (), None
        if action == 'team-channel-set':
            return ('--transport', form.get('transport', 'ntfy').strip()), form.get('address', '').strip() + '\n'
        description = ' '.join(form.get('description', '').split())
        if action == 'team-add':
            return ('--description', description), None
        rename = form.get('rename', '').strip()
        _team_name(name, rename)
        if rename and rename != name:
            return ('--description', description, '--rename', rename), None
        return ('--description', description), None
    return (), None  # v2-remove: the branch and the environment are the whole request


@dataclass(frozen=True)
class Action:
    """One kind of console pull request, as the console's forms and jobs see it: the environments its branch may
    name (none for a team's), the job it runs as, and the instance it changes when that is not the branch's
    (a promotion changes production). What the pull request is belongs to v2's kind table."""
    envs: tuple[str, ...]
    job: str = ''  # when different from the action's name
    target_env: str = ''


SHA = '[0-9a-f]{7,40}'


def parse(branch: str) -> tuple[str, str, str] | None:
    """``(action, app or team, env)`` of a console branch ``console/<action>-<name>[-<env>]-<commit>``,
    env '' for a team's; None for any other branch. Its env is the branch's, not always the target's."""
    for action, entry in ACTIONS.items():
        envs = f'-({"|".join(entry.envs)})' if entry.envs else '()'
        found = re.fullmatch(rf'console/{re.escape(action)}-([a-z][a-z0-9-]*?){envs}-{SHA}', branch)
        if found and records.NAME_RE.fullmatch(found[1]):
            return action, found[1], found[2]
    return None


def target(action: str, name: str, env: str) -> str:
    """What a change is to: ``<app>/<env>`` (promotion: production) or ``team:<name>``."""
    entry = ACTIONS[action]
    return f'{name}/{entry.target_env or env}' if entry.envs else f'team:{name}'


ACTIONS: dict[str, Action] = {
    'new': Action(('staging',), job='new app and repository'),
    **{f'team-{action}': Action((), job=f'team {action}') for action in ('add', 'edit', 'remove')},
    **{f'team-channel-{action}': Action((), job=f'channel {action}') for action in ('set', 'remove')},
    'v2-scale': Action(('staging', 'prod')),
    'v2-expose': Action(('staging', 'prod')),
    'v2-connect': Action(('staging', 'prod')),
    'v2-disconnect': Action(('staging', 'prod')),
    'v2-allow': Action(('staging', 'prod')),
    # The team is the app's: the edit names no environment and rewrites both, whichever page it was asked from.
    'v2-team': Action(('staging', 'prod')),
    'v2-remove': Action(('staging', 'prod')),
    'v2-promote': Action(('staging',), target_env='prod'),
}
"""Every console change the forms can start: its branch's environments, its job and its target. A person merges
every one; the console reads it back by branch with :func:`parse`."""
V2_ACTION = {name.removeprefix('v2-'): name for name in ACTIONS if name.startswith('v2-')}
